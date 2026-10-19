// Package notify 實現漏洞發現的 IM / 郵件推送渠道適配層。
//
// 分層：本包是**葉子包**，只依賴標準庫。它不認識資料庫、不認識 server。渠道配置
// 以 map[string]any 傳入（對應 notification_channels.config 這一 JSONB 列），
// 待推送內容以 Message 傳入。這樣拆開的好處是：簽名計算、UTF-8 截斷、過濾匹配這些
// 真正容易出錯的地方可以脫離 PostgreSQL 單測，宿主只需在 server 側做編排。
//
// 併發約定：Channel 的實現必須**無狀態**。同一個 Channel 例項會被多個渠道配置
// （甚至同一渠道的多個機器人例項）併發複用，所有憑據一律從 cfg 引數傳入，
// 不允許把 webhook URL 之類的東西快取進實現自身的欄位。
package notify

// 渠道型別標識。取值同時是 notification_channels.kind 的合法集合，由 server 側
// 白名單校驗（與 findings.status 同理，不用 DB CHECK，方便後續加渠道）。
const (
	KindDingTalk = "dingtalk" // 釘釘自定義機器人
	KindFeishu   = "feishu"   // 飛書(含 Lark)自定義機器人
	KindWeCom    = "wecom"    // 企業微信群機器人
	KindWebhook  = "webhook"  // 通用 Webhook：自定義方法/頭/JSON 模板
	KindTelegram = "telegram" // Telegram Bot API
	KindEmail    = "email"    // SMTP 郵件
)

// 事件型別，對應 notification_events.kind。
const (
	EventFindingCreated       = "finding_created"
	EventFindingStatusChanged = "finding_status_changed"
)

// InitKind 是 config 裡為空的 kind 的兜底值。
const InitKind = KindDingTalk

// severityRank 把漏洞級別對映成可比較的序數。未知級別返回 0，因此任何
// min_severity 設定都會把未知級別擋在外面——存疑時不推，避免誤報刷屏。
var severityRank = map[string]int{
	"low":      1,
	"medium":   2,
	"high":     3,
	"critical": 4,
}

// SeverityRank 返回級別的序數；未知級別返回 0。
func SeverityRank(severity string) int { return severityRank[severity] }

// SeverityLabel 返回帶 emoji 的中文級別名，用於訊息標題與卡片配色。
// 未知級別原樣回顯，不臆造。
func SeverityLabel(severity string) string {
	switch severity {
	case "critical":
		return "🔴 嚴重"
	case "high":
		return "🟠 高危"
	case "medium":
		return "🟡 中危"
	case "low":
		return "🔵 低危"
	default:
		return severity
	}
}

// StatusLabel 把處置狀態翻譯成中文，用於狀態變更訊息。
func StatusLabel(status string) string {
	switch status {
	case "pending":
		return "待處理"
	case "in_progress":
		return "處理中"
	case "confirmed":
		return "已確認"
	case "resolved":
		return "已處理"
	case "fixed":
		return "已修復"
	case "false_positive":
		return "誤報"
	case "ignored":
		return "忽略"
	case "duplicate":
		return "重複"
	case "risk_accepted":
		return "風險接受"
	default:
		return status
	}
}

// AtLeast 判斷 severity 是否達到 min 門檻。min 為空表示不設門檻，一律通過。
// 注意未知 severity 的序數為 0，會被任何非空 min 拒掉（見 severityRank 註釋）。
func AtLeast(severity, min string) bool {
	if min == "" {
		return true
	}
	return SeverityRank(severity) >= SeverityRank(min)
}
