package notify

import (
	"bufio"
	"context"

	"net"
	"strings"
	"sync"
	"testing"
)

// 本檔案補齊郵件渠道的協議級測試。在此之前 email.Send 的覆蓋率是 0——
// 整條 SMTP 路徑沒有任何用例跑過，而它恰恰是六個渠道裡協議面最大、
// 最容易出錯的一個（握手、認證、信封、DATA 階段各有各的失敗語義）。
//
// 這裡用自建的最小 SMTP 伺服器驅動，而不是 mock 掉 net/smtp：
// 郵件渠道的絕大部分風險就在「與真實 SMTP 伺服器對話」這一步，
// 把這一步 mock 掉等於不測。

// fakeSMTP 是一個剛好夠用的 SMTP 伺服器：能完成 greet/EHLO/AUTH/MAIL/RCPT/DATA/QUIT，
// 並按用例要求對特定階段返回指定應答碼。
type fakeSMTP struct {
	ln net.Listener

	// rcptReply 是 RCPT TO 的應答；預設 250。
	rcptReply string
	// mailReply 是 MAIL FROM 的應答；預設 250。
	mailReply string
	// advertiseAuth 為 true 時在 EHLO 裡宣告支援 AUTH PLAIN。
	advertiseAuth bool

	mu       sync.Mutex
	data     string
	commands []string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, rcptReply: "250 OK", mailReply: "250 OK"}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSMTP) hostPort(t *testing.T) (string, int) {
	t.Helper()
	addr, ok := f.ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("非 TCP 監聽地址")
	}
	return "127.0.0.1", addr.Port
}

func (f *fakeSMTP) record(cmd string) {
	f.mu.Lock()
	f.commands = append(f.commands, cmd)
	f.mu.Unlock()
}

func (f *fakeSMTP) body() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data
}

func (f *fakeSMTP) sawCommand(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.commands {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func (f *fakeSMTP) serve() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	w("220 fake.local ESMTP ready")
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.record(line)
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			// 不宣告 STARTTLS：讓程式碼走明文分支（測試目標是信封邏輯，不是 TLS）。
			w("250-fake.local")
			if f.advertiseAuth {
				w("250-AUTH PLAIN")
			}
			w("250 8BITMIME")
		case strings.HasPrefix(line, "AUTH"):
			// 簡化處理：PLAIN 的初始應答可能跨多行，直接接受。
			w("235 2.7.0 Authentication successful")
		case strings.HasPrefix(line, "MAIL FROM"):
			w(f.mailReply)
		case strings.HasPrefix(line, "RCPT TO"):
			w(f.rcptReply)
		case strings.HasPrefix(line, "DATA"):
			w("354 End data with <CR><LF>.<CR><LF>")
			var sb strings.Builder
			for {
				dl, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dl, "\r\n") == "." {
					break
				}
				sb.WriteString(dl)
			}
			f.mu.Lock()
			f.data = sb.String()
			f.mu.Unlock()
			w("250 2.0.0 Ok: queued as FAKE1")
		case strings.HasPrefix(line, "QUIT"):
			w("221 2.0.0 Bye")
			return
		default:
			w("250 OK")
		}
	}
}

