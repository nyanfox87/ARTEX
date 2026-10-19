package notify

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Channel 是一個通知渠道的介面卡。實現必須**無狀態**：同一個例項會被多個渠道
// 配置併發複用，憑據一律從 cfg 引數傳入。
type Channel interface {
	// Kind 返回渠道型別標識，須與登錄檔的鍵一致。
	Kind() string
	// Validate 在儲存配置時呼叫，校驗必填欄位與格式。返回的錯誤會直接展示給
	// 配置者，所以文案要說明「缺哪個欄位」而不是泛泛的「配置無效」。
	Validate(cfg map[string]any) error
	// Send 投遞一次訊息，返回**實際送達的條目數**與錯誤。
	//
	// 為什麼要返回條數：各平臺都有訊息長度上限，彙總訊息裝不下整批時會被截斷。
	// 若呼叫方無條件把整批標記為已送達，被截掉的那些條目就消失了——訊息裡看不到、
	// 投遞歷史裡也顯示成功，沒有任何地方能發現漏洞從未發出。返回 kept 後，
	// 呼叫方只標記前 kept 條，其餘留待下一批。
	//
	// 返回錯誤表示投遞失敗，其中 *PermanentError 表示不該重試。
	// 失敗時 kept 無意義，呼叫方應忽略它。
	Send(ctx context.Context, cfg map[string]any, m Message) (int, error)
	// DefaultRatePerMin 返回該渠道官方建議的每分鐘投遞上限，作為新建渠道例項
	// 時的預設限流值。返回 0 表示無已知限制。
	DefaultRatePerMin() int
	// SecretKeys 返回該渠道配置裡屬於憑據的鍵名。API 回顯時這些鍵的值會被掩碼，
	// 更新時收到掩碼值則保留庫中的原值。只有實現自己清楚哪些欄位算憑據
	// （企業微信的整個 Webhook 地址就是憑據，而釘釘的只是其中的 secret），
	// 所以這個知識必須由渠道提供，不能由上層猜測。
	SecretKeys() []string
	// DestinationKeys 返回該渠道配置裡決定「訊息發往哪裡」的鍵名。
	//
	// 與 SecretKeys 一樣是安全相關的東西：目標地址與憑據是兩套獨立欄位，
	// 若允許「只改地址、憑據原樣保留」，任何能改渠道配置的人都能把庫裡的真憑據
	// 發到自己控制的伺服器，渠道配置的掩碼就完全失去意義。
	// 詳見 PrepareConfigUpdate。
	DestinationKeys() []string
}

// registry 是渠道登錄檔。刻意用顯式字面量而不是 init() 自注冊：這樣「有哪些渠道」
// 在一個地方就能看全，且新增渠道會在編譯期暴露遺漏，而不是靠執行時副作用。
var registry = map[string]Channel{
	KindDingTalk: dingTalkChannel{},
	KindFeishu:   feishuChannel{},
	KindWeCom:    weComChannel{},
	KindWebhook:  webhookChannel{},
	KindTelegram: telegramChannel{},
	KindEmail:    emailChannel{},
}

// Get 按型別取渠道實現。
func Get(kind string) (Channel, bool) {
	c, ok := registry[kind]
	return c, ok
}

// ValidKind 報告 kind 是否為受支援的渠道型別。
func ValidKind(kind string) bool {
	_, ok := registry[kind]
	return ok
}

// Kinds 返回全部受支援的渠道型別，按字典序排列（供 UI 下拉穩定展示）。
func Kinds() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PermanentError 標記一個不該重試的投遞失敗：憑據錯誤、目標拒絕、請求體非法等。
// 重試只對瞬時故障（網路抖動、限流、對端 5xx）有意義；對永久失敗反覆退避重試
// 既不會成功，又會把真正的錯誤刷沒在重試日誌裡。
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent 把 err 標記為永久失敗。err 為 nil 時返回 nil，
// 方便寫成 `return Permanent(someCheck())`。
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent 報告 err 鏈上是否帶有永久失敗標記。
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

// ---- 配置讀取helper ----
//
// 渠道配置來自資料庫的 JSONB 列，經 encoding/json 反序列化後是 map[string]any，
// 數值一律是 float64、陣列是 []any。下面這些 helper 統一這層轉換，並容忍使用者
// 在 UI 裡留空導致的型別偏差（如把埠填成字串）。

// cfgString 取字串配置項，前後空白一律裁掉——從網頁表單複製貼上很容易帶上。
func cfgString(cfg map[string]any, key string) string {
	v, ok := cfg[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// cfgInt 取整數配置項，相容 float64（JSON 預設）與字串兩種來源。
func cfgInt(cfg map[string]any, key string) int {
	switch v := cfg[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// cfgBool 取布林配置項，相容字串 "true"/"1"。
func cfgBool(cfg map[string]any, key string) bool {
	switch v := cfg[key].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "yes"
	default:
		return false
	}
}

// cfgStrings 取字串陣列配置項，自動裁空白並丟棄空串。
func cfgStrings(cfg map[string]any, key string) []string {
	raw, ok := cfg[key].([]any)
	if !ok {
		// 也接受單個字串，方便只有一個值時的表單提交。
		if s := cfgString(cfg, key); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// cfgMap 取字串對映配置項（如自定義 HTTP 頭），鍵值都裁空白，丟棄空鍵。
func cfgMap(cfg map[string]any, key string) map[string]string {
	raw, ok := cfg[key].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		out[k] = s
	}
	return out
}
