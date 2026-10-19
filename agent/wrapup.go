package agent

import (
	"strings"

	"github.com/Autumn-27/norma/harness"
)

// 收尾提示詞(wrap-up / settlement prompt):當 agent 因【步數耗盡(MaxTurns)】或
// 【超時(run_seconds/MaxDuration)】被終止時,SDK 的 settlement 階段會注入這段提示,
// 讓 agent 先把已識別但未寫回的內容落庫、再輸出一句總結,避免爛尾。
//
// 每個 agent 的收尾提示詞可在後臺按需覆蓋(存 agents.wrapup_prompt),留空則用這裡的
// 內建預設。僅【提示詞正文】可編輯;停用哪些工具、收尾自身給幾輪預算屬程式碼固定策略。

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.
var WrapupOverride func(agentKey string) (string, bool)

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.
var WrapupMaxTurnsOverride func(agentKey string) (int, bool)

// 內建預設收尾提示詞,按 agent key 索引。worker 複用歷史上硬編碼的 settleWrapUpPrompt
// (定義在 worker.go),planner/mainagent 各有一版;未命中的(自定義 agent)走通用兜底。
var wrapupDefaults = map[string]string{
	"worker":    settleWrapUpPrompt,
	"planner":   plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// wrapupTurnDefaults: 各 agent 收尾階段【自身】的輪數預算內建預設(可被後臺 >0 覆蓋)。
// 均給 10 輪,保證收尾階段有足夠步數落庫。未命中走 genericWrapupTurns。
var wrapupTurnDefaults = map[string]int{
	"worker":    10,
	"planner":   10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "你本輪規劃的步數即將用盡——注意只是【這一輪】結束,系統之後仍會隨態勢變化再次喚醒你繼續規劃,並非任務終止,你無需在此收束整個規劃。請把本輪已經想清楚的結論落地、別讓這一輪白跑,但也【不要為了收尾硬湊意圖】(本輪 0 個意圖仍是完全正常的結果)：(1) 若已判斷出【當前就該派發】的探索方向,用一次 add_intent 批次提交(想好的別憋著不發);(2) 對已被某發現/事實證明達成的目標,調 prove_goal 標記 met(別漏判);(3) 若識別出需要分步的序列利用鏈,用 TodoWrite 記下,便於下次喚醒接著派。做完直接結束本輪,無需輸出總結文本。"

const mainAgentWrapUpDefault = "你的步數即將用盡,本次互動就要結束。不要再發起新的探索/操作。請**單獨用一句話純文本**向使用者總結當前進展、關鍵結論,以及建議的下一步。"

const genericWrapUpDefault = "你即將因預算耗盡被終止。請先把已完成但未落庫的結果寫回,再**單獨用一句話純文本**總結你做了什麼、得到哪些關鍵結論(這句會作為本次執行的結果展示)。"

// WrapupDefault returns the built-in default wrap-up prompt for an agent key —
// used by the admin UI as the "restore default" value and empty-field placeholder.
func WrapupDefault(agentKey string) string {
	if d, ok := wrapupDefaults[agentKey]; ok {
		return d
	}
	return genericWrapUpDefault
}

// WrapupTurnsDefault returns the built-in wrap-up turn budget for an agent key —
// used by the admin UI as the "0 = default N" hint.
func WrapupTurnsDefault(agentKey string) int {
	if n, ok := wrapupTurnDefaults[agentKey]; ok {
		return n
	}
	return genericWrapupTurns
}

// resolveWrapup returns the effective wrap-up prompt: the DB override (if set and
// non-empty) over the built-in default.
func resolveWrapup(agentKey string) string {
	if WrapupOverride != nil {
		if t, ok := WrapupOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return WrapupDefault(agentKey)
}

// resolveWrapupTurns returns the effective wrap-up turn budget: a positive DB
// override over the built-in per-agent default.
func resolveWrapupTurns(agentKey string) int {
	if WrapupMaxTurnsOverride != nil {
		if v, ok := WrapupMaxTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return WrapupTurnsDefault(agentKey)
}

// wrapupSettlement builds the settlement config for an agent's run. Prompt and the
// turn budget are admin-editable per agent; disabled tools are code-owned policy so
// a user can't edit away the "stop probing" guardrail. Resolved fresh each run
// (reads DB live), so edits apply on the next run without a restart.
func wrapupSettlement(agentKey string, disabledTools []string) *harness.Settlement {
	return &harness.Settlement{
		Prompt:        resolveWrapup(agentKey),
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
}

// ---------- 任務級超時收尾詞（見 docs/任務級超時與收尾設計.md）----------
//
// 與 per-run 收尾詞是【兩套】：per-run 是"你這一次 run 的預算用完了"；任務超時是
// "整個任務到點、即將結束"。語義常相反（尤其 planner：per-run 說"別停繼續規劃"，
// 任務超時說"到點停止規劃、做最後判定"）。只給 worker/planner 配置。

// WrapupTaskTimeoutOverride / …TurnsOverride：任務超時收尾詞與輪數的 DB 覆蓋
// （wire 到 agents.task_timeout_wrapup_prompt / _max_turns，僅 worker/planner）。
var (
	WrapupTaskTimeoutOverride      func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)
)

var taskTimeoutWrapupDefaults = map[string]string{
	"worker":  workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**整個任務已到達超時上限，即將結束**（不是你這次 run 的預算，是整場探索到點了）。這是最後機會：(1) 把你已識別但還沒寫回的內容【全部】落庫——新資產 insert_assets、探索結論/事實 record_fact、確認漏洞 report_finding；(2) 不要再啟動任何新命令/探測；(3) **最後單獨用一句話純文本**總結你在本意圖上的關鍵結論。"

const plannerTaskTimeoutDefault = "**整個任務已到達超時上限，即將結束**（不是本輪，是整個任務終止）。請基於當前【全部】事實與發現，做最後一次目標判定：對已被證據證明達成的目標調 prove_goal 標記 met（別漏判）。**不要再生成任何新意圖**（此時派意圖也不會再被執行）。判定完即收束，無需輸出總結文本。"

// TaskTimeoutWrapupDefault 返回某 agent 的任務超時內建預設收尾詞（供後臺佔位/恢復預設）。
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey] // 未配置(mainagent/chat)返回空串
}

// resolveTaskTimeoutWrapup：DB 覆蓋(非空) > 內建預設。空串表示該 agent 無任務超時詞
// （非 worker/planner），此時呼叫方應回退 per-run 詞。
func resolveTaskTimeoutWrapup(agentKey string) string {
	if WrapupTaskTimeoutOverride != nil {
		if t, ok := WrapupTaskTimeoutOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return TaskTimeoutWrapupDefault(agentKey)
}

func resolveTaskTimeoutTurns(agentKey string) int {
	if WrapupTaskTimeoutTurnsOverride != nil {
		if v, ok := WrapupTaskTimeoutTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return resolveWrapupTurns(agentKey) // 預設沿用 per-run 輪數
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
//   - clamped=true  → 本次 run 被任務 deadline 夾逼：因 Timeout 收尾=任務到點→任務超時詞；
//     因 MaxTurns 收尾=夾逼視窗內步數先耗盡、任務還剩幾分鐘→回落 per-run 詞。
//   - clamped=false → 任務還早：兩種 reason 都用 per-run 詞（即退化為 wrapupSettlement）。
//
// 交給 harness 的 PromptByReason 在收尾時按【實際】reason 現場挑，無 build 時錯配。
func wrapupSettlementForTask(agentKey string, disabledTools []string, clamped bool) *harness.Settlement {
	perRun := resolveWrapup(agentKey)
	st := &harness.Settlement{
		Prompt:        perRun, // 兜底(也是非 clamped 時兩種 reason 的取值)
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
	if clamped {
		if tt := resolveTaskTimeoutWrapup(agentKey); tt != "" {
			st.PromptByReason = map[harness.TerminalReason]string{
				harness.ReasonTimeout:  tt,     // 任務到點
				harness.ReasonMaxTurns: perRun, // 步數先耗盡、任務還剩時間
			}
			st.MaxTurns = resolveTaskTimeoutTurns(agentKey)
		}
	}
	return st
}
