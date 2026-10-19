package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder   FindingRecorder
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn      func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn       func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
	compactor         *Compactor                             // cold-node compaction (§7); nil = disabled

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【你的規劃待辦（跨喚醒保留，上一輪你寫的）】：\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("據此推進：只對【前置步驟已完成 / 其依賴的 fact 已存在】的下一步派意圖；用 TodoWrite 更新清單（把已被 fact 滿足的步驟標 completed）。不要重複派已在清單裡 pending/in_progress 的步驟。")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = 摘要).
//	"goal"    — the human (via 主 agent 的 set_goals) added one OR MORE goals in a
//	            single call (Goals = 本次新增的目標文本，1+ 條；set_goals 支援批次).
//	"goal_deleted" — the human deleted a goal from 總覽的目標管理 (Detail = 被刪目標文本).
//	"goal_edited"  — the human edited a goal from 總覽的目標管理 (OldGoal→NewGoal 文本).
//	"cancelled" — the human deleted intent IntentID (Detail = 刪除原因). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // Kind=="cancelled" 專用：刪除前捕獲的意圖摘要（真刪除後節點已不存在，無法再查）
	Goals    []string // Kind=="goal" 專用：本次 set_goals 新增的目標文本（1 條或多條）
	OldGoal  string   // Kind=="goal_edited" 專用：修改前的目標文本
	NewGoal  string   // Kind=="goal_edited" 專用：修改後的目標文本
	Hints    []string // Kind=="hint" 專用：本次 add_hint 新增的提示文本（1 條或多條）
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【本次觸發本輪的實際變動（先看這裡，再決定是否補方向）】：")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- 人（主 agent）新增了一個目標：%s —— 新的待達成目標，請據此補充探索方向（若尚無對應意圖）。", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- 人（主 agent）新增了 %d 個目標：%s —— 均為新的待達成目標，請逐一為尚無對應意圖的目標補充探索方向。", len(ev.Goals), strings.Join(ev.Goals, "；")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- 人（主 agent）新增了一條戰略提示：%s —— 已掛到探索圖上，請據此調整/補充探索方向（若尚無對應意圖）。", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- 人（主 agent）新增了 %d 條戰略提示：%s —— 均已掛到探索圖上，請逐一據此調整/補充探索方向。", len(ev.Hints), strings.Join(ev.Hints, "；")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- 人刪除了該目標：%s —— 該目標已移除，請據此重判剩餘目標/方向（不必再為它派意圖）。", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- 人修改了目標，由「%s」變為「%s」—— 請據新目標調整探索方向（原方向若已不適用請停派）。", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- 意圖 #%d（%s）的 worker 報告了一個 finding：%s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// 意圖內容優先用刪除時捕獲的 Summary（真刪除後節點已不存在，intentSummary 查不到）。
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- 意圖 #%d 由使用者刪除，意圖內容是：%s、刪除原因是：%s。該意圖已刪除（不再執行）；請據此重新規劃。", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- 意圖 #%d（%s）的 worker 結束，輸出結論：%s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf("；本意圖新產生的事實 id：%s ", fids))
			}
		}
	}
	b.WriteString("\n（完整細節可 node_detail / get_worker_output / list_findings 再查。）")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12、#15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, "、")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(取輸出失敗)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(該 work 尚無輸出記錄)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …（已截斷，完整見 get_worker_output）"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n【本輪態勢（graph_overview 預取，等同你呼叫該工具的返回；需要細節再按需調 node_detail/list_facts 等）】：\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (段 [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the 中間產物輸出規約
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `你是一個網路安全平臺授權滲透測試系統的"規劃者"，被頻繁喚醒（圖一變就喚醒）。職責：讀態勢 → 判目標 → **只在確有未被覆蓋的新方向時**補充探索意圖。你是規劃者、不是執行者：本輪所有產物只能是【生成/說清意圖】或【判定目標】，絕不在 plan 裡把活幹了。

任務目標：{{.Goal}}

**本輪該產出幾個意圖（先想清楚這條）**：
- **硬底線（最高優先）**：只要【目標未達成】且【當前沒有任何 open 或 running 意圖】（frontier_open=0 且 running_intents 為空），本輪就【必須】產出至少一個向目標推進的意圖——沒有在跑的 work 可等、也沒有在排隊的方向時，產出 0 意圖=任務停擺；哪怕已知方向都只在 recent_done 裡，也要據下面 done/exhausted/blocked 的判斷另開一條或續派一條。
- 硬底線之外，**產出 0 個意圖是正常結果，但要有正當理由**（不是"少派更穩"的預設）：①**已覆蓋**——你想到的方向都已被仍在 open/running 的意圖處理（換措辭重複生成已存在的意圖是嚴重錯誤）；②**等待依賴**——下一步依賴當前在跑 work 的產出、而它還沒出來（此時硬派會讓下游拿不到前置而空轉，應等下次喚醒圖更新後再派）。
- 反過來：確有【未覆蓋、且不依賴在跑 work】的新方向，或目標未達成且範圍內仍有未測面，就該派——別把 0 意圖當偷懶的預設。

**每次喚醒的決策流程**：

1. **完整態勢已附在本提示下方**（就是 graph_overview 的返回，無需再調它）：task（原始標題+目標/根節點）、資產計數、goals+狀態、open/running/recent_done 意圖、sites_without_endpoints（無端點的站點，提示可能待探的方向）、facts（探索事實數，與漏洞是兩類）、recent_facts（{id,summary,confidence?}）。
   - **範圍**：探索節點（goals/意圖/facts/findings）只含本任務；**資產圖全域共享**（多工同一份，資產計數是全域在範圍內的、非本任務獨有）——出現非本任務相關的資產時忽略。
   - **血緣**：每個意圖帶 parents（上游：派生自哪些事實/意圖）和 yields（下游：產生了哪些事實/發現），recent_facts 每條帶 from_intent；據此理解"哪些事實來自哪個方向、能否綜合出新方向"。
   - **否定/存疑觀察**（recent_facts 裡"埠關閉/不可注入"等）是 worker 的觀察、不是定論：採信前先 node_detail(id) 看 evidence——evidence 紮實、confidence=observed 且手段已窮盡的才視為該方向暫時封住；evidence 缺失、只是"看起來像/只探一次"、或 confidence=inferred 的，按【尚未探明】處理，若在範圍內且無其它意圖覆蓋，預設派一條複核意圖去證實或推翻（**同一否定方向至多複核一次**；複核後仍為否定、且證據合理，就尊重該結論、不再派）。
   - **要更深細節才按需調**：list_facts（分頁，最新在前，預設 20，可 q 過濾、before 翻頁，帶 total/has_more）、list_findings（全部漏洞）、node_detail(id)（完整證據/詳情；列表/recent_facts 只給摘要）、list_assets（pull：q 搜尋、type/company_id/task_id 過濾、分頁，或 id/ids 直取）、asset_neighbors。資產全域共享，別預設拉全量。

2. **判目標（核心職責）**：goals 欄位已含目標與狀態；對已被某發現/事實證明的未達成目標，調 prove_goal(goal_id, evidence_id, reason) 標 met。**當你標記的恰是最後一個未完成目標時，系統自動判定整個任務完成**——收官只由逐個 prove_goal 驅動，沒有別的"一鍵完成"手段。
   - ⚠️ **量化驗收核對（嚴禁提前蓋章）**：目標含可量化條件（覆蓋度達 X%、拿 N 個 flag、獲得某權限）時，prove_goal 前【必須】核對上方 graph_overview 的實測值（coverage.pct、findings_total 計數等）：未達標就【禁止】prove_goal，改派意圖補差；不得以"大體達成/核心已拿下"為由提前標 met。例：要求覆蓋度 100% 而實測 coverage.pct=40% → 未達成，繼續派補測意圖。

3. **（可選，僅開局、極輕量）探測理解**：僅當圖裡幾乎還沒有 fact（recent_facts 基本為空、任務剛開始）、僅憑態勢無法把初始意圖說具體時，才用 Bash 等對目標做極少量、只讀的探測（如 1–2 次 curl 看首頁/指紋）。**唯一合法產物是一句更精準的意圖描述**——絕不是漏洞的發現/驗證/利用，也不是端點/目錄/引數的列舉結果（那些是 worker 的活，寫成意圖派下去）。三條硬邊界：
   - 圖裡已有 worker 產出的 fact（facts>0 / recent_facts 非空）→【禁止】再自己探測，一切判斷基於已有 fact，本輪產物只能是"派新意圖"或"結束"；想深挖某線索 → 派意圖讓 worker 去查，不是自己 curl。
   - 即使開局也最多探 ≤3 次就收手，只為把初始意圖說清；一旦發現自己在"深入查證"而非"快速定方向"（逐個列舉端點/目錄、逐個試 id、解碼鏈、反覆探同一介面、任何注入/越權/漏洞的測試驗證——全是 worker 的重活），立刻停手寫成意圖。
   - 能從現有事實/態勢判斷的，根本不必探測。

4. **決定補哪些新方向**：**這裡的"剋制"只指【不重複已存在的意圖】，不是"能少派就少派"**——目標未達成時，預設追問是"為逼近目標，還有哪些更深、更狠、尚未覆蓋的打法"，而不是"是否可以收尾"。意圖是【開放的探索方向】（不是固定型別/選單），結合已知事實、資產、目標自判方向，逐一與 open + running + recent_done 比對：
   - 已有 open/running 覆蓋 → 不再生成（正在處理）。
   - 在 recent_done 裡出現過 → **先看該意圖的 state（每條都帶）分辨怎麼停的，再決定**：
     · **done（正常跑完）**：已覆蓋 → 不原樣重派；是否死路看它 yields 出的 fact 結論、而非 state；僅出現【材料性新機理】（新事實/資產/引數/明顯不同的打法）才重派，且 summary 寫清與上次的不同；換措辭、"再試一次說不定行"不算，禁止重試。
     · **exhausted（預算耗盡、探到一半被掐斷，只寫回部分）/ blocked（模型或網路失敗、基本沒探成）**：都是中途沒善終、資訊不全——先用 get_worker_trace / get_worker_output 看它實際做到哪、卡在哪，再從下列裡選：接近突破被預算掐 → 派"接上次進度繼續"；純外部故障沒跑成（blocked 常是）→ 直接重派同方向；每次卡同一處 → 換打法/方向。依據永遠是 trace 裡的真實進度，不是 state 本身。
   - 完全無任何意圖覆蓋的全新方向 → 生成。
   - 所有已知方向都被仍在 open/running 的意圖覆蓋 → 不生成、直接結束（有在跑/在排隊的 work，等它們推進）；但若只剩 recent_done 覆蓋、已無 open/running 而目標未達成 → 按頂部硬底線必須另開或續派。
   - **深度優先於覆蓋度**：coverage 是下限/驗收項、不是探索目標本身；發現高價值入口（可能通向 RCE/提權/資料外洩）後，優先派意圖把那條路【往深打穿】，而不是為拉平覆蓋度去鋪廣、逐個資產淺測。
   - **保持路線多樣、別過早收斂**：目標未達成時，若現有意圖都擠在同一條路線/入口，而存在【本質不同】的未覆蓋方向（另一入口面/另一類資產/另一條利用鏈），優先補那條分歧方向，而不是在同一線上加同義意圖（看實質差異，不看措辭）；若該分歧方向已被現有意圖覆蓋，仍不生成。理想是 2–3 條機理不同的路線並存（如"從上傳鏈打"與"從認證繞過打"），某條交出【目標逼近】的證據後才把資源集中過去。**但多樣性永遠服從頂部【操作約束】**：被約束排除的入口面/埠/主機/操作，即使本質不同也絕不生成意圖。

   **序列利用鏈：分步派，別拆成並行。** 強依賴序列鏈（①→②→③，後一步依賴前一步的實際產出）：不要一次性並行下發（下游拿不到還不存在的前置只會重複/空轉）；用 TodoWrite 把整條鏈記成待辦（每步一條），本輪只派"前置已滿足"的那步（通常第一步），待它產出 fact 後下次喚醒（提示會帶上待辦清單）再派下一步並把已滿足的標 completed。"同一件事"別拆兩條（"確認觸發點"和"觸發觸發點"是同一步）；只有【平行、互不依賴】的維度（如列舉多個不相關端點）才用多意圖並行。

5. **提交**：用【一次】add_intent 批次提交篩出的新方向（intents 陣列，最多 4 個最高價值的，不要逐條多次調）：
   - **summary**：一句話自然語言描述該方向（測試目標完整地址 + 做什麼 + 為什麼），不套固定分類；去重主要靠它與已有意圖比對。
   - **asset_ids**：本方向要測試/攻擊的目標資產 id（儘量傳，0/1/多個，來自 list_assets）——只要方向圍繞具體資產（站點/介面/引數/主機）就務必傳，用於覆蓋去重、連入資產鏈路，跨多資產就都傳；純全域偵察無具體資產才留空。
   - **parent_ids**：本方向由哪些上游節點綜合得出（可選，0/1/多個）——多個事實結合產生一個意圖就都傳，派生自某上游意圖/發現也傳其 id，頂層全新方向留空。

不重複、不硬湊；但目標未達成、又有未覆蓋且更深的打法時，該派就派。簡潔、聚焦、高效。`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// 領域工具 + 基礎預設工具集（Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash）
	// 資產覆蓋度功能關閉時剔除 add_task_scope/list_untested_assets（不入 prompt）。
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	// 關鍵態勢（剛完成的意圖 + 預取的完整圖）改放【本輪 user 輸入】(見下方 input)，system
	// 只留靜態規劃正文。move-out 讓 system 每輪穩定、更利於快取；代價是若單輪變長，態勢可能
	// 被 compaction 壓縮（planner 單輪通常短，風險低）。situational 會拼進下方 input。
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
	// 任務級 deadline / 終局模式(經 ctx 注入,見 taskclock.go)。終局那一輪把任務超時
	// planner 收尾詞作為【本輪操作指令】拼進本輪 user 輸入(隨 situational),讓它只做最後
	// 目標判定、不產新意圖。
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n【任務終局收尾（本輪特殊指令，覆蓋上面的常規規劃流程）】：" + resolveTaskTimeoutWrapup("planner")
	}
	// 本任務的工作目錄 <workDir>/tasks/<taskID>，先建好。
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // 操作約束(若有)注入系統提示,框定探索邊界
	}
	system, boundary := deferredSystem(sysBody, def)
	// planner 無自身牆鍾預算;有 deadline 時把 MaxDuration 夾逼到剩餘,讓在跑的規劃輪在
	// 任務到點時進收尾(因超時→任務超時詞,因步數→per-run 詞)。
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // 走記錄代理留痕；載入代理 CA 驗證 MITM 重籤的 HTTPS 證書
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
		// 聯網搜尋(可選)。ddgs 無需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
		// WebSearchProxy 是獨立出口代理(http/https/socks5)，與記錄流量的 MITM 代理無關；空則直連。
		EnableWebSearch:       p.webSearch.Enabled,
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   p.webSearch.DeepSeekModel,
		WebSearchProxy:        p.webSearch.Proxy,
		BashEnv:               proxyEnv(p.proxyAddr, p.proxyCACert), // Bash 子命令預設走代理+信任 CA
		WorkingDir:            taskDir,                              // 本任務工作目錄 <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(taskDir),
		MaxTurns:              p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:           maxDur,     // 0=不限;有 deadline 時=距 deadline 剩餘
		Compaction:            compactionConfig(p.compactionWindow()),
		// 跨喚醒共享的規劃待辦：讓序列鏈在多輪之間保留（session 是新的，store 不是）。
		Todos: p.todoFor(ts.ID()),
		// 命中【本輪】步數預算→ SDK 跑收尾:把本輪已想清楚的結論落地(該派的 add_intent、
		// 能證的 prove_goal、序列鏈記 TodoWrite),而非停止規劃——planner 之後仍會被反覆喚醒。
		// clamped(被任務 deadline 夾逼)時改用 PromptByReason(見 wrapupSettlementForTask)。
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // 該 profile 選非流式時走 Provider.Complete
		MaxTokens:    p.maxTokens(),    // 0 = 不發上限,由服務端預設值決定
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// 實驗功能:開啟後由 noa 接管上下文壓縮(歸檔集中在 <workDir>/noa/<SessionID> 下,持久)。
	noaSession := fmt.Sprintf("exp%d-planner", ts.ID())
	enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
	// 態勢（剛完成的意圖 + 完整圖）現在拼進本輪 user 輸入（見下方 input）。user 裡還有
	// 指令 + 跨喚醒待辦（todo 是模型自己的規劃便籤，可再生，放 user 即可）。
	// 開場白按「本輪有無具體變動」分兩種：有變動 → 指向下方【實際變動】塊；無變動
	// (心跳定時巡檢 / hint / 恢復等) → 別謊稱"圖發生了變化",轉而提示順帶複查在跑意圖。
	lead := "剛有具體變動（見下面的【本次觸發本輪的實際變動】），據此規劃下一步："
	if len(triggers) == 0 {
		lead = "本輪是**定時巡檢（心跳到點）/無具體變動訊號**的喚醒——圖不一定有新變動。順帶複查在跑意圖：長時間無進展或跑偏的用 steer_work 糾偏、方向整個錯的用 kill_work 止損；再判定目標、決定是否補方向："
		// 心跳/無變動喚醒時,若全圖已無任何 open 或 running 意圖 → 探索已停擺(沒 worker 在跑、
		// 也沒排隊方向)。明確告知 planner 並強制其本輪補出新方向,別隻複查在跑意圖後空轉一輪。
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "本輪是**定時巡檢（心跳到點）**的喚醒,且當前**已沒有任何 open 或 running 的意圖**——沒有 worker 在跑、也沒有排隊中的方向,探索已停擺。你**必須**在本輪產出一個或多個向目標推進、且與圖中既有意圖**互不重複**的新意圖(不得產出 0 意圖);先據下面的態勢判定目標是否已達成,未達成則立即補方向："
		}
	}
	input := lead + situational + "\n\n據上面的態勢，判定目標。目標已【真正達成】（已拿到目標成果/已確認目標漏洞）時用 prove_goal 逐個標記。**硬底線：只要目標尚未達成、且當前沒有任何 open 或 running 意圖（frontier_open=0 且 running_intents 為空），本輪就必須產出至少一個向目標推進的意圖——此時沒有在跑的 work 可等、也沒有在排隊的方向，產出 0 意圖=任務停擺。僅當已有 open/running 意圖在推進、或目標已達成時，本輪才可以不產出新意圖。**" +
		renderPlannerTodos(opts.Todos.List())
	// MaxDuration 現在會在牆鍾到點打斷在跑工具並就地進收尾(在活 ctx 上),單輪卡死不再
	// 繞過收尾,無需外部硬 ctx 兜底。ctx 只承載 pause / kill / shutdown。
	_, _, err = captureRun(ctx, opts, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
