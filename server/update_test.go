package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Autumn-27/artex/selfupdate"
)

// releaseCache 是保護 GitHub 配額的那一層：未認證的 API 只有 60 次/小時/IP，
// 而頂欄的"有新版本"提示每次整頁載入都會查一次。快取一旦失效，使用者多開幾個
// 標籤頁就會把配額耗光，之後真想更新反而查不動。

func newTestCache(fetch func(context.Context, *http.Client) (*selfupdate.Release, error)) *releaseCache {
	return &releaseCache{fetch: fetch}
}

func TestReleaseCacheServesFromCache(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	for range 5 {
		rel, err := c.get(t.Context(), nil, false)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if rel.TagName != "v0.3.8" {
			t.Fatalf("TagName = %q", rel.TagName)
		}
	}
	if calls != 1 {
		t.Errorf("5 次查詢只應回源 1 次，實際 %d 次", calls)
	}
}

func TestReleaseCacheForceBypasses(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	// 使用者點「檢查更新」必須拿到即時結果，否則剛釋出的版本要等快取過期才看得見。
	if _, err := c.get(t.Context(), nil, true); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("force 應繞過快取，期望回源 2 次，實際 %d 次", calls)
	}
}

func TestReleaseCacheExpiresAfterTTL(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	// 把落庫時間往前撥到剛過期，模擬 TTL 到點。
	c.at = time.Now().Add(-releaseTTL - time.Second)
	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("TTL 過期後應重新回源，期望 2 次，實際 %d 次", calls)
	}
}

func TestReleaseCacheUsesShorterTTLForErrors(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return nil, errors.New("github 不可達")
	})

	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("期望返回錯誤")
	}
	// 失敗結果也要快取一會兒，否則 GitHub 不可達時每次頁面載入都白等一次超時。
	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("期望返回錯誤")
	}
	if calls != 1 {
		t.Errorf("錯誤應短時快取，期望回源 1 次，實際 %d 次", calls)
	}

	// 但錯誤的 TTL 必須明顯短於成功的，網路恢復後要能很快自愈。
	if releaseErrTTL >= releaseTTL {
		t.Fatalf("錯誤 TTL(%v) 必須短於成功 TTL(%v)", releaseErrTTL, releaseTTL)
	}
	c.at = time.Now().Add(-releaseErrTTL - time.Second)
	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("期望返回錯誤")
	}
	if calls != 2 {
		t.Errorf("錯誤 TTL 過期後應重試，期望 2 次，實際 %d 次", calls)
	}
}

func TestReleaseCacheDoesNotPoisonOnCallerCancel(t *testing.T) {
	good := &selfupdate.Release{TagName: "v0.3.8"}
	c := newTestCache(func(ctx context.Context, _ *http.Client) (*selfupdate.Release, error) {
		return good, nil
	})
	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}

	// 訪客關掉標籤頁會取消請求。那不代表 GitHub 有問題，絕不能把"已取消"
	// 寫進快取——否則接下來 30 分鐘內每個訪客都會收到一條莫名其妙的錯誤。
	c.fetch = func(ctx context.Context, _ *http.Client) (*selfupdate.Release, error) {
		return nil, ctx.Err()
	}
	c.at = time.Now().Add(-releaseTTL - time.Second) // 讓快取過期，逼它回源

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx, nil, false); err == nil {
		t.Fatal("呼叫方已取消時應把錯誤透傳給它")
	}

	// 關鍵不變數：被取消的那一次不留下任何痕跡——快取裡既沒有"已取消"這個錯誤，
	// 也還保著上一次的好結果。
	if c.err != nil {
		t.Fatalf("取消錯誤不應寫進快取，得到 %v", c.err)
	}
	if c.rel == nil || c.rel.TagName != "v0.3.8" {
		t.Fatalf("快取應保留上一次的好結果，得到 %+v", c.rel)
	}

	// 那次取消沒換來任何新資料，所以下一個訪客理應重新回源——而且能正常拿到結果，
	// 不會被上一次的取消連累。
	c.fetch = func(context.Context, *http.Client) (*selfupdate.Release, error) {
		return good, nil
	}
	rel, err := c.get(t.Context(), nil, false)
	if err != nil {
		t.Fatalf("取消之後的正常請求不應報錯: %v", err)
	}
	if rel == nil || rel.TagName != "v0.3.8" {
		t.Fatalf("應拿到正常結果，得到 %+v", rel)
	}
}
