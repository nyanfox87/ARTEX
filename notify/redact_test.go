package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// 本檔案是不變數測試：**任何**從渠道實現裡冒出來的錯誤文本都不得攜帶憑據。
//
// 為什麼單獨拉一個檔案：最初的渠道用例只覆蓋了成功路徑與平臺業務錯誤，
// 完全沒看傳輸層失敗。而恰恰是傳輸層錯誤（連線被拒/DNS 失敗/超時）最危險——
// http.Client.Do 返回的 *url.Error 會把**完整 URL** 打進錯誤文本，而本功能
// 這幾家的憑據就在 URL 裡。憑據順著這條串流到了四個出口：
//
//	notification_deliveries.last_error  → 明文落庫
//	GET /api/notify/deliveries 回應     → 繞過渠道配置的掩碼，回顯給瀏覽器
//	服務端日誌                          → 常被外發留存
//	測試傳送介面的 502 回應             → 直接彈在前端
//
// 所以這裡不只測一個函式，而是逐個渠道真發一次必然失敗的請求，斷言錯誤文本
// 裡找不到那個憑據。

// credentialCases 覆蓋所有「憑據在 URL 裡」的渠道形態：
// 釘釘/企微在 query，飛書在路徑末段，Telegram 在路徑中段。
var credentialCases = []struct {
	name   string
	ch     Channel
	cfg    map[string]any
	secret string
}{
	{
		name:   "釘釘 access_token 在 query",
		ch:     dingTalkChannel{},
		cfg:    map[string]any{"webhook": "http://127.0.0.1:1/robot/send?access_token=" + leakProbeToken},
		secret: leakProbeToken,
	},
	{
		name:   "企業微信 key 在 query",
		ch:     weComChannel{},
		cfg:    map[string]any{"webhook": "http://127.0.0.1:1/cgi-bin/webhook/send?key=" + leakProbeToken},
		secret: leakProbeToken,
	},
	{
		name:   "飛書 hook id 在路徑末段",
		ch:     feishuChannel{},
		cfg:    map[string]any{"webhook": "http://127.0.0.1:1/open-apis/bot/v2/hook/" + leakProbeToken},
		secret: leakProbeToken,
	},
	{
		name:   "Telegram bot token 在路徑中段",
		ch:     telegramChannel{},
		cfg:    map[string]any{"bot_token": leakProbeToken, "chat_id": "1", "base_url": "http://127.0.0.1:1"},
		secret: leakProbeToken,
	},
	{
		name:   "釘釘加簽金鑰",
		ch:     dingTalkChannel{},
		cfg:    map[string]any{"webhook": "http://127.0.0.1:1/robot/send", "secret": leakProbeToken},
		secret: leakProbeToken,
	},
}

// leakProbeToken 是一個絕不可能是真實憑據的哨兵值，用於在錯誤文本裡搜它。
const leakProbeToken = "LEAKPROBE0123456789abcdef"

// TestChannelErrorsNeverLeakCredentials 是核心不變數。
func TestChannelErrorsNeverLeakCredentials(t *testing.T) {
	for _, tc := range credentialCases {
		t.Run(tc.name, func(t *testing.T) {
			// 必然失敗的對端：127.0.0.1:1 無人監聽，走的是連線被拒這條路徑。
			_, err := tc.ch.Send(context.Background(), tc.cfg, Message{
				Items: []Item{{FindingID: 1, Severity: "high", Name: "洩露探針"}},
			})
			if err == nil {
				t.Fatal("對不可達地址應報錯")
			}
			assertNoSecret(t, err.Error(), tc.secret)
		})
	}
}

// TestChannelErrorsNeverLeakCredentialsInPermanentPath 覆蓋永久失敗分支：
// URL 校驗失敗、平臺業務錯誤等也會把錯誤文本外傳，同樣不能帶憑據。
func TestChannelErrorsNeverLeakCredentialsInPermanentPath(t *testing.T) {
	cases := []struct {
		name string
		ch   Channel
		cfg  map[string]any
	}{
		// 地址裡帶憑據但格式非法 → 觸發 validateHTTPURL / url.Parse 分支。
		{"釘釘地址非法", dingTalkChannel{}, map[string]any{"webhook": "file:///" + leakProbeToken}},
		{"企微地址非法", weComChannel{}, map[string]any{"webhook": "gopher://" + leakProbeToken}},
		{"飛書地址非法", feishuChannel{}, map[string]any{"webhook": "ftp://" + leakProbeToken + "/hook"}},
		{"Telegram API 地址非法", telegramChannel{}, map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": "file://" + leakProbeToken}},
		{"通用 Webhook 地址非法", webhookChannel{}, map[string]any{"url": "javascript:" + leakProbeToken}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.ch.Send(context.Background(), tc.cfg, Message{Items: []Item{{Severity: "high"}}})
			if err == nil {
				t.Fatal("非法配置應報錯")
			}
			assertNoSecret(t, err.Error(), leakProbeToken)
		})
	}
}

