package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Worker is an LLM work agent (docs §4.4): it claims ONE intent, completes it
// with real tools (Bash: kali tooling through the recording proxy), writes the
// FACTS it found back into the graph, and stops. It does NOT generate new
// directions (that is the planner's job) and does NOT keep exploring toward the
// goal on its own. Multiple workers run concurrently as goroutines.
// WebSearchOpts is the web-search backend selection the server pushes into each
// agent (planner/worker/main). Enabled=false leaves the web_search tool off.
// Backend is "ddgs" (no key), "brave-free" (BraveKey required), "tavily"
// (TavilyKey required), or "deepseek" (DeepSeek* required, filled from the
// active LLM profile). It maps directly onto agentcore.Options.
// Proxy is a dedicated egress proxy for the search request (http/https/socks5),
// independent of the traffic-recording MITM proxy — set it when the search endpoint
// is only reachable via a VPN/SOCKS proxy. Empty = direct.
//
// 注意 deepseek 後端與其它三個的性質不同：DeepSeek 沒有可直接呼叫的搜尋介面，
// 搜尋只存在於其 Anthropic 相容 messages 介面內部(web_search_20250305 server
// tool)，因此每次搜尋會消耗一次模型呼叫，且搜尋請求由 DeepSeek 服務端發出——
// 不經過本機 Proxy，也不會進流量留痕。
type WebSearchOpts struct {
	Enabled   bool
	Backend   string
	BraveKey  string
	TavilyKey string
	Proxy     string
	// DeepSeek* 來自當前啟用的 LLM 配置(僅 anthropic 格式的 DeepSeek 官方端點)，
	// 不單獨配置，隨 LLM 配置切換而變。
	DeepSeekBaseURL string
	DeepSeekAPIKey  string
	DeepSeekModel   string
}

type Worker struct {
	findingRecorder FindingRecorder
	prov            llm.Provider
	model           string
	workDir         string
	proxyAddr       string
	proxyCACert     string            // recording proxy's CA cert path (for WebFetch HTTPS verify)
	webSearch       WebSearchOpts     // web_search tool backend selection (off by default)
	tx              *transcript.Store // raw LLM conversation persistence (nil = off)
	window          int               // context window in tokens (for compaction)
	windowFn        func() int        // optional dynamic task-chain minimum
	maxTurns        int               // max agent turns per run (0 = unlimited)
	// runTimeout is the wall-clock budget for the main exploration of one intent
	// (0 = unlimited). When it fires, the run is cut and a settlement round is
	// forced so already-identified facts get written back instead of being lost.
	runTimeout time.Duration
	// extraTools are host-provided tools (e.g. traffic query, oast) appended to
	// the worker's graph write-back tools.
	extraTools []actool.CoreTool
	// injectConstraints resolves whether this task's operation constraints get
	// injected into the worker system prompt. Read per run so the settings toggle
	// takes effect without rebuilding the agent. nil = inject (default).
	injectConstraints func() bool
	// nonStreamingFn resolves whether this run uses the non-streaming (Complete)
	// path. Read per run so a profile/task toggle takes effect without rebuilding
	// the agent. nil = streaming (default).
	nonStreamingFn func() bool
	// noaEnabledFn resolves whether this run uses the experimental noa context-
	// compression mechanism. Read per run, like nonStreaming. nil = off (built-in
	// compaction).
	noaEnabledFn func() bool
	// maxTokensFn resolves the per-reply output cap in tokens, on the same
	// per-run basis. nil or 0 = send no cap and let the endpoint decide.
	maxTokensFn func() int
}

// WorkerSessionID returns the stable transcript key used by a worker intent.
// Worker slots are reusable, so the intent id (rather than work#N) is the
// session identity. Keep this helper public so the Worker message API and UI
// can refer to exactly the conversation that will be resumed.
func WorkerSessionID(explorationID, intentID int64) string {
	return fmt.Sprintf("exp%d-worker-i%d", explorationID, intentID)
}

const workerChatMarkerPrefix = "<!-- ARTEX_WORKER_CHAT:"

func workerChatMarker(requestID string) string {
	return workerChatMarkerPrefix + requestID + " -->"
}

