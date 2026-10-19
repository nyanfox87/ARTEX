package notify

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// emailDialTimeout / emailSessionTimeout 分別約束建連與整段 SMTP 會話。
// net/smtp 自身沒有任何超時機制，不設這兩道的話，一個卡住的對端會讓
// 投遞 goroutine 永久掛在那裡——而 dispatcher 是單 goroutine 序列處理的，
// 等於整個通知系統停擺。
const (
	emailDialTimeout    = 10 * time.Second
	emailSessionTimeout = 45 * time.Second
)

// emailChannel 實現 SMTP 郵件投遞。
type emailChannel struct{}

func (emailChannel) Kind() string { return KindEmail }

// 郵件沒有平臺限流，但不該用它刷屏；給一個寬鬆的預設值。
func (emailChannel) DefaultRatePerMin() int { return 60 }

// 只掩碼密碼。SMTP 主機、帳號、收件人都不算秘密，掩碼它們只會讓編輯變麻煩。
func (emailChannel) SecretKeys() []string { return []string{"password"} }

// host/port 決定把密碼交給哪臺伺服器；tls 決定是否加密傳輸。三者任一變化都
// 要求重新表態密碼——順帶讓「關掉 TLS」這一步必須顯式帶上憑據，而不是順手一改。
func (emailChannel) DestinationKeys() []string { return []string{"host", "port", "tls"} }

func (emailChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "host") == "" {
		return errors.New("缺少 SMTP 伺服器地址")
	}
	port := cfgInt(cfg, "port")
	if port <= 0 || port > 65535 {
		return errors.New("SMTP 埠無效（應為 1-65535）")
	}
	if cfgString(cfg, "from") == "" {
		return errors.New("缺少發件人地址")
	}
	if len(cfgStrings(cfg, "to")) == 0 {
		return errors.New("至少需要一個收件人地址")
	}
	return nil
}

