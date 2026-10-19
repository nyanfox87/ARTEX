package agent

import (
	"context"
	"fmt"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// MainAgent is the thin human-interface orchestrator (docs §4.2 / §7). The human
// chats with it; it observes (read tools), and steers by injecting hints
// (→planner) or direct high-priority intents (→frontier). It does NOT run the
// autonomous intent-generation loop (that is the planner's job).
type MainAgent struct {
	findingRecorder FindingRecorder
	prov            llm.Provider
	model           string
	tx              *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window          int                                    // context window in tokens (for compaction)
	windowFn        func() int                             // optional dynamic task-chain minimum
	maxTurns        int                                    // max agent turns per run (0 = unlimited)
	proxyAddr       string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert     string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch       WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir         string                                 // shared work dir (surfaced in prompt as artifact-output target)
	steerWork       func(intentID int64, msg string) error // engine callback: steer a running work (nil = off)
	nonStreamingFn  func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn    func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn     func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
}

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (m *MainAgent) SetNoaEnabled(fn func() bool) { m.noaEnabledFn = fn }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (m *MainAgent) SetNonStreaming(fn func() bool) { m.nonStreamingFn = fn }

func (m *MainAgent) nonStreaming() bool { return m.nonStreamingFn != nil && m.nonStreamingFn() }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (m *MainAgent) SetMaxTokens(fn func() int) { m.maxTokensFn = fn }

func (m *MainAgent) maxTokens() int {
	if m.maxTokensFn == nil {
		return 0
	}
	return m.maxTokensFn()
}

func NewMainAgent(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *MainAgent {
	return &MainAgent{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns}
}

func (m *MainAgent) SetCompactionWindowResolver(fn func() int) { m.windowFn = fn }

func (m *MainAgent) compactionWindow() int {
	if m.windowFn != nil {
		return m.windowFn()
	}
	return m.window
}

// SetProxy points the main agent's WebFetch at the recording proxy plus the CA
// cert it trusts to verify HTTPS through it (empty addr = direct).
func (m *MainAgent) SetProxy(addr, caCert string) { m.proxyAddr, m.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the main agent (off by default).
func (m *MainAgent) SetWebSearch(o WebSearchOpts) { m.webSearch = o }

// SetSteerWork wires the engine callback that lets the main agent's steer_work
// tool inject a mid-run course-correction into a running work (nil = tool off).
func (m *MainAgent) SetSteerWork(fn func(intentID int64, msg string) error) { m.steerWork = fn }

// mainAgentDefaultTmpl is the built-in EDITABLE body (段 [A]) of the main agent
// prompt, seeded into agent_prompts. Goal is a {{.Goal}} template var; the 中間
// 產物輸出規約 tail is code-owned (artifactSpec), appended after rendering.
const mainAgentDefaultTmpl = `你是一個授權滲透測試系統的"主 agent"，是人類操作員的介面。你不親自探索、也不自主連續生成意圖（那是規劃者的工作）。你的職責：

1. 觀察：用 graph_overview / list_findings / list_facts / list_assets / get_worker_output 回答人關於當前進展的問題。
2. 操舵（把人的意圖落到系統）：
   - 人想"改方向/強調某類漏洞/重點某區域" → 用 add_hint 寫提示（規劃者下次會讀到）。
   - 人想"立刻測某個具體目標" → 用 add_intent 直接注入一條高優先順序意圖（priority 8-10）。系統會自動把已完成的任務拉回執行態、讓 worker 領這條意圖執行，跑完即回到已完成狀態。
     **當任務目標已全部達成時**（graph_overview 裡 goals 均為 met）：下發前先判斷這條意圖背後是否隱含一個"新的、要達成的結果"。若隱含，用一句話把你猜測的目標複述給人，並**反問是否要登記為正式目標**——人要 → 用 set_goals 登記（任務隨後進入常規規劃、規劃者會自主往下推進）；人不要 / 只是想臨時探一下 → 只 add_intent 下發這一條，worker 執行完任務即回到已完成狀態（不會自主繼續）。若這條意圖明顯只是一次性查證、不隱含新目標，直接 add_intent 即可，不必每次都問。
   - 人想"對某條正在執行的意圖(work)即時糾偏（別再走 X、聚焦 Y）" → 用 steer_work（不打斷、不丟已有進展，worker 下一步動作前生效）；先用 get_worker_output 看它在幹嘛。方向整個錯了則改用 add_intent 另下新意圖。
   - 人想"新增一個要達成的最終目標" → 用 set_goals 增補目標。系統會把該目標寫入任務圖並**自動把已完成/暫停的任務拉回執行態繼續跑**（規劃者隨後會據此重新判斷是否達成），無需人工再點恢復。
   - 人想"增/改測試約束（允許/禁止某類操作，如『僅測當前埠』『禁止爆破』『只做被動偵察』）" → 用 set_constraints 登記（type=allow 允許 / type=deny 禁止）。約束會在下一輪規劃時注入 planner/worker 的提示詞以框定探索邊界；也可在總覽「約束管理」裡增刪改。
3. 用人話簡潔回覆，說明你做了什麼。

當前任務目標：{{.Goal}}

不要編造發現；只根據工具返回的真實資料回答。`

func mainAgentSystem(goal, dataDir, workDir string) string {
	body := renderSystem("mainagent", mainAgentDefaultTmpl, MainVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Chat handles one human message and returns the assistant reply. emit, if
// non-nil, receives each execution step (thinking / tool_use / tool_result /
// text / result) so the main-agent session shows its work — exactly like the
// worker/planner sessions — not just the final answer.
func (m *MainAgent) Chat(ctx context.Context, taskID int64, mainSeg int, as *db.AssetStore, ts *db.ExplorationStore, goal, message string, emit func(db.Activity), notify, resume func(), notifyGoal, notifyHint func([]string)) (string, error) {
	tsx := NewToolSet(ts, "human")
	tsx.SetFindingRecorder(m.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.SetNotify(notify)         // 通用喚醒（無專用回撥的寫操作走它，debounced）
	tsx.SetResumeTask(resume)     // set_goals 新增目標 → 把已完成/暫停的任務拉回 running
	tsx.SetNotifyGoal(notifyGoal) // set_goals 新增目標 → 給 planner 記一條「人新增了目標：…」觸發
	tsx.SetNotifyHint(notifyHint) // add_hint 新增提示 → 給 planner 記一條「人新增了 N 條戰略提示：…」觸發
	tsx.steerWork = m.steerWork   // enable steer_work tool (nil = unavailable)
	// 領域工具 + 基礎預設工具集（Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash）
	// 資產覆蓋度功能關閉時剔除 add_task_scope/list_untested_assets（不入 prompt）。
	base := append(tsx.DropCoverageTools(tsx.MainAgentTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "mainagent", base)
	defer cleanup()
	// 本任務的工作目錄 <workDir>/tasks/<taskID>，先建好。
	mainDir := ensureRunDir(m.workDir, taskID, 0)
	ctx = intercept.WithReviewWorkingDirectory(ctx, mainDir)
	system, boundary := deferredSystem(mainAgentSystem(goal, m.workDir, mainDir), def)
	opts := agentcore.Options{
		Provider:        m.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // 走記錄代理留痕；載入代理 CA 驗證 MITM 重籤的 HTTPS 證書
		WebFetchProxy:   m.proxyAddr,
		WebFetchCACert:  m.proxyCACert,
		// 聯網搜尋(可選)。ddgs 無需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
		// WebSearchProxy 是獨立出口代理(http/https/socks5)，與記錄流量的 MITM 代理無關；空則直連。
		EnableWebSearch:       m.webSearch.Enabled,
		WebSearchBackend:      m.webSearch.Backend,
		BraveSearchAPIKey:     m.webSearch.BraveKey,
		TavilySearchAPIKey:    m.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: m.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  m.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   m.webSearch.DeepSeekModel,
		WebSearchProxy:        m.webSearch.Proxy,
		BashEnv:               proxyEnv(m.proxyAddr, m.proxyCACert), // Bash 子命令預設走代理+信任 CA
		WorkingDir:            mainDir,                              // 本任務工作目錄 <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(mainDir),
		MaxTurns:              m.maxTurns,                             // 0 = unlimited (configurable in agent management)
		Compaction:            compactionConfig(m.compactionWindow()), // long chats stay within the window
		Todos:                 actool.NewTodoStore(),                  // 會話級臨時待辦（TodoWrite），純規劃用，退出即丟
		// 命中預算(步數)→ SDK 跑收尾:向使用者輸出一句進展總結。Prompt 與收尾輪數可後臺編輯(預設 10 輪)。
		Settlement:   wrapupSettlement("mainagent", nil),
		NonStreaming: m.nonStreaming(), // 該 profile 選非流式時走 Provider.Complete
		MaxTokens:    m.maxTokens(),    // 0 = 不發上限,由服務端預設值決定
	}
	if m.tx != nil { // persist raw human↔AI conversation; one accumulating file per segment
		opts.Transcript = m.tx
		// Segment 0 keeps the legacy "exp%d-main" name so existing transcripts still
		// load; each new session (seg>=1) gets its own file for a clean context.
		opts.SessionID = fmt.Sprintf("exp%d-main", ts.ID())
		if mainSeg > 0 {
			opts.SessionID = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
		}
	}
	// 實驗功能:開啟後由 noa 接管上下文壓縮(歸檔集中在 <workDir>/noa/<SessionID> 下,持久)。
	// session id 與 transcript 同規則(分段感知),使歸檔與恢復對齊。
	noaSession := fmt.Sprintf("exp%d-main", ts.ID())
	if mainSeg > 0 {
		noaSession = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
	}
	enableNoa(&opts, m.noaEnabledFn, m.workDir, noaSession, noaWarn(noaSession))
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close()
	// reload the prior conversation from the transcript so the agent has context
	// across turns (each Chat is a fresh session; without this it can't see earlier
	// messages). First turn: no file yet → Resume loads nothing and proceeds.
	if m.tx != nil {
		_ = s.Resume(opts.SessionID)
	}
	// C2: this session is fresh each turn; re-unlock skill-gated MCPs from prior
	// Skill() calls in the reloaded history so revealed tools stay callable.
	seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
	text, _, err := captureRunSession(ctx, s, message, func(r db.Activity) {
		if emit != nil {
			r.Worker = "mainagent"
			emit(r)
		}
	})
	return text, err
}
