package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// singleMsg 構造一條帶引號與換行的單發訊息。刻意用含 `"` 與 `\n` 的標題/摘要：
// 這正是模板插值最容易產出的非法 JSON 的輸入。
func singleMsg() Message {
	return Message{
		Items: []Item{{
			FindingID: 42,
			Name:      `登入處 "SQL注入" 風險`,
			VulnClass: "SQL注入",
			Severity:  "high",
			Summary:   "引數 id\n未過濾 導致注入",
			Assets:    []string{"a.example.com", "b.example.com"},
			DetailURL: "https://artex.local/function/findings/detail?id=42",
		}},
	}
}

// batchMsg 構造一批匯總訊息。
func batchMsg(n int) Message {
	m := Message{Batch: true, WindowMinutes: 30, HomeURL: "https://artex.local/function/findings"}
	for i := 0; i < n; i++ {
		m.Items = append(m.Items, Item{
			FindingID: int64(i + 1),
			Name:      "漏洞" + itoa(i+1),
			VulnClass: "XSS",
			Severity:  "medium",
			Summary:   "反射型跨站指令碼",
			Assets:    []string{"target.example.com"},
		})
	}
	return m
}

// capturePost 起一個假接收端，把收到的請求體與頭回傳給斷言函式。
func capturePost(t *testing.T, respBody string, assert func(t *testing.T, body map[string]any, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("請求體不是合法 JSON: %v\n原文: %s", err, raw)
			}
		}
		if assert != nil {
			assert(t, body, r)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDingTalkSendsActionCardWhenLinkPresent(t *testing.T) {
	srv := capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["msgtype"] != "actionCard" {
			t.Fatalf("有回鏈時應發 actionCard，得到 %v", body["msgtype"])
		}
		card, _ := body["actionCard"].(map[string]any)
		if card["singleURL"] != "https://artex.local/function/findings/detail?id=42" {
			t.Errorf("回鏈丟失: %v", card["singleURL"])
		}
	})
	if _, err := (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg()); err != nil {
		t.Fatalf("投遞失敗: %v", err)
	}
}

func TestDingTalkFallsBackToMarkdownForBatch(t *testing.T) {
	srv := capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["msgtype"] != "markdown" {
			t.Fatalf("彙總訊息應發 markdown，得到 %v", body["msgtype"])
		}
		md, _ := body["markdown"].(map[string]any)
		if !strings.Contains(md["text"].(string), "近 30 分鐘") {
			t.Errorf("彙總正文缺少時間窗: %v", md["text"])
		}
	})
	if _, err := (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, batchMsg(3)); err != nil {
		t.Fatalf("投遞失敗: %v", err)
	}
}

// TestDingTalkBusinessErrorIsPermanent 鎖住「HTTP 200 但 errcode 非 0」的判定。
// 不檢查 errcode 會把投遞失敗記成成功——這是各家國內 IM 平臺共有的坑。
func TestDingTalkBusinessErrorIsPermanent(t *testing.T) {
	srv := capturePost(t, `{"errcode":310000,"errmsg":"keywords not in content"}`, nil)
	_, err := (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg())
	if err == nil {
		t.Fatal("errcode 非 0 應報錯")
	}
	if !IsPermanent(err) {
		t.Fatalf("關鍵詞不匹配屬於配置錯誤，應標記為永久失敗，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "310000") {
		t.Errorf("錯誤資訊應帶上平臺錯誤碼，得到 %v", err)
	}
}

func TestWeComTruncatesCJKWithinByteLimit(t *testing.T) {
	var contentLen int
	srv := capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		md, _ := body["markdown"].(map[string]any)
		content, _ := md["content"].(string)
		contentLen = len(content)
		if !utf8.ValidString(content) {
			t.Fatal("截斷後不是合法 UTF-8——企微會整條拒收")
		}
	})
	// 造一批足夠長的中文彙總，必然超過 4096 位元組。
	m := batchMsg(200)
	if _, err := (weComChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, m); err != nil {
		t.Fatalf("投遞失敗: %v", err)
	}
	if contentLen > weComMarkdownLimit {
		t.Fatalf("正文 %d 位元組超出企微上限 %d", contentLen, weComMarkdownLimit)
	}
	if contentLen == 0 {
		t.Fatal("正文為空")
	}
}

