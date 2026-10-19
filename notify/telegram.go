package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// telegramTextLimit 是 Telegram sendMessage 的 text 欄位上限（字元數）。
const telegramTextLimit = 4096

// telegramChannel 實現 Telegram Bot API。
//
// 平臺特性：
//   - 鑑權全部在 URL path 裡（/bot<token>/sendMessage），無需加簽。
//   - 用 HTML 解析模式而不是 MarkdownV2：MarkdownV2 要求轉義 `_*[]()~`>#+-=|{}.!`
//     共 18 個字元，漏一個就整條訊息被拒；HTML 只需轉義 & < > 三個。
//   - 業務錯誤同樣藏在 HTTP 200 裡，靠 ok 欄位判斷。
type telegramChannel struct{}

func (telegramChannel) Kind() string { return KindTelegram }

// Telegram 單聊約 1 條/秒、群組 20 條/分鐘。取保守值。
func (telegramChannel) DefaultRatePerMin() int { return 20 }

// Bot Token 是完整憑據；chat_id 只是收件人，不算秘密（拿到它沒有 Token 也發不了訊息）。
func (telegramChannel) SecretKeys() []string { return []string{"bot_token"} }

// base_url 決定 Token 被髮往哪個 API 端點（如自建反代），改它必須重新表態 Token。
func (telegramChannel) DestinationKeys() []string { return []string{"base_url"} }

func (telegramChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "bot_token") == "" {
		return errors.New("缺少 Bot Token")
	}
	if cfgString(cfg, "chat_id") == "" {
		return errors.New("缺少 Chat ID")
	}
	if base := cfgString(cfg, "base_url"); base != "" {
		if err := validateHTTPURL(base); err != nil {
			return fmt.Errorf("API 地址無效: %w", err)
		}
	}
	return nil
}

func (c telegramChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	endpoint, err := telegramEndpoint(cfg)
	if err != nil {
		return 0, Permanent(err)
	}
	text, kept := telegramHTML(m)
	payload := map[string]any{
		"chat_id":                  cfgString(cfg, "chat_id"),
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": false,
	}
	raw, err := doJSON(ctx, "POST", endpoint, nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("解析 Telegram 回應失敗: %w (%s)", err, snippet(raw))
	}
	if res.OK {
		return kept, nil
	}
	// 429 是限流，退避後重試有效；其餘（400 引數錯、401 token 錯、403 被拉黑、
	// 404 chat 不存在）都是配置問題，重試不會自愈。
	if res.ErrorCode == 429 {
		return 0, fmt.Errorf("Telegram 限流: %s", res.Description)
	}
	return 0, Permanent(fmt.Errorf("Telegram 返回錯誤 %d: %s", res.ErrorCode, res.Description))
}

// telegramEndpoint 拼出 sendMessage 地址。base_url 留空時用官方 API，
// 非空時用於自建 Bot API 反代（國內網路下的常見需求）。
func telegramEndpoint(cfg map[string]any) (string, error) {
	base := cfgString(cfg, "base_url")
	if base == "" {
		base = "https://api.telegram.org"
	}
	base = strings.TrimSuffix(base, "/")
	token := cfgString(cfg, "bot_token")
	raw := base + "/bot" + token + "/sendMessage"
	u, err := url.Parse(raw)
	if err != nil {
		// 不透傳 err：地址裡含 Bot Token，且此時連 addr 都不該回顯。
		return "", fmt.Errorf("拼接 API 地址失敗（API 地址：%s）", redactRequestTarget(base))
	}
	return u.String(), nil
}

// telegramHTML 渲染 HTML 正文，返回正文與實際寫入的條目數（見 Channel.Send）。
func telegramHTML(m Message) (string, int) {
	var b strings.Builder
	b.WriteString("<b>" + telegramEscape(markdownTitle(m)) + "</b>\n")
	if m.Batch {
		// Telegram 的上限是**字元數**，所以打包也按字元計量（runeSize）。
		footer := ""
		if m.HomeURL != "" {
			footer = fmt.Sprintf("\n\n<a href=\"%s\">在平臺中檢視全部</a>", telegramEscapeAttr(m.HomeURL))
		}
		kept := packItemCount(m.Items, telegramTextLimit, telegramReservedRunes, footer, runeSize, func(it Item, idx int) string {
			return telegramBatchLine(it, idx+1)
		})
		items := m.Items[:kept]
		b.Reset()
		b.WriteString("<b>" + telegramEscape(telegramBatchTitle(m, items, len(m.Items))) + "</b>")
		for i, it := range items {
			b.WriteString("\n" + telegramEscape(telegramBatchLine(it, i+1)))
		}
		b.WriteString(footer)
		return TruncateHTML(b.String(), telegramTextLimit), kept
	}
	if len(m.Items) == 0 {
		return b.String(), 0
	}
	it := m.Items[0]
	if it.IsStatusChange() {
		b.WriteString(fmt.Sprintf("\n<b>狀態變更</b>：%s → %s",
			telegramEscape(StatusLabel(it.FromStatus)), telegramEscape(StatusLabel(it.ToStatus))))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		b.WriteString("\n<b>型別</b>：" + telegramEscape(it.VulnClass))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		b.WriteString("\n<b>資產</b>：" + telegramEscape(a))
	}
	if s := OneLine(it.Summary, maxSummaryRunes); s != "" {
		b.WriteString("\n<b>摘要</b>：" + telegramEscape(s))
	}
	if it.DetailURL != "" {
		b.WriteString(fmt.Sprintf("\n\n<a href=\"%s\">檢視詳情</a>", telegramEscapeAttr(it.DetailURL)))
	}
	return TruncateHTML(b.String(), telegramTextLimit), 1
}

// telegramReservedRunes 預留給訊息標題與可能出現的截斷提示（按字元計）。
const telegramReservedRunes = 160

// telegramBatchLine 渲染彙總裡的一條（未轉義，由呼叫方統一轉義）。
func telegramBatchLine(it Item, idx int) string {
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		return fmt.Sprintf("%d. %s · %s — %s", idx, SeverityLabel(it.Severity), it.Title(), a)
	}
	return fmt.Sprintf("%d. %s · %s", idx, SeverityLabel(it.Severity), it.Title())
}

// telegramBatchTitle 渲染彙總訊息的標題行。條數用的是**本條實際包含**的條數，
// 而不是本批總數——否則讀者會以為訊息頭寫的數字就是全部。
func telegramBatchTitle(m Message, items []Item, total int) string {
	title := fmt.Sprintf("漏洞彙總 · 共 %d 條", total)
	if extra := total - len(items); extra > 0 {
		title += fmt.Sprintf("（顯示前 %d 條，其餘 %d 條下一條繼續）", len(items), extra)
	}
	if m.WindowMinutes > 0 {
		title = fmt.Sprintf("近 %d 分鐘 · %s", m.WindowMinutes, title)
	}
	return title
}

// telegramEscape 轉義 HTML 文本內容。
// Telegram 只認這三種實體，轉義後 &amp; 之類的已有實體會被二次轉義——這正是
// 期望行為：我們要顯示的是原始字元，不是讓使用者注入 HTML。
func telegramEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// telegramEscapeAttr 轉義 HTML 屬性值。在文本轉義之外還要處理引號——
// URL 裡帶引號會提前閉合 href 屬性，把後面的內容變成注入點。
func telegramEscapeAttr(s string) string {
	s = telegramEscape(s)
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