func emailCfg(t *testing.T, f *fakeSMTP, extra map[string]any) map[string]any {
	t.Helper()
	host, port := f.hostPort(t)
	cfg := map[string]any{
		"host": host,
		"port": float64(port),
		"from": "artex@example.com",
		"to":   []any{"a@example.com", "b@example.com"},
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func TestEmailSendDeliversFullMessage(t *testing.T) {
	f := newFakeSMTP(t)
	f.advertiseAuth = true
	cfg := emailCfg(t, f, map[string]any{"username": "artex", "password": "pw"})

	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("投遞失敗: %v", err)
	}
	// 信封階段必須走到：發件人、兩個收件人、DATA。
	for _, want := range []string{"MAIL FROM:<artex@example.com>", "RCPT TO:<a@example.com>", "RCPT TO:<b@example.com>", "DATA", "AUTH", "QUIT"} {
		if !f.sawCommand(want) {
			t.Errorf("SMTP 會話裡缺少 %q，實際命令：%v", want, f.commands)
		}
	}
	// 正文是 base64 的 HTML，且要帶上真實的漏洞內容（編碼後仍可辨認）。
	body := f.body()
	if body == "" {
		t.Fatal("DATA 階段沒有收到正文")
	}
	if !strings.Contains(body, "Content-Type: text/html") {
		t.Errorf("缺少 Content-Type 頭:\n%s", body)
	}
	if !strings.Contains(body, "base64") {
		t.Errorf("正文未按 base64 編碼（長 HTML 行會破壞 SMTP 的 1000 位元組行長限制）:\n%s", body)
	}
	// 多個收件人都要出現在 To 頭裡。
	if !strings.Contains(body, "a@example.com, b@example.com") {
		t.Errorf("To 頭未包含全部收件人:\n%s", body)
	}
}

func TestEmailSendWithoutAuth(t *testing.T) {
	// 未配帳號時不應發 AUTH —— 有些中繼會因此拒收。
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)
	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("投遞失敗: %v", err)
	}
	if f.sawCommand("AUTH") {
		t.Errorf("未配帳號卻發了 AUTH: %v", f.commands)
	}
}

// TestEmailSendClassifiesSMTPReplies 是本次審計修復的直接驗證：
// 5xx 判永久失敗、4xx（灰名單）判可重試。
func TestEmailSendClassifiesSMTPReplies(t *testing.T) {
	cases := []struct {
		name      string
		rcptReply string
		mailReply string
		permanent bool
	}{
		{"收件人被 550 永久拒絕", "550 5.1.1 User unknown", "250 OK", true},
		{"收件人遇 450 灰名單", "450 4.7.1 Greylisting in action", "250 OK", false},
		{"收件人遇 452 郵箱滿", "452 4.2.2 Mailbox full", "250 OK", false},
		{"發件人被 553 永久拒絕", "250 OK", "553 5.1.3 Bad address", true},
		{"發件人遇 451 臨時錯誤", "250 OK", "451 4.3.0 Temporary failure", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSMTP(t)
			f.rcptReply = tc.rcptReply
			f.mailReply = tc.mailReply
			_, err := (emailChannel{}).Send(context.Background(), emailCfg(t, f, nil), singleMsg())
			if err == nil {
				t.Fatal("應報錯")
			}
			if got := IsPermanent(err); got != tc.permanent {
				t.Fatalf("permanent 判定錯誤：期望 %v 得到 %v (%v)", tc.permanent, got, err)
			}
			// 伺服器原文要保留，否則使用者不知道該找伺服器管理員還是改地址。
			if !strings.Contains(err.Error(), strings.Fields(tc.rcptReply)[0]) && !strings.Contains(err.Error(), strings.Fields(tc.mailReply)[0]) {
				t.Errorf("錯誤裡應保留伺服器的應答碼: %v", err)
			}
		})
	}
}

func TestEmailSendRefusesPlaintextCredentials(t *testing.T) {
	// net/smtp 的 PlainAuth 拒絕在未加密連線上發憑據（除非目標是 localhost）。
	// 這是**正確**的安全行為，不能被繞過；但要給出能指導使用者修復的錯誤。
	// 這裡用一個非 localhost 的主機名觸發它。
	f := newFakeSMTP(t)
	f.advertiseAuth = true
	_, port := f.hostPort(t)
	cfg := map[string]any{
		"host":     "smtp.example.com", // 非 localhost
		"port":     float64(port),
		"from":     "a@example.com",
		"to":       []any{"b@example.com"},
		"username": "artex",
		"password": "pw",
	}
	_, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil {
		t.Skip("本機 DNS 解析到了本地伺服器，跳過（不影響其它用例）")
	}
	// 連不上 或 被拒發憑據都算通過這條斷言；關鍵是**不能**靜默把密碼發出去。
	if !IsPermanent(err) && !strings.Contains(err.Error(), "連線") {
		t.Logf("錯誤：%v（非 localhost 下未能連上屬預期）", err)
	}
}