func TestWeComRateLimitIsRetryableButKeyErrorIsPermanent(t *testing.T) {
	limited := capturePost(t, `{"errcode":45009,"errmsg":"api freq out of limit"}`, nil)
	_, err := (weComChannel{}).Send(context.Background(), map[string]any{"webhook": limited.URL}, singleMsg())
	if err == nil || IsPermanent(err) {
		t.Fatalf("45009 是滾動視窗限流，應可重試，得到 %v", err)
	}

	badKey := capturePost(t, `{"errcode":93000,"errmsg":"invalid webhook url"}`, nil)
	_, err = (weComChannel{}).Send(context.Background(), map[string]any{"webhook": badKey.URL}, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("93000 是 key 無效，重試不會自愈，應為永久失敗，得到 %v", err)
	}
}

func TestFeishuCardStructureAndSign(t *testing.T) {
	const secret = "SECtest123"
	srv := capturePost(t, `{"code":0,"msg":"success"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["msg_type"] != "interactive" {
			t.Fatalf("應發交互式卡片，得到 %v", body["msg_type"])
		}
		card, _ := body["card"].(map[string]any)
		header, _ := card["header"].(map[string]any)
		if header["template"] != "orange" {
			t.Errorf("high 級別應為 orange 配色，得到 %v", header["template"])
		}
		// 配了 secret 就必須帶加簽引數，否則飛書會以 19021 拒收。
		if body["sign"] == nil || body["timestamp"] == nil {
			t.Fatalf("缺少加簽引數: %v", body)
		}
		// 卡片元素裡應包含一個按鈕，其 url 指向漏洞詳情。
		elements, _ := card["elements"].([]any)
		foundButton := false
		for _, e := range elements {
			em, _ := e.(map[string]any)
			if em["tag"] != "action" {
				continue
			}
			actions, _ := em["actions"].([]any)
			for _, a := range actions {
				am, _ := a.(map[string]any)
				if am["url"] == "https://artex.local/function/findings/detail?id=42" {
					foundButton = true
				}
			}
		}
		if !foundButton {
			t.Fatal("卡片裡沒有指向詳情頁的按鈕")
		}
	})
	cfg := map[string]any{"webhook": srv.URL, "secret": secret}
	if _, err := (feishuChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("投遞失敗: %v", err)
	}
}

func TestFeishuWithoutSecretOmitsSign(t *testing.T) {
	srv := capturePost(t, `{"code":0,"msg":"success"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["sign"] != nil || body["timestamp"] != nil {
			t.Fatalf("未配置 secret 時不應帶加簽引數: %v", body)
		}
	})
	if _, err := (feishuChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg()); err != nil {
		t.Fatalf("投遞失敗: %v", err)
	}
}

