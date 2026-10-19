package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// 本檔案覆蓋 doJSON 的 HTTP 層錯誤分級。
//
// 為什麼要單獨測：各渠道介面卡只管平臺自己的業務錯誤碼（釘釘 errcode、
// 飛書 code、Telegram ok 欄位），而**HTTP 層**的分級是 doJSON 統一做的，
// 兩者是兩道獨立的防線。少了這道，一個返回 503 的中轉閘道器會被當成永久失敗、
// 直接放棄重試；而一個 403 會被當成可重試、白白退避三輪。

func replyServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDoJSONClassifiesHTTPStatus(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		permanent bool
	}{
		{"200 成功不算錯誤", 200, false},
		{"429 限流可重試", 429, false},
		{"408 請求超時可重試", 408, false},
		{"500 服務端錯誤可重試", 500, false},
		{"502 閘道器錯誤可重試", 502, false},
		{"503 服務不可用可重試", 503, false},
		{"400 引數錯誤永久失敗", 400, true},
		{"401 鑑權失敗永久失敗", 401, true},
		{"403 禁止訪問永久失敗", 403, true},
		{"404 地址不存在永久失敗", 404, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := replyServer(t, tc.status, `{"detail":"upstream says no"}`)
			_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
			if tc.status < 300 {
				if err != nil {
					t.Fatalf("2xx 不應報錯: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("非 2xx 應報錯")
			}
			if got := IsPermanent(err); got != tc.permanent {
				t.Fatalf("HTTP %d 的 permanent 判定錯誤：期望 %v 得到 %v (%v)",
					tc.status, tc.permanent, got, err)
			}
			// 狀態碼必須出現在錯誤裡，否則使用者無從判斷是自己配錯了還是對端掛了。
			// 斷言數字而不是 Go 的英文 StatusText：本包的文案是中文的
			// （與專案其它部分一致），數字才是語言無關、可穩定斷言的部分。
			if !strings.Contains(err.Error(), strconv.Itoa(tc.status)) {
				t.Errorf("錯誤資訊應帶上 HTTP 狀態碼 %d，得到 %v", tc.status, err)
			}
		})
	}
}

// TestDoJSONIncludesResponseSnippet 覆蓋 snippet：對端返回的錯誤說明要帶回來，
// 否則使用者只知道「失敗了」，不知道對端為什麼拒絕。
func TestDoJSONIncludesResponseSnippet(t *testing.T) {
	srv := replyServer(t, 400, `{"error":"invalid webhook token"}`)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("應報錯")
	}
	if !strings.Contains(err.Error(), "invalid webhook token") {
		t.Errorf("錯誤資訊應帶回對端的說明，得到 %v", err)
	}
}

// TestDoJSONSnippetIsSingleLineAndBounded 約束 snippet 的形態：
// 它對端回應會原樣進 last_error 列與前端表格，多行/超長會破壞排版與載荷。
func TestDoJSONSnippetIsSingleLineAndBounded(t *testing.T) {
	// 帶換行、製表符與 5000 字元超長內容的回應。
	long := strings.Repeat("x", 5000)
	srv := replyServer(t, 500, "line1\nline2\r\n\tline3 "+long)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("應報錯")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\r\n\t") {
		t.Errorf("錯誤資訊應壓成單行，得到 %q", msg)
	}
	// snippet 上限 200 字元 + 固定字首，總量必須遠小於原始回應。
	if len(msg) > 400 {
		t.Errorf("錯誤資訊過長（%d 位元組），應被 snippet 截斷: %q", len(msg), msg)
	}
}

// TestDoJSONRejectsOversizedResponse 確認讀取有上限：對端異常返回超大內容時
// 不能把整個回應讀進記憶體（投遞歷史裡每一條都會存一份 last_error）。
func TestDoJSONRejectsOversizedResponse(t *testing.T) {
	huge := strings.Repeat("A", 1<<20) // 1 MiB
	srv := replyServer(t, 400, huge)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("應報錯")
	}
	if len(err.Error()) > 400 {
		t.Errorf("超大回應應被限長讀取並截斷，錯誤資訊長度 %d", len(err.Error()))
	}
}