func (c emailChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	host := cfgString(cfg, "host")
	port := cfgInt(cfg, "port")
	from := cfgString(cfg, "from")
	to := cfgStrings(cfg, "to")
	username := cfgString(cfg, "username")
	password := cfgString(cfg, "password")
	implicitTLS := cfgBool(cfg, "tls")

	msg, err := buildEmailMessage(from, to, m)
	if err != nil {
		return 0, Permanent(err)
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	client, err := emailDial(ctx, addr, host, implicitTLS)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	// STARTTLS：對端支援就升級。明文會話下不能發憑據（見下面的 auth 說明）。
	if !implicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return 0, fmt.Errorf("STARTTLS 失敗: %w", err)
			}
		}
	}
	if username != "" {
		if err := client.Auth(smtp.PlainAuth("", username, password, host)); err != nil {
			// smtp.PlainAuth 會拒絕在未加密連線上傳送憑據（除非目標是 localhost）。
			// 這是**正確**的安全行為，不能繞過，但需要把原因翻譯清楚——
			// 否則使用者只會看到「unencrypted connection」而不知道該怎麼辦。
			if strings.Contains(err.Error(), "unencrypted connection") {
				return 0, Permanent(fmt.Errorf("拒發憑據：連線未加密。請啟用 TLS，或改用 465 埠(隱式 TLS)，或把「啟用 TLS」勾上 (%w)", err))
			}
			return 0, Permanent(fmt.Errorf("SMTP 認證失敗: %w", err))
		}
	}
	if err := client.Mail(from); err != nil {
		return 0, smtpStageError(fmt.Sprintf("發件人 %s 被拒", from), err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return 0, smtpStageError(fmt.Sprintf("收件人 %s 被拒", rcpt), err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return 0, fmt.Errorf("SMTP DATA 失敗: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return 0, fmt.Errorf("寫入郵件正文失敗: %w", err)
	}
	if err := w.Close(); err != nil {
		return 0, fmt.Errorf("提交郵件失敗: %w", err)
	}
	// Quit 失敗不影響「郵件已被伺服器接收」這個事實，因此忽略其錯誤。
	_ = client.Quit()
	// 郵件沒有長度截斷（HTML 正文全部發送），整批都算送達。
	return len(m.Items), nil
}

// emailDial 建立 SMTP 連線。
//
// implicitTLS=true 走 465 這類「連上即 TLS」的方式；false 走 25/587 明文建連後再
// STARTTLS。兩者不能混：對 465 埠發明文 greeting 會被直接斷開。
//
// 會話期限在**建連處**就設好（而非事後補設），因為 net/smtp 的 Client 把底層
// 連線藏在未匯出欄位裡，外部拿不到它；連線一旦交出去就只能靠預先設定的 deadline
// 兜底。這也順帶覆蓋了握手階段的阻塞。
// Control 掛 blockInternalDial 與 HTTP 系渠道共用同一道守衛。不掛的話 SMTP
// 就是整套 SSRF 防護的缺口：host 填 169.254.169.254 或 127.0.0.1 能直接連上，
// 而 smtp.NewClient 握手失敗時會把對端返回的那一行包進錯誤、經 last_error
// 由投遞歷史介面回顯，構成半盲讀原語；「連線被拒 vs 超時」的耗時差異還能
// 用來探測埠。撥號階段是最終生效點，也覆蓋 DNS 重綁定。
func emailDial(ctx context.Context, addr, host string, implicitTLS bool) (*smtp.Client, error) {
	d := &net.Dialer{Timeout: emailDialTimeout, Control: blockInternalDial}
	var conn net.Conn
	var err error
	if implicitTLS {
		conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("連線 SMTP 伺服器失敗: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(emailSessionTimeout))
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SMTP 握手失敗: %w", err)
	}
	return client, nil
}

// smtpStageError 按 SMTP 應答碼把某個階段的失敗分成「可重試」與「永久失敗」。
//
// 為什麼必須區分：SMTP 的 4xx 與 5xx 語義完全不同——
//   - 4xx（450 灰名單、451 本地錯誤、452 儲存不足）是**臨時**拒絕，
//     正規做法是稍後重試；尤其是灰名單，幾乎每次首次投遞都會遇到。
//   - 5xx（550 使用者不存在、553 地址非法）是永久拒絕，重試沒有意義。
//
// 若一律判永久失敗，一個啟用灰名單的郵件伺服器會讓**每一條**推送都在第一次
// 嘗試後落入 failed——而這類失敗恰恰是自動重試最該發揮作用的場景。
// 應答碼取錯誤文本的前三位數字；取不到碼時按可重試處理（寧可多試一次，
// 也不要因為解析不出就把可能的瞬時故障判死）。
func smtpStageError(what string, err error) error {
	code := smtpReplyCode(err.Error())
	if code >= 500 && code < 600 {
		return Permanent(fmt.Errorf("%s: %w", what, err))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// smtpReplyCode 從 SMTP 錯誤文本里取前導的三位應答碼，取不到返回 0。
// net/smtp 不匯出錯誤碼欄位，只能從文本里取；格式為「450 4.7.1 ...」。
func smtpReplyCode(text string) int {
	if len(text) < 3 {
		return 0
	}
	n, err := strconv.Atoi(text[:3])
	if err != nil {
		return 0
	}
	return n
}

// buildEmailMessage 組裝完整的 RFC 5322 郵件。
//
// 正文用 base64 編碼有兩個原因：一是 SMTP 規定單行不超過 1000 位元組，而 HTML
// 正文（尤其彙總郵件）很容易出現超長行；二是 base64 天然不會出現以 "." 開頭
// 的行，省去 SMTP 點號轉義的麻煩。
func buildEmailMessage(from string, to []string, m Message) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	// 中文主題必須做 RFC 2047 編碼，否則會被客戶端顯示成亂碼。
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", htmlTitle(m)))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	// 郵件沒有長度硬上限，因此不截斷正文。
	b.WriteString("\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(htmlBody(m, 0)))
	// base64 按 76 字元折行，符合 RFC 2045。
	for len(encoded) > 76 {
		b.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded + "\r\n")
	return b.String(), nil
}
