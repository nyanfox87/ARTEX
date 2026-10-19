package notify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Filter 是 notification_channels.filter 這一 JSONB 列的契約：渠道例項的過濾條件。
// 所有欄位都可選，預設即「不過濾」——這正是畸形配置的兜底語義，見 ParseFilter。
type Filter struct {
	// MinSeverity 是最低級別門檻（low/medium/high/critical），空=不設門檻。
	MinSeverity string `json:"min_severity"`
	// TaskIDs / AssetIDs 為空陣列表示不限；非空則要求事件與它有交集。
	TaskIDs  []int64 `json:"task_ids"`
	AssetIDs []int64 `json:"asset_ids"`
	// VulnClassInclude 為空表示全收；非空則要求 vulnclass 命中其中任一關鍵詞。
	// VulnClassExclude 命中任一關鍵詞即排除（排除優先於包含）。
	// 匹配方式為大小寫不敏感的子串——比正則安全：使用者配錯正則不會讓渠道靜默失效。
	VulnClassInclude []string `json:"vulnclass_include"`
	VulnClassExclude []string `json:"vulnclass_exclude"`
	// OnStatusChange 決定該渠道是否接收漏洞狀態變更事件（僅 realtime 模式有意義）。
	OnStatusChange bool `json:"on_status_change"`
}

// ParseFilter 解析渠道過濾配置。
//
// **永不返回 error。** 這是刻意的設計選擇：過濾條件配置畸形時一律退化為零值
// Filter（= 不過濾 = 全部命中），因為對一個漏洞通知系統來說，**多推一條遠好過
// 靜默漏掉一條高危**。讓解析失敗變成「不推送」，等於給使用者一個看起來配好了、
// 實際什麼都不推的渠道——這是最糟的失敗模式。
func ParseFilter(raw []byte) Filter {
	var f Filter
	if len(raw) == 0 {
		return f
	}
	// 解析失敗時 f 保持零值，即不過濾。
	_ = json.Unmarshal(raw, &f)
	return f
}

// ValidMinSeverity 報告 s 是否為合法的級別門檻（空串表示不設門檻）。
func ValidMinSeverity(s string) bool {
	if s == "" {
		return true
	}
	_, ok := severityRank[s]
	return ok
}

// Validate 校驗過濾配置裡**取值受限**的欄位，供儲存渠道時呼叫。
//
// 為什麼必須在寫入時攔：Match 對未知門檻的判定是 `rank >= 0`，恆為真——
// 也就是說 min_severity 打錯一個字（"hgih"），過濾器會**靜默失效**變成
// 「全推」。這與本包「寧可多推不可漏推」的取捨方向一致（不會漏），
// 但後果是使用者以為自己在做分級推送、實際把全部漏洞灌進群裡，
// 而且沒有任何跡象提示他配錯了。這類「靜默降級」正應該在入口處攔掉。
//
// 注意 Validate 只用於**寫入**路徑。讀取路徑仍走 ParseFilter 的寬容語義，
// 這樣歷史資料裡已經存在的壞值不會讓渠道整個讀不出來。
func (f Filter) Validate() error {
	if !ValidMinSeverity(f.MinSeverity) {
		return fmt.Errorf("最低級別 %q 無效，可選：low / medium / high / critical，或留空表示不限", f.MinSeverity)
	}
	return nil
}

// Match 判定一個事件是否應投遞到帶有該過濾條件的渠道。
//
// **永不返回 error**，理由同 ParseFilter：任何內部異常都按「命中」處理。
// 判定順序：事件型別 → 級別門檻 → 任務/資產範圍 → 漏洞型別關鍵詞。
func Match(f Filter, s Snapshot) bool {
	// 狀態變更事件只有顯式開啟的渠道才接收。預設關，因為絕大多數使用者
	// 期望「推送」指的是「發現新漏洞」，而不是流水賬式地跟進每個狀態流轉。
	if s.Kind == EventFindingStatusChanged && !f.OnStatusChange {
		return false
	}
	if !AtLeast(s.Severity, f.MinSeverity) {
		return false
	}
	if len(f.TaskIDs) > 0 && !slices.Contains(f.TaskIDs, s.TaskID) {
		return false
	}
	if len(f.AssetIDs) > 0 && !intersectsInt(f.AssetIDs, s.AssetIDs) {
		return false
	}
	// 排除優先：命中任一排除關鍵詞即出局，即便同時命中了包含列表。
	if len(f.VulnClassExclude) > 0 && containsAnyFold(s.VulnClass, f.VulnClassExclude) {
		return false
	}
	if len(f.VulnClassInclude) > 0 && !containsAnyFold(s.VulnClass, f.VulnClassInclude) {
		return false
	}
	return true
}

func intersectsInt(a, b []int64) bool {
	// 小集合線性掃描即可；兩邊的量級都是「人手勾選的幾十個」，
	// 建 map 的開銷大於收益。
	for _, v := range b {
		if slices.Contains(a, v) {
			return true
		}
	}
	return false
}

// containsAnyFold 報告 s 是否包含 keywords 中任一關鍵詞（大小寫不敏感）。
func containsAnyFold(s string, keywords []string) bool {
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw != "" && strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
