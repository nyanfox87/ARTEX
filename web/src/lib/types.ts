// ARTEX domain model — types used across the UI.
// Derived from the functional spec (section 7: 關鍵資料形狀).

export type TaskStatus = "created" | "queued" | "running" | "paused" | "done" | "failed" | "timeout";
export type EngineMode = "exploring" | "paused" | "stalled" | "idle";

export interface Task {
  distinct_findings?: FindingDistinctStats;
  id: string;
  name?: string; // 可選任務名稱;空/預設=未命名,展示時回退到描述
  category_id?: number;
  category_name?: string;
  pinned?: boolean;
  pinned_at?: string | null;
  description: string;
  goal: string;
  status: TaskStatus;
  created_at: string;
  created_unix?: number; // created_at as unix seconds (run-duration calc)
  completed_at?: string; // RFC3339 finish time (done/failed); "" if unfinished
  completed_unix?: number; // completed_at as unix seconds (0/undef if unfinished)
  last_activity_unix?: number; // unix seconds of the last activity (0/undef if none)
  paused?: boolean;
  queued?: boolean;
  active?: boolean;
  in_flight?: number;
  findings?: { critical: number; high: number; medium: number; low: number }; // 已登記漏洞數(按嚴重度分檔)
  last_activity?: string;
  stalled?: boolean;
  goals_total?: number;
  goals_met?: number;
  engine_mode?: EngineMode;
  tokens?: TokenTotal; // whole-task token consumption
  llm_profile_id?: number; // LLM profile used; absent = default profile
  llm_profile_ids?: number[]; // ordered task-level failover chain
  active_llm_profile_id?: number; // profile used by the next LLM call
  llm_failover_state?: "default" | "ready" | "chain_exhausted" | string;
  llm_failover_reason?: string;
  source_task_ids?: string[]; // directly related tasks inherited as read-only context
  archive_blocked_by_task_id?: string; // live direct dependent that must be archived first
  company_ids?: number[]; // associated company scopes; current company assets join the task at creation
  coverage_enabled?: boolean; // 資產覆蓋度功能開關(建立時定,預設開)；false=不計算/不展示覆蓋度
}

export interface TaskCategory {
  id: number;
  name: string;
  task_count: number;
  created_at: string;
  updated_at: string;
}

export interface TaskTemplate {
  id: number;
  name: string;
  description: string;
  goal: string;
  category_id?: number | null; // 預設分類；null/預設=無
  intercept_rules?: AssetInterceptRuleInput[]; // 預設的任務級攔截/允許規則
  created_at: string;
  updated_at: string;
}

export interface DeleteTaskOptions {
  delete_assets: boolean;
  delete_traffic: boolean;
  delete_files: boolean;
  delete_findings: boolean;
  delete_llm_records: boolean;
}

export interface DeleteTaskResult {
  deleted: string;
  assets_deleted: number;
  assets_detached: number;
  traffic_deleted: number;
  files_deleted: boolean;
  findings_deleted: number;
  llm_records_deleted: number;
  cleanup_warning?: string;
}

export type TaskArchiveState =
  | "archive_queued"
  | "archiving"
  | "archive_failed"
  | "ready"
  | "restore_queued"
  | "restoring"
  | "restore_failed"
  | "delete_queued"
  | "deleting"
  | "delete_failed";

