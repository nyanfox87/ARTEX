"use client";

import * as React from "react";

import Link from "next/link";

import {
  ArrowUpRightIcon,
  BugIcon,
  ChevronRightIcon,
  ClockIcon,
  DownloadIcon,
  InfoIcon,
  SearchIcon,
  ShieldAlertIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { FindingCaseList } from "@/components/finding-case-list";
import { FindingRetestDialog } from "@/components/finding-retest-dialog";
import { StatusBadge } from "@/components/status-badge";
import { TablePagination } from "@/components/table-pagination";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { api } from "@/lib/api";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { statusMeta } from "@/lib/status";
import type {
  ActiveFindingRetest,
  Finding,
  FindingAssetNode,
  FindingCaseListRow,
  FindingGroup,
  FindingStats,
  FindingStatus,
  Severity,
} from "@/lib/types";
import { cn } from "@/lib/utils";

import { AssetTree, assetPathOf } from "./_components/asset-tree";
import {
  FINDING_STATUSES,
  type FindingEdit,
  type FindingReport,
  FindingsTable,
  findingRowKey,
  fmtTime,
  isSameFinding,
  SEVERITIES,
  UNASSIGNED_TASK,
} from "./_components/findings-table";

const FINDING_LIST_PREFERENCE_KEY = "artex_finding_list_preferences";

// 列表檢視:flat = 跨任務平鋪大表(預設);grouped = 按任務分組摺疊;
// asset = 左側資產樹 + 右側該子樹下的發現。
type FindingView = "cases" | "flat" | "grouped" | "asset";

const FINDING_VIEWS: FindingView[] = ["cases", "flat", "grouped", "asset"];

// 資產樹的一次性快照。與另外兩個檢視不同,資產檢視不輪詢:進入檢視、改篩選、
// 或本頁改動了發現之後才重新查詢。
interface AssetTreeState {
  nodes: FindingAssetNode[];
  findingTotal: number;
  truncated: boolean;
  droppedKinds: string[];
  loaded: boolean;
  loading: boolean;
}

const EMPTY_ASSET_TREE: AssetTreeState = {
  nodes: [],
  findingTotal: 0,
  truncated: false,
  droppedKinds: [],
  loaded: false,
  loading: false,
};

// 分組視圖裡每個已展開任務組自帶一份分頁狀態,彼此獨立。
interface GroupFindingsState {
  caseRows: FindingCaseListRow[];
  items: Finding[];
  total: number;
  page: number;
  pageSize: number;
  loaded: boolean;
  loading: boolean;
}

// 平鋪檢視的頁碼單獨放 state(而非塞進快照),篩選一變就能連帶重置並觸發重新載入。
interface FlatFindingsState {
  caseRows: FindingCaseListRow[];
  items: Finding[];
  total: number;
  loaded: boolean;
  loading: boolean;
}

const EMPTY_FLAT_STATE: FlatFindingsState = { caseRows: [], items: [], total: 0, loaded: false, loading: false };

function findingGroupKey(group: FindingGroup) {
  return group.task_id === null ? UNASSIGNED_TASK : String(group.task_id);
}

const EMPTY_STATS: FindingStats = {
  total: 0,
  pending: 0,
  critical: 0,
  high: 0,
  medium: 0,
  low: 0,
  vulnclasses: [],
  tasks: [],
};

export default function FindingsPage() {
  const [view, setView] = React.useState<FindingView>("cases");
  const [severity, setSeverity] = React.useState<"all" | Severity>("all");
  const [status, setStatus] = React.useState<"all" | FindingStatus>("all");
  const [vulnclass, setVulnclass] = React.useState<string>("all");
  const [task, setTask] = React.useState<string>("all");
  const [sort, setSort] = React.useState<"severity" | "time">("severity");
  const [search, setSearch] = React.useState("");
  const [query, setQuery] = React.useState("");
  const [expanded, setExpanded] = React.useState<string | null>(null);
  const [flat, setFlat] = React.useState<FlatFindingsState>(EMPTY_FLAT_STATE);
  const [flatPage, setFlatPage] = React.useState(1);
  const [flatPageSize, setFlatPageSize] = React.useState(20);
  const [assetTree, setAssetTree] = React.useState<AssetTreeState>(EMPTY_ASSET_TREE);
  const [assetScope, setAssetScope] = React.useState<string | null>(null);
  const [groups, setGroups] = React.useState<FindingGroup[]>([]);
  const [groupTotal, setGroupTotal] = React.useState(0);
  const [expandedGroups, setExpandedGroups] = React.useState<Set<string>>(() => new Set());
  const [groupFindings, setGroupFindings] = React.useState<Record<string, GroupFindingsState>>({});
  const [total, setTotal] = React.useState(0);
  const [stats, setStats] = React.useState<FindingStats>(EMPTY_STATS);
  const [statsLoaded, setStatsLoaded] = React.useState(false);
  const [preferencesHydrated, setPreferencesHydrated] = React.useState(false);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(10);
  const [deepenFinding, setDeepenFinding] = React.useState<Finding | null>(null);
  const [retestFinding, setRetestFinding] = React.useState<Finding | null>(null);
  const [activeRetests, setActiveRetests] = React.useState<Record<string, ActiveFindingRetest>>({});
  const activeRetestFingerprint = Object.values(activeRetests)
    .map((item) => item.id)
    .join(",");
  const retestRefreshVersion = React.useRef(0);
  const [deepenDescription, setDeepenDescription] = React.useState("");
  const [deepening, setDeepening] = React.useState(false);
  const filterFingerprint = JSON.stringify([severity, status, vulnclass, task, sort, query]);
  const activeFilterFingerprint = React.useRef(filterFingerprint);
  activeFilterFingerprint.current = filterFingerprint;

  // 一個輕量請求覆蓋所有行/檢視，避免逐行拉取完整複測歷史；等待上一輪完成再輪詢。
  React.useEffect(() => {
    let disposed = false;
    let failed = false;
    let timer: ReturnType<typeof setTimeout>;
    async function refreshRetests() {
      const version = retestRefreshVersion.current;
      try {
        const rows = await api.activeFindingRetests();
        if (disposed || version !== retestRefreshVersion.current) return;
        setActiveRetests(Object.fromEntries(rows.map((item) => [item.finding_id, item])));
        failed = false;
      } catch (error) {
        if (!disposed && !failed) toast.error(`載入複測狀態失敗：${(error as Error).message}`);
        failed = true;
      } finally {
        if (!disposed) timer = setTimeout(() => void refreshRetests(), 3000);
      }
    }
    void refreshRetests();
    return () => {
      disposed = true;
      clearTimeout(timer);
    };
  }, []);

  React.useEffect(() => {
    const raw = getLocalStorageValue(FINDING_LIST_PREFERENCE_KEY);
    if (raw) {
      try {
        const parsed = JSON.parse(raw) as {
          view?: unknown;
          severity?: unknown;
          status?: unknown;
          vulnclass?: unknown;
          task?: unknown;
          sort?: unknown;
        };
        if (FINDING_VIEWS.includes(parsed.view as FindingView)) setView(parsed.view as FindingView);
        if (parsed.severity === "all" || SEVERITIES.includes(parsed.severity as Severity)) {
          setSeverity(parsed.severity as "all" | Severity);
        }
        if (parsed.status === "all" || FINDING_STATUSES.includes(parsed.status as FindingStatus)) {
          setStatus(parsed.status as "all" | FindingStatus);
        }
        if (typeof parsed.vulnclass === "string" && parsed.vulnclass) setVulnclass(parsed.vulnclass);
        if (typeof parsed.task === "string" && parsed.task) setTask(parsed.task);
        if (parsed.sort === "severity" || parsed.sort === "time") setSort(parsed.sort);
      } catch {
        // Ignore malformed or legacy preferences and retain the defaults.
      }
    }
    setPreferencesHydrated(true);
  }, []);

  React.useEffect(() => {
    if (!preferencesHydrated) return;
    setLocalStorageValue(
      FINDING_LIST_PREFERENCE_KEY,
      JSON.stringify({ view, severity, status, vulnclass, task, sort }),
    );
  }, [preferencesHydrated, severity, sort, status, task, view, vulnclass]);

  React.useEffect(() => {
    const timer = window.setTimeout(() => setQuery(search.trim()), 300);
    return () => window.clearTimeout(timer);
  }, [search]);

  // setFindings 同時改寫兩個檢視快取裡的同一條發現,切換檢視不會看到過期狀態。
  const setFindings = React.useCallback((update: (current: Finding[]) => Finding[]) => {
    function applyUpdate<T extends { items: Finding[]; caseRows: FindingCaseListRow[] }>(current: T): T {
      const items = update(current.items);
      const byId = new Map(items.map((f) => [findingRowKey(f), f]));
      const caseRows = current.caseRows.flatMap((row) => {
        if (row.case) return [row];
        const finding = row.finding ? byId.get(findingRowKey(row.finding)) : undefined;
        return finding ? [{ ...row, finding }] : [];
      });
      return { ...current, items, caseRows };
    }
    setFlat(applyUpdate);
    setGroupFindings((current) => {
      const next: Record<string, GroupFindingsState> = {};
      for (const [key, state] of Object.entries(current)) {
        next[key] = applyUpdate(state);
      }
      return next;
    });
  }, []);

  // 勾選匯出:按 finding_id(獨立表 id)記選中項,跨頁保留。
  const [selectedIds, setSelectedIds] = React.useState<Set<string>>(() => new Set());
  // 匯出彈窗狀態:範圍(當前篩選/全部/選中) × 格式(md 單檔案/md 分檔案 zip/csv/json)。
  const [exportOpen, setExportOpen] = React.useState(false);
  const [exportScope, setExportScope] = React.useState<"filtered" | "all" | "selected">("filtered");
  const [exportFormat, setExportFormat] = React.useState<"md-single" | "md-zip" | "csv" | "json">("md-single");
  const [includeOriginals, setIncludeOriginals] = React.useState(false);
  const [reviewing, setReviewing] = React.useState(false);
  const [exporting, setExporting] = React.useState(false);

  const toggleSelected = React.useCallback((id: string, checked: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);

  const toggleSelectedPage = React.useCallback((ids: string[], checked: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      for (const id of ids) {
        if (checked) next.add(id);
        else next.delete(id);
      }
      return next;
    });
  }, []);

  // 開啟匯出彈窗時,若有勾選項則預設範圍切到「選中」,否則「當前篩選」。
  function openExport() {
    setExportScope(selectedIds.size > 0 ? "selected" : "filtered");
    setExportOpen(true);
  }

  async function doExport() {
    setExporting(true);
    try {
      await api.exportFindings({
        format: exportFormat,
        scope: exportScope,
        filters: {
          severity,
          status,
          vulnclass,
          task,
          query,
          sort,
          assetScope: view === "asset" ? (assetScope ?? undefined) : undefined,
        },
        ids: [...selectedIds],
        includeOriginals,
      });
      setExportOpen(false);
      toast.success("已開始下載匯出檔案");
    } catch (e) {
      toast.error(`匯出失敗：${(e as Error).message}`);
    } finally {
      setExporting(false);
    }
  }

  const flatRequest = React.useRef(0);
  const assetTreeRequest = React.useRef(0);
  const groupRequests = React.useRef<Record<string, number>>({});
  const groupsRequest = React.useRef(0);
  const flatStateRef = React.useRef(flat);
  const expandedGroupsRef = React.useRef(expandedGroups);
  const groupFindingsRef = React.useRef(groupFindings);
  const visibleGroupKeysRef = React.useRef<Set<string>>(new Set());
  flatStateRef.current = flat;
  expandedGroupsRef.current = expandedGroups;
  groupFindingsRef.current = groupFindings;
  visibleGroupKeysRef.current = new Set(groups.map(findingGroupKey));

  // 資產檢視右側列表 = 平鋪列表 + 選中子樹的篩選,所以兩個檢視共用一份列表狀態。
  const activeAssetScope = view === "asset" ? assetScope : null;

  // loadFlat 拉取平鋪檢視的當前頁;task 篩選交給後端,與分組檢視共用同一批篩選條件。
  const loadFlat = React.useCallback(async () => {
    const requestFilter = filterFingerprint;
    if (activeFilterFingerprint.current !== requestFilter) return;
    const request = ++flatRequest.current;
    setFlat((current) => ({ ...current, loading: true }));
    try {
      const result = await api.findingCases(
        {
          page: flatPage,
          pageSize: flatPageSize,
          severity,
          status,
          vulnclass,
          task,
          query,
          sort,
          assetScope: activeAssetScope ?? undefined,
        },
        true,
      );
      if (request !== flatRequest.current || activeFilterFingerprint.current !== requestFilter) return;
      setFlat({
        caseRows: result.items,
        items: result.items.flatMap((row) => (row.finding ? [row.finding] : [])),
        total: result.total,
        loaded: true,
        loading: false,
      });
    } catch {
      if (request !== flatRequest.current || activeFilterFingerprint.current !== requestFilter) return;
      // Polling keeps the last successful snapshot visible.
      setFlat((current) => ({ ...current, loading: false }));
    }
  }, [activeAssetScope, filterFingerprint, flatPage, flatPageSize, severity, status, vulnclass, task, query, sort]);

  // loadAssetTree 取整棵資產樹。樹不隨選中節點變化(否則選一下就塌成一條鏈),
  // 所以這裡不帶 assetScope。
  const loadAssetTree = React.useCallback(async () => {
    const requestFilter = filterFingerprint;
    if (activeFilterFingerprint.current !== requestFilter) return;
    const request = ++assetTreeRequest.current;
    setAssetTree((current) => ({ ...current, loading: true }));
    try {
      const result = await api.findingAssetTree({ severity, status, vulnclass, task, query, sort });
      if (request !== assetTreeRequest.current || activeFilterFingerprint.current !== requestFilter) return;
      setAssetTree({
        nodes: result.nodes ?? [],
        findingTotal: result.finding_total ?? 0,
        truncated: Boolean(result.truncated),
        droppedKinds: result.dropped_kinds ?? [],
        loaded: true,
        loading: false,
      });
    } catch (e) {
      if (request !== assetTreeRequest.current || activeFilterFingerprint.current !== requestFilter) return;
      setAssetTree((current) => ({ ...current, loading: false }));
      toast.error(`資產樹載入失敗：${(e as Error).message}`);
    }
  }, [filterFingerprint, severity, status, vulnclass, task, query, sort]);

  const refreshGroups = React.useCallback(async () => {
    const requestFilter = filterFingerprint;
    if (activeFilterFingerprint.current !== requestFilter) return;
    const request = ++groupsRequest.current;
    try {
      const result = await api.findingGroups({
        page,
        pageSize,
        severity,
        status,
        vulnclass,
        task,
        query,
        sort,
      });
      if (request !== groupsRequest.current || activeFilterFingerprint.current !== requestFilter) return;
      setGroups(result.items);
      setGroupTotal(result.total);
      setTotal(result.finding_total);
    } catch {
      // Polling keeps the last successful snapshot visible.
    }
  }, [filterFingerprint, page, pageSize, severity, status, vulnclass, task, query, sort]);

  const loadGroup = React.useCallback(
    async (key: string, groupPage: number, groupPageSize: number) => {
      const request = (groupRequests.current[key] ?? 0) + 1;
      const requestFilter = filterFingerprint;
      if (activeFilterFingerprint.current !== requestFilter) return;
      groupRequests.current[key] = request;
      setGroupFindings((current) => ({
        ...current,
        [key]: {
          caseRows: current[key]?.caseRows ?? [],
          items: current[key]?.items ?? [],
          total: current[key]?.total ?? 0,
          page: groupPage,
          pageSize: groupPageSize,
          loaded: current[key]?.loaded ?? false,
          loading: true,
        },
      }));
      try {
        const result = await api.findingCases(
          {
            page: groupPage,
            pageSize: groupPageSize,
            severity,
            status,
            vulnclass,
            task: key,
            query,
            sort,
          },
          true,
        );
        if (groupRequests.current[key] !== request || activeFilterFingerprint.current !== requestFilter) return;
        setGroupFindings((current) => ({
          ...current,
          [key]: {
            caseRows: result.items,
            items: result.items.flatMap((row) => (row.finding ? [row.finding] : [])),
            total: result.total,
            page: result.page,
            pageSize: result.page_size,
            loaded: true,
            loading: false,
          },
        }));
      } catch {
        if (groupRequests.current[key] !== request || activeFilterFingerprint.current !== requestFilter) return;
        setGroupFindings((current) => ({
          ...current,
          [key]: {
            ...(current[key] ?? {
              caseRows: [],
              items: [],
              total: 0,
              page: groupPage,
              pageSize: groupPageSize,
              loaded: false,
            }),
            loading: false,
          },
        }));
      }
    },
    [filterFingerprint, severity, status, vulnclass, query, sort],
  );

  React.useEffect(() => {
    for (const [key, state] of Object.entries(groupFindings)) {
      if (!state.loaded || state.loading) continue;
      const lastPage = Math.max(1, Math.ceil(state.total / state.pageSize));
      if (state.page > lastPage) void loadGroup(key, lastPage, state.pageSize);
    }
  }, [groupFindings, loadGroup]);

  const toggleGroup = React.useCallback(
    (key: string) => {
      const opening = !expandedGroups.has(key);
      const next = new Set(expandedGroups);
      if (opening) next.add(key);
      else next.delete(key);
      setExpandedGroups(next);
      const state = groupFindings[key];
      if (opening && !state?.loaded && !state?.loading) {
        void loadGroup(key, state?.page ?? 1, state?.pageSize ?? 10);
      }
    },
    [expandedGroups, groupFindings, loadGroup],
  );

  // 行內改動後重新整理當前檢視:平鋪檢視重拉當前頁,分組檢視刷組頭 + 該發現所在的組。
  const refreshAfterMutation = React.useCallback(
    (finding: Finding, removed = false) => {
      if (view === "asset") {
        // 資產檢視不輪詢,所以改完要順帶把樹的計數也重新算一次。
        void loadFlat();
        void loadAssetTree();
        return;
      }
      if (view === "flat") {
        // 刪空最後一頁時,頁碼由越界修正 effect 回退並連帶重新載入。
        void loadFlat();
        return;
      }
      void refreshGroups();
      const key = finding.task_id ?? UNASSIGNED_TASK;
      const state = groupFindingsRef.current[key];
      if (state?.loaded) {
        const nextTotal = Math.max(0, state.total - (removed ? 1 : 0));
        const lastPage = Math.max(1, Math.ceil(nextTotal / state.pageSize));
        void loadGroup(key, Math.min(state.page, lastPage), state.pageSize);
      }
    },
    [loadAssetTree, loadFlat, loadGroup, refreshGroups, view],
  );

  // Reset every view's pagination and expansion when a shared finding filter changes.
  React.useEffect(() => {
    void filterFingerprint;
    setPage(1);
    setExpanded(null);
    setExpandedGroups(new Set());
    setGroupFindings({});
    setFlatPage(1);
    setFlat(EMPTY_FLAT_STATE);
    // 篩選變了樹也會變,原先選中的節點可能已經不在樹裡,退回「全部資產」。
    setAssetScope(null);
    setAssetTree(EMPTY_ASSET_TREE);
  }, [filterFingerprint]);

  // 換資產節點等於換了一份結果集,回到第一頁。
  React.useEffect(() => {
    void assetScope;
    setFlatPage(1);
  }, [assetScope]);

  // 資產樹只在進入檢視 / 篩選變化時查一次(以及本頁改動發現後由
  // refreshAfterMutation 主動重拉),不做輪詢。
  React.useEffect(() => {
    if (!preferencesHydrated || view !== "asset") return;
    void activeRetestFingerprint; // 複測結束可能改變狀態篩選下的資產計數。
    void loadAssetTree();
  }, [activeRetestFingerprint, loadAssetTree, preferencesHydrated, view]);

  // 只輪詢當前檢視:平鋪檢視刷當前頁,分組檢視刷組頭與每個已展開的組(其分頁彼此獨立)。
  // 資產檢視只查一次(見下面的 return),它的左樹是導航結構,沒必要每 5 秒重算。
  // 等偏好水合後再發首個請求,否則會先按預設檢視/篩選白拉一次。
  React.useEffect(() => {
    if (!preferencesHydrated) return;
    void activeRetestFingerprint; // 包括不定時輪詢的資產檢視，也在複測結束後重新整理處置狀態。
    const refresh = () => {
      if (view === "flat" || view === "asset") {
        if (!flatStateRef.current.loading) void loadFlat();
        return;
      }
      void refreshGroups();
      for (const key of expandedGroupsRef.current) {
        if (!visibleGroupKeysRef.current.has(key)) continue;
        const state = groupFindingsRef.current[key];
        if (state?.loaded && !state.loading) void loadGroup(key, state.page, state.pageSize);
      }
    };
    refresh();
    if (view === "asset") return;
    const timer = setInterval(refresh, 5000);
    return () => clearInterval(timer);
  }, [activeRetestFingerprint, loadFlat, loadGroup, preferencesHydrated, refreshGroups, view]);

  React.useEffect(() => {
    const lastPage = Math.max(1, Math.ceil(groupTotal / pageSize));
    if (page > lastPage) setPage(lastPage);
  }, [groupTotal, page, pageSize]);

  React.useEffect(() => {
    if (!flat.loaded) return;
    const lastPage = Math.max(1, Math.ceil(flat.total / flatPageSize));
    if (flatPage > lastPage) setFlatPage(lastPage);
  }, [flat.loaded, flat.total, flatPage, flatPageSize]);

  // Whole-table aggregates (stat cards + vuln-class options) — independent of the
  // current page, so they stay exact.
  React.useEffect(() => {
    let alive = true;
    const load = () => {
      api
        .findingStats()
        .then((s) => {
          if (alive) {
            setStats(s);
            setStatsLoaded(true);
          }
        })
        .catch(() => {
          // Keep the previous aggregate snapshot until the next poll.
        });
    };
    load();
    const t = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  React.useEffect(() => {
    if (!statsLoaded) return;
    if (vulnclass !== "all" && !stats.vulnclasses.includes(vulnclass)) setVulnclass("all");
    if (
      task !== "all" &&
      task !== UNASSIGNED_TASK &&
      !(stats.tasks ?? []).some((option) => String(option.id) === task)
    ) {
      setTask("all");
    }
  }, [stats, statsLoaded, task, vulnclass]);

  // updateStatus optimistically flips one finding's triage state, reverting on error.
  const updateStatus = React.useCallback(
    async (f: Finding, next: FindingStatus) => {
      if (!f.finding_id || next === f.status) return;
      const prev = f.status;
      setFindings((cur) => cur.map((x) => (isSameFinding(x, f) ? { ...x, status: next } : x)));
      try {
        await api.setFindingStatus(f.finding_id, next);
        toast.success(`已標記為「${statusMeta("finding", next).label}」`);
        // refresh stat cards (pending count) and drop the row if it no longer matches the status filter
        api
          .findingStats()
          .then(setStats)
          .catch(() => {
            // The row update remains valid even if the aggregate refresh fails.
          });
        if (status !== "all" && next !== status) {
          setFindings((cur) => cur.filter((x) => !isSameFinding(x, f)));
          setTotal((t) => Math.max(0, t - 1));
          setFlat((cur) => ({ ...cur, total: Math.max(0, cur.total - 1) }));
        }
        refreshAfterMutation(f);
      } catch (e) {
        setFindings((cur) => cur.map((x) => (isSameFinding(x, f) ? { ...x, status: prev } : x)));
        toast.error(`更新失敗：${(e as Error).message}`);
      }
    },
    [refreshAfterMutation, setFindings, status],
  );

  // 行內展開的詳細報告快取按全域穩定行鍵存。report 是大段 Markdown,列表查詢不帶它,
  // 故展開時才按 finding_id 單獨拉取一次;done 且文本為空 = 該漏洞暫無報告。
  const [reports, setReports] = React.useState<Record<string, FindingReport>>({});

  // 行內可編輯緩衝:當前展開行的名稱/類別/嚴重等級,展開時用該行資料初始化,收起清空。
  // 單行展開,故一份緩衝即可。
  const [edit, setEdit] = React.useState<FindingEdit | null>(null);
  const [saving, setSaving] = React.useState(false);

  // toggle 展開/收起一行;新展開時初始化編輯緩衝,並(尚未取過時)按 finding_id 拉一次報告快取。
  const toggleRow = React.useCallback(
    (f: Finding) => {
      const key = findingRowKey(f);
      const willOpen = expanded !== key;
      setExpanded(willOpen ? key : null);
      if (!willOpen) {
        setEdit(null);
        return;
      }
      setEdit({ name: f.name ?? "", vulnclass: f.vulnclass, severity: f.severity });
      if (!f.finding_id || reports[key]) return;
      const fid = f.finding_id;
      setReports((r) => ({ ...r, [key]: { status: "loading", text: "" } }));
      api
        .getFinding(fid)
        .then((full) => setReports((r) => ({ ...r, [key]: { status: "done", text: full.report ?? "" } })))
        .catch(() => setReports((r) => ({ ...r, [key]: { status: "error", text: "" } })));
    },
    [expanded, reports],
  );

  // saveEdit 儲存當前展開行的名稱/類別/嚴重等級,回寫本地列表並重新整理統計(類別下拉/嚴重計數可能變)。
  const saveEdit = React.useCallback(
    async (f: Finding) => {
      if (!f.finding_id || !edit) return;
      setSaving(true);
      try {
        const updated = await api.updateFinding(f.finding_id, {
          name: edit.name.trim(),
          vulnclass: edit.vulnclass.trim(),
          severity: edit.severity,
        });
        setFindings((cur) =>
          cur.map((x) =>
            isSameFinding(x, f)
              ? { ...x, name: updated.name, vulnclass: updated.vulnclass, severity: updated.severity }
              : x,
          ),
        );
        toast.success("已儲存");
        api
          .findingStats()
          .then(setStats)
          .catch(() => {
            // The edit remains valid even if the aggregate refresh fails.
          });
        refreshAfterMutation(f);
      } catch (e) {
        toast.error(`儲存失敗：${(e as Error).message}`);
      } finally {
        setSaving(false);
      }
    },
    [edit, refreshAfterMutation, setFindings],
  );

  // deleteFinding 刪除一個漏洞(需二次確認):刪成功後從列表移除、收起行、重新整理統計。
  const deleteFinding = React.useCallback(
    async (f: Finding) => {
      if (!f.finding_id) return;
      try {
        await api.deleteFinding(f.finding_id);
        setFindings((cur) => cur.filter((x) => !isSameFinding(x, f)));
        setSelectedIds((current) => {
          const next = new Set(current);
          next.delete(f.finding_id as string);
          return next;
        });
        setTotal((t) => Math.max(0, t - 1));
        setFlat((cur) => ({ ...cur, total: Math.max(0, cur.total - 1) }));
        const rowKey = findingRowKey(f);
        setExpanded((cur) => (cur === rowKey ? null : cur));
        toast.success("已刪除漏洞");
        api
          .findingStats()
          .then(setStats)
          .catch(() => {
            // The deletion remains valid even if the aggregate refresh fails.
          });
        refreshAfterMutation(f, true);
      } catch (e) {
        toast.error(`刪除失敗：${(e as Error).message}`);
      }
    },
    [refreshAfterMutation, setFindings],
  );

  const openDeepen = React.useCallback((f: Finding) => {
    setDeepenFinding(f);
    setDeepenDescription("");
  }, []);

  async function submitDeepen() {
    if (!deepenFinding?.finding_id || !deepenDescription.trim() || deepening) return;
    setDeepening(true);
    try {
      const result = await api.deepenFinding(deepenFinding.finding_id, deepenDescription.trim());
      toast.success(
        result.queued
          ? `深入意圖 #${result.intent_id} 已進入任務佇列`
          : `已建立高優先順序 Worker 意圖 #${result.intent_id}`,
      );
      refreshAfterMutation(deepenFinding);
      setDeepenFinding(null);
      setDeepenDescription("");
    } catch (error) {
      toast.error(`提交失敗：${(error as Error).message}`);
    } finally {
      setDeepening(false);
    }
  }

  const statCards = [
    { label: "獨立漏洞", value: stats.distinct?.total ?? stats.total, icon: BugIcon },
    { label: "上報記錄", value: stats.total, tone: "text-amber-500", icon: ClockIcon },
    { label: "嚴重", value: stats.distinct?.critical ?? stats.critical, tone: "text-rose-600", icon: ShieldAlertIcon },
    { label: "高危", value: stats.distinct?.high ?? stats.high, tone: "text-red-500", icon: TriangleAlertIcon },
    { label: "中危", value: stats.distinct?.medium ?? stats.medium, tone: "text-amber-500", icon: TriangleAlertIcon },
    { label: "低危", value: stats.distinct?.low ?? stats.low, tone: "text-slate-500", icon: InfoIcon },
  ];

  // 匯出彈窗裡「當前篩選」的條數:兩個檢視的篩選一致,只是統計口徑來源不同。
  // 平鋪與資產檢視共用 flat 列表狀態,分組檢視的口徑來自組介面的 finding_total。
  const [caseTotal, setCaseTotal] = React.useState(0);
  const filteredTotal = view === "cases" ? caseTotal : view === "grouped" ? total : flat.total;
  const assetPath = React.useMemo(
    () => (view === "asset" ? assetPathOf(assetTree.nodes, assetScope) : []),
    [assetScope, assetTree.nodes, view],
  );

  const rowProps = {
    selectedIds,
    onToggleSelected: toggleSelected,
    onToggleSelectedPage: toggleSelectedPage,
    expandedKey: expanded,
    onToggleRow: toggleRow,
    reports,
    edit,
    onEditChange: setEdit,
    saving,
    onSave: saveEdit,
    onStatusChange: updateStatus,
    onRetest: setRetestFinding,
    activeRetests,
    onDeepen: openDeepen,
    onDelete: deleteFinding,
  };

  // 平鋪檢視與資產檢視右側是同一張表 + 同一份分頁,只是篩選條件不同。
  const flatListCard = (
    <Card className="gap-0 py-0">
      <CardContent className="px-0">
        {flat.loading && !flat.loaded ? (
          <div className="flex min-h-36 items-center justify-center">
            <Spinner />
          </div>
        ) : (
          <>
            <FindingsTable
              items={flat.items}
              caseRows={flat.caseRows}
              presentation={view === "asset" ? "asset" : "records"}
              selectAllLabel="選擇當前頁全部"
              {...rowProps}
            />
            <TablePagination
              page={flatPage}
              pageSize={flatPageSize}
              total={flat.total}
              onPageChange={setFlatPage}
              onPageSizeChange={(nextSize) => {
                setFlatPageSize(nextSize);
                setFlatPage(1);
              }}
              pageSizeOptions={[10, 20, 50, 100]}
            />
          </>
        )}
      </CardContent>
    </Card>
  );

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">發現</h1>
          <p className="text-muted-foreground text-sm">跨任務漏洞彙總</p>
        </div>
        <Tabs value={view} onValueChange={(v) => setView(v as FindingView)}>
          <TabsList>
            <TabsTrigger value="cases">按漏洞</TabsTrigger>
            <TabsTrigger value="flat">原始上報</TabsTrigger>
            <TabsTrigger value="grouped">按任務分組</TabsTrigger>
            <TabsTrigger value="asset">按資產</TabsTrigger>
          </TabsList>
        </Tabs>
      </div>
      <div className="flex flex-1 flex-col gap-4 md:gap-6">
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-6">
          {statCards.map((stat) => {
            const StatIcon = stat.icon;
            return (
              <Card key={stat.label} className="gap-1 py-4">
                <CardHeader className="px-4">
                  <CardDescription>{stat.label}</CardDescription>
                  <CardTitle className={cn("flex items-center gap-2 text-2xl tabular-nums", stat.tone)}>
                    <StatIcon className="size-5" aria-hidden="true" />
                    {stat.value}
                  </CardTitle>
                </CardHeader>
              </Card>
            );
          })}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <InputGroup className="w-full sm:w-72">
            <InputGroupInput
              type="search"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="檢索漏洞內容"
              aria-label="檢索漏洞內容"
            />
            <InputGroupAddon>
              <SearchIcon aria-hidden="true" />
            </InputGroupAddon>
          </InputGroup>

          <ToggleGroup
            type="single"
            value={severity}
            onValueChange={(value) => value && setSeverity(value as "all" | Severity)}
            variant="outline"
            size="sm"
            spacing={0}
          >
            {(
              [
                ["all", "全部"],
                ["critical", "嚴重"],
                ["high", "高危"],
                ["medium", "中危"],
                ["low", "低危"],
              ] as const
            ).map(([val, label]) => (
              <ToggleGroupItem key={val} value={val} aria-label={`按${label}等級篩選`}>
                {label}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>

          <Select value={status} onValueChange={(v) => setStatus(v as "all" | FindingStatus)}>
            <SelectTrigger size="sm" className="w-32">
              <SelectValue placeholder="狀態" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部狀態</SelectItem>
              {FINDING_STATUSES.map((st) => (
                <SelectItem key={st} value={st}>
                  {statusMeta("finding", st).label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select value={vulnclass} onValueChange={setVulnclass}>
            <SelectTrigger size="sm" className="w-40">
              <SelectValue placeholder="漏洞型別" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部型別</SelectItem>
              {stats.vulnclasses.map((vc) => (
                <SelectItem key={vc} value={vc}>
                  {vc}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select value={task} onValueChange={setTask}>
            <SelectTrigger size="sm" className="w-48">
              <SelectValue placeholder="任務" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部任務</SelectItem>
              <SelectItem value={UNASSIGNED_TASK}>未關聯 / 任務已刪除</SelectItem>
              {(stats.tasks ?? []).map((t) => {
                const id = String(t.id);
                const label = t.name || t.description || `任務 #${id}（已刪除）`;
                return (
                  <SelectItem key={id} value={id}>
                    <span className="flex w-full items-center gap-2">
                      <span className="max-w-[14rem] truncate" title={label}>
                        {label}
                      </span>
                      <span className="inline-flex items-center gap-1 text-muted-foreground tabular-nums">
                        <BugIcon className="size-3.5" aria-hidden="true" />
                        {t.count}
                      </span>
                    </span>
                  </SelectItem>
                );
              })}
            </SelectContent>
          </Select>

          <Select value={sort} onValueChange={(v) => setSort(v as "severity" | "time")}>
            <SelectTrigger size="sm" className="w-36">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="severity">按嚴重度</SelectItem>
              <SelectItem value="time">按時間</SelectItem>
            </SelectContent>
          </Select>

          <div className="ml-auto flex items-center gap-3">
            {selectedIds.size > 0 && (
              <span className="text-xs text-muted-foreground tabular-nums">已選 {selectedIds.size} 條</span>
            )}
            <Button
              size="sm"
              variant="outline"
              disabled={reviewing || selectedIds.size === 0}
              onClick={async () => {
                setReviewing(true);
                try {
                  await api.reviewFindingCases([...selectedIds]);
                  toast.success("已提交整理，按任務分別執行");
                } catch (e) {
                  toast.error((e as Error).message);
                } finally {
                  setReviewing(false);
                }
              }}
            >
              {reviewing ? <Spinner /> : null}整理所選（{selectedIds.size}）
            </Button>
            <Button size="sm" variant="outline" onClick={openExport}>
              <DownloadIcon /> 匯出
            </Button>
          </div>
        </div>

        {view === "flat" && flatListCard}

        {view === "asset" && (
          <div className="grid min-h-0 items-start gap-4 lg:grid-cols-[20rem_minmax(0,1fr)] xl:grid-cols-[24rem_minmax(0,1fr)]">
            <Card className="gap-0 py-3 lg:sticky lg:top-4">
              <CardContent className="flex flex-col px-3">
                {assetTree.loading && !assetTree.loaded ? (
                  <div className="flex min-h-36 items-center justify-center">
                    <Spinner />
                  </div>
                ) : (
                  <AssetTree
                    nodes={assetTree.nodes}
                    selected={assetScope}
                    onSelect={setAssetScope}
                    loading={assetTree.loading}
                    truncated={assetTree.truncated}
                    droppedKinds={assetTree.droppedKinds}
                    findingTotal={assetTree.findingTotal}
                    onRefresh={() => void loadAssetTree()}
                  />
                )}
              </CardContent>
            </Card>
            <div className="flex min-w-0 flex-col gap-2">
              <div className="flex min-w-0 flex-wrap items-center gap-1 text-sm text-muted-foreground">
                <button
                  type="button"
                  className={cn("hover:text-foreground", assetScope === null && "font-medium text-foreground")}
                  onClick={() => setAssetScope(null)}
                >
                  全部資產
                </button>
                {assetPath.map((node) => (
                  <React.Fragment key={node.key}>
                    <ChevronRightIcon className="size-3.5 shrink-0" aria-hidden="true" />
                    <button
                      type="button"
                      className={cn(
                        "max-w-[16rem] truncate hover:text-foreground",
                        node.key === assetScope && "font-medium text-foreground",
                      )}
                      title={node.label}
                      onClick={() => setAssetScope(node.key)}
                    >
                      {node.display}
                    </button>
                  </React.Fragment>
                ))}
                <span className="ml-auto shrink-0 text-xs tabular-nums">共 {flat.total} 條</span>
              </div>
              {flatListCard}
            </div>
          </div>
        )}

        {view === "cases" && (
          <FindingCaseList
            onTotal={setCaseTotal}
            query={{ severity, status, vulnclass, task, query, sort }}
            selectedIds={selectedIds}
            onSelect={toggleSelected}
          />
        )}
        {view === "grouped" && (
          <div className="flex flex-col gap-3">
            {groups.map((group) => {
              const key = findingGroupKey(group);
              const groupOpen = expandedGroups.has(key);
              const state = groupFindings[key] ?? {
                caseRows: [],
                items: [],
                total: group.count,
                page: 1,
                pageSize: 10,
                loaded: false,
                loading: false,
              };
              return (
                <Card key={key} className="gap-0 py-0">
                  <CardHeader className="px-4 py-3">
                    <div className="flex min-w-0 flex-wrap items-center gap-3">
                      <button
                        type="button"
                        className="flex min-w-0 flex-1 items-center gap-3 text-left"
                        aria-expanded={groupOpen}
                        onClick={() => toggleGroup(key)}
                      >
                        <ChevronRightIcon
                          className={cn(
                            "size-4 shrink-0 text-muted-foreground transition-transform",
                            groupOpen && "rotate-90",
                          )}
                        />
                        <div className="flex min-w-0 flex-col gap-1">
                          <CardTitle className="truncate text-sm">
                            {group.task_id === null
                              ? "未關聯 / 任務已刪除"
                              : group.task_name
                                ? `${group.task_name}（任務 #${group.task_id}）`
                                : `任務 #${group.task_id}`}
                          </CardTitle>
                          <CardDescription className="truncate" title={group.task_description}>
                            {group.task_description || "來源任務不可用"}
                          </CardDescription>
                        </div>
                      </button>
                      <div className="flex flex-wrap items-center gap-2">
                        {group.task_status && <StatusBadge domain="task" value={group.task_status} dot />}
                        {SEVERITIES.map((level) => {
                          const count = group[level];
                          if (count === 0) return null;
                          return (
                            <span key={level} className="inline-flex items-center gap-1">
                              <StatusBadge domain="severity" value={level} dot />
                              <span className="text-xs tabular-nums text-muted-foreground">{count}</span>
                            </span>
                          );
                        })}
                        <span className="text-xs tabular-nums text-muted-foreground">
                          {fmtTime(group.last_found_at)}
                        </span>
                        {group.task_id !== null && (
                          <Button size="icon-sm" variant="ghost" asChild>
                            <Link
                              href={`/function/tasks/detail?id=${group.task_id}`}
                              aria-label={`檢視任務 #${group.task_id}`}
                            >
                              <ArrowUpRightIcon />
                            </Link>
                          </Button>
                        )}
                      </div>
                    </div>
                  </CardHeader>
                  {groupOpen && (
                    <CardContent className="px-0">
                      {state.loading && !state.loaded ? (
                        <div className="flex min-h-36 items-center justify-center">
                          <Spinner />
                        </div>
                      ) : (
                        <>
                          <FindingsTable
                            items={state.items}
                            caseRows={state.caseRows}
                            presentation="task"
                            selectAllLabel="選擇本組當前頁全部"
                            {...rowProps}
                          />
                          <TablePagination
                            page={state.page}
                            pageSize={state.pageSize}
                            total={state.total}
                            onPageChange={(nextPage) => void loadGroup(key, nextPage, state.pageSize)}
                            onPageSizeChange={(nextSize) => void loadGroup(key, 1, nextSize)}
                          />
                        </>
                      )}
                    </CardContent>
                  )}
                </Card>
              );
            })}
            {groups.length === 0 && (
              <Card>
                <CardContent className="py-12 text-center text-sm text-muted-foreground">沒有匹配的發現。</CardContent>
              </Card>
            )}
            <TablePagination
              page={page}
              pageSize={pageSize}
              total={groupTotal}
              onPageChange={setPage}
              onPageSizeChange={(nextPageSize) => {
                setPageSize(nextPageSize);
                setPage(1);
              }}
              pageSizeOptions={[5, 10, 20]}
            />
          </div>
        )}
      </div>

      {retestFinding?.finding_id ? (
        <FindingRetestDialog
          key={retestFinding.finding_id}
          findingId={retestFinding.finding_id}
          findingName={retestFinding.name || retestFinding.vulnclass || retestFinding.summary}
          onStarted={(retest) => {
            const findingId = retestFinding.finding_id;
            if (!findingId || retest.conversation_id == null || !["pending", "running"].includes(retest.status)) return;
            retestRefreshVersion.current++;
            const active: ActiveFindingRetest = {
              id: retest.id,
              finding_id: findingId,
              conversation_id: retest.conversation_id,
              status: retest.status === "pending" ? "pending" : "running",
            };
            setActiveRetests((current) => ({ ...current, [findingId]: active }));
          }}
          onClose={() => setRetestFinding(null)}
        />
      ) : null}

      <Dialog
        open={deepenFinding !== null}
        onOpenChange={(open) => {
          if (open || deepening) return;
          setDeepenFinding(null);
          setDeepenDescription("");
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>深入利用漏洞</DialogTitle>
            <DialogDescription className="break-words">
              將在原任務 #{deepenFinding?.task_id} 中建立優先順序 10 的 Worker 意圖，基於當前漏洞開展二次驗證：
              {deepenFinding?.name || deepenFinding?.vulnclass || deepenFinding?.summary}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="finding-deepen-description">利用描述</FieldLabel>
              <Textarea
                id="finding-deepen-description"
                value={deepenDescription}
                onChange={(event) => setDeepenDescription(event.target.value)}
                maxLength={4000}
                placeholder="描述需要驗證的利用路徑、邊界條件、目標或期望證據"
                disabled={deepening}
              />
              <FieldDescription className="flex justify-between gap-3">
                <span>新意圖會繼承該漏洞的資產錨點。</span>
                <span className="shrink-0 tabular-nums">{deepenDescription.length} / 4000</span>
              </FieldDescription>
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                setDeepenFinding(null);
                setDeepenDescription("");
              }}
              disabled={deepening}
            >
              取消
            </Button>
            <Button onClick={submitDeepen} disabled={deepening || !deepenDescription.trim()}>
              {deepening && <Spinner data-icon="inline-start" />}
              建立深入意圖
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={exportOpen} onOpenChange={setExportOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>匯出發現</DialogTitle>
            <DialogDescription>選擇匯出範圍與格式,生成後瀏覽器會自動下載。</DialogDescription>
          </DialogHeader>

          <div className="flex flex-col gap-5 py-1">
            <div className="flex flex-col gap-2">
              <span className="text-xs text-muted-foreground">匯出範圍</span>
              <RadioGroup value={exportScope} onValueChange={(v) => setExportScope(v as typeof exportScope)}>
                <label htmlFor="export-scope-filtered" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-scope-filtered" value="filtered" /> 匯出當前篩選結果（共 {filteredTotal}{" "}
                  條）
                </label>
                <label htmlFor="export-scope-all" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-scope-all" value="all" /> 匯出全部
                </label>
                <label
                  htmlFor="export-scope-selected"
                  className={cn("flex items-center gap-2 text-sm", selectedIds.size === 0 && "text-muted-foreground")}
                >
                  <RadioGroupItem id="export-scope-selected" value="selected" disabled={selectedIds.size === 0} />
                  匯出勾選的 {selectedIds.size} 條
                </label>
              </RadioGroup>
            </div>

            <div className="flex flex-col gap-2">
              <span className="text-xs text-muted-foreground">匯出格式</span>
              <div className="flex items-center gap-2">
                <Checkbox
                  id="include-original-reports"
                  checked={includeOriginals}
                  onCheckedChange={(v) => setIncludeOriginals(v === true)}
                />
                <label htmlFor="include-original-reports" className="text-sm">
                  包含原始子報告（預設每個資料夾一份統一報告）
                </label>
              </div>
              <RadioGroup value={exportFormat} onValueChange={(v) => setExportFormat(v as typeof exportFormat)}>
                <label htmlFor="export-format-md-single" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-format-md-single" value="md-single" /> Markdown 彙總報告（單個 .md 檔案）
                </label>
                <label htmlFor="export-format-md-zip" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-format-md-zip" value="md-zip" /> Markdown 分檔案（一漏洞一 .md,打包 .zip）
                </label>
                <label htmlFor="export-format-csv" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-format-csv" value="csv" /> CSV 表格（.csv）
                </label>
                <label htmlFor="export-format-json" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-format-json" value="json" /> JSON（.json）
                </label>
              </RadioGroup>
            </div>
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setExportOpen(false)} disabled={exporting}>
              取消
            </Button>
            <Button onClick={doExport} disabled={exporting || (exportScope === "selected" && selectedIds.size === 0)}>
              <DownloadIcon /> {exporting ? "匯出中…" : "匯出"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
