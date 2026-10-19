package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// goalsDefaultTmpl is the built-in EDITABLE body (段 [A]) of the goals-decomposer
// prompt, seeded into agent_prompts. No template vars are used today.
const goalsDefaultTmpl = `你是滲透測試目標分解器。你的職責是從使用者輸入中識別出**最終要達成的結果**，而不是規劃攻擊步驟。

**第一步（拆分目標之前先做）：抽取操作約束**
從「任務目標 / 任務描述」裡識別操作員對【可以做什麼、不可以做什麼操作】的明確規定，呼叫 set_constraints 逐條登記（如果描述、目標中不涉及操作約束可以不進行提取操作約束）：
- type=deny：禁止的操作（如「不掃埠」「不得對生產環境做寫/刪操作」「禁止爆破」「不碰某子域」）。
- type=allow：明確允許/限定的操作範圍（如「只允許被動偵察」「僅針對某域名」）。
- 約束 ≠ 目標，也 ≠ 攻擊步驟：它是對操作行為邊界的規定。
- **約束必須【自包含、寫死具體目標】**：把「當前目標/當前埠/當前IP/當前域名/本站」這類**指代詞**替換成任務目標/描述裡的**具體值**。約束會被單獨注入到執行階段的提示裡，脫離上下文後指代詞無法判斷指誰。
  例：目標是 https://abc.example.net → 寫「只允許測試 abc.example.net」而不是「只允許測試當前目標」；「僅測目標埠 443，不掃其他埠」而不是「只測當前埠」。若原文只說「當前目標」但目標地址已明確，就把地址填進去。
- **只登記目標/描述裡【明確寫出或強調】的約束，嚴禁臆造**；拿不準型別時用 deny（更保守）。
- 若目標/描述裡確實沒有任何操作約束，則**不要**呼叫 set_constraints。
登記完約束（如有）後，再進行下面的目標拆分。

**目標 = 最終可交付/可核驗的結果**

**不是目標的內容（禁止列為子目標）**：
- 資訊收集、偵察、端點掃描
- 漏洞分析與驗證過程
- 攻擊步驟、利用手段
- 結果驗證步驟

**拆分原則**：
- 使用者描述的最終目標只有一個 → 輸出一個
- 存在多個**相互獨立**的最終交付物 → 分別列出
- 能對應明確漏洞類的標註 vulnclass；資訊收集/業務邏輯類目標留空
- 嚴禁臆造使用者未提及的目標

呼叫 set_goals 提交結果。`

// goalsScopeTail is the code-owned tail appended after the editable goals body
// WHEN an asset store + task context are available. It teaches the decomposer to
// also lift the explicit asset scope out of the goal/description and register it
// via add_task_scope. Kept in code (not the DB-editable body) so it always applies
// on released DBs and can't be edited away — same pattern as the trafficTool tail.
const goalsScopeTail = `

**額外職責：登記測試資產範圍**
除拆分目標外，你還要從「任務目標 / 任務描述」裡識別出**明確給出的測試資產範圍**，呼叫 add_task_scope 登記（本任務的授權邊界，也是資產測試覆蓋度的分母）。**最小範圍原則：只登記使用者明確點到的那一個目標，絕不擅自放大。**
- 目標是 URL 或帶主機名的地址（如 https://xxx.example.com/path、app.example.com）→ 取其**完整主機名**，kind=subdomain，value=完整主機名。
  例：目標 https://a1b2c3.lab.example.net/path → kind=subdomain，value=a1b2c3.lab.example.net（**不是** example.net）。
  **嚴禁**把帶子域的主機名縮成根域名——看到 xxx.example.com 就登記整個 example.com 會把範圍擴到使用者目標之外，違背最小範圍原則。
- 僅當使用者給的就是**裸根域名、且不含任何子域**（如直接寫 example.com），或明確說“整個站點 / 所有子域 / 全域名” → 才用 kind=root_domain，value=example.com。
- 純 IP 或網段 → kind=ip / cidr，value=IP 或 CIDR。
- **不要**登記公司範圍（company）——任務剛建立、資產系統裡通常還沒有這家公司，登記不上，公司級範圍交由後續 plan 階段處理。
其它規則：
- 只登記**目標/描述裡明確寫出**的範圍；嚴禁臆造或推斷未提及的域名/IP。
- reason 簡述依據來自哪句話，便於審計。
- 若目標/描述中沒有任何明確資產範圍，則**不要**呼叫 add_task_scope。
先用 add_task_scope 登記範圍（如有），再呼叫 set_goals 提交目標。`

// GoalSpec is one decomposed objective.
type GoalSpec struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
}