export interface TaskArchiveTokenStats {
  calls?: number;
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface TaskArchive {
  id: number;
  task_id: number;
  state: TaskArchiveState;
  phase: string;
  progress: number;
  error?: string;
  warnings?: string[];
  format_version: number;
  sha256?: string;
  original_size: number;
  compressed_size: number;
  task_name: string;
  task_description: string;
  task_goal: string;
  original_status: TaskStatus;
  category_id?: number;
  category_name?: string;
  source_task_ids: number[];
  remaining_timeout_seconds: number;
  data_counts: Record<string, number>;
  aggregate_stats: {
    tokens?: TaskArchiveTokenStats;
    skills?: Record<string, number>;
    tools?: Record<string, number>;
    findings?: Record<string, number>;
  };
  archived_at?: string;
  requested_at: string;
  created_at: string;
  updated_at: string;
}

export interface TaskArchivePage {
  items: TaskArchive[];
  total: number;
  page: number;
  size: number;
}

export interface ArchiveBatchItem {
  id: string;
  archive_id?: number;
  ok: boolean;
  queued: boolean;
  error?: string;
}

// ---- Asset graph (global, shared across tasks) ----
export type AssetType =
  | "company"
  | "domain"
  | "ip"
  | "port"
  | "service"
  | "site"
  | "endpoint"
  | "parameter"
  | "tech"
  | "credential"
  | "data";

export type NodeState = "observed" | "confirmed" | "tombstoned";

export interface AssetNode {
  id: string;
  type: AssetType;
  name: string;
  key: string; // nkey
  value?: string;
  company_id?: string; // 歸屬公司資產 id；空=未歸屬
  state: NodeState;
  confidence: number; // 0..1
  attrs?: Record<string, unknown>;
  first_seen: string;
  last_seen: string;
}

export type AssetRel =
  | "owns"
  | "resolves"
  | "exposes"
  | "runs"
  | "serves"
  | "has_endpoint"
  | "has_param"
  | "fingerprinted"
  | "authenticates_as"
  | "reachable"
  | "has_subdomain";

export interface Edge {
  src: string;
  dst: string;
  rel: AssetRel | ExploreRel;
}

// Task asset view — server-side enriched, paginated.
export interface TaskAssetRef {
  id: string;
  name?: string;
  key: string;
  attrs?: Record<string, unknown>;
}

export interface TaskAssetItem extends AssetNode {
  techs?: TaskAssetRef[];
  auth?: TaskAssetRef[];
  params?: TaskAssetRef[];
}

export interface TaskAssetView {
  counts: Record<string, number>;
  total: number;
  items: TaskAssetItem[];
}

// ---- New unified asset model (new backend) ----
export type NewAssetType = "root_domain" | "ip" | "subdomain" | "app" | "service" | "endpoint";

export interface Asset {
  id: number;
  type: NewAssetType;
  company_id?: number;
  task_ids: number[];
  domain?: string;
  root_domain?: string;
  ip?: string;
  c_segment?: string;
  port?: number;
  icp?: string;
  bound_domains?: string[];
  open_ports?: { port: number; service?: string }[];
  record_type?: string;
  record_value?: string[] | string;
  bundle_id?: string;
  app_name?: string;
  category?: string;
  app_description?: string;
  app_icp?: string;
  url?: string;
  service_type?: string;
  service_name?: string;
  favicon_mmh3?: string;
  status_code?: number;
  content_length?: number;
  page_title?: string;
  technologies?: string[];
  auth?: Record<string, unknown>[];
  method?: string;
  params?: Record<string, unknown>[];
  extra?: Record<string, unknown>;
  last_seen: string;
  task_source?: string;
  task_source_summary?: string;
  task_source_node_id?: number;
}

export interface IntentAsset {
  intent_id: number | string;
  asset_id: number;
  type: NewAssetType;
  label: string;
  source: string;
  source_summary: string;
  source_node_id?: number;
  source_task_id: number;
  inherited: boolean;
}

export interface TaskAssetMutation {
  requested: number;
  attached: number;
  existing: number;
}

export interface TaskAssetScopeMutation {
  requested: number;
  assets_linked: number;
  assets_existing: number;
  scopes_added: number;
  scopes_existing: number;
}

// ---- Asset coverage graph (per task) ----
// 力導向「資產覆蓋圖」的一個節點。key 唯一：資產="a:<id>"、公司="c:<id>"、
// 無資產行的根域名="r:<domain>"。in_scope=false 的是僅用於連線的灰色上下文節點。
export interface CoverageGraphNode {
  key: string;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint";
  label: string;
  tested: boolean;
  in_scope: boolean;
  asset_id?: number;
  company_id?: number;
  domain?: string;
  root_domain?: string;
  ip?: string;
  url?: string;
  port?: number;
  service_type?: string;
  app_name?: string;
  page_title?: string;
  status_code?: number;
}

export interface CoverageGraphEdge {
  src: string;
  dst: string;
}

export interface CoverageGraphData {
  nodes: CoverageGraphNode[];
  edges: CoverageGraphEdge[];
}

// 某資產在本任務探索圖裡關聯到的意圖/事實/發現（覆蓋圖節點抽屜用）。
export interface CoverageAssetRef {
  id: number;
  kind: string;
  state: string;
  summary: string;
  source_task_id?: string;
  inherited?: boolean;
}
export interface CoverageAssetRefs {
  intents: CoverageAssetRef[];
  facts: CoverageAssetRef[];
  findings: CoverageAssetRef[];
}

// ---- Workspace file manager (workDir) ----
export interface WorkspaceEntry {
  name: string;
  path: string; // workspace-relative, forward slashes
  dir: boolean;
  size: number;
  mtime: number; // unix millis
}
export interface WorkspaceListing {
  path: string;
  entries: WorkspaceEntry[];
}
export interface WorkspaceFile {
  path: string;
  size: number;
  binary: boolean;
  too_large?: boolean;
  content?: string;
}

// 任務測試範圍的一條（覆蓋度分母 + 授權邊界）。
export interface TaskScopeRow {
  id: number;
  task_id: number;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "cidr" | "icp" | "keyword";
  company_id?: number;
  company_name?: string; // 後端 JOIN companies 解析，僅 kind=company 有值
  domain?: string;
  net?: string;
  value?: string;
  source: "auto" | "agent" | "manual";
  reason?: string;
}

export type CompanyScopeKind = "domain" | "ip" | "cidr" | "icp" | "keyword";

// 新增企業時提交的結構化資產範圍規則。
export interface CompanyScopeRule {
  kind: CompanyScopeKind;
  value: string;
}

// 資產範圍寫入的結果。errors 是本次提交裡不合法的行；warnings 是與本次提交無關、
// 但會讓歸屬結果不符合預期的既有資料問題（如 ip 欄位存了主機名的資產）。
export interface CompanyScopeMutation {
  added: number;
  skipped: number;
  invalid: number;
  errors?: string[];
  warnings?: string[];
}

// 公司資產範圍規則的一條（歸屬唯一真值來源）。
export interface ScopeRow {
  id: number;
  company_id: number;
  kind: CompanyScopeKind;
  domain?: string; // kind=domain 時有值
  net?: string; // kind=ip|cidr 時有值
  value?: string; // kind=icp|keyword 時可能由後端直接返回
  raw: string; // 原始使用者輸入，用於顯示和回填
  reason?: string;
}

// 企業：type=company 的資產節點 + 圖示 + 資產計數 + 資產範圍規則。
export interface Company {
  id: number;
  name: string;
  logo?: string; // 遠端圖示 URL；空則前端用名稱首字母
  asset_count: number;
  scope?: ScopeRow[];
}

// ---- Exploration graph (per task) ----
export type ExploreKind = "task" | "begin" | "goal" | "intent" | "fact" | "finding" | "hint" | "digest";
export type GoalState = "open" | "met" | "abandoned";
export type IntentState = "open" | "running" | "paused" | "done" | "blocked" | "exhausted" | "stopped";
export type FindingState = "confirmed" | "dismissed";
export type HintState = "active" | "consumed";
export type ExploreRel = "spawns" | "derived_from" | "yields" | "proves" | "covers";

export interface TaskNode {
  id: string;
  type: ExploreKind;
  payload?: string;
  priority: number; // 0..10
  state: string; // GoalState | IntentState | FindingState | HintState
  origin: string;
  ts: string;
  source_task_id?: string;
  inherited?: boolean;
  delete_reason?: string; // 意圖假刪除(state='deleted')時的刪除原因
}

// 播報板一頁:按建立順序分頁的節點 + 這一頁涉及的邊 + 邊另一端的節點(refs,按 id 索引),
// 這樣每條播報都能說清「從哪來、產出了什麼」,而不用把整張圖拉下來。
export interface ExplorationNodePage {
  items: TaskNode[];
  total: number;
  page: number;
  size: number;
  edges: Edge[];
  refs: Record<string, TaskNode>;
  // 節點 id → 該節點錨定的資產(播報板展開時順帶展示,含本頁節點與其鄰居)。
  assets: Record<string, FindingAsset[]>;
}

export interface ExplorationNodeQuery {
  page?: number;
  size?: number;
  kinds?: ExploreKind[];
  states?: string[];
  q?: string;
  order?: "asc" | "desc";
}

// 目標管理卡片用的目標(後端已把 payload 拆成 text/vulnclass)。
export interface TaskGoal {
  id: string;
  text: string;
  vulnclass?: string;
  state: string; // GoalState
  origin?: string;
  ts: string;
}

// 約束管理卡片用的操作約束(allow=允許 / deny=禁止)。
export type ConstraintKind = "allow" | "deny";
export interface TaskConstraint {
  id: string;
  kind: ConstraintKind;
  text: string;
  origin?: string;
  ts?: string;
}

// ---- Findings ----
export type Severity = "critical" | "high" | "medium" | "low";

// 漏洞處置狀態:待處理 / 處理中 / 已確認 / 已處理 / 已修復 / 誤報 / 忽略 / 重複 / 風險接受。
export type FindingStatus =
  | "pending"
  | "in_progress"
  | "confirmed"
  | "resolved"
  | "fixed"
  | "false_positive"
  | "ignored"
  | "duplicate"
  | "risk_accepted";

// FindingAsset 是一個漏洞綁定的資產(已在後端預渲染 label)。
export interface FindingAsset {
  id: string;
  type: string;
  label: string;
}

export interface Finding {
  case_id?: string;
  traffic_count?: number;
  evidence_version?: number;
  report_evidence_version?: number;
  report_stale?: boolean;
  id: string;
  finding_id?: string; // 獨立 findings 表的行 id,狀態更新的控制代碼(任務內舊節點可能缺失)
  vulnclass: string;
  name?: string; // 漏洞名稱;為空時展示回退到 vulnclass
  severity: Severity;
  status: FindingStatus;
  summary: string;
  evidence: string;
  report?: string; // 詳細報告(Markdown);僅詳情介面返回,列表為空
  intent_id?: string;
  param_id?: string;
  task_id?: string;
  task_description?: string;
  source_task_id?: string;
  inherited?: boolean;
  assets?: FindingAsset[];
  ts: string;
}

// FindingsPage 是發現列表的服務端分頁回應。
export interface FindingsPage {
  items: Finding[];
  total: number;
  page: number;
  page_size: number;
}

export interface FindingGroup {
  task_id: string | number | null;
  task_name?: string; // 可選任務名稱;空/預設=未命名
  task_description: string;
  task_status: string;
  count: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingGroupsPage {
  items: FindingGroup[];
  total: number;
  finding_total: number;
  page: number;
  page_size: number;
}

export interface FindingDeepenResponse {
  task_id: string;
  intent_id: string;
  state: IntentState;
  queued: boolean;
}

// FindingStats 是發現全表聚合(統計卡 + 漏洞型別下拉),服務端計算,不受分頁影響。
export interface FindingStats {
  distinct?: FindingDistinctStats;
  total: number;
  pending: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  vulnclasses: string[];
  tasks: FindingTaskOption[];
}

// FindingTaskOption 是發現頁「按任務」篩選下拉的一項:有漏洞的任務(描述為空表示任務已刪除,
// 前端回退展示 id)及其漏洞條數。
export interface FindingTaskOption {
  id: string | number;
  name?: string; // 可選任務名稱;空/預設=未命名
  description: string;
  count: number;
}

// FindingQuery 是發現列表分頁/篩選/排序引數。
export interface FindingQuery {
  page: number;
  pageSize: number;
  severity?: "all" | Severity;
  status?: "all" | FindingStatus;
  vulnclass?: string;
  task?: string; // 任務 id;"all"/空 = 不按任務篩選
  query?: string;
  sort?: "severity" | "time";
  // 資產樹節點 key;選中一個節點 = 選中它的整棵子樹。空 = 不按資產篩選。
  assetScope?: string;
}

// ---- Findings by asset (資產檢視) ----
export type FindingAssetKind = "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint" | "none";

// FindingAssetNode 是資產樹的一個節點。key 形如 a:<id>(資產)、c:<id>(企業)、
// r:<domain>(庫裡沒有資產行的根域名)、__none__(未關聯資產)。
export interface FindingAssetNode {
  key: string;
  parent?: string;
  kind: FindingAssetKind;
  label: string;
  asset_id?: number;
  company_id?: number;
  self: number; // 直接掛在該資產上的發現數
  total: number; // 含子孫、按發現去重
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingAssetTree {
  nodes: FindingAssetNode[];
  finding_total: number;
  truncated: boolean;
  dropped_kinds?: string[];
}

// FINDING_UNASSIGNED_ASSET 與後端 db.FindingUnassignedAsset 對應。
export const FINDING_UNASSIGNED_ASSET = "__none__";

// ---- Activity / sessions ----
export type ActivityKind =
  | "tool_use"
  | "tool_result"
  | "text"
  | "thinking"
  | "result"
  | "user"
  | "intent" // LLM-generated exploration objective leading a worker session (UI-synthesized)
  | "round" // planner round boundary marker (engine-emitted)
  | "usage" // live cumulative token usage (per model turn); not rendered
  | "llm_switch" // automatic/manual task-level LLM switch
  | "llm_failover" // task-level provider switch / chain exhaustion audit event
  | "intercept_request"; // user-approval request from the intercept layer

// ChatAttachment 是一次上傳的檔案:path 相對該會話/任務工作目錄(即 agent 的 CWD)。
export interface ChatAttachment {
  name: string;
  path: string;
  size: number;
  abs?: string; // 絕對路徑(scope=staging 暫存上傳時返回;建任務前把它寫進描述)
}

export interface Activity {
  seq: number;
  intent_id?: string;
  worker: string; // session owner: planner | mainagent | work#1 ...
  ts: string;
  kind: ActivityKind;
  tool?: string;
  tool_use_id?: string;
  is_error?: boolean;
  summary: string;
  detail?: string;
  metadata?: {
    llm_transition?: LLMTransition;
  };
  source_task_id?: string;
  inherited?: boolean;
  main_seg?: number; // main-agent conversation segment (present only on worker="mainagent" rows)
  // token usage (present only on kind='result')
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface LLMAuditProfile {
  id: number;
  name: string;
  format: string;
  model: string;
}

export interface LLMTransition {
  mode: "automatic" | "manual" | "exhausted";
  reason: string;
  previous?: LLMAuditProfile;
  next?: LLMAuditProfile;
}

export interface TaskLLMResolution {
  profile_id?: number;
  name: string;
  format: string;
  model: string;
  source: "task_chain" | "agent_binding" | "global_profile" | "environment" | "global";
  available: boolean;
  reason?: string;
}

export interface TaskLLMResolutions {
  mainagent: TaskLLMResolution;
  planner: TaskLLMResolution;
  worker: TaskLLMResolution;
}

// ---- Agent triggers (P3 排程，僅自定義 agent) ----
export interface AgentTrigger {
  id: number;
  agent_key: string;
  enabled: boolean;
  interval_sec: number; // 定時:每 N 秒(0=不定時)
  on_finding: boolean; // 任意任務發現 finding 時觸發
  on_goal_met: boolean; // 任意任務達成目標時觸發
  on_task_timeout: boolean; // 任意任務超時時觸發
  on_tool_call: boolean; // 選中工具被呼叫(執行完成)時觸發
  on_task_create: boolean; // 任意任務被建立時觸發
  interval_message: string; // 各觸發條件的獨立使用者訊息
  finding_message: string;
  goal_message: string;
  task_timeout_message: string;
  tool_call_message: string;
  task_create_message: string;
  tool_names: string[]; // on_tool_call 選中的工具 key(至少一個)
  last_fire?: string;
}

export interface TaskSchedule {
  id: number;
  name: string;
  enabled: boolean;
  timezone_mode: "beijing" | "system";
  timezone?: string;
  schedule_type: "once" | "weekly";
  run_date?: string;
  end_date?: string;
  weekdays: number[];
  start_time: string;
  end_time?: string;
  start_immediately: boolean;
  status: "waiting" | "running" | "outside" | "completed" | "error" | "paused";
  last_window_key?: string;
  last_transition_at?: string;
  last_error?: string;
  next_start?: string;
  next_end?: string;
  run_count?: number;
  last_run_at?: string;
  task_ids: number[];
  history?: Array<{
    id: number;
    task_id?: number;
    action: string;
    success: boolean;
    message: string;
    created_at: string;
  }>;
}

// ---- Conversations (chat page) ----
export interface ActiveFindingRetest {
  id: number;
  finding_id: string;
  conversation_id: number;
  status: "pending" | "running";
}

export interface FindingRetest {
  id: number;
  finding_id: number;
  conversation_id: number | null;
  status: "pending" | "running" | "completed" | "failed" | "stopped";
  verdict: "" | "reproduced" | "fixed" | "inconclusive";
  notes: string;
  summary: string;
  evidence: string;
  error: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface Conversation {
  id: number;
  running?: boolean; // live server state, returned with the conversation list
  agent_key: string;
  title: string;
  llm_profile_id?: number;
  pinned?: boolean;
  pinned_at?: string | null;
  created_at: string;
  updated_at: string;
}

// ---- Backend logs (/logs page) ----
export interface LogLine {
  seq: number;
  db_id?: number; // server_logs.id; present for DB-persisted lines
  ts: string;
  level: "info" | "warn" | "error";
  tag: string;
  text: string;
}

export type SessionRole = "mainagent" | "planner" | "worker" | "system";
export type SessionStatus = "running" | "paused" | "done" | "blocked" | "exhausted" | "pending" | "stopped" | "deleted";

// Daily token aggregate bucket (GET /api/tokens/daily).
export interface DailyTokenBucket {
  date: string; // "YYYY-MM-DD"
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Per-worker token usage (GET /api/exploration/tokens).
export interface TokenUsage {
  worker: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface SessionTokenUsage {
  session: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface BatchControlItem {
  id: string;
  ok: boolean;
  status?: string;
  queued?: boolean;
  error?: string;
}

// 批次改分類的逐任務結果。失敗只可能是任務已被刪除，分類本身的寫入是原子的。
export interface BatchCategoryItem {
  id: string;
  ok: boolean;
  error?: string;
}

// Whole-task (all agents) token aggregate.
export interface TokenTotal {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Global per-profile token spend from the llm_usage ledger (GET /api/tokens/usage).
export interface ProfileUsage {
  profile_name: string;
  calls: number;
  tasks: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// One (profile, UTC day) token bucket for the dashboard's daily chart (new source).
export interface ProfileDayUsage {
  profile_name: string;
  date: string; // YYYY-MM-DD
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
}

// Response of GET /api/tokens/usage — the dashboard's "new" (llm_usage) token view.
export interface UsageStats {
  by_profile: ProfileUsage[];
  daily: ProfileDayUsage[];
}

// Per-model token usage for one task (GET /api/llm/records/by-model), from the
// always-on llm_usage metering ledger. calls = number of LLM calls on this model.
export interface ModelTokenStat {
  model: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface Session {
  id: string;
  role: SessionRole;
  title: string;
  status: SessionStatus;
  live: boolean;
  last_activity: string;
  intent_id?: string;
  source_task_id?: string;
  inherited?: boolean;
  seg?: number; // main-agent session: which conversation segment (0 = original)
}

// ---- Security ----
export interface AuditEntry {
  ts: string;
  tool: string;
  action: "allow" | "block";
  reason?: string;
  command?: string;
}

export interface Audit {
  entries?: AuditEntry[];
  attributions?: Record<string, number>;
}

// ---- Traffic ----
export interface TrafficExchange {
  id: string;
  ts: string;
  host: string;
  method: string;
  url: string;
  status: number;
  content_type: string;
  resp_len: number;
}

export interface TrafficResp {
  enabled: boolean;
  proxy?: string;
  count?: number; // global total (unfiltered)
  total?: number; // rows matching the current filter (for pagination)
  page?: number;
  size?: number;
  exchanges?: TrafficExchange[];
}

// Full raw request/response of one exchange (lazy-loaded on row select).
export interface TrafficDetail {
  req: string;
  resp: string;
}

// One distinct recorded host with its exchange count (target picker).
export interface TrafficHost {
  host: string;
  count: number;
}

// ---- App settings (runtime toggles) ----
export interface Settings {
  traffic_capture: boolean;
  agent_traffic_binding: boolean; // Agent 自動綁定流量證據，預設關閉；不影響人工綁定
  llm_record: boolean; // LLM 錄製開關（預設關）；關閉時不記錄任何 LLM 呼叫
  // Web search. brave_key_set / tavily_key_set reflect whether a key is stored
  // (the values are never returned). On PUT, send the corresponding field to set/clear.
  web_search_enabled: boolean;
  web_search_backend: string; // "ddgs" | "brave-free" | "tavily" | "deepseek"
  brave_key_set: boolean;
  tavily_key_set: boolean;
  // write-only: only sent on PUT to store/clear the key.
  brave_search_api_key?: string;
  tavily_search_api_key?: string;
  // 獨立出口代理(http/https/socks5)，用於訪問搜尋端點；與記錄流量的 MITM 代理無關。空=直連。
  web_search_proxy?: string;
  // 全域出口代理(http/https/socks5，可帶 user:pass)，所有目標流量走它。開啟流量捕獲時作為
  // MITM 上游；關閉捕獲時直接注入 agent 的 bash/WebFetch。空=直連。
  global_proxy?: string;
  python_interpreter?: string; // 自定義指令碼工具的 python 直譯器路徑(空=執行時檢測)
  workers?: number; // 併發工作 agent 數(預設3)；對之後啟動的任務生效
  // 任務併發上限:同時「執行中」的任務數上限。關閉=不限;開啟後新建任務超限則排隊,有空位自動啟動。
  task_concurrency_enabled?: boolean; // 預設 false
  task_concurrency_limit?: number; // 開啟後預設 5
  // LLM 輪詢(故障轉移)。預設關；開啟後「未指定模型」的 agent 在當前配置不可用
  // （餘額不足/key 失效/限流/服務異常）時自動切到下一個配置。
  llm_pool_enabled?: boolean; // 預設 false
  // 綁定了指定配置的 agent/任務失敗時是否也回落到輪詢鏈。預設 false = 綁定即獨佔。
  llm_pool_bind_fallback?: boolean;
  // 操作約束注入範圍(預設都開):把任務的 allow/deny 約束拼進對應 agent 的系統提示。
  constraints_inject_planner?: boolean;
  constraints_inject_worker?: boolean;
  // 實驗功能:noa 模型驅動上下文壓縮(預設關)。開啟後平臺接入的四類 agent(planner/
  // worker/主 agent/對話)由 noa 接管上下文壓縮,取代內建 compaction;每 run 讀一次,對
  // 之後啟動的 run 生效。
  noa_compaction?: boolean;
  // ---- 漏洞 IM 推送（渠道本身是獨立資源，見 /api/notify/*，這裡只有三項全域配置）----
  notify_enabled?: boolean; // 推送總開關，預設開；用於維護期一鍵止血
  notify_public_base_url?: string; // 漏洞詳情回鏈的外部訪問地址；空=訊息不帶回鏈
  notify_digest_interval_min?: number; // 彙總模式週期（分鐘），預設 30
}

// ---- 漏洞 IM 推送 ----

// NotificationFilter 是渠道的過濾條件，欄位全部可選，預設即不過濾。
// 後端對所有欄位都不加校驗：配置畸形時按「命中」處理（寧可多推不可漏推）。
export interface NotificationFilter {
  min_severity?: string; // "" | low | medium | high | critical
  task_ids?: number[]; // 空=不限；非空則要求與漏洞所屬任務有交集
  asset_ids?: number[]; // 空=不限；非空則要求與漏洞錨定資產有交集
  vulnclass_include?: string[]; // 空=全收；非空則要求漏洞型別命中任一關鍵詞（大小寫不敏感子串）
  vulnclass_exclude?: string[]; // 命中任一關鍵詞即排除（排除優先於包含）
  on_status_change?: boolean; // 是否也接收漏洞處置狀態變更事件
}

// NotificationChannel 是一個渠道例項。config 的欄位隨 kind 而異，
// 且憑據欄位在讀取時被替換成 "__masked__" 開頭的掩碼值——原樣回傳即表示「不改」。
export interface NotificationChannel {
  id: number;
  name: string;
  kind: string;
  enabled: boolean;
  mode: "realtime" | "digest";
  config: Record<string, unknown>;
  filter: NotificationFilter;
  rate_per_min: number;
  created_at: string;
  updated_at: string;
  // secret_keys 由後端按渠道型別給出，前端據此渲染密碼框與「留空即不改」提示，
  // 不硬編碼任何渠道知識。
  secret_keys: string[];
}

// NotificationKind 是 /api/notify/meta 返回的渠道型別後設資料。
export interface NotificationKind {
  kind: string;
  default_rate_per_min: number;
  secret_keys: string[];
}

export interface NotificationMeta {
  kinds: NotificationKind[];
  enabled: boolean;
  public_base_url: string;
  digest_interval_min: string;
  defaults: { digest_interval_min: number };
  stats: {
    channels: number;
    channels_on: number;
    pending: number;
    failed: number;
    sent_today: number;
    backlog_age_ms: number;
  };
}

// NotificationDelivery 是一條投遞記錄，用於投遞歷史與失敗重發。
export interface NotificationDelivery {
  id: number;
  finding_id: string;
  event_kind: string; // finding_created | finding_status_changed
  channel_id: number;
  channel_name: string;
  channel_kind: string;
  state: "pending" | "sending" | "sent" | "failed" | "skipped";
  attempts: number;
  last_error: string;
  batch_id?: number;
  created_at: string;
  sent_at?: string;
  next_attempt_at: string;
  title: string;
  severity: string;
}

// ---- LLM config ----
export interface LLMProfile {
  id: string;
  name: string;
  format: "openai" | "anthropic" | "openai-responses";
  base_url?: string;
  proxy?: string;
  model: string;
  api_key_hint?: string;
  rate_per_second: number;
  rate_per_minute: number;
  context_window_k?: number;
  // 思考開關(thinking.type): ""=不傳送(預設) | "disabled"=關閉 | "enabled"=開啟
  thinking_type?: string;
  // 思考強度: ""=不傳送(預設) | "low"/"medium"/"high"/"xhigh"/"max"
  reasoning_effort?: string;
  is_default: boolean;
  // 輪詢順位：越大越先被選中。啟用配置恆為鏈首，與本值無關。
  priority?: number;
  // true = 不作為故障轉移目標（仍可被 agent/任務顯式綁定使用）。
  pool_exclude?: boolean;
  // true（預設）= 流式(SSE) | false = 真·非流式(stream:false，一次性返回)。
  streaming?: boolean;
  // 單次回覆的輸出上限(token)。0 = 不傳送該欄位，由服務端預設值決定。
  // 注意與 context_window_k 區分：後者是模型總容量，只在本地用於壓縮閾值。
  max_tokens?: number;
  // 上限用哪個請求欄位名，僅 format="openai" 有意義：
  // ""=max_tokens(預設) | "max_completion_tokens"(OpenAI 推理模型只認它)
  max_tokens_field?: string;
  // 自定義會話頭名：非空時每次請求帶該 HTTP 頭，頭值=當前會話/意圖的 session id。
  // ""=不傳送。用於按 session-id 頭做提示快取/粘性路由的閘道器。
  session_header_key?: string;
  // 本配置對重試的覆蓋（建連/空回應/同 provider 安全視窗）。留空/全 0 = 跟隨全域策略。
  retry?: LLMRetryOverride;
}

// ---- LLM 重試策略 ----
// 一層重試的兩個旋鈕。兩者都是「0 = 未配置」：
//   attempts    0=用預設次數 | -1=關閉該層重試 | >0=重試次數
//   interval_ms 0=用預設的指數退避 | >0=改用這個固定毫秒間隔
export interface LLMRetryRule {
  attempts: number;
  interval_ms: number;
}

// 單個 LLM 配置能覆蓋的三層（都是「跟著端點走」的重試）。
export interface LLMRetryOverride {
  connect: LLMRetryRule; // 建連重試：連線重置/超時/429/5xx，流開始前
  empty: LLMRetryRule; // 空回應重試：完成但沒有任何內容（僅 openai 格式）
  stream: LLMRetryRule; // 同 provider 安全視窗重試：未交付輸出前的斷流重放
}

// 全域策略 = 上面三層的預設值 + 兩層只有全域的：
//   breaker 輪詢熔斷（attempts=連續幾次瞬時失敗熔斷，interval_ms=固定冷卻時長）
//   intent  意圖重跑（worker 以 model_error 收場後整條意圖重跑）
export interface LLMRetryPolicy extends LLMRetryOverride {
  breaker: LLMRetryRule;
  intent: LLMRetryRule;
}

// ---- LLM 輪詢（故障轉移）----
// 一個配置在輪詢鏈中的位置與健康狀態。state:
//   ok       正常
//   degraded 有連續失敗但未達熔斷閾值
//   tripped  已熔斷，冷卻期內被跳過（cooldown_secs 為剩餘秒數）
export interface LLMPoolMember {
  profile_id: string;
  name: string;
  model: string;
  format: string;
  priority: number;
  active: boolean; // 是否為當前啟用配置（恆為鏈首）
  excluded: boolean; // pool_exclude：不參與輪詢
  state: "ok" | "degraded" | "tripped";
  fails: number;
  trips: number;
  cooldown_secs: number;
  last_error?: string;
  last_at?: string;
}

export interface LLMPoolStatus {
  enabled: boolean;
  bind_fallback: boolean;
  chain: LLMPoolMember[];
}

// ---- Agents ----
export interface Agent {
  id: string;
  key: string; // 內建為 goals/planner/mainagent/worker；自定義為使用者自定 key
  name: string;
  description?: string;
  role: string;
  builtin: boolean;
  enabled: boolean;
  llm_profile_id?: number | null; // 綁定的 LLM 配置；null/absent = 跟隨任務/會話/全域
  max_turns?: number; // 0 = 無限制
  run_seconds?: number; // worker 單次執行牆鐘上限(秒)；0 = 無限制
  web_search?: boolean; // 是否啟用網路搜尋(受系統全域開關門控)
  interactive_shell?: boolean; // 是否啟用互動式 shell(持久 PTY 會話工具族)
  // P3 觸發後處理策略(僅自定義 agent 有意義)
  trigger_run_mode?: "serial" | "parallel"; // 序列排隊 / 每次觸發各自併發一個會話
  trigger_merge_mode?: "by_task" | "all" | "none"; // 僅 serial：同任務合併 / 全部合併 / 不合並
  trigger_max_parallel?: number; // 僅 parallel：每 agent 併發上限；0=不限
  // 綁定數量(僅列表介面返回)：可見 MCP / 可見 Skill / 綁定工具
  mcp_count?: number;
  skill_count?: number;
  tool_count?: number;
}

export interface PromptVar {
  name: string;
  description: string;
  example: string;
  source: "exploration" | "runtime" | "distilled";
}

export interface PromptVersion {
  version: number;
  ts: string;
  note: string;
  template_text: string;
}

export interface AgentDetail {
  agent: Agent;
  prompt: string;
  variables: PromptVar[];
  versions: PromptVersion[];
  visibility: { mcp: number[]; skill: string[] };
  // 可綁定的 LLM 配置候選(供「預設模型」下拉)；當前綁定見 agent.llm_profile_id
  llm_profiles?: { id: number; name: string; model: string; is_default: boolean }[];
  wrapup_prompt?: string; // 已儲存的收尾提示詞(空=用內建預設)
  wrapup_default?: string; // 內建預設收尾提示詞(佔位/恢復預設)
  wrapup_max_turns?: number; // 已儲存的收尾輪數(0=用內建預設)
  wrapup_max_turns_default?: number; // 內建預設收尾輪數(供 "0=預設N" 提示)
  // 任務級超時收尾詞(僅 worker/planner，task_timeout_wrapup_supported=true 時才顯示該分割槽)
  task_timeout_wrapup_supported?: boolean;
  task_timeout_wrapup_prompt?: string;
  task_timeout_wrapup_default?: string;
  task_timeout_wrapup_max_turns?: number;
  task_timeout_wrapup_max_turns_default?: number;
}

// ---- MCP ----
export interface MCPServer {
  id: number;
  name: string;
  transport: "stdio" | "http" | "sse";
  command?: string;
  args: string[];
  env: Record<string, string>;
  url?: string;
  enabled: boolean;
  insecure?: boolean; // http: skip TLS cert verification (self-signed servers)
  tools?: string[]; // mcp_tools_cache (names only, for the count)
}

export interface MCPTool {
  name: string;
  description: string;
}

// ---- Skills ----
// Fields align with the agentskills.io open specification.
// description covers both "what the skill does" and "when to use it".
export interface SkillItem {
  name: string; // unique key = directory name
  description?: string; // required per spec; covers what + when to use
  license?: string; // optional: SPDX identifier or free text
  compatibility?: string; // optional: environment requirements
  mcps?: string[]; // MCP server names this skill unlocks on load
  files: string[]; // files in the skill directory
  // 呼叫統計（skill_usage 賬本）。從未被呼叫過的 skill：calls=0、last_used 預設。
  calls: number;
  tasks: number; // 載入過它的任務數（chat 會話不計入）
  usage_agents: string[]; // 載入過它的 agent key
  last_used?: string;
}

// SkillCall 是一次 Skill() 呼叫（單個 skill 的最近呼叫列表）。
export interface SkillCall {
  ts: string;
  agent_key: string;
  task_id: number; // 0 = 非任務場景（對話會話）
  session_id: string;
  args_len: number;
}

// MissingSkill 是被點名但不存在的 skill —— "想用但沒有"的缺口。
export interface MissingSkill {
  skill: string;
  calls: number;
  agents: string[];
  last_used?: string;
}

// ---- Tools (內建工具目錄) ----
// key + handler live in Go; only these fields are page-editable. system tools lock
// the key and the parameter *structure* (name/type/required) — the per-param
// description/default and the agent binding are what move.
export interface Tool {
  key: string;
  system: boolean;
  description: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  schema: Record<string, any>; // full JSON-Schema (object with properties)
  agents: string[]; // bound agent keys
  enabled: boolean;
  kind?: "builtin" | "shell" | "command" | "script" | "http"; // 自定義工具型別
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  exec?: Record<string, any>; // 自定義工具執行規格(kind!=builtin)
  deferred?: boolean; // schema 延遲(SearchExtraTools/ExecuteExtraTool)
  calls?: number; // persistent runtime invocation count (older APIs may omit it)
}

// ---- Stats ----
export interface Stats {
  assets: number;
  engine_mode: EngineMode;
  llm_configured: boolean;
  roe_enabled: boolean;
  findings_confirmed: number;
  active_task?: Partial<Task>;
}

// ---- Intercept Rules ----
export type InterceptAction = "allow" | "deny" | "ask";
export type InterceptMatchTarget = "tool_name" | "tool_input";
export type InterceptMatchType = "string" | "regex";

export interface InterceptRule {
  id: number;
  name: string;
  enabled: boolean;
  priority: number;
  match_target: InterceptMatchTarget;
  match_type: InterceptMatchType;
  pattern: string;
  action: InterceptAction;
  message: string;
  timeout_enabled: boolean;
  timeout_seconds: number;
  timeout_action: "deny" | "allow";
  created_at: string;
  updated_at: string;
}

// ---- Asset Intercept Rules（資產攔截：全域黑名單） ----
export type AssetInterceptKind =
  | "exact_domain"
  | "exact_ip"
  | "exact_url"
  | "fuzzy_domain"
  | "fuzzy_ip"
  | "fuzzy_url"
  | "cidr";

// action 僅用於任務級規則：block=攔截(禁止測試) allow=允許(白名單)。
export type AssetInterceptAction = "block" | "allow";

// 任務級資產攔截/允許規則的錄入項（建立任務、任務詳情編輯使用）。
export interface AssetInterceptRuleInput {
  action: AssetInterceptAction;
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  enabled: boolean;
}

export interface AssetInterceptRule {
  id: number;
  enabled: boolean;
  action?: AssetInterceptAction; // 全域規則不帶此欄位（恆為攔截）；任務級規則區分 block/allow
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  builtin: boolean;
  created_at: string;
  updated_at: string;
}

export interface InterceptPending {
  decision_source?: "rule" | "model" | "manual" | "unknown" | "";
  id: number;
  rule_id?: number;
  conversation_id?: number;
  task_id?: string;
  agent_name: string;
  tool_name: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  tool_input: Record<string, any>;
  status: "pending" | "allowed" | "denied" | "timeout";
  reason: string; // 規則 message 或模型判定理由(模型判定帶 [模型] 字首)
  decided_at?: string;
  created_at: string;
}

// JudgeConfig: 模型兜底審批(僅當沒有任何攔截規則命中時由模型判斷)的全域配置。
export interface JudgeConfig {
  enabled: boolean;
  profile_id: number; // 0 = 跟隨啟用/預設配置
  prompt: string; // 判定提示詞;GET 未設定時後端回填內建模板全文
  timeout_seconds: number; // 模型呼叫超時
  fail_action: "allow" | "ask" | "deny"; // 模型出錯/超時/不可解析時的回退
  ask_timeout_seconds: number; // 模型判 ask 轉人工後的審批等待超時
  ask_timeout_action: "allow" | "deny"; // 審批超時後的預設動作
  incomplete_script_action: "ask" | "model"; // 指令碼快照不完整時：人工審查或繼續模型審查
}

// JudgeUsage: 模型兜底審批(judge 通道)的累計 token 用量 + 近 N 天每日序列。
export interface JudgeDayUsage {
  date: string; // YYYY-MM-DD (UTC)
  calls: number;
  input_tokens: number;
  output_tokens: number;
}
export interface JudgeUsage {
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  daily: JudgeDayUsage[];
}

export interface InterceptApprovalFilter {
  status?: InterceptPending["status"];
  decision_source?: "rule" | "model" | "manual" | "unknown";
  q?: string;
  from?: string;
  to?: string;
}

// InterceptApprovalRow enriches InterceptPending with conversation/task and rule context.
export interface InterceptApprovalRow extends InterceptPending {
  conv_title: string; // "" if no linked conversation
  conv_agent_key: string; // "" if no linked conversation
  rule_name: string; // "" if rule was deleted
}

// ── 資產同步 (ScopeSentry 資料來源) ──────────────────────────────────────────────
export interface SSProject {
  id: string; // MongoDB ObjectID — used as filter.project
  name: string;
  logo?: string;
  AssetCount?: number;
  tag?: string;
}

export interface SSTask {
  id: string;
  name: string; // used as filter.task
  status?: number;
  progress?: number;
  creatTime?: string;
  endTime?: string;
}

// ConvTokenSummary — one conversation's token total (+ profile/date) for merging
// chat usage into the dashboard token stats. GET /api/tokens/conversations.
export interface ConvTokenSummary {
  llm_profile_id: number | null;
  created_at: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// ---- Command recording (Bash execution history) ----
export interface CommandRecord {
  id: number;
  exploration_id: number;
  worker: string;
  tool: string;
  command: string; // raw tool input (JSON)
  output: string;
  is_error: boolean;
  created_at: string;
}

// 單個工具的呼叫統計（/commands/stats）；errors 為其中失敗的次數。
export interface ToolStat {
  tool: string;
  total: number;
  errors: number;
}

// ---- LLM recording ----
export interface LLMRecordItem {
  id: number;
  ts: string;
  model: string;
  profile_name: string;
  session_id: string;
  task_id: string;
  worker: string;
  latency_ms: number;
  input_tokens: number;
  output_tokens: number;
  cache_read: number;
  cache_write: number;
  status: string;
  error?: string;
}

export interface LLMRecordDetail extends LLMRecordItem {
  request_body: string;
  response_body: string;
  // provider 實際收發的 HTTP 原文：請求為 buildBody() 發出的完整 body（含工具
  // schema），回應為原始 SSE 幀。上面的 request_body/response_body 是歸一化檢視，
  // 丟棄了工具 schema 與 tool_use 塊。舊記錄為空。
  raw_request?: string;
  raw_response?: string;
}

// One distinct task with its LLM-record count (task picker on the records page).
export interface LLMTask {
  task_id: string;
  count: number;
}

// The exact JSON sent to the review model, retained for all model verdicts.
export interface InterceptReviewInput {
  version: number;
  script_capture_status?: "not_applicable" | "complete" | "incomplete";
  script_files?: {
    path: string;
    source?: "file" | "command";
    sha256?: string;
    bytes?: number;
    content?: string;
    complete?: boolean;
    truncated?: boolean;
    read_error?: string;
  }[];
  background?: {
    // worker_summary is retained only for immutable v2/v3 snapshots.
    source: "user_message" | "worker_summary";
    text: string;
    truncated?: boolean;
  };
  // Version 1 snapshots are immutable and remain readable in historical audits.
  task?: {
    task_id: number;
    description: string;
    goal: string;
    constraints: { id: number; kind: string; text: string; origin: string; created_at: number }[];
    truncated?: boolean;
  };
  working_directory?: string;
  worker_intent?: string;
  turn_input?: string;
  background_truncated?: boolean;
  // Legacy v1/v2 snapshots only; v3 never sends execution history.
  history?: {
    tool_use_id: string;
    tool: string;
    arguments_preview: string;
    result: string;
    status: "succeeded" | "failed";
    truncated?: boolean;
  }[];
  history_truncated?: boolean;
  correlation?: "exact" | "ambiguous" | "unavailable";
  tool_name: string;
  arguments: Record<string, unknown>;
}

// Immutable review snapshot plus separately recorded execution outcome.
export interface InterceptAudit {
  model_input?: InterceptReviewInput;
  model_input_digest?: string;
  run_id?: string;
  tool_use_id?: string;
  correlation: "exact" | "ambiguous" | "unavailable";
  input_digest: string;
  user_message: string;
  user_truncated?: boolean;
  context:
    | { kind: string; tool?: string; tool_use_id?: string; text: string; is_error?: boolean; truncated?: boolean }[]
    | null;
  context_truncated?: boolean;
  captured_at: string;
  model_fallback?: boolean;
  initial_action: "allow" | "ask" | "deny";
  initial_reason: string;
  effective_action?: "allow" | "deny";
  decision_reason?: string;
  rule_name?: string;
  config_digest?: string;
  profile_id?: number;
  execution_status: "not_started" | "not_executed" | "awaiting_result" | "succeeded" | "failed" | "unknown";
  output?: string;
  output_truncated?: boolean;
  execution_ended_at?: string;
}
export interface InterceptDetail extends InterceptApprovalRow {
  audit: InterceptAudit | null;
}

export type TrafficEvidenceRole = "baseline" | "proof" | "verification" | "supporting";
export interface TrafficEvidenceRef {
  traffic_id: string;
  role?: TrafficEvidenceRole;
  note?: string;
}
export interface TrafficEvidenceSnapshot {
  id: string;
  source_traffic_id: string;
  captured_at: number;
  url: string;
  method: string;
  status: number;
  content_type: string;
  req_head?: string;
  resp_head?: string;
  req_hash: string;
  resp_hash: string;
  req_len: number;
  resp_len: number;
}
export interface FindingTrafficBinding {
  id: string;
  finding_id: string;
  snapshot_id: string;
  role: TrafficEvidenceRole;
  note: string;
  position: number;
  created_at: string;
  snapshot: TrafficEvidenceSnapshot;
}
export interface FindingTraffic {
  finding_id: string;
  version: number;
  report_version: number;
  bindings: FindingTrafficBinding[];
}
export interface EvidenceBodyPreview {
  content: string;
  offset: number;
  total: number;
  next_offset: number;
  truncated: boolean;
  binary: boolean;
}
export interface FindingTrafficDetail {
  binding: FindingTrafficBinding;
  request: EvidenceBodyPreview;
  response: EvidenceBodyPreview;
}

/** GET /api/update/check —— 當前版本與 GitHub 最新正式版的比較結果。 */
export interface UpdateCheck {
  /** 當前執行的版本；開發構建為 "dev" 或 git describe 的帶字尾形式。 */
  current: string;
  /** 執行形態。docker 下換裝只作用於容器可寫層，重建容器會退回映像版本。 */
  mode: "docker" | "binary";
  os: string;
  arch: string;
  repo: string;
  /** 是否存在可回滾的上一版本（artex.old）。 */
  has_backup: boolean;
  /** 本次啟動時自更新自舉的結論（換裝失敗 / 已回滾等），無事發生時為空。 */
  boot_notice?: string;
  rolled_back?: boolean;
  /** 查詢 GitHub 失敗時給出原因，此時下面的欄位都不會有。 */
  error?: string;
  latest?: string;
  notes?: string;
  html_url?: string;
  published_at?: string;
  /** 當前平臺對應的釋出包名，以及該 Release 是否真的帶了它。 */
  asset?: string;
  asset_available?: boolean;
  size?: number;
  has_update?: boolean;
  /** 雙方版本號是否可比較；開發構建為 false，此時停用一鍵更新。 */
  comparable?: boolean;
  /** comparable 為 false 時的說明。 */
  reason?: string;
}

/** /api/update/stream 推送的一條更新進度。 */
export interface UpdateProgress {
  phase: "idle" | "downloading" | "verifying" | "extracting" | "staged" | "failed";
  /** 僅下載階段有意義（0-100）；其餘階段為 -1。 */
  percent: number;
  message: string;
  version?: string;
  error?: string;
}

// Original execution selected from an approval, never submitted to the reviewer.
export interface InterceptExecution {
  conversation_id: number | null;
  task_id: string | null;
  session: string;
  seq: number;
  items: Activity[];
}

export interface FindingDistinctStats {
  total: number;
  reports: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  unassessed: number;
}
export interface FindingCase {
  id: string;
  task_id: string | null;
  title: string;
  reason: string;
  severity: Severity | "";
  severity_reason: string;
  report?: string;
  version: number;
  report_version: number;
  active: boolean;
  count: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  created_at: string;
}
export interface FindingCaseListRow {
  task_name?: string;
  task_description?: string;
  assets?: FindingAsset[];
  asset_count?: number;
  last_found_at?: string;
  case?: FindingCase | null;
  finding?: Finding;
  matched_ids: number[];
}
export interface FindingCasePage {
  matching_reports?: number;
  items: FindingCaseListRow[];
  total: number;
  page: number;
  page_size: number;
  stats: FindingDistinctStats;
}
export interface FindingCaseEvent {
  id: string;
  action: string;
  actor: string;
  reason: string;
  finding_ids: number[];
  created_at: string;
}
export interface FindingCaseDetail {
  case: FindingCase;
  events: FindingCaseEvent[];
}
export interface FindingCaseSuggestion {
  id: string;
  task_id: string;
  left_id: string;
  right_id: string;
  title: string;
  reason: string;
  state: string;
}
export interface FindingCaseReviewRun {
  conversation_id: number;
  task_id: string;
  state: "queued" | "running" | "done" | "failed";
  error: string;
}
