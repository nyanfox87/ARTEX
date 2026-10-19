package notify

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// 本檔案覆蓋兩條相關的加固：
//   ① 投遞地址不得把服務端當跳板打內網 / 雲後設資料（SSRF）
//   ② 地址校驗的錯誤資訊不得帶出地址裡的憑據
//
// 關於測試環境：本包大量用例用 127.0.0.1 上的 httptest 假接收端，守衛預設會攔下
// 它們。所以 TestMain 裡統一開啟 AllowLocalTargetsEnv，而下面每個 SSRF 用例都
// 顯式把它清掉，以斷言**預設拒絕**的行為。

func TestMain(m *testing.M) {
	// 讓常規用例能連本地的假接收端；SSRF 用例會自己臨時清空。
	_ = os.Setenv(AllowLocalTargetsEnv, "1")
	os.Exit(m.Run())
}

// TestDialGuardRejectsLoopbackByDefault 是 SSRF 防護的核心斷言：
// 預設配置下，投遞到環回地址必須被**連線層**拒絕。
func TestDialGuardRejectsLoopbackByDefault(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "") // 關掉逃生口 = 預設行為
	_, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}})
	if err == nil {
		t.Fatal("預設不應允許投遞到環回地址")
	}
	if hit {
		t.Fatal("請求已經打到了本機服務——守衛沒生效")
	}
	// 錯誤資訊要能指導使用者怎麼放開（本機 SMTP 中繼是合法配置）。
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("拒絕資訊應說明如何顯式放開: %v", err)
	}
}

// TestDialGuardAllowsLoopbackWhenOptedIn 反向用例：顯式開啟後必須能用，
// 否則本機 postfix / 內網中繼這類合法部署會被一刀切廢掉。
func TestDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}}); err != nil {
		t.Fatalf("顯式放開後應可投遞: %v", err)
	}
}

func TestIsBlockedDialIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"169.254.169.254", // 雲後設資料端點——本函式存在的主要理由
		"169.254.1.1", "fe80::1",
		"0.0.0.0", "::",
		"224.0.0.1", "ff02::1",
		"::ffff:127.0.0.1", // IPv4-mapped 形式必須還原後再判，否則是繞過口
		"",
	}
	for _, s := range blocked {
		if !isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s 應被拒絕", s)
		}
	}
	// RFC1918 私網**刻意放行**：內網自建 Mattermost / SMTP 中繼是常見合法用法。
	// 這條斷言把這個取捨固定下來——若將來有人順手加上私網判斷，這裡會失敗，
	// 從而逼出一次有意識的決定（而不是靜默廢掉一批部署）。
	allowed := []string{"10.0.0.5", "172.16.3.4", "192.168.1.10", "8.8.8.8", "2606:4700::1111"}
	for _, s := range allowed {
		if isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s 應被放行（私網是常見的合法投遞目標）", s)
		}
	}
}

// TestValidateHTTPURLRejectsLiteralPrivateTargets 覆蓋配置階段的前置提示：
// 字面 IP 在儲存時就該被拒，而不是等第一次投遞失敗。
func TestValidateHTTPURLRejectsLiteralPrivateTargets(t *testing.T) {
	t.Setenv(AllowLocalTargetsEnv, "")
	for _, raw := range []string{
		"http://127.0.0.1:8080/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]:8080/hook",
	} {
		if err := validateHTTPURL(raw); err == nil {
			t.Errorf("%s 應在配置階段被拒絕", raw)
		}
	}
	// 公網地址與私網地址照常通過（私網留給撥號階段，那裡不攔）。
	for _, raw := range []string{"https://oapi.dingtalk.com/robot/send", "http://10.0.0.9/hook"} {
		if err := validateHTTPURL(raw); err != nil {
			t.Errorf("%s 應通過校驗: %v", raw, err)
		}
	}
}

// TestValidateHTTPURLErrorNeverLeaksCredentials 是審計指出我上一輪遺漏的分支。
//
// url.Parse **失敗**時會返回 *url.Error，其 Error() 含完整原始地址。上一輪我只
// 脫敏了 http.Client.Do 的返回錯誤，漏了這裡；而當時補的「永久失敗路徑」用例
// （file://、gopher://、ftp://）其實都能被 url.Parse 解析成功、走的是 scheme 分支，
// 所以全綠也證明不了這條路徑安全——是假保證。
func TestValidateHTTPURLErrorNeverLeaksCredentials(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/%zz?access_token=" + leakProbeToken,         // 非法百分號轉義
		"https://a.example.com:port/x?access_token=" + leakProbeToken, // 埠非數字
		"http://[::1?access_token=" + leakProbeToken,                  // 括號不配對
	}
	for _, raw := range cases {
		// 先確認這個輸入**確實**讓 url.Parse 失敗。不做這一步的話，用例可能在
		// 毫無察覺的情況下走到別的分支（上一輪的假保證就是這麼來的）。
		if _, err := url.Parse(raw); err == nil {
			t.Errorf("%q 本應解析失敗，否則這條用例沒有覆蓋到目標分支", raw)
			continue
		}
		err := validateHTTPURL(raw)
		if err == nil {
			t.Errorf("%q 應校驗失敗", raw)
			continue
		}
		assertNoSecret(t, err.Error(), leakProbeToken)
	}
	// 確認渠道層的包裝也沒有把地址帶出去。
	t.Setenv(AllowLocalTargetsEnv, "")
	err := (dingTalkChannel{}).Validate(map[string]any{"webhook": cases[0]})
	if err == nil {
		t.Fatal("非法地址應校驗失敗")
	}
	assertNoSecret(t, err.Error(), leakProbeToken)
}

// TestEmailDialGuardRejectsLoopbackByDefault 覆蓋 SMTP 渠道的撥號守衛。
//
// 郵件渠道曾經用的是裸 net.Dialer，是整套 SSRF 防護裡唯一的缺口：host 填成
// 169.254.169.254 或 127.0.0.1 能直接連上，而 smtp.NewClient 握手失敗時會把
// 對端返回的那一行包進錯誤、經 last_error 由投遞歷史介面回顯——正是其它渠道
// 已經關掉的半盲讀原語；「連線被拒 vs 超時」的耗時差異還能用來探測埠。
//
// 本包的 TestMain 全域打開了 AllowLocalTargetsEnv（大量用例用 127.0.0.1 上的
// 假接收端），所以這個用例必須自己把它清掉——否則守衛在不在都會通過，
// 這也正是缺口當初沒被任何測試發現的原因。
func TestEmailDialGuardRejectsLoopbackByDefault(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "") // 關掉逃生口 = 預設行為
	_, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil {
		t.Fatal("預設不應允許把郵件投遞到環回地址")
	}
	// 連線根本不該建立：守衛在 Control 鉤子裡攔下，EHLO 永遠發不出去。
	if f.sawCommand("EHLO") || f.sawCommand("HELO") {
		t.Fatal("SMTP 會話已經建立——守衛沒生效")
	}
	// 錯誤資訊要能指導使用者怎麼放開（本機 postfix 中繼是合法配置）。
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("拒絕資訊應說明如何顯式放開: %v", err)
	}
}

// TestEmailDialGuardAllowsLoopbackWhenOptedIn 是配對的反向用例：顯式開啟後
// 必須能正常投遞。內網自建 SMTP / 本機中繼是非常常見的部署，守衛不能一刀切。
func TestEmailDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("顯式放開後本機 SMTP 應可投遞: %v", err)
	}
	if !f.sawCommand("EHLO") {
		t.Fatal("未看到 EHLO——會話沒真正建立")
	}
}
