// Centralised status → color/label semantics, reused across the whole app.
// Spec §8.3: 意圖 / 覆蓋 / 任務 / 嚴重度 each have a consistent color set.

export type Tone = "neutral" | "blue" | "green" | "amber" | "red" | "rose" | "violet" | "slate";

export const toneClasses: Record<Tone, string> = {
  neutral: "bg-muted text-muted-foreground border-transparent",
  blue: "bg-blue-500/15 text-blue-600 dark:text-blue-400 border-blue-500/20",
  green: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-emerald-500/20",
  amber: "bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/20",
  red: "bg-red-500/15 text-red-600 dark:text-red-400 border-red-500/20",
  // rose 用作「嚴重」——實心高強調,視覺上明顯高於「高危」的軟紅描邊。
  rose: "bg-rose-600 text-white border-rose-600 dark:bg-rose-600 dark:text-white",
  violet: "bg-violet-500/15 text-violet-600 dark:text-violet-400 border-violet-500/20",
  slate: "bg-slate-500/15 text-slate-600 dark:text-slate-400 border-slate-500/20",
};

export const toneDot: Record<Tone, string> = {
  neutral: "bg-muted-foreground",
  blue: "bg-blue-500",
  green: "bg-emerald-500",
  amber: "bg-amber-500",
  red: "bg-red-500",
  rose: "bg-white",
  violet: "bg-violet-500",
  slate: "bg-slate-500",
};

interface StatusMeta {
  label: string;
  tone: Tone;
}

const intent: Record<string, StatusMeta> = {
  open: { label: "待領", tone: "slate" },
  running: { label: "執行中", tone: "blue" },
  paused: { label: "已暫停", tone: "amber" },
  done: { label: "已完成", tone: "green" },
  // blocked = 模型/API/網路故障重試用盡，這條意圖基本沒真正探成（非目標攔截）。
  blocked: { label: "執行出錯", tone: "red" },
  // exhausted = 達到步數/時間預算被中途掐斷、只寫回部分結果（非方向已探盡）。
  exhausted: { label: "預算耗盡", tone: "violet" },
  // stopped = 歷史軟刪除狀態（保留,歷史資料）。
  stopped: { label: "已停止", tone: "slate" },
  // deleted = 使用者假刪除了該意圖（保留節點與血緣，刪除原因見 delete_reason 欄位）。
  deleted: { label: "已刪除", tone: "slate" },
};

const task: Record<string, StatusMeta> = {
  created: { label: "已建立", tone: "slate" },
  queued: { label: "排隊中", tone: "amber" },
  running: { label: "執行中", tone: "blue" },
  paused: { label: "已暫停", tone: "amber" },
  done: { label: "已完成", tone: "green" },
  failed: { label: "失敗", tone: "red" },
  timeout: { label: "已超時", tone: "amber" },
};

const severity: Record<string, StatusMeta> = {
  critical: { label: "嚴重", tone: "rose" },
  high: { label: "高危", tone: "red" },
  medium: { label: "中危", tone: "amber" },
  low: { label: "低危", tone: "slate" },
};

const finding: Record<string, StatusMeta> = {
  pending: { label: "待處理", tone: "amber" },
  in_progress: { label: "處理中", tone: "blue" },
  confirmed: { label: "已確認", tone: "red" },
  resolved: { label: "已處理", tone: "green" },
  fixed: { label: "已修復", tone: "green" },
  false_positive: { label: "誤報", tone: "slate" },
  ignored: { label: "忽略", tone: "neutral" },
  duplicate: { label: "重複", tone: "neutral" },
  risk_accepted: { label: "風險接受", tone: "violet" },
};

const engine: Record<string, StatusMeta> = {
  exploring: { label: "探索中", tone: "blue" },
  paused: { label: "已暫停", tone: "amber" },
  stalled: { label: "停滯", tone: "red" },
  idle: { label: "空閒", tone: "neutral" },
};

const goal: Record<string, StatusMeta> = {
  open: { label: "進行中", tone: "blue" },
  met: { label: "已達成", tone: "green" },
  abandoned: { label: "已放棄", tone: "slate" },
};

const audit: Record<string, StatusMeta> = {
  allow: { label: "放行", tone: "green" },
  block: { label: "攔截", tone: "red" },
};

const node: Record<string, StatusMeta> = {
  observed: { label: "觀測", tone: "slate" },
  confirmed: { label: "確認", tone: "green" },
  tombstoned: { label: "廢棄", tone: "neutral" },
};

// 推送投遞狀態。sending 用 blue 而不是 amber：它不是「有問題」，
// 而是「已被領取、正在發」，與 pending 的等待語義要能區分開。
const delivery: Record<string, StatusMeta> = {
  pending: { label: "待發送", tone: "amber" },
  sending: { label: "傳送中", tone: "blue" },
  sent: { label: "已送達", tone: "green" },
  failed: { label: "失敗", tone: "red" },
  skipped: { label: "已跳過", tone: "neutral" },
};

const maps = {
  intent,
  task,
  severity,
  finding,
  engine,
  goal,
  audit,
  node,
  delivery,
} as const;

export type StatusDomain = keyof typeof maps;

export function statusMeta(domain: StatusDomain, key: string): StatusMeta {
  return maps[domain][key] ?? { label: key, tone: "neutral" };
}
