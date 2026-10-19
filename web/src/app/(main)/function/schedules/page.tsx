"use client";

import * as React from "react";

import {
  CalendarClock,
  ChevronDown,
  ChevronRight,
  Loader2,
  Pause,
  Play,
  Plus,
  Search,
  Trash2,
  Zap,
} from "lucide-react";
import { toast } from "sonner";

import { DateSelect } from "@/components/date-select";
import { TimeSelect } from "@/components/time-select";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, type ScheduleInput } from "@/lib/api";
import type { Task, TaskSchedule } from "@/lib/types";

const weekdays = ["週一", "週二", "週三", "週四", "週五", "週六", "週日"];

const blank: ScheduleInput = {
  name: "",
  schedule_type: "weekly",
  weekdays: [6, 7],
  start_time: "18:00",
  end_time: "",
  task_ids: [],
  timezone_mode: "beijing",
  start_immediately: false,
};

function statusText(s: TaskSchedule) {
  return (
    (
      {
        waiting: "等待視窗",
        running: "視窗執行中",
        outside: "視窗外",
        completed: "已完成",
        error: "錯誤",
        paused: "已暫停",
      } as Record<string, string>
    )[s.status] ?? "未知狀態"
  );
}

function formatTime(value?: string) {
  return value ? new Date(value).toLocaleString("zh-CN") : "—";
}

function formatDateValue(value?: string) {
  if (!value) return "未設定";
  const [year, month, day] = value.split("-");
  return year && month && day ? `${year}年${Number(month)}月${Number(day)}日` : value;
}

function formatClock(value?: string) {
  return value ? value.slice(0, 5) : "未設定";
}

function periodLabel(item: TaskSchedule) {
  if (item.schedule_type === "once") {
    return item.end_date
      ? `一次性 · ${formatDateValue(item.run_date)} 至 ${formatDateValue(item.end_date)}`
      : `一次性 · ${formatDateValue(item.run_date)}`;
  }
  const selected = item.weekdays.map((day) => weekdays[day - 1]).filter(Boolean);
  return `每週 · ${selected.length ? selected.join("、") : "未選擇星期"}`;
}

function windowLabel(item: TaskSchedule) {
  const start = formatClock(item.start_time);
  const end = formatClock(item.end_time);
  if (item.schedule_type === "once") {
    const startText = `${formatDateValue(item.run_date)} ${start}`;
    if (item.end_date) return `${startText} 至 ${formatDateValue(item.end_date)} ${item.end_time ? end : "24:00"}`;
    if (!item.end_time) return `${startText} 起持續執行`;
    return `${startText} 至 ${end}${end <= start ? "（次日）" : ""}`;
  }
  if (!item.end_time) return `${start} 起持續執行`;
  return `${start} 至 ${end}${end <= start ? "（次日）" : ""}`;
}

function startLabel(item: TaskSchedule) {
  return item.schedule_type === "once"
    ? `${formatDateValue(item.run_date)} ${formatClock(item.start_time)}`
    : formatClock(item.start_time);
}

function endLabel(item: TaskSchedule) {
  if (item.schedule_type === "once" && item.end_date) {
    return `${formatDateValue(item.end_date)} ${item.end_time ? formatClock(item.end_time) : "24:00"}`;
  }
  if (!item.end_time) return "持續執行";
  const end = formatClock(item.end_time);
  const start = formatClock(item.start_time);
  return `${end}${end <= start ? "（次日）" : ""}`;
}

function timezoneLabel(item: TaskSchedule) {
  return item.timezone_mode === "beijing" ? "中國北京時間" : `系統時區${item.timezone ? `（${item.timezone}）` : ""}`;
}

function nextWindowLabel(value?: string, timezone?: string) {
  if (!value) return "—";
  try {
    const timeZone = timezone && timezone !== "Local" ? timezone : undefined;
    return new Intl.DateTimeFormat("zh-CN", {
      timeZone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    }).format(new Date(value));
  } catch {
    return formatTime(value);
  }
}