// DecomposeGoals asks the LLM to break a pentest task goal into discrete,
// independently-verifiable objectives (each becomes a goal node). Returns nil if
// no provider is configured or the call yields nothing — the caller then falls
// back to a rule-based split so goal nodes always exist.
//
// prov is supplied by the caller (rather than built here from a Config) so goal
// decomposition rides the SAME provider instance as the rest of the engine — it
// shares the rate limiter, gets recorded by llmrec, and participates in LLM
// failover instead of quietly bypassing all three.
//
// desc is the task's free-text description (背景：靶標範圍/flag 數量/交戰說明等).
// It is fed alongside the goal so the decomposer no longer splits blind — the
// prompt still forbids inventing anything the two texts don't state.
//
// emit, when non-nil, receives every LLM step (thinking/tool_use/result) with
// Worker="planner" so the round-0 goal-decomposition activity is visible in the UI.
//
// as + taskID, when non-nil/positive, wire the add_task_scope tool so the
// decomposer can register the explicit asset scope it extracts from the goal.
//
// ts is the task's exploration store: set_goals writes the decomposed goal nodes
// straight into it (the same managed tool the main agent uses to add goals at
// runtime). The returned specs are read back from the store so callers can emit
// per-goal activity and detect the "LLM produced nothing" case for their fallback.
func DecomposeGoals(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	return DecomposeGoalsWithProvider(ctx, prov, dataDir, goalText, desc, as, ts, taskID, false, 0, emit)
}

// DecomposeGoalsWithProvider is the task-runtime variant used when a task has an
// ordered provider chain. It preserves the same tools and write behavior while
// letting the caller own provider selection/failover. maxTokens is the profile's
// per-reply output cap (0 = send none).
func DecomposeGoalsWithProvider(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, nonStreaming bool, maxTokens int, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	// 目標拆解是一次性呼叫：不掛 transcript store，所以 agentcore 不會往 ctx 上掛
	// session id（它只在有 writer 時才掛，見 agentcore.Prompt）。而按 session-id 頭
	// 做提示快取/粘性路由的閘道器（opencode zen 缺 x-opencode-session 直接 400
	// MissingSessionID）讀的就是 ctx 上這個值——不補就是「對話正常、拆解 400」。
	// 顯式掛一個穩定 id：同一探索的拆解請求共享它（利於命中快取），且命名與
	// planner/worker 不衝突，能被 llmrec.parseSession 正確歸因。
	if ts != nil {
		ctx = transcript.WithSessionID(ctx, fmt.Sprintf("exp%d-goals", ts.ID()))
	}
	// worker="goals" tags the goal nodes' provenance; ts/taskID let set_goals link
	// each goal under the task root. This is the catalog's real set_goals tool, so a
	// web-edited description/schema on it applies here too.
	tsx := &ToolSet{as: as, ts: ts, taskID: taskID, worker: "goals"}
	// Description rides in the user message (same channel as the goal), NOT via the
	// {{.EngagementDescription}} template var — else a prompt that references the var
	// would inject the description twice. System prompt stays pure static instructions.
	sys := renderSystem("goals", goalsDefaultTmpl, GoalsVars{DataDir: dataDir, Now: nowStr()})
	// set_constraints 始終可用(不依賴 asset store):正文已含「先抽操作約束再拆目標」這步
	// (可在 agent 編輯頁改措辭),這裡只需接上工具。
	tools := []actool.CoreTool{tsx.setGoals(), tsx.setConstraints()}
	// Wire add_task_scope only when we have a real asset store + task to write to.
	// The scope-extraction tail is appended in lockstep so the prompt never asks for
	// a tool that isn't present.
	if as != nil && taskID > 0 {
		tools = append(tools, tsx.addTaskScope())
		sys += goalsScopeTail
	}
	userMsg := "任務目標：\n" + goalText
	if d := strings.TrimSpace(desc); d != "" {
		userMsg += "\n\n任務描述（背景資訊，可能含靶標範圍/flag 數量/交戰說明；僅供參考，不要臆造其中未提及的內容）：\n" + d
	}
	// Use captureRun so every LLM step is emitted as an activity record (visible in
	// the plan tab under the round-0 marker). Falls back gracefully when emit is nil.
	captureEmit := func(r db.Activity) {
		if emit != nil {
			r.Worker = "planner"
			emit(r)
		}
	}
	captureRun(ctx, agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{sys},
		Tools:                  tools,
		PermissionMode:         acperm.ModeBypass,
		DisableBackgroundTasks: true,
		// 3 步(抽約束 → 登記範圍 → 拆目標)各需一次工具呼叫,給足回合避免收尾前漏調 set_goals。
		MaxTurns:     8,
		NonStreaming: nonStreaming, // 該 profile 選非流式時走 Provider.Complete
		MaxTokens:    maxTokens,    // 0 = 不發上限,由服務端預設值決定
	}, userMsg, captureEmit)
	// set_goals persisted the goals directly; read them back so the caller sees what
	// was written (empty slice ⇒ the LLM produced nothing ⇒ caller falls back).
	if ts == nil {
		return nil
	}
	nodes, _ := ts.ListByKind(db.KindGoal, 10000)
	var out []GoalSpec
	for _, n := range nodes {
		var p struct {
			Text      string `json:"text"`
			VulnClass string `json:"vulnclass"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if strings.TrimSpace(p.Text) != "" {
			out = append(out, GoalSpec{Text: p.Text, VulnClass: p.VulnClass})
		}
	}
	return out
}