func assertNoSecret(t *testing.T, text, secret string) {
	t.Helper()
	if strings.Contains(text, secret) {
		t.Fatalf("錯誤文本洩露了憑據 %q:\n    %s", secret, text)
	}
}

func TestRedactRequestTargetKeepsOnlySchemeAndHost(t *testing.T) {
	cases := map[string]string{
		"https://oapi.dingtalk.com/robot/send?access_token=S1":    "https://oapi.dingtalk.com/…",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=S2": "https://qyapi.weixin.qq.com/…",
		"https://open.feishu.cn/open-apis/bot/v2/hook/S3":         "https://open.feishu.cn/…",
		"https://api.telegram.org/botS4/sendMessage":              "https://api.telegram.org/…",
		"http://10.0.0.5:8080/hook":                               "http://10.0.0.5:8080/…",
	}
	for in, want := range cases {
		got := redactRequestTarget(in)
		if got != want {
			t.Errorf("redactRequestTarget(%q) = %q，期望 %q", in, got, want)
		}
		// 脫敏結果本身不得再含有原地址的任何路徑/查詢片段。
		if parts := strings.SplitN(in, "://", 2); len(parts) == 2 {
			if hostAndRest := strings.SplitN(parts[1], "/", 2); len(hostAndRest) == 2 && hostAndRest[1] != "" {
				if strings.Contains(got, hostAndRest[1]) {
					t.Errorf("脫敏後仍含路徑/查詢片段 %q: %q", hostAndRest[1], got)
				}
			}
		}
	}
	// 不可解析的輸入絕不回顯原串。
	for _, bad := range []string{"", "://", "not a url", "http://"} {
		if got := redactRequestTarget(bad); strings.Contains(got, bad) && bad != "" {
			t.Errorf("不可解析輸入 %q 被回顯為 %q", bad, got)
		}
	}
}

// TestRedactTransportErrorStripsURL 直接盯住 *url.Error 這個具體型別：
// 它是 http.Client.Do 的返回型別，也是洩露的第一現場。
func TestRedactTransportErrorStripsURL(t *testing.T) {
	inner := errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
	uerr := &url.Error{
		Op:  "Post",
		URL: "https://api.telegram.org/bot" + leakProbeToken + "/sendMessage",
		Err: inner,
	}
	got := redactTransportError(uerr)
	assertNoSecret(t, got, leakProbeToken)
	if !strings.Contains(got, "api.telegram.org") {
		t.Errorf("應保留 host 以便排查，得到 %q", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Errorf("應保留底層原因以便排查，得到 %q", got)
	}
	// Op 也要保留（POST 還是 GET 對排查有意義）。
	if !strings.Contains(got, "Post") {
		t.Errorf("應保留操作名，得到 %q", got)
	}
}

// TestRedactURLsInTextHandlesFallback 兜底路徑：非 *url.Error 的自定義錯誤
// （如重定向策略返回的錯誤）裡的地址同樣要被摘掉。
func TestRedactURLsInTextHandlesFallback(t *testing.T) {
	in := fmt.Sprintf("拒絕跨主機重定向（a.example → http://b.example/bot%s/send）", leakProbeToken)
	got := redactURLsInText(in)
	assertNoSecret(t, got, leakProbeToken)
	if !strings.Contains(got, "http://b.example/…") {
		t.Errorf("應把地址替換為脫敏形態，得到 %q", got)
	}
	// 不含地址的文本原樣保留。
	if plain := "dial tcp: connection refused"; redactURLsInText(plain) != plain {
		t.Error("不含地址的文本不應被改動")
	}
}

// TestCrossHostRedirectRefused 覆蓋「憑據在 URL 裡 + 跟隨跨主機跳轉 = 交出憑據」。
// httptest 的兩個服務監聽在 127.0.0.1 的不同埠，埠不同即 Host 不同，
// 正好構成跨主機跳轉。
func TestCrossHostRedirectRefused(t *testing.T) {
	var hit bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/robot/send?access_token="+leakProbeToken, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	_, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": redirector.URL + "/robot/send?access_token=" + leakProbeToken},
		Message{Items: []Item{{Severity: "high"}}})
	if err == nil {
		t.Fatal("跨主機重定向應被拒絕")
	}
	if hit {
		t.Fatal("跳轉目標被訪問了——憑據已隨重定向外洩")
	}
	assertNoSecret(t, err.Error(), leakProbeToken)
}

// TestSameHostRedirectAllowed 反向用例：同主機跳轉（如末尾補斜槓）必須仍然可用，
// 否則會把正常工作流一起擋掉。
func TestSameHostRedirectAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robot/send" {
			// 同主機、同埠的跳轉。
			http.Redirect(w, r, "/robot/send/", http.StatusTemporaryRedirect)
			return
		}
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()

	if _, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"},
		Message{Items: []Item{{Severity: "high"}}}); err != nil {
		t.Fatalf("同主機重定向不應被拒絕: %v", err)
	}
}