function scheduleErrorText(value?: string) {
  if (!value) return "";
  return value
    .replaceAll("end_date", "截止日期")
    .replaceAll("run_date", "開始日期")
    .replaceAll("start_time", "開始時間")
    .replaceAll("end_time", "結束時間")
    .replaceAll("weekdays", "星期")
    .replaceAll("timezone_mode", "時區")
    .replaceAll("schedule_type", "規則");
}

type ScheduleHistoryEntry = NonNullable<TaskSchedule["history"]>[number];

function historyActionText(action: string) {
  return (
    (
      {
        running: "視窗開始",
        outside: "視窗結束",
        completed: "計劃完成",
        waiting: "等待視窗",
        paused: "計劃暫停",
        resume: "恢復任務",
        pause: "暫停任務",
        error: "執行錯誤",
      } as Record<string, string>
    )[action] ?? "狀態變化"
  );
}

function historyEventClass(event: ScheduleHistoryEntry) {
  if (!event.success)
    return "border-red-200 bg-red-50 text-red-700 dark:border-red-900 dark:bg-red-950/40 dark:text-red-300";
  if (event.action === "running" || event.action === "resume") {
    return "border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-300";
  }
  if (event.action === "outside" || event.action === "pause" || event.action === "paused") {
    return "border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-300";
  }
  return "border-slate-200 bg-slate-50 text-slate-700 dark:border-slate-800 dark:bg-slate-900/60 dark:text-slate-300";
}

function historyDotClass(event: ScheduleHistoryEntry) {
  if (!event.success) return "bg-red-500";
  if (event.action === "running" || event.action === "resume") return "bg-emerald-500";
  if (event.action === "outside" || event.action === "pause" || event.action === "paused") return "bg-amber-500";
  return "bg-slate-400";
}