func TestTelegramEscapesHTMLInUntrustedContent(t *testing.T) {
	var text string
	srv := capturePost(t, `{"ok":true}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		text, _ = body["text"].(string)
		if body["parse_mode"] != "HTML" {
			t.Fatalf("應使用 HTML 解析模式，得到 %v", body["parse_mode"])
		}
	})
	m := Message{Items: []Item{{
		Severity: "high",
		// 標題與摘要來自被測目標/模型輸出，是不可信內容。
		Name:    `<script>alert(1)</script>`,
		Summary: "a & b < c",
	}}}
	if _, err := (telegramChannel{}).Send(context.Background(),
		map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": srv.URL}, m); err != nil {
		t.Fatalf("投遞失敗: %v", err)
	}
	if strings.Contains(text, "<script>") {
		t.Fatalf("未轉義 HTML，存在注入: %q", text)
	}
	if !strings.Contains(text, "&lt;script&gt;") {
		t.Fatalf("期望轉義後的實體，得到 %q", text)
	}
	if !strings.Contains(text, "a &amp; b") {
		t.Fatalf("& 未轉義，得到 %q", text)
	}
}

func TestTelegramErrorClassification(t *testing.T) {
	rateLimited := capturePost(t, `{"ok":false,"error_code":429,"description":"Too Many Requests"}`, nil)
	_, err := (telegramChannel{}).Send(context.Background(),
		map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": rateLimited.URL}, singleMsg())
	if err == nil || IsPermanent(err) {
		t.Fatalf("429 應可重試，得到 %v", err)
	}

	forbidden := capturePost(t, `{"ok":false,"error_code":403,"description":"bot was blocked by the user"}`, nil)
	_, err = (telegramChannel{}).Send(context.Background(),
		map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": forbidden.URL}, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("403 是配置問題，應為永久失敗，得到 %v", err)
	}
}

func TestWebhookDefaultTemplateProducesValidJSON(t *testing.T) {
	// 這條是預設模板存在的意義：標題裡帶引號與換行時，任何樸素的
	// `"title": "{{.Title}}"` 寫法都會產出非法 JSON。{{json .}} 才不會。
	srv := capturePost(t, `{"ok":true}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["title"] != `[🟠 高危] 登入處 "SQL注入" 風險` {
			t.Errorf("標題未正確還原: %v", body["title"])
		}
		items, _ := body["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("items 數量應為 1，得到 %d", len(items))
		}
		it, _ := items[0].(map[string]any)
		if it["summary"] != "引數 id\n未過濾 導致注入" {
			t.Errorf("摘要未正確還原: %v", it["summary"])
		}
		// 數值必須是 JSON 數字而不是字串（json:"...,string" 之類的寫法會踩這坑）。
		if _, ok := it["finding_id"].(float64); !ok {
			t.Errorf("finding_id 應為數字，得到 %T", it["finding_id"])
		}
	})
	if _, err := (webhookChannel{}).Send(context.Background(), map[string]any{"url": srv.URL}, singleMsg()); err != nil {
		t.Fatalf("投遞失敗: %v", err)
	}
}

func TestWebhookCustomTemplateAndHeaders(t *testing.T) {
	srv := capturePost(t, `{"ok":true}`, func(t *testing.T, body map[string]any, r *http.Request) {
		if r.Header.Get("X-Token") != "s3cret" {
			t.Errorf("自定義頭丟失: %v", r.Header)
		}
		if body["msg"] != "3 條" {
			t.Errorf("自定義模板渲染有誤: %v", body["msg"])
		}
		if body["first"] != "漏洞1" {
			t.Errorf("range 提取有誤: %v", body["first"])
		}
	})
	cfg := map[string]any{
		"url":           srv.URL,
		"headers":       map[string]any{"X-Token": "s3cret"},
		"body_template": `{"msg": {{json (printf "%d 條" .Count)}}, "first": {{json (index .Items 0).Name}}}`,
	}
	if _, err := (webhookChannel{}).Send(context.Background(), cfg, batchMsg(3)); err != nil {
		t.Fatalf("投遞失敗: %v", err)
	}
}

func TestWebhookRejectsNonJSONRenderResult(t *testing.T) {
	cfg := map[string]any{"url": "https://example.com/hook", "body_template": `not json at all`}
	_, err := (webhookChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("渲染結果非 JSON 應為永久失敗（模板寫錯了，重試無用），得到 %v", err)
	}
}

func TestWebhookValidateCatchesBadConfigEarly(t *testing.T) {
	bad := []map[string]any{
		{},
		{"url": "file:///etc/passwd"},
		{"url": "https://example.com", "method": "DELETE"},
		{"url": "https://example.com", "body_template": `{{.Items.`},
	}
	for i, cfg := range bad {
		if err := (webhookChannel{}).Validate(cfg); err == nil {
			t.Errorf("第 %d 組配置應被拒絕: %v", i, cfg)
		}
	}
}