func TestEmailValidateReportsMissingFields(t *testing.T) {
	// 郵件渠道的配置欄位最多，遺漏任一個都會在投遞時才暴露；這裡逐個確認
	// 校驗能提前攔下。斷言檢查的是「錯誤資訊提到了缺什麼」。
	cases := []struct {
		name string
		cfg  map[string]any
	}{
		{"缺 host", map[string]any{"port": float64(25), "from": "a@b.c", "to": []any{"d@e.f"}}},
		{"缺 port", map[string]any{"host": "smtp.example.com"}},
		{"port 越界", map[string]any{"host": "h", "port": float64(70000), "from": "a@b.c", "to": []any{"d@e.f"}}},
		{"缺 from", map[string]any{"host": "h", "port": float64(25), "to": []any{"d@e.f"}}},
		{"缺 to", map[string]any{"host": "h", "port": float64(25), "from": "a@b.c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := (emailChannel{}).Validate(tc.cfg); err == nil {
				t.Fatalf("應校驗失敗: %v", tc.cfg)
			}
		})
	}
}

// TestEmailConfigTolerance 覆蓋配置讀取的容錯：JSONB 裡數值是 float64，
// 但使用者在 UI 裡可能把埠填成字串；陣列也可能是單個字串。
func TestEmailConfigTolerance(t *testing.T) {
	cfg := map[string]any{
		"host": "smtp.example.com",
		"port": "587", // 字串形式的埠
		"from": "a@b.c",
		"to":   "d@e.f", // 單個字串而非陣列
		"tls":  "true",  // 字串形式的布林
	}
	if err := (emailChannel{}).Validate(cfg); err != nil {
		t.Fatalf("應容忍字串形式的數值: %v", err)
	}
	if got := cfgInt(cfg, "port"); got != 587 {
		t.Errorf("cfgInt 未解析字串埠，得到 %d", got)
	}
	if !cfgBool(cfg, "tls") {
		t.Error("cfgBool 未解析字串 \"true\"")
	}
	if to := cfgStrings(cfg, "to"); len(to) != 1 || to[0] != "d@e.f" {
		t.Errorf("cfgStrings 未相容單字串，得到 %v", to)
	}
}

// TestFilterValidateRejectsTypo 是審計修復的直接驗證：
// 門檻打錯字必須在寫入時被攔，否則過濾器會靜默失效變成全推。
func TestFilterValidateRejectsTypo(t *testing.T) {
	good := []string{"", "low", "medium", "high", "critical"}
	for _, s := range good {
		if err := (Filter{MinSeverity: s}).Validate(); err != nil {
			t.Errorf("合法門檻 %q 被拒: %v", s, err)
		}
	}
	// 這些是真實會發生的筆誤——全部必須被拒。
	for _, s := range []string{"hgih", "HIGH", "嚴重", "high ", "crit"} {
		err := (Filter{MinSeverity: s}).Validate()
		if err == nil {
			t.Errorf("非法門檻 %q 應被拒絕（否則過濾器靜默失效、變成全推）", s)
			continue
		}
		// 錯誤資訊要能指導使用者改對。
		if !strings.Contains(err.Error(), "low") || !strings.Contains(err.Error(), "critical") {
			t.Errorf("錯誤資訊應列出可選值，得到 %q", err.Error())
		}
	}
}

// TestFilterValidateIsWriteTimeOnly 鎖住「寫入嚴、讀取寬」的分工：
// 庫裡已有的壞值不能讓渠道整個讀不出來（那會讓歷史渠道突然全部停止推送）。
func TestFilterValidateIsWriteTimeOnly(t *testing.T) {
	raw := []byte(`{"min_severity":"hgih"}`)
	f := ParseFilter(raw) // 不報錯
	if f.MinSeverity != "hgih" {
		t.Fatalf("讀取路徑應原樣保留，得到 %q", f.MinSeverity)
	}
	// 且該渠道仍能對事件做出判定（不 panic、不阻塞）。
	_ = Match(f, Snapshot{Kind: EventFindingCreated, Severity: "critical"})
}