function ScheduleTimeline({ schedule, loading }: { schedule?: TaskSchedule; loading: boolean }) {
  if (loading) {
    return (
      <div className="flex items-center gap-2 py-4 text-muted-foreground text-sm">
        <Loader2 className="size-4 animate-spin" />
        載入執行時間線…
      </div>
    );
  }
  // The API returns newest first. Keep that order so the latest transition is
  // visible immediately when a row is expanded.
  const events = schedule?.history ?? [];
  if (!events.length) {
    return <div className="py-4 text-muted-foreground text-sm">暫無執行記錄，計劃進入第一個視窗後會顯示時間線。</div>;
  }
  return (
    <div className="grid gap-3 rounded-lg border bg-muted/20 p-4">
      <div className="flex items-center justify-between gap-3">
        <div className="font-medium text-sm">執行時間線</div>
        <span className="text-muted-foreground text-xs">最近 {Math.min(events.length, 20)} 條記錄</span>
      </div>
      <div className="grid gap-0">
        {events.slice(0, 20).map((event, index) => (
          <div key={event.id} className="relative flex gap-3 pb-3 last:pb-0">
            {index < Math.min(events.length, 20) - 1 ? (
              <span className="absolute top-4 bottom-0 left-[5px] w-px bg-border" aria-hidden="true" />
            ) : null}
            <span
              className={`relative mt-1.5 size-3 shrink-0 rounded-full border-2 border-background shadow-sm ${historyDotClass(event)}`}
              title={historyActionText(event.action)}
            />
            <div className="min-w-0 flex-1 rounded-md border bg-background px-3 py-2">
              <div className="flex flex-wrap items-center gap-2 text-xs">
                <Badge variant="outline" className={historyEventClass(event)}>
                  {historyActionText(event.action)}
                </Badge>
                <span className="text-muted-foreground">{formatTime(event.created_at)}</span>
                <span className="text-muted-foreground">{event.task_id ? `任務 #${event.task_id}` : "計劃任務"}</span>
              </div>
              {event.message ? (
                <div className={`mt-1 text-xs ${event.success ? "text-muted-foreground" : "text-destructive"}`}>
                  {event.message}
                </div>
              ) : null}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function statusClass(status: TaskSchedule["status"]) {
  return (
    {
      waiting: "border-sky-200 bg-sky-50 text-sky-700 dark:border-sky-900 dark:bg-sky-950/40 dark:text-sky-300",
      running:
        "border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-300",
      outside:
        "border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-300",
      completed: "border-blue-200 bg-blue-50 text-blue-700 dark:border-blue-900 dark:bg-blue-950/40 dark:text-blue-300",
      error: "border-red-200 bg-red-50 text-red-700 dark:border-red-900 dark:bg-red-950/40 dark:text-red-300",
      paused:
        "border-slate-200 bg-slate-100 text-slate-700 dark:border-slate-800 dark:bg-slate-900/60 dark:text-slate-300",
    } as Record<TaskSchedule["status"], string>
  )[status];
}

function ScheduleForm({
  value,
  tasks,
  onSave,
  onCancel,
  saving,
}: {
  value: ScheduleInput;
  tasks: Task[];
  onSave: (v: ScheduleInput) => void;
  onCancel?: () => void;
  saving: boolean;
}) {
  const [draft, setDraft] = React.useState(value);
  const [taskQuery, setTaskQuery] = React.useState("");
  const set = (patch: Partial<ScheduleInput>) => setDraft((v) => ({ ...v, ...patch }));
  const toggleTask = (id: number, checked: boolean) =>
    set({ task_ids: checked ? [...(draft.task_ids ?? []), id] : (draft.task_ids ?? []).filter((x) => x !== id) });
  const toggleDay = (day: number) =>
    set({
      weekdays: (draft.weekdays ?? []).includes(day)
        ? (draft.weekdays ?? []).filter((x) => x !== day)
        : [...(draft.weekdays ?? []), day].sort(),
    });
  const filteredTasks = React.useMemo(() => {
    const query = taskQuery.trim().toLowerCase();
    if (!query) return tasks;
    return tasks.filter((task) =>
      [String(task.id), task.name ?? "", task.description, task.goal].some((value) =>
        value.toLowerCase().includes(query),
      ),
    );
  }, [taskQuery, tasks]);
  let taskListContent: React.ReactNode;
  if (tasks.length === 0) {
    taskListContent = <span className="text-muted-foreground text-sm">暫無任務</span>;
  } else if (filteredTasks.length === 0) {
    taskListContent = <span className="text-muted-foreground text-sm">沒有匹配的任務</span>;
  } else {
    taskListContent = filteredTasks.map((task) => (
      <div key={task.id} className="flex items-center gap-2 py-1 text-sm">
        <Checkbox
          id={`schedule-task-${task.id}`}
          checked={(draft.task_ids ?? []).includes(Number(task.id))}
          onCheckedChange={(v) => toggleTask(Number(task.id), !!v)}
        />
        <Label htmlFor={`schedule-task-${task.id}`} className="truncate">
          #{task.id} {task.name || task.description}
        </Label>
      </div>
    ));
  }
  return (
    <div className="grid gap-4 rounded-lg border p-4">
      <div className="grid gap-2">
        <Label>計劃名稱</Label>
        <Input value={draft.name} onChange={(e) => set({ name: e.target.value })} placeholder="例如：週末持續掃描" />
      </div>
      <div className="grid grid-cols-2 gap-3">
        <div className="grid gap-2">
          <Label>規則</Label>
          <Select
            value={draft.schedule_type}
            onValueChange={(value) => set({ schedule_type: value as ScheduleInput["schedule_type"] })}
          >
            <SelectTrigger className="h-10 w-full bg-background">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="weekly">按週</SelectItem>
              <SelectItem value="once">指定日期</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="grid gap-2">
          <Label>時區</Label>
          <Select
            value={draft.timezone_mode}
            onValueChange={(value) => set({ timezone_mode: value as ScheduleInput["timezone_mode"] })}
          >
            <SelectTrigger className="h-10 w-full bg-background">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="beijing">中國北京時間</SelectItem>
              <SelectItem value="system">系統當前時區</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
      {draft.schedule_type === "once" ? (
        <div className="grid grid-cols-2 gap-3">
          <div className="grid gap-2">
            <Label>開始日期</Label>
            <DateSelect
              value={draft.run_date ?? ""}
              onValueChange={(value) => set({ run_date: value })}
              aria-label="開始日期"
            />
          </div>
          <div className="grid gap-2">
            <Label>截止日期（可選）</Label>
            <DateSelect
              value={draft.end_date ?? ""}
              onValueChange={(value) => set({ end_date: value })}
              optional
              placeholder="不設定截止日期"
              aria-label="截止日期，可選"
            />
          </div>
        </div>
      ) : (
        <div className="grid gap-2">
          <Label>星期</Label>
          <div className="flex flex-wrap gap-3">
            {weekdays.map((label, index) => (
              <div key={label} className="flex items-center gap-2 text-sm">
                <Checkbox
                  id={`schedule-weekday-${index + 1}`}
                  checked={(draft.weekdays ?? []).includes(index + 1)}
                  onCheckedChange={() => toggleDay(index + 1)}
                />
                <Label htmlFor={`schedule-weekday-${index + 1}`}>{label}</Label>
              </div>
            ))}
          </div>
        </div>
      )}
      <div className="grid grid-cols-2 gap-3">
        <div className="grid gap-2">
          <Label>開始時間</Label>
          <TimeSelect
            value={draft.start_time}
            onValueChange={(value) => set({ start_time: value })}
            aria-label="開始時間"
          />
        </div>
        <div className="grid gap-2">
          <Label>結束時間（可選）</Label>
          <TimeSelect
            value={draft.end_time ?? ""}
            onValueChange={(value) => set({ end_time: value })}
            optional
            aria-label="結束時間，可選"
          />
        </div>
      </div>
      <p className="text-muted-foreground text-xs">
        一次性計劃可設定截止日期；截止日期為空時僅按結束時間控制。結束時間為空表示持續執行，結束時間早於開始時間表示跨午夜視窗。
      </p>
      <div className="flex items-center gap-2 text-sm">
        <Checkbox
          id="schedule-start-immediately"
          checked={draft.start_immediately}
          onCheckedChange={(v) => set({ start_immediately: !!v })}
        />
        <Label htmlFor="schedule-start-immediately">建立後立即啟動，不等待第一個視窗</Label>
      </div>
      <div className="grid gap-2">
        <div className="flex items-center justify-between gap-3">
          <Label>綁定任務</Label>
          <span className="text-muted-foreground text-xs">已選 {(draft.task_ids ?? []).length} 個</span>
        </div>
        <div className="relative">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={taskQuery}
            onChange={(event) => setTaskQuery(event.target.value)}
            placeholder="檢索任務名稱、描述或 ID"
            className="h-9 pl-8"
          />
        </div>
        <div className="max-h-40 overflow-auto rounded border p-2">{taskListContent}</div>
      </div>
      <div className="flex justify-end gap-2">
        <Button variant="outline" onClick={onCancel}>
          取消
        </Button>
        <Button disabled={saving} onClick={() => onSave(draft)}>
          {saving && <Loader2 className="animate-spin" data-icon="inline-start" />}儲存計劃
        </Button>
      </div>
    </div>
  );
}

export default function SchedulesPage() {
  const [items, setItems] = React.useState<TaskSchedule[]>([]);
  const [tasks, setTasks] = React.useState<Task[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [editing, setEditing] = React.useState<{ id?: number; value: ScheduleInput } | null>(null);
  const [saving, setSaving] = React.useState(false);
  const [query, setQuery] = React.useState("");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState("10");
  const [selectedIds, setSelectedIds] = React.useState<number[]>([]);
  const [expandedId, setExpandedId] = React.useState<number | null>(null);
  const [timelineById, setTimelineById] = React.useState<Record<number, TaskSchedule>>({});
  const [timelineLoadingId, setTimelineLoadingId] = React.useState<number | null>(null);
  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const [s, t] = await Promise.all([api.schedules(), api.tasks()]);
      setItems(s);
      setTasks(t.tasks);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);
  React.useEffect(() => {
    void load();
  }, [load]);
  const filteredItems = React.useMemo(() => {
    const normalized = query.trim().toLowerCase();
    if (!normalized) return items;
    return items.filter((item) =>
      [item.name, String(item.id), periodLabel(item), windowLabel(item), ...item.task_ids.map(String)]
        .join(" ")
        .toLowerCase()
        .includes(normalized),
    );
  }, [items, query]);
  const size = Number(pageSize);
  const totalPages = Math.max(1, Math.ceil(filteredItems.length / size));
  const visibleItems = filteredItems.slice((page - 1) * size, page * size);
  const visibleIds = visibleItems.map((item) => item.id);
  const selectedVisibleCount = visibleIds.filter((id) => selectedIds.includes(id)).length;
  const allVisibleSelected = visibleItems.length > 0 && selectedVisibleCount === visibleItems.length;
  const someVisibleSelected = selectedVisibleCount > 0 && !allVisibleSelected;
  let headerSelectionState: boolean | "indeterminate" = false;
  if (allVisibleSelected) headerSelectionState = true;
  else if (someVisibleSelected) headerSelectionState = "indeterminate";
  React.useEffect(() => {
    setPage((current) => Math.min(current, totalPages));
  }, [totalPages]);
  const updateQuery = (value: string) => {
    setQuery(value);
    setPage(1);
  };
  const updatePageSize = (value: string) => {
    setPageSize(value);
    setPage(1);
  };
  const toggleSelected = (id: number, checked: boolean) => {
    setSelectedIds((current) => {
      if (!checked) return current.filter((selected) => selected !== id);
      return current.includes(id) ? current : [...current, id];
    });
  };
  const toggleAllVisible = (checked: boolean) => {
    setSelectedIds((current) => {
      if (checked) return Array.from(new Set([...current, ...visibleIds]));
      return current.filter((id) => !visibleIds.includes(id));
    });
  };
  const refreshTimeline = async (id: number) => {
    setTimelineLoadingId(id);
    try {
      const detail = await api.schedule(id);
      setTimelineById((current) => ({ ...current, [id]: detail }));
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setTimelineLoadingId(null);
    }
  };
  const invalidateTimeline = async (id: number) => {
    setTimelineById((current) => {
      if (!current[id]) return current;
      const next = { ...current };
      delete next[id];
      return next;
    });
    if (expandedId === id) await refreshTimeline(id);
  };
  const toggleTimeline = async (id: number) => {
    if (expandedId === id) {
      setExpandedId(null);
      return;
    }
    setExpandedId(id);
    if (timelineById[id]) return;
    await refreshTimeline(id);
  };
  const save = async (value: ScheduleInput) => {
    if (
      !value.name.trim() ||
      !value.start_time ||
      (value.schedule_type === "once" ? !value.run_date : !value.weekdays?.length)
    ) {
      toast.error("請補全計劃名稱、日期/星期和開始時間");
      return;
    }
    setSaving(true);
    try {
      if (editing?.id) await api.updateSchedule(editing.id, value);
      else await api.createSchedule(value);
      toast.success("計劃已儲存");
      setEditing(null);
      await load();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  };
  const remove = async (id: number) => {
    if (!window.confirm("確定刪除這個計劃嗎？")) return;
    try {
      await api.deleteSchedule(id);
      setTimelineById((current) => {
        const next = { ...current };
        delete next[id];
        return next;
      });
      if (expandedId === id) setExpandedId(null);
      await load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  };
  const action = async (id: number, type: "pause" | "resume" | "run") => {
    try {
      if (type === "pause") await api.pauseSchedule(id);
      else if (type === "resume") await api.resumeSchedule(id);
      else {
        const result = await api.runScheduleNow(id);
        if (result.errors?.length) {
          toast.warning(
            `已啟動 ${result.started} 個任務，但有 ${result.errors.length} 個任務未啟動：${result.errors.join("；")}`,
          );
        } else {
          toast.success(`已立即執行 ${result.started} 個任務`);
        }
      }
      await invalidateTimeline(id);
      await load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  };
  const bulkRun = async () => {
    const ids = [...selectedIds];
    if (!ids.length) return;
    let started = 0;
    const errors: string[] = [];
    const results = await Promise.allSettled(ids.map((id) => api.runScheduleNow(id)));
    results.forEach((result, index) => {
      if (result.status === "rejected") {
        errors.push(`計劃 #${ids[index]}：${(result.reason as Error).message}`);
        return;
      }
      started += result.value.started;
      if (result.value.errors?.length) errors.push(`計劃 #${ids[index]}：${result.value.errors.join("；")}`);
    });
    if (errors.length) {
      toast.warning(`已啟動 ${started} 個任務，但有 ${errors.length} 項未完成：${errors.join("；")}`);
    } else {
      toast.success(`已立即執行 ${started} 個任務`);
    }
    setSelectedIds([]);
    await Promise.all(ids.map((id) => invalidateTimeline(id)));
    await load();
  };
  const bulkRemove = async () => {
    const ids = [...selectedIds];
    if (!ids.length || !window.confirm(`確定刪除選中的 ${ids.length} 個計劃嗎？`)) return;
    const results = await Promise.allSettled(ids.map((id) => api.deleteSchedule(id)));
    const failed = results.filter((result) => result.status === "rejected").length;
    if (failed) toast.warning(`已刪除 ${ids.length - failed} 個計劃，${failed} 個計劃刪除失敗`);
    else toast.success(`已刪除 ${ids.length} 個計劃`);
    setTimelineById((current) => {
      const next = { ...current };
      ids.forEach((id) => {
        delete next[id];
      });
      return next;
    });
    if (expandedId && ids.includes(expandedId)) setExpandedId(null);
    setSelectedIds([]);
    await load();
  };
  let scheduleContent: React.ReactNode;
  if (loading) {
    scheduleContent = (
      <div className="flex items-center gap-2 text-muted-foreground">
        <Loader2 className="animate-spin" />
        載入計劃中…
      </div>
    );
  } else if (items.length === 0) {
    scheduleContent = (
      <Card>
        <CardContent className="py-10 text-center text-muted-foreground">
          暫無計劃任務，點選“新建計劃”開始。
        </CardContent>
      </Card>
    );
  } else {
    scheduleContent = (
      <div className="grid gap-3">
        <div className="flex flex-col gap-3 rounded-lg border bg-card p-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="relative min-w-0 flex-1 sm:max-w-md">
            <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(event) => updateQuery(event.target.value)}
              placeholder="檢索計劃名稱、計劃 ID 或任務 ID"
              className="h-9 pl-8"
            />
          </div>
          <div className="flex items-center gap-2 text-sm">
            <span className="text-muted-foreground">每頁</span>
            <Select value={pageSize} onValueChange={updatePageSize}>
              <SelectTrigger className="h-9 w-24 bg-background">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="10">10 條</SelectItem>
                <SelectItem value="20">20 條</SelectItem>
                <SelectItem value="50">50 條</SelectItem>
              </SelectContent>
            </Select>
            <span className="text-muted-foreground">共 {filteredItems.length} 條</span>
          </div>
        </div>
        {selectedIds.length ? (
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-primary/20 bg-primary/5 px-3 py-2">
            <span className="text-sm">已選 {selectedIds.length} 個計劃</span>
            <div className="flex flex-wrap gap-2">
              <Button size="sm" variant="outline" onClick={() => void bulkRun()}>
                <Zap data-icon="inline-start" />
                立即執行
              </Button>
              <Button size="sm" variant="destructive" onClick={() => void bulkRemove()}>
                <Trash2 data-icon="inline-start" />
                刪除
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setSelectedIds([])}>
                清空選擇
              </Button>
            </div>
          </div>
        ) : null}
        {filteredItems.length === 0 ? (
          <Card>
            <CardContent className="py-10 text-center text-muted-foreground">沒有匹配的計劃任務。</CardContent>
          </Card>
        ) : (
          <div className="overflow-x-auto rounded-lg border bg-card">
            <Table>
              <TableHeader className="bg-muted/40">
                <TableRow>
                  <TableHead className="w-12">
                    <Checkbox
                      aria-label="選擇當前頁計劃"
                      checked={headerSelectionState}
                      onCheckedChange={(checked) => toggleAllVisible(checked === true)}
                    />
                  </TableHead>
                  <TableHead className="min-w-44">計劃名稱</TableHead>
                  <TableHead className="w-28">時間線</TableHead>
                  <TableHead className="min-w-48">執行週期</TableHead>
                  <TableHead className="min-w-40">開始時間</TableHead>
                  <TableHead className="min-w-40">結束時間</TableHead>
                  <TableHead className="min-w-24 text-center">執行次數</TableHead>
                  <TableHead className="min-w-36">綁定任務</TableHead>
                  <TableHead className="min-w-32">狀態</TableHead>
                  <TableHead className="min-w-72 text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {visibleItems.map((item) => (
                  <React.Fragment key={item.id}>
                    <TableRow>
                      <TableCell className="w-12">
                        <Checkbox
                          aria-label={`選擇計劃 ${item.name}`}
                          checked={selectedIds.includes(item.id)}
                          onCheckedChange={(checked) => toggleSelected(item.id, checked === true)}
                        />
                      </TableCell>
                      <TableCell>
                        <div className="font-medium">{item.name}</div>
                        <div className="text-muted-foreground text-xs">計劃 #{item.id}</div>
                      </TableCell>
                      <TableCell>
                        <Button size="sm" variant="outline" onClick={() => void toggleTimeline(item.id)}>
                          {expandedId === item.id ? (
                            <ChevronDown data-icon="inline-start" />
                          ) : (
                            <ChevronRight data-icon="inline-start" />
                          )}
                          {expandedId === item.id ? "收起" : "檢視"}
                        </Button>
                      </TableCell>
                      <TableCell className="text-sm">{periodLabel(item)}</TableCell>
                      <TableCell className="whitespace-nowrap text-sm">
                        {startLabel(item)}
                        {item.next_start ? (
                          <div className="mt-1 text-muted-foreground text-xs">
                            下次：{nextWindowLabel(item.next_start, item.timezone)}
                          </div>
                        ) : null}
                      </TableCell>
                      <TableCell className="whitespace-nowrap text-sm">
                        {endLabel(item)}
                        {item.next_end ? (
                          <div className="mt-1 text-muted-foreground text-xs">
                            下次：{nextWindowLabel(item.next_end, item.timezone)}
                          </div>
                        ) : null}
                      </TableCell>
                      <TableCell className="text-center text-sm tabular-nums">
                        {item.run_count ?? 0}
                        {item.last_run_at ? (
                          <div className="mt-1 text-muted-foreground text-xs">最近 {formatTime(item.last_run_at)}</div>
                        ) : null}
                      </TableCell>
                      <TableCell
                        className="max-w-52 truncate text-sm"
                        title={item.task_ids.map((id) => `#${id}`).join("、")}
                      >
                        {item.task_ids.length ? item.task_ids.map((id) => `#${id}`).join("、") : "無"}
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline" className={statusClass(item.status)}>
                          {statusText(item)}
                        </Badge>
                        <div className="mt-1 text-muted-foreground text-xs">{timezoneLabel(item)}</div>
                        <div className="mt-1 text-muted-foreground text-xs">
                          最近：{formatTime(item.last_transition_at)}
                        </div>
                        {item.last_error ? (
                          <div
                            className="mt-1 max-w-40 truncate text-destructive text-xs"
                            title={scheduleErrorText(item.last_error)}
                          >
                            {scheduleErrorText(item.last_error)}
                          </div>
                        ) : null}
                      </TableCell>
                      <TableCell>
                        <div className="flex flex-wrap justify-end gap-2">
                          <Button
                            size="sm"
                            variant="outline"
                            onClick={() =>
                              setEditing({
                                id: item.id,
                                value: {
                                  name: item.name,
                                  enabled: item.enabled,
                                  timezone_mode: item.timezone_mode,
                                  schedule_type: item.schedule_type,
                                  run_date: item.run_date,
                                  end_date: item.end_date,
                                  weekdays: item.weekdays,
                                  start_time: item.start_time.slice(0, 5),
                                  end_time: item.end_time?.slice(0, 5),
                                  start_immediately: item.start_immediately,
                                  task_ids: item.task_ids,
                                },
                              })
                            }
                          >
                            編輯
                          </Button>
                          {item.enabled ? (
                            <Button size="sm" variant="outline" onClick={() => void action(item.id, "pause")}>
                              <Pause data-icon="inline-start" />
                              暫停
                            </Button>
                          ) : (
                            <Button size="sm" variant="outline" onClick={() => void action(item.id, "resume")}>
                              <Play data-icon="inline-start" />
                              啟用
                            </Button>
                          )}
                          <Button size="sm" variant="outline" onClick={() => void action(item.id, "run")}>
                            <Zap data-icon="inline-start" />
                            立即執行
                          </Button>
                          <Button size="sm" variant="ghost" onClick={() => void remove(item.id)}>
                            <Trash2 data-icon="inline-start" />
                            刪除
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                    {expandedId === item.id ? (
                      <TableRow>
                        <TableCell colSpan={10} className="bg-muted/10 p-3">
                          <ScheduleTimeline schedule={timelineById[item.id]} loading={timelineLoadingId === item.id} />
                        </TableCell>
                      </TableRow>
                    ) : null}
                  </React.Fragment>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
        <div className="flex items-center justify-between gap-3 text-sm">
          <span className="text-muted-foreground">
            第 {page} / {totalPages} 頁
          </span>
          <div className="flex gap-2">
            <Button size="sm" variant="outline" disabled={page <= 1} onClick={() => setPage((current) => current - 1)}>
              上一頁
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={page >= totalPages}
              onClick={() => setPage((current) => current + 1)}
            >
              下一頁
            </Button>
          </div>
        </div>
      </div>
    );
  }
  return (
    <div className="flex flex-1 flex-col gap-5 p-5">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="flex items-center gap-2 font-semibold text-xl">
            <CalendarClock />
            計劃任務
          </h1>
          <p className="mt-1 text-muted-foreground text-sm">按北京時間或系統時區控制掃描任務的開始、暫停和恢復。</p>
        </div>
        <Button onClick={() => setEditing({ value: { ...blank, task_ids: [] } })}>
          <Plus data-icon="inline-start" />
          新建計劃
        </Button>
      </div>
      {editing && (
        <Card>
          <CardHeader>
            <CardTitle>{editing.id ? "編輯計劃" : "新建計劃"}</CardTitle>
            <CardDescription>計劃結束時會暫停任務，下一次有效視窗自動繼續。</CardDescription>
          </CardHeader>
          <CardContent>
            <ScheduleForm
              value={editing.value}
              tasks={tasks}
              saving={saving}
              onCancel={() => setEditing(null)}
              onSave={save}
            />
          </CardContent>
        </Card>
      )}
      {scheduleContent}
    </div>
  );
}