func hasWorkerChatMessage(messages []llm.Message, requestID string) bool {
	marker := workerChatMarker(requestID)
	for _, message := range messages {
		if message.Role == llm.RoleUser && strings.Contains(message.Text(), marker) {
			return true
		}
	}
	return false
}

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default). Read per
// run so a profile or task-chain toggle takes effect without rebuilding.
func (w *Worker) SetNonStreaming(fn func() bool) { w.nonStreamingFn = fn }

func (w *Worker) nonStreaming() bool { return w.nonStreamingFn != nil && w.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (w *Worker) SetNoaEnabled(fn func() bool) { w.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (w *Worker) SetMaxTokens(fn func() int) { w.maxTokensFn = fn }

func (w *Worker) maxTokens() int {
	if w.maxTokensFn == nil {
		return 0
	}
	return w.maxTokensFn()
}

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the worker system prompt. nil = inject (default).
func (w *Worker) SetConstraintInject(fn func() bool) { w.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (w *Worker) wantConstraints() bool { return w.injectConstraints == nil || w.injectConstraints() }

// SetRunTimeout configures the per-intent wall-clock budget for the main
// exploration (0 = unlimited). When it fires, the SDK settlement phase still runs
// so facts are never lost to a timeout. Safe to call before Execute.
func (w *Worker) SetRunTimeout(run time.Duration) {
	w.runTimeout = run
}

// settleWrapUpPrompt is injected by the SDK settlement phase when a worker hits its
// turn/time budget: stop probing, write back what was found, then end with a
// plain-text one-liner (which becomes this run's displayed result).
const settleWrapUpPrompt = "你即將因預算耗盡被終止。不要再執行任何命令/探測。請依次：(1) 把你上面已識別但還沒寫回的內容逐條寫回——新資產用 insert_assets、探索結論/事實用 record_fact、確認漏洞用 report_finding；(2) **最後單獨用一句話純文本**總結你做了什麼、得到哪些關鍵結論（這句會作為本次執行的結果展示，務必輸出）。"

func NewWorker(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int, extra ...actool.CoreTool) *Worker {
	return &Worker{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, extraTools: extra}
}

// defaultToolsExcept returns actool.DefaultTools() minus the named tools (by
// CoreTool.Name()). Used to trim SDK default tools an agent shouldn't have.
func defaultToolsExcept(exclude ...string) []actool.CoreTool {
	drop := make(map[string]bool, len(exclude))
	for _, n := range exclude {
		drop[n] = true
	}
	all := actool.DefaultTools()
	out := make([]actool.CoreTool, 0, len(all))
	for _, t := range all {
		if !drop[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}

func (w *Worker) SetCompactionWindowResolver(fn func() int) { w.windowFn = fn }

func (w *Worker) compactionWindow() int {
	if w.windowFn != nil {
		return w.windowFn()
	}
	return w.window
}

// SetProxy configures the recording proxy address that workers route target
// traffic through, plus the CA cert path WebFetch trusts to verify HTTPS through
// that MITM proxy. Empty addr disables the hint.
func (w *Worker) SetProxy(addr, caCert string) { w.proxyAddr, w.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for this worker (off by default).
func (w *Worker) SetWebSearch(o WebSearchOpts) { w.webSearch = o }

// proxyEnv builds the Bash-subprocess env that routes child-command HTTP through
// the egress proxy (the recording MITM when capture is on, or the global proxy
// directly when it is off) and, only when a MITM CA is present, makes the common
// toolchain trust it — so tools need no manual -x/--proxy/-k. Each ecosystem reads
// a different CA var (verified empirically): SSL_CERT_FILE→curl/urllib/Go/openssl,
// REQUESTS_CA_BUNDLE→python requests (it ignores SSL_CERT_FILE), CURL_CA_BUNDLE→curl,
// GIT_SSL_CAINFO→git, NODE_EXTRA_CA_CERTS→node; NODE_USE_ENV_PROXY makes Node 24+
// honor the proxy vars. ALL_PROXY is set too so a socks5 egress proxy (which curl
// only reads from ALL_PROXY, not HTTP(S)_PROXY) works in the capture-off path.
// Empty proxyAddr → nil (direct, unchanged env).
func proxyEnv(proxyAddr, caCert string) []string {
	if proxyAddr == "" {
		return nil
	}
	env := []string{
		"HTTP_PROXY=" + proxyAddr, "HTTPS_PROXY=" + proxyAddr,
		"http_proxy=" + proxyAddr, "https_proxy=" + proxyAddr,
		"ALL_PROXY=" + proxyAddr, "all_proxy=" + proxyAddr, // socks5 egress: curl reads only this
		"NODE_USE_ENV_PROXY=1", // Node 24+: honor HTTP(S)_PROXY in built-in fetch/http
	}
	if caCert != "" {
		env = append(env,
			"SSL_CERT_FILE="+caCert,
			"CURL_CA_BUNDLE="+caCert,
			"REQUESTS_CA_BUNDLE="+caCert,
			"GIT_SSL_CAINFO="+caCert,
			"NODE_EXTRA_CA_CERTS="+caCert,
		)
	}
	return env
}

// workerDefaultTmpl is the built-in EDITABLE body (段 [A]) of the worker system
// prompt, seeded into agent_prompts. The trafficTool block and the 中間產物輸出規約
// are NOT here — they are code-owned and appended by workerSystem after rendering
// (段 [B]/[C]), so editing the DB body can never drop them.
const workerDefaultTmpl = `你是一個網路安全平臺授權滲透測試系統的"執行者"(work agent)。你領到【一條意圖】(一句話探索方向)，唯一職責：**完成這一條意圖、把發現寫回知識圖譜、然後停止返回。**

**邊界（紅線）**：
1. **只做你領到的這一條意圖**。**探本意圖時若瞥見本意圖之外值得深挖的線索**（報錯洩露的路徑、可能與其它資產聯動的點、疑似另一條利用鏈的入口），**在 fact 的 summary 裡點一句交給規劃者**。
2. 初次受阻（payload 被過濾 / 404 / 注入無回顯）不代表已探透——把本意圖的所有繞過手段走完再輸出結論；
3. 只在授權範圍內操作。系統提示頂部若附【操作約束】，那是最高優先順序紅線：每條命令/探測執行前先自檢，違反即不做（哪怕它落在你領到的意圖裡）。

**邊發現邊寫回**（寫進圖才算數，腦子/文字裡的不算；每得一個結果立刻寫，別攢到最後被步數耗盡丟掉）。三種寫回，別串圖：
- **新資產/資源 → insert_assets（資產圖）**：子域 / service / endpoint / 指紋 / 憑據 等一切資產【本身】。**這裡只登記資產；探索結論/判斷不寫這裡，用 record_fact。**
- **探索結論/事實 → record_fact（探索圖，傳 intent_id）**：都用它。**多個觀察彙總成【一條】事實**（summary 一句總結 + detail寫對總結的拓展，依靠真實的執行過程），不要一個屬性一條、一意圖通常只一條，拆碎會讓圖譜無限膨脹——**預設就寫一條，能並進 detail 的都並進去**；僅當確有【彼此完全獨立、無法歸併】的結論時才用 facts 陣列分條，這是極少數例外，不是常規。**只寫增量**：只記這次【新得到】的，別把已有事實換措辭重記（只印證已有、無新增就不必記）。**只寫真實看到的**：給 evidence（一行：命令+最能證明的一兩行輸出，簡潔，細節在 detail）、標 confidence（observed=直接看到 / inferred=據現象推斷）。
- **確認漏洞 → report_finding（探索圖，含 PoC，傳 intent_id）**：**只有你本次真實觸發過、拿到可重現證據（請求/回應或命令輸出）才用**。嚴禁把"版本/指紋匹配到 CVE""引數看起來可注入""外部漏洞庫/更新日誌/程式碼 diff 推斷"當已確認，也不要用查 CVE 庫或對比補丁版本替代實際觸發。觸發不了但有嫌疑 → 用 record_fact 記一條 inferred 事實（嫌疑點+為何未觸發）交規劃者，別硬記成 finding。


完成本意圖後用一句話總結你做了什麼、寫回了哪些事實。`

// workerTrafficBlock is 段 [B]: the traffic-tool note, code-injected only when
// traffic capture (recording) is on — i.e. the traffic_* tools actually exist.
// Gated on recording, NOT on the egress proxy: a global proxy with capture off
// routes traffic but records nothing, so the tools would not be there. Not stored,
// not editable.
func workerTrafficBlock(recording bool) string {
	if !recording {
		return ""
	}
	return "\n\n**流量工具**：\n- traffic_search / traffic_get / traffic_blob：回看回應、找已訪問過的資源，**先查流量、不要重複 curl 同一 URL**。traffic_search **必須指定 host**、預設只回 3 條極輕量索引(id/method/url/status/resp_len，無回應內容)，需要更多顯式調大 limit；可用 body_contains 在請求/回應正文裡做全文搜尋(至少 3 字元，支援子串和中文，如找密碼/金鑰/報錯/內網地址)；要看某條原文用 traffic_get(id)，其中超大正文顯示為 @blob sha256:<hash>，用 traffic_blob(hash) 分段取全文。"
}

// artifactSpec is 段 [C]: the code-owned, non-editable tail appended to every
// pentest agent's prompt — intermediate artifacts must land in the shared work
// dir, never /tmp. Guaranteed present regardless of how the DB body is edited.
func artifactSpec(dir string) string {
	return "\n\n**中間產物輸出規約**：指令碼、payload、抓到的回應體、臨時資料等一切中間產物，**一律寫到本任務工作目錄 " + dir + "**（相對路徑即寫在這裡，也可用該絕對路徑）——**不要寫 /tmp、不要用其它絕對路徑**。"
}

// workerArtifactSpec is the worker's 段 [C]: its per-intent run dir is pre-created
// by the engine (ensureRunDir), so it just writes relative paths there — no manual
// mkdir, no cross-worker name collisions.
func workerArtifactSpec(runDir string) string {
	return "\n\n**中間產物輸出規約**：指令碼、payload、抓到的回應體、臨時資料等一切中間產物，**一律寫到本次意圖的專屬工作目錄 " + runDir + "**（已自動建好，直接用相對路徑寫在這裡即可，無需再手動建目錄）——**不要寫 /tmp、不要用其它絕對路徑**。"
}

// ensureRunDir builds and creates an agent's working directory under base:
// <base>/tasks/<taskID> for planner/main; <base>/tasks/<taskID>/i<intentID> for a
// worker (intentID<=0 → task dir only). The "tasks/" segment groups per-task dirs
// symmetrically with the chat agent's "sessions/<sessionID>". Best-effort mkdir — on
// failure, writes fail the same way an unwritable CWD would.
func ensureRunDir(base string, taskID, intentID int64) string {
	dir := filepath.Join(base, "tasks", strconv.FormatInt(taskID, 10))
	if intentID > 0 {
		dir = filepath.Join(dir, "i"+strconv.FormatInt(intentID, 10))
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// cmdOutDir is the SDK large-tool-output spill dir under an agent's run dir.
func cmdOutDir(dir string) string { return filepath.Join(dir, "cmd-output") }

func workerSystem(proxyAddr, caCert, dataDir, runDir string) string {
	body := renderSystem("worker", workerDefaultTmpl, WorkerVars{ProxyAddr: proxyAddr, DataDir: dataDir, Now: nowStr()})
	// caCert is present only when the recording MITM is on, which is exactly when
	// the traffic_* tools are registered — so it gates the traffic-tool note.
	// Optional finding guidance is added for every role after tool resolution.
	return body + workerTrafficBlock(caCert != "") + workerArtifactSpec(runDir)
}

// renderIntentTask formats the claimed intent for the worker's launch USER message:
// the intent is the worker's whole job. It used to live in the system prompt; it now
// rides in the first user turn (together with the situational overview) so the system
// prompt stays static/role-only — same move as the planner's situational block.
// intentAssetIDs pulls the intent's target asset ids out of its payload
// (planner's add_intent stores them as a numeric asset_ids array). nil on absence
// or malformed payload.
func intentAssetIDs(intent *db.Node) []int64 {
	if intent == nil {
		return nil
	}
	var p struct {
		AssetIDs []int64 `json:"asset_ids"`
	}
	if err := json.Unmarshal(intent.Payload, &p); err != nil {
		return nil
	}
	return p.AssetIDs
}

func renderIntentTask(intent *db.Node) string {
	return fmt.Sprintf("\n\n【你領到的意圖（本次唯一任務：只做這一條、只產生事實、做完即停）】：\n%s\n意圖 id: %d（寫回 record_fact / report_finding 時傳它）", string(intent.Payload), intent.ID)
}

// renderWorkerGraphOverview folds the global situational snapshot into the worker's
// launch USER message for AWARENESS ONLY. The framing is deliberately strong: the overview
// must NOT widen the worker's job — it still does only its assigned intent. Its sole
// purpose is letting the worker read context (existing facts/assets/hints)
// so it avoids redundant work and doesn't re-derive what others already found.
func renderWorkerGraphOverview(data map[string]any) string {
	// coverage 是給規劃者判斷「哪類測得少 / 要不要擴範圍」的訊號，與 worker「只做領到的
	// 那條意圖、別追未覆蓋的點」的職責邊界相悖 → 從 worker 視圖裡剔除。data 是本次 worker
	// 專屬的新 map，刪鍵不影響 planner。
	delete(data, "coverage")
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back silently: the worker just won't have the global context
	}
	return "\n\n【全域探索態勢（只讀，幫你把自己這條意圖放進大局看）】：\n" +
		"下面是整個任務當前的探索概況。用途有兩個：一是知道別人已發現什麼，別重複；二是讓你探自己這條意圖時，能聯想到它和全域的關係。\n" +
		"**發散是好事**：探本意圖時儘管深想、多聯想。唯一的界線是——別真的動手去執行別的意圖（那是別的 worker 的事，由規劃者排程）。但凡你聯想到有價值的線索（跨資產的聯動、疑似另一條利用鏈的入口、全域層面的可疑點），**務必寫進 fact 交規劃者**——這是你重要的產出，不是可有可無。寧可多報一條讓規劃者判斷，也別自己嚥下去。\n" +
		string(b)
}

// Execute runs one intent. hooks (the per-task Guard) gates every tool call; may
// be nil. emit, if non-nil, receives one ActivityRecord per execution step.
// notifyFinding, if non-nil, is called (intentID, summary) when this worker writes
// a finding (report_finding) so the task's planner wakes mid-flight — with context
// on which intent found what — instead of waiting for the worker to finish.
// Returns the terminal reason (so the engine can distinguish completed vs
// max_turns) and a per-kind breakdown of what was written back (so an intent that
// explored but persisted nothing isn't mistaken for done, and the engine can log
// facts/assets/findings separately instead of lumping them under "facts").
func (w *Worker) Execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string)) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, "", "")
}

// ExecuteWithMessage runs the next turn in the same intent conversation with a
// human-authored message. The HTTP handler does not edit the transcript;
// agentcore records the message as a normal user turn when this Worker starts.
// This keeps Worker continuation identical to the regular agent chat flow.
func (w *Worker) ExecuteWithMessage(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, strings.TrimSpace(requestID), strings.TrimSpace(message))
}

func (w *Worker) execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	tsx := NewToolSet(ts, name)
	tsx.SetFindingRecorder(w.findingRecorder)
	tsx.SetTaskID(taskID)
	coverageEnabled := as == nil || as.CoverageEnabled(taskID)
	tsx.SetCoverageEnabled(coverageEnabled)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetOwnerNode(intent.ID)         // assets this worker discovers anchor to its intent → visible to the task
	tsx.SetEnrich(enr)                  // async DNS/HTTP auto-completion for assets this worker writes
	tsx.SetNotifyFinding(notifyFinding) // report_finding 落庫時當場喚醒 planner，帶上「哪個意圖+finding」
	// base = built-in worker tools ∪ host tools (traffic) ∪ default tools (incl. Bash);
	// then augment with the agent's visible skills/MCP. During the SDK settlement
	// phase, Bash is hidden via Settlement.DisabledTools (no local gating needed).
	base := append(tsx.WorkerTools(), w.extraTools...)
	// worker 刻意不給 MultiEdit/Glob/Grep：檔案精改用 Edit、檢索走 Bash(grep/find)，
	// 收斂工具面、減少低價值呼叫。其餘 SDK 預設工具(Read/Write/Edit/LS/Bash/Sleep)照常。
	base = append(base, defaultToolsExcept("MultiEdit", "Glob", "Grep")...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts), IntentID: intent.ID})
	tools, def, cleanup := AugmentTools(ctx, "worker", base)
	defer cleanup()

	// 意圖是 worker 的【唯一職責、貫穿整個 run 的不變數】→ 連同啟動指令、意圖錨定的目標資產
	// 原始資料一起放進 system prompt：system 每次 run 都重新拼一遍、絕不會被 compaction 壓掉，
	// 長 run 裡意圖永遠在場，續跑時也不依賴 transcript 歷史是否留住那條首訊息。代價是 system
	// 混入 per-intent 易變資料、失去跨意圖快取複用；這是刻意的取捨（意圖丟失比省 token 嚴重得多）。
	// 與 planner「態勢塊放 user turn」分叉是有意的：planner 本身是產意圖的那個、沒有單一 mandate，
	// worker 有。僅【全域態勢 overview】留在啟動 user 訊息裡——它可降級、容忍 stale，壓掉無礙。
	// 本次意圖的專屬工作目錄 <workDir>/tasks/<taskID>/i<intentID>，引擎側先建好。
	runDir := ensureRunDir(w.workDir, taskID, intent.ID)
	// The run-wide intent is not the current tool action. Do not forward it or
	// inherit a parent run's background into the action reviewer.
	ctx = intercept.WithReviewContext(ctx, runDir, intercept.ReviewBackground{})
	overview := renderWorkerGraphOverview(tsx.graphOverviewData())
	sysBody := workerSystem(w.proxyAddr, w.proxyCACert, w.workDir, runDir)
	if w.wantConstraints() {
		sysBody += constraintBlock(ts) // 操作約束(若有)注入系統提示,worker 執行時嚴格遵守
	}
	// 意圖塊 → 意圖錨定資產塊 → 啟動指令，依次追加到 system 尾部（與 constraintBlock 同一套追加法）。
	sysBody += renderIntentTask(intent)
	if as != nil {
		if ids := intentAssetIDs(intent); len(ids) > 0 {
			if assets, err := as.GetByIDs(ids); err == nil && len(assets) > 0 {
				if b, err := json.Marshal(assets); err == nil {
					sysBody += "\n\n本意圖 asset_ids 對應的目標資產：\n" + string(b)
				}
				// 意圖明確針對的這些資產 → 自動納入任務測試範圍（與 insertAssets 同一套
				// 保守粒度）。upsertTaskScope 的 ON CONFLICT DO NOTHING + uq_task_scope
				// 唯一索引保證不會重複新增；重跑/重試同樣是冪等 no-op。
				// 資產覆蓋度功能關閉時不再累積測試範圍(分母)。
				if coverageEnabled {
					for _, a := range assets {
						_ = as.AddAutoScope(taskID, a.Type, a.Domain, a.URL, a.IP)
					}
				}
			}
		}
	}
	sysBody += "\n\n開始執行上面這條意圖：只做它、只產生事實、assets、finding、做完即停。"
	system, boundary := deferredSystem(sysBody, def)
	// 任務級 deadline(經 ctx 注入)夾逼本 run 的牆鍾預算 + 決定收尾詞(見 taskclock.go)。
	tc := taskClockFrom(ctx)
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, w.runTimeout)
	settle := wrapupSettlement("worker", []string{"Bash"})
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("worker", []string{"Bash"}, clamped)
	}
	opts := agentcore.Options{
		Provider:        w.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		// WebFetch 走記錄代理，其 HTTP 與 curl 一樣被留痕；載入代理 CA 讓經 MITM
		// 重籤的 HTTPS 證書能【正常校驗通過】（而非關掉校驗）。proxy 空則直連。
		EnableWebFetch: true,
		WebFetchProxy:  w.proxyAddr,
		WebFetchCACert: w.proxyCACert,
		// 聯網搜尋(可選)。ddgs 無需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
		// WebSearchProxy 是獨立的出口代理(http/https/socks5)，與記錄流量的 MITM 代理無關；空則直連。
		EnableWebSearch:       w.webSearch.Enabled,
		WebSearchBackend:      w.webSearch.Backend,
		BraveSearchAPIKey:     w.webSearch.BraveKey,
		TavilySearchAPIKey:    w.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: w.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  w.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   w.webSearch.DeepSeekModel,
		WebSearchProxy:        w.webSearch.Proxy,
		// Bash 子命令的 HTTP 預設走記錄代理 + 信任其 CA（工具無需 -x/-k）。
		BashEnv:    proxyEnv(w.proxyAddr, w.proxyCACert),
		WorkingDir: runDir,
		MaxTurns:   w.maxTurns, // 0 = unlimited (configurable in agent management)
		// 牆鍾預算,輪邊界判,不打斷半路;0 = 不限。有任務級 deadline 時夾逼到 min(自身預算,
		// 距 deadline 剩餘),讓本 run 在任務到點時自然進收尾(見 taskclock.go)。
		MaxDuration: maxDur,
		// 命中預算(輪次 OR 時長)→ SDK 跑一輪收尾(隱藏 Bash),把已識別的寫回,避免爛尾。
		// clamped(被任務 deadline 夾逼)時用 PromptByReason:因超時=任務到點→任務超時詞,
		// 因步數=夾逼視窗內步數先耗盡→回落 per-run 詞。非 clamped 維持純 per-run。
		Settlement: settle,
		// large tool output spills to cmd-output/ with a head + pointer (SDK tool.Capture);
		// full output preserved on disk. 截斷上限用 SDK 預設(30000 字元)。
		ToolOutputDir: cmdOutDir(runDir),
		Compaction:    compactionConfig(w.compactionWindow()), // long tool-heavy runs stay within the window
		Todos:         actool.NewTodoStore(),                  // 會話級臨時待辦（TodoWrite），純規劃用，退出即丟
		NonStreaming:  w.nonStreaming(),                       // 該 profile 選非流式時走 Provider.Complete
		MaxTokens:     w.maxTokens(),                          // 0 = 不發上限,由服務端預設值決定
	}
	if hooks != nil { // typed-nil guard: only set when concrete (avoids harness panic)
		opts.Hooks = hooks
	}
	if w.tx != nil { // persist raw LLM conversation; one file per worked intent
		opts.Transcript = w.tx
		opts.SessionID = WorkerSessionID(ts.ID(), intent.ID)
	}
	intentID := intent.ID
	emitWrap := func(r db.Activity) {
		if emit != nil {
			r.NodeID, r.Worker = &intentID, name
			emit(r)
		}
	}
	// 意圖 / 啟動指令 / 意圖錨定資產已隨 system prompt 下發（見上方 sysBody 組裝）。
	// 這條啟動 user 訊息只承載【全域態勢 overview】——可降級的瞭解大局資訊，壓掉無礙。
	// overview 罕見地 marshal 失敗為空時，回退一句啟動詞，避免首輪出現空 user 訊息。
	input := overview
	if strings.TrimSpace(input) == "" {
		input = "開始執行 system 裡領到的意圖：只做它、只產生事實、assets、finding、做完即停。"
	}

	// 實驗功能:開啟後由 noa 接管上下文壓縮(歸檔集中在 <workDir>/noa/<SessionID> 下,持久)。
	noaSession := WorkerSessionID(ts.ID(), intent.ID)
	enableNoa(&opts, w.noaEnabledFn, w.workDir, noaSession, noaWarn(noaSession))
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close() // release the session's background-task manager (temp dir + processes)

	// Resume prior conversation if this intent was paused/blocked/exhausted and is
	// being re-run. The transcript ID is deterministic per intent, so if a prior
	// session exists the worker continues from where it left off instead of
	// restarting from scratch.
	alreadyRecorded := false
	if w.tx != nil {
		_ = s.Resume(opts.SessionID)
		alreadyRecorded = requestID != "" && hasWorkerChatMessage(s.Messages(), requestID)
		if len(s.Messages()) > 0 && message == "" {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
			input = "繼續執行。"
		} else if len(s.Messages()) > 0 {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
		}
	}
	if message != "" {
		if alreadyRecorded {
			input = "繼續執行上一次人工對話輸入的新意圖。不要重複已經完成的動作。"
		} else if len(s.Messages()) > 0 {
			input = workerChatMarker(requestID) + "\n【人工對話輸入的新意圖】\n" + message +
				"\n\n請立即按這條人工輸入執行，完成後再根據上下文決定原任務是否需要繼續。"
		} else {
			input += "\n\n" + workerChatMarker(requestID) + "\n【人工對話輸入的新意圖】\n" + message +
				"\n\n請優先執行這條人工輸入。"
		}
	}

	// Budgets + settlement are owned by the SDK (MaxTurns/MaxDuration + Settlement):
	// on hit it runs a wrap-up turn and finishes with ReasonMaxTurns/ReasonTimeout.
	// MaxDuration now interrupts an in-flight tool at the wall-clock deadline and
	// enters the wrap-up phase on the live ctx, so a run whose tool overran the budget
	// still settles (no external hard-timeout backstop needed). ctx itself carries only
	// pause / planner kill / shutdown, which the engine distinguishes and re-queues/stops.
	_, reason, err := captureRunSession(ctx, s, input, emitWrap)
	return reason, tsx.Writes(), err
}