func TestEmailMessageIsWellFormed(t *testing.T) {
	msg, err := buildEmailMessage("artex@example.com", []string{"a@example.com", "b@example.com"}, singleMsg())
	if err != nil {
		t.Fatalf("組裝郵件失敗: %v", err)
	}
	if !strings.HasPrefix(msg, "From: artex@example.com\r\n") {
		t.Fatalf("From 頭有誤:\n%s", msg)
	}
	if !strings.Contains(msg, "To: a@example.com, b@example.com\r\n") {
		t.Fatalf("To 頭有誤:\n%s", msg)
	}
	// 中文主題必須 RFC 2047 編碼，否則客戶端顯示成亂碼。
	if !strings.Contains(msg, "Subject: =?utf-8?") {
		t.Fatalf("主題未做 RFC 2047 編碼:\n%s", msg)
	}
	if dec, err := new(mime.WordDecoder).DecodeHeader(mustExtractHeader(t, msg, "Subject")); err != nil {
		t.Fatalf("主題無法解碼: %v", err)
	} else if !strings.Contains(dec, "SQL注入") {
		t.Fatalf("主題解碼後內容有誤: %q", dec)
	}

	// 正文是 base64，解出來應是合法 HTML。
	parts := strings.SplitN(msg, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatal("郵件缺少頭/體分隔")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(parts[1]), "\r\n", ""))
	if err != nil {
		t.Fatalf("正文 base64 解碼失敗: %v", err)
	}
	html := string(decoded)
	if !strings.HasPrefix(html, "<div") {
		t.Fatalf("正文不是 HTML: %.80s", html)
	}
	// 標題原樣出現在文本位置：HTML 文本內容裡的雙引號是合法字元，無需轉義。
	// 這裡斷言「原樣保留」是為了防止將來有人誤加一層引號轉義，讓中文引號
	// 顯示成 &quot;。
	if !strings.Contains(html, `"SQL注入"`) {
		t.Fatalf("標題中的引號在文本位置應原樣保留: %.200s", html)
	}
}

