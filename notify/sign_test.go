package notify

import (
	"net/url"
	"testing"
	"time"
)

// 簽名基準值由 OpenSSL 獨立算出，不是用本包自己的實現生成的——
// 否則只能證明「程式碼沒變」，證明不了「演算法對」。
//
//	TS=1700000000000, SECRET=SECtest123
//	釘釘: printf '%s\n%s' "$TS" "$SECRET" | openssl dgst -sha256 -hmac "$SECRET" -binary | openssl base64 -A
//	      -> w3RMHXzixTMdzr8OHJUmVLS4IoPJVdu+Ut1LE48MePE=
//	飛書: printf '' | openssl dgst -sha256 -hmac "$(printf '%s\n%s' "$TS" "$SECRET")" -binary | openssl base64 -A
//	      -> Hd4xFWQU6R6ad4nzy4ETIznzlqebqH7xcTFVmONTudo=
const (
	signTestTSMillis = int64(1700000000000)
	signTestSecret   = "SECtest123"
	dingTalkExpected = "w3RMHXzixTMdzr8OHJUmVLS4IoPJVdu+Ut1LE48MePE="
	feishuExpected   = "Hd4xFWQU6R6ad4nzy4ETIznzlqebqH7xcTFVmONTudo="
)

func TestDingTalkSignMatchesReference(t *testing.T) {
	got, err := dingTalkSignedURL("https://oapi.dingtalk.com/robot/send?access_token=tok", signTestSecret, time.UnixMilli(signTestTSMillis))
	if err != nil {
		t.Fatalf("簽名失敗: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("產出地址不可解析: %v", err)
	}
	q := u.Query()
	if q.Get("sign") != dingTalkExpected {
		t.Errorf("簽名不符\n期望 %s\n得到 %s", dingTalkExpected, q.Get("sign"))
	}
	if q.Get("timestamp") != "1700000000000" {
		t.Errorf("時間戳應為毫秒且原樣帶上，得到 %q", q.Get("timestamp"))
	}
	// 原有 query 引數（access_token）不能被簽名覆蓋掉。
	if q.Get("access_token") != "tok" {
		t.Errorf("原有 query 引數丟失，得到 %q", q.Get("access_token"))
	}
}

func TestFeishuSignMatchesReference(t *testing.T) {
	got := feishuSign("1700000000000", signTestSecret)
	if got != feishuExpected {
		t.Errorf("簽名不符\n期望 %s\n得到 %s", feishuExpected, got)
	}
}

// TestSignAlgorithmsDiffer 鎖住兩家的演算法差異。它們剛好互為對方的引數順序
// （釘釘 key=secret，飛書 key=待籤串），照另一家抄必然校驗失敗，
// 這條用例確保將來重構不會把兩者合併成同一個函式。
func TestSignAlgorithmsDiffer(t *testing.T) {
	ts := "1700000000000"
	dingURL, err := dingTalkSignedURL("https://example.com/hook", signTestSecret, time.UnixMilli(signTestTSMillis))
	if err != nil {
		t.Fatal(err)
	}
	dq, _ := url.Parse(dingURL)
	if dq.Query().Get("sign") == feishuSign(ts, signTestSecret) {
		t.Fatal("釘釘與飛書籤名相同，說明其中一家的演算法實現錯了")
	}
}

func TestDingTalkNoSecretLeavesURLUntouched(t *testing.T) {
	// 未開啟加簽的機器人：不得憑空新增 timestamp/sign 引數。
	const hook = "https://oapi.dingtalk.com/robot/send?access_token=tok"
	got, err := dingTalkSignedURL(hook, "", time.UnixMilli(signTestTSMillis))
	if err != nil {
		t.Fatal(err)
	}
	if got != hook {
		t.Fatalf("未配置 secret 時地址不應改動，得到 %q", got)
	}
}

func TestValidateHTTPURL(t *testing.T) {
	ok := []string{"https://example.com/hook", "http://10.0.0.1:8080/x?y=1"}
	for _, s := range ok {
		if err := validateHTTPURL(s); err != nil {
			t.Errorf("%q 應被接受: %v", s, err)
		}
	}
	// file:// 之類不應放行——http.Client 對它們的處理超出預期範圍。
	bad := []string{"", "file:///etc/passwd", "ftp://example.com", "https://", "gopher://x"}
	for _, s := range bad {
		if err := validateHTTPURL(s); err == nil {
			t.Errorf("%q 應被拒絕", s)
		}
	}
}