// TestEmailEscapesStructuralInjection 覆蓋郵件正文真正需要防的注入：
// 漏洞標題與摘要來自被測目標與模型輸出，是不可信內容。文本位置必須轉義
// & < >（否則可以注入標籤），屬性位置還必須轉義引號（否則可以閉合 href）。
func TestEmailEscapesStructuralInjection(t *testing.T) {
	m := Message{
		Items: []Item{{
			Severity:  "high",
			Name:      `<script>alert(1)</script>`,
			Summary:   "a & b > c",
			DetailURL: `https://artex.local/x?a="onmouseover=alert(1)`,
		}},
	}
	html := htmlBody(m, 0)
	if strings.Contains(html, "<script>") {
		t.Fatalf("標題未轉義，可注入標籤: %s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Fatalf("期望轉義後的實體: %s", html)
	}
	if !strings.Contains(html, "a &amp; b &gt; c") {
		t.Fatalf("& 與 > 未轉義: %s", html)
	}
	// 回鏈是管理員可配的 public_base_url，本身可信度較高，但屬性位置仍必須
	// 轉義引號——否則一個帶引號的地址會閉合 href 並注入事件處理器。
	if strings.Contains(html, `onmouseover=alert(1)">`) {
		t.Fatalf("href 屬性未正確轉義: %s", html)
	}
	if !strings.Contains(html, "&quot;") {
		t.Fatalf("屬性位置的引號應被轉義: %s", html)
	}
}

func mustExtractHeader(t *testing.T, msg, name string) string {
	t.Helper()
	for _, line := range strings.Split(msg, "\r\n") {
		if strings.HasPrefix(line, name+": ") {
			return strings.TrimPrefix(line, name+": ")
		}
	}
	t.Fatalf("未找到 %s 頭", name)
	return ""
}

func TestChannelValidateReportsMissingFields(t *testing.T) {
	// 校驗錯誤會直接展示給配置者，必須說清楚缺什麼，而不是泛泛的「配置無效」。
	cases := []struct {
		kind   string
		cfg    map[string]any
		substr string
	}{
		{KindDingTalk, map[string]any{}, "Webhook"},
		{KindFeishu, map[string]any{}, "Webhook"},
		{KindWeCom, map[string]any{}, "Webhook"},
		{KindTelegram, map[string]any{}, "Bot Token"},
		{KindTelegram, map[string]any{"bot_token": "t"}, "Chat ID"},
		{KindEmail, map[string]any{}, "SMTP"},
		{KindEmail, map[string]any{"host": "h"}, "埠"},
		{KindEmail, map[string]any{"host": "h", "port": 587, "from": "f"}, "收件人"},
	}
	for _, tc := range cases {
		ch, ok := Get(tc.kind)
		if !ok {
			t.Fatalf("渠道 %s 未註冊", tc.kind)
		}
		err := ch.Validate(tc.cfg)
		if err == nil {
			t.Errorf("%s 配置 %v 應校驗失敗", tc.kind, tc.cfg)
			continue
		}
		if !strings.Contains(err.Error(), tc.substr) {
			t.Errorf("%s 的錯誤資訊應提到 %q，得到 %q", tc.kind, tc.substr, err.Error())
		}
	}
}

// TestEmailSMTPErrorClassification 鎖住 SMTP 4xx/5xx 的語義區分。
// 若把 4xx 也判成永久失敗，一個啟用灰名單的郵件伺服器會讓每條推送都在第一次
// 嘗試後落入 failed —— 而灰名單恰恰是自動重試最該發揮作用的場景。
func TestEmailSMTPErrorClassification(t *testing.T) {
	cases := []struct {
		reply     string
		permanent bool
	}{
		{"450 4.7.1 Greylisting in action, please come back later", false},
		{"451 4.3.0 Temporary system failure", false},
		{"452 4.2.2 Mailbox full", false},
		{"550 5.1.1 User unknown", true},
		{"553 5.1.3 Bad address syntax", true},
		{"554 5.7.1 Relay access denied", true},
		// 取不到應答碼時按「可重試」處理：寧可多試一次，也不要把可能的瞬時
		// 故障判死。
		{"unexpected EOF", false},
		{"", false},
	}
	for _, tc := range cases {
		err := smtpStageError("收件人被拒", errors.New(tc.reply))
		if got := IsPermanent(err); got != tc.permanent {
			t.Errorf("應答 %q: 期望 permanent=%v 得到 %v", tc.reply, tc.permanent, got)
		}
		// 無論怎麼分類，原文都要保留給使用者排查。
		if tc.reply != "" && !strings.Contains(err.Error(), tc.reply) {
			t.Errorf("應答 %q 的原文被丟棄: %v", tc.reply, err)
		}
	}
}

func TestRegistryCoversAllKinds(t *testing.T) {
	// 六個渠道缺一不可——少一個會在 UI 下拉裡靜默消失。
	want := []string{KindDingTalk, KindEmail, KindFeishu, KindTelegram, KindWebhook, KindWeCom}
	got := Kinds()
	if len(got) != len(want) {
		t.Fatalf("渠道數量應為 %d，得到 %d: %v", len(want), len(got), got)
	}
	for _, k := range want {
		if !ValidKind(k) {
			t.Errorf("渠道 %s 未註冊", k)
		}
		if ch, ok := Get(k); !ok || ch.Kind() != k {
			t.Errorf("渠道 %s 的 Kind() 與註冊鍵不一致", k)
		}
	}
	if ValidKind("nope") {
		t.Error("未註冊的型別不應通過校驗")
	}
}

func TestPermanentErrorUnwrap(t *testing.T) {
	base := &permanentSentinel{}
	err := Permanent(base)
	if !IsPermanent(err) {
		t.Fatal("應識別為永久失敗")
	}
	if !strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("錯誤資訊應透傳底層: %v", err)
	}
	if Permanent(nil) != nil {
		t.Fatal("Permanent(nil) 必須返回 nil")
	}
	if IsPermanent(nil) {
		t.Fatal("nil 不是永久失敗")
	}
}

type permanentSentinel struct{}

func (*permanentSentinel) Error() string { return "sentinel" }
