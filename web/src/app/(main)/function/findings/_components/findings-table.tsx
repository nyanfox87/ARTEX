"use client";

import * as React from "react";

import Link from "next/link";

import {
  ArrowUpRightIcon,
  ChevronRightIcon,
  FileTextIcon,
  FlaskConicalIcon,
  FolderIcon,
  FolderOpenIcon,
  RotateCcwIcon,
  ShieldAlertIcon,
  Trash2Icon,
} from "lucide-react";

import { CopyButton } from "@/components/copy-button";
import { FindingCaseMembers, FindingSeverityCounts } from "@/components/finding-case-list";
import { Markdown } from "@/components/markdown";
import { StatusBadge } from "@/components/status-badge";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { statusMeta } from "@/lib/status";
import type {
  ActiveFindingRetest,
  Finding,
  FindingAsset,
  FindingCaseListRow,
  FindingStatus,
  Severity,
} from "@/lib/types";
import { cn } from "@/lib/utils";

export const SEVERITIES: Severity[] = ["critical", "high", "medium", "low"];

export const FINDING_STATUSES: FindingStatus[] = [
  "pending",
  "in_progress",
  "confirmed",
  "resolved",
  "fixed",
  "false_positive",
  "ignored",
  "duplicate",
  "risk_accepted",
];

export const UNASSIGNED_TASK = "__unassigned__";

// 行內編輯緩衝:當前展開行的名稱/類別/嚴重等級。
export interface FindingEdit {
  name: string;
  vulnclass: string;
  severity: Severity;
}

export type FindingReport = { status: "loading" | "done" | "error"; text: string };

// Exploration node ids are only unique inside a task. Prefer the persisted
// finding id and otherwise namespace the node id by task so editing one group
// cannot update a similarly-named node in another expanded group.
export function findingRowKey(finding: Finding): string {
  if (finding.finding_id) return `finding:${finding.finding_id}`;
  return `node:${finding.task_id ?? UNASSIGNED_TASK}:${finding.id}`;
}

export function isSameFinding(left: Finding, right: Finding): boolean {
  return findingRowKey(left) === findingRowKey(right);
}

export function fmtTime(ts: string) {
  return new Date(ts).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

const COLUMN_COUNT = 9;

interface FindingsTableProps {
  items: Finding[];
  caseRows?: FindingCaseListRow[];
  hideHeader?: boolean;
  matchedIds?: number[];
  presentation?: "records" | "task" | "asset";
  selectedIds: Set<string>;
  onToggleSelected: (id: string, checked: boolean) => void;
  onToggleSelectedPage: (ids: string[], checked: boolean) => void;
  /** 當前展開行的 findingRowKey;null = 全部收起。 */
  expandedKey: string | null;
  onToggleRow: (finding: Finding) => void;
  reports: Record<string, FindingReport>;
  edit: FindingEdit | null;
  onEditChange: React.Dispatch<React.SetStateAction<FindingEdit | null>>;
  saving: boolean;
  onSave: (finding: Finding) => void;
  onStatusChange: (finding: Finding, next: FindingStatus) => void;
  onRetest: (finding: Finding) => void;
  activeRetests: Record<string, ActiveFindingRetest>;
  onDeepen: (finding: Finding) => void;
  onDelete: (finding: Finding) => void;
  /** 全選框的無障礙標籤,平鋪檢視與分組檢視措辭不同。 */
  selectAllLabel?: string;
}

// FindingsTable 是發現列表的表格主體,平鋪檢視與按任務分組檢視共用同一份行渲染
// (勾選 / 行內展開 / 行內改名與改狀態 / 複測 / 深入 / 刪除),差異只在外層容器與分頁。
export function FindingsTable({
  items,
  caseRows,
  hideHeader = false,
  matchedIds,
  presentation = "records",
  selectedIds,
  onToggleSelected,
  onToggleSelectedPage,
  expandedKey,
  onToggleRow,
  reports,
  edit,
  onEditChange,
  saving,
  onSave,
  onStatusChange,
  onRetest,
  activeRetests,
  onDeepen,
  onDelete,
  selectAllLabel = "選擇當前頁全部",
}: FindingsTableProps) {
  const selectableIds = items.map((finding) => finding.finding_id).filter((id): id is string => Boolean(id));
  const selectedCount = selectableIds.filter((id) => selectedIds.has(id)).length;
  let headerChecked: boolean | "indeterminate" = false;
  if (selectableIds.length > 0 && selectedCount === selectableIds.length) {
    headerChecked = true;
  } else if (selectedCount > 0) {
    headerChecked = "indeterminate";
  }

  const rows: FindingCaseListRow[] = caseRows ?? items.map((finding) => ({ finding, matched_ids: [] }));

  const shared: Omit<FindingsTableProps, "items" | "caseRows"> = {
    presentation,
    selectedIds,
    onToggleSelected,
    onToggleSelectedPage,
    expandedKey,
    onToggleRow,
    reports,
    edit,
    onEditChange,
    saving,
    onSave,
    onStatusChange,
    onRetest,
    activeRetests,
    onDeepen,
    onDelete,
  };

  return (
    /* 固定列寬保證展開內容不撐開表格；窄屏只在表格內部橫向滾動。 */
    <Table className="min-w-[60rem] table-fixed">
      {hideHeader ? (
        <colgroup>
          <col className="w-8" />
          <col className="w-8" />
          <col className="w-20" />
          <col />
          <col className="w-44" />
          <col className="w-28" />
          <col className="w-32" />
          <col className="w-24" />
          <col className="w-48" />
        </colgroup>
      ) : null}
      {!hideHeader ? (
        <TableHeader>
          <TableRow>
            <TableHead className="w-8">
              <Checkbox
                checked={headerChecked}
                onCheckedChange={(checked) => onToggleSelectedPage(selectableIds, checked === true)}
                aria-label={selectAllLabel}
              />
            </TableHead>
            <TableHead className="w-8" />
            <TableHead className="w-20">嚴重度</TableHead>
            <TableHead>漏洞名稱</TableHead>
            <TableHead className="w-44">資產</TableHead>
            <TableHead className="w-28">狀態</TableHead>
            <TableHead className="w-32">所屬任務</TableHead>
            <TableHead className="w-24">時間</TableHead>
            <TableHead className="w-48">操作</TableHead>
          </TableRow>
        </TableHeader>
      ) : null}
      <TableBody>
        {rows.map((row) => {
          if (row.case) return <FindingFolderTableRows key={`case:${row.case.id}`} row={row} shared={shared} />;
          const f = row.finding;
          if (!f) return null;
          const rowKey = findingRowKey(f);
          const open = expandedKey === rowKey;
          const retest = f.finding_id ? activeRetests[f.finding_id] : undefined;
          return (
            <React.Fragment key={rowKey}>
              <TableRow
                className={cn(
                  "cursor-pointer",
                  matchedIds && !matchedIds.includes(Number(f.finding_id)) && "bg-muted/30",
                )}
                role="button"
                tabIndex={0}
                aria-expanded={open}
                onClick={() => onToggleRow(f)}
                onKeyDown={(event) => {
                  if (event.target !== event.currentTarget || (event.key !== "Enter" && event.key !== " ")) return;
                  event.preventDefault();
                  onToggleRow(f);
                }}
              >
                <TableCell onClick={(e) => e.stopPropagation()}>
                  {f.finding_id && (
                    <Checkbox
                      checked={selectedIds.has(f.finding_id)}
                      onCheckedChange={(c) => onToggleSelected(f.finding_id as string, c === true)}
                      aria-label="選擇該漏洞"
                    />
                  )}
                </TableCell>
                <TableCell>
                  <ChevronRightIcon
                    className={cn("size-4 text-muted-foreground transition-transform", open && "rotate-90")}
                  />
                </TableCell>
                <TableCell>
                  <StatusBadge domain="severity" value={f.severity} dot />
                </TableCell>
                <TableCell className="max-w-md">
                  <div className="flex min-w-0 flex-col gap-0.5">
                    {f.finding_id ? (
                      <Link
                        href={`/function/findings/detail?id=${f.finding_id}`}
                        onClick={(e) => e.stopPropagation()}
                        className="truncate font-medium hover:text-primary hover:underline"
                        title="檢視發現詳情"
                      >
                        {f.name || f.vulnclass || "未分類"}
                      </Link>
                    ) : (
                      <span className="truncate font-medium">{f.name || f.vulnclass || "未分類"}</span>
                    )}
                    <span className="truncate text-xs text-muted-foreground">{f.summary}</span>
                    {matchedIds && !matchedIds.includes(Number(f.finding_id)) ? (
                      <Badge variant="outline" className="w-fit">
                        未命中當前篩選
                      </Badge>
                    ) : null}
                    <Badge variant="outline">流量證據 {f.traffic_count ?? 0} 條</Badge>
                  </div>
                </TableCell>
                <TableCell>
                  <FindingAssetLabels assets={f.assets} />
                </TableCell>
                <TableCell onClick={(e) => e.stopPropagation()}>
                  {f.finding_id ? (
                    <Select value={f.status} onValueChange={(v) => onStatusChange(f, v as FindingStatus)}>
                      <SelectTrigger size="sm" className="h-7 w-full border-none px-1 shadow-none focus-visible:ring-0">
                        <StatusBadge domain="finding" value={f.status} dot />
                      </SelectTrigger>
                      <SelectContent position="popper" align="end">
                        {FINDING_STATUSES.map((st) => (
                          <SelectItem key={st} value={st}>
                            {statusMeta("finding", st).label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  ) : (
                    <StatusBadge domain="finding" value={f.status} dot />
                  )}
                </TableCell>
                <TableCell>
                  {f.task_id ? (
                    <Link
                      href={`/function/tasks/detail?id=${f.task_id}`}
                      onClick={(e) => e.stopPropagation()}
                      className="inline-flex max-w-full items-center gap-1 text-primary hover:underline"
                      title={f.task_description}
                    >
                      <span className="truncate">{f.task_description}</span>
                      <ArrowUpRightIcon className="size-3 shrink-0" />
                    </Link>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </TableCell>
                <TableCell className="text-xs text-muted-foreground tabular-nums">{fmtTime(f.ts)}</TableCell>
                <TableCell onClick={(e) => e.stopPropagation()}>
                  <div className="flex items-center gap-1">
                    {retest ? (
                      <Button asChild size="sm" variant="ghost">
                        <Link href={`/chat?c=${retest.conversation_id}`} title="檢視正在進行的複測會話">
                          <Spinner data-icon="inline-start" />
                          複測中
                        </Link>
                      </Button>
                    ) : null}
                    {!retest && f.finding_id && !f.inherited ? (
                      <Button size="sm" variant="ghost" onClick={() => onRetest(f)} title="在獨立會話中複測該漏洞">
                        <RotateCcwIcon data-icon="inline-start" />
                        複測
                      </Button>
                    ) : null}
                    {f.finding_id && f.task_id && (
                      <Button size="sm" variant="ghost" onClick={() => onDeepen(f)}>
                        <FlaskConicalIcon data-icon="inline-start" />
                        深入
                      </Button>
                    )}
                    {f.finding_id && (
                      <AlertDialog>
                        <AlertDialogTrigger asChild>
                          <Button
                            size="icon"
                            variant="ghost"
                            className="size-7 text-muted-foreground hover:text-destructive"
                            aria-label="刪除漏洞"
                          >
                            <Trash2Icon className="size-4" />
                          </Button>
                        </AlertDialogTrigger>
                        <AlertDialogContent>
                          <AlertDialogHeader>
                            <AlertDialogTitle>確認刪除該漏洞？</AlertDialogTitle>
                            <AlertDialogDescription className="break-words">
                              「
                              <span className="break-all">
                                {f.name || f.vulnclass || f.summary || `#${f.finding_id}`}
                              </span>
                              」將被永久刪除， 同時從發現列表、任務發現 Tab 與探索圖中移除，此操作不可撤銷。
                            </AlertDialogDescription>
                          </AlertDialogHeader>
                          <AlertDialogFooter>
                            <AlertDialogCancel>取消</AlertDialogCancel>
                            <AlertDialogAction onClick={() => onDelete(f)}>刪除</AlertDialogAction>
                          </AlertDialogFooter>
                        </AlertDialogContent>
                      </AlertDialog>
                    )}
                  </div>
                </TableCell>
              </TableRow>
              {open && (
                <TableRow className="hover:bg-transparent">
                  {/* whitespace-normal 覆蓋 TableCell 預設的 nowrap,否則展開區文字
                      被強制單行、直接溢位單元格。 */}
                  <TableCell colSpan={COLUMN_COUNT} className="bg-muted/30 whitespace-normal">
                    <div className="flex flex-col gap-2 px-2 py-1">
                      {/* 行內編輯:名稱/類別/嚴重等級,可改並儲存(僅獨立 finding 行)。 */}
                      {f.finding_id && edit && (
                        <div className="flex flex-wrap items-end gap-3 rounded-md border bg-background px-3 py-2.5">
                          <div className="flex min-w-[12rem] flex-1 flex-col gap-1">
                            <Label className="text-xs text-muted-foreground">漏洞名稱</Label>
                            <Input
                              value={edit.name}
                              onChange={(e) => onEditChange((s) => (s ? { ...s, name: e.target.value } : s))}
                              placeholder="可讀標題，留空回退類別"
                            />
                          </div>
                          <div className="flex min-w-[10rem] flex-col gap-1">
                            <Label className="text-xs text-muted-foreground">類別</Label>
                            <Input
                              value={edit.vulnclass}
                              onChange={(e) => onEditChange((s) => (s ? { ...s, vulnclass: e.target.value } : s))}
                              placeholder="如 SQL Injection"
                            />
                          </div>
                          <div className="flex flex-col gap-1">
                            <Label className="text-xs text-muted-foreground">嚴重等級</Label>
                            <Select
                              value={edit.severity}
                              onValueChange={(v) => onEditChange((s) => (s ? { ...s, severity: v as Severity } : s))}
                            >
                              <SelectTrigger size="sm" className="w-28">
                                <SelectValue />
                              </SelectTrigger>
                              <SelectContent>
                                {SEVERITIES.map((sv) => (
                                  <SelectItem key={sv} value={sv}>
                                    {statusMeta("severity", sv).label}
                                  </SelectItem>
                                ))}
                              </SelectContent>
                            </Select>
                          </div>
                          <Button size="sm" disabled={saving} onClick={() => onSave(f)}>
                            {saving ? "儲存中…" : "儲存"}
                          </Button>
                        </div>
                      )}
                      <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                        <ShieldAlertIcon className="size-3.5" />
                        證據
                        {f.vulnclass && (
                          <span>
                            · 型別：
                            <code className="rounded bg-muted px-1.5 py-0.5 font-mono">{f.vulnclass}</code>
                          </span>
                        )}
                        {f.param_id && <code className="rounded bg-muted px-1.5 py-0.5 font-mono">{f.param_id}</code>}
                        {f.assets && f.assets.length > 0 && (
                          <span className="flex flex-wrap items-center gap-1">
                            · 資產：
                            {f.assets.map((a) => (
                              <code key={a.id} className="rounded bg-muted px-1.5 py-0.5 font-mono" title={a.type}>
                                {a.label}
                              </code>
                            ))}
                          </span>
                        )}
                      </div>
                      <pre className="overflow-x-auto rounded-md bg-muted px-3 py-2 font-mono text-xs whitespace-pre-wrap">
                        {f.evidence}
                      </pre>

                      {/* 詳細報告(Markdown):展開時按 finding_id 懶載入,免進詳情頁即可檢視。 */}
                      {f.finding_id && (
                        <div className="flex flex-col gap-1.5">
                          <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
                            <span className="flex items-center gap-2">
                              <FileTextIcon className="size-3.5" />
                              詳細報告
                            </span>
                            {reports[rowKey]?.status === "done" && reports[rowKey]?.text.trim() && (
                              <CopyButton
                                text={reports[rowKey]?.text}
                                successMessage="已複製詳細報告"
                                variant="ghost"
                                className="h-6 px-2 text-xs"
                              />
                            )}
                          </div>
                          {(() => {
                            const rep = reports[rowKey];
                            if (!rep || rep.status === "loading")
                              return <p className="text-xs text-muted-foreground">載入中…</p>;
                            if (rep.status === "error")
                              return <p className="text-xs text-muted-foreground">報告載入失敗。</p>;
                            if (!rep.text.trim())
                              return <p className="text-xs text-muted-foreground">暫無詳細報告。</p>;
                            return (
                              // break-words 會繼承到段落/列表,pre 另加
                              // whitespace-pre-wrap 讓程式碼塊也換行——否則長程式碼行/長 URL
                              // 會撐寬 colSpan 單元格,把整張表擠出橫向捲軸。
                              <div className="min-w-0 break-words rounded-md border bg-background px-3 py-2 [&_pre]:whitespace-pre-wrap">
                                <Markdown text={rep.text} />
                              </div>
                            );
                          })()}
                        </div>
                      )}
                    </div>
                  </TableCell>
                </TableRow>
              )}
            </React.Fragment>
          );
        })}
        {(caseRows ? caseRows.length : items.length) === 0 && (
          <TableRow>
            <TableCell colSpan={COLUMN_COUNT} className="py-12 text-center text-sm text-muted-foreground">
              沒有匹配的發現。
            </TableCell>
          </TableRow>
        )}
      </TableBody>
    </Table>
  );
}

// Ordinary reports and merged folders share one asset presentation, including
// truncation and overflow counts, so labels align in every table view.
function FindingAssetLabels({
  assets = [],
  totalCount = assets.length,
}: {
  assets?: FindingAsset[];
  totalCount?: number;
}) {
  if (assets.length === 0) return <span className="text-muted-foreground">—</span>;
  const visibleAssets = assets.slice(0, 3);
  const remaining = Math.max(totalCount, assets.length) - visibleAssets.length;
  return (
    <div className="flex min-w-0 flex-wrap gap-1">
      {visibleAssets.map((asset) => (
        <code
          key={asset.id}
          className="max-w-full truncate rounded bg-muted px-1.5 py-0.5 font-mono text-xs"
          title={`${asset.type} · ${asset.label}`}
        >
          {asset.label}
        </code>
      ))}
      {remaining > 0 ? <span className="text-xs text-muted-foreground">+{remaining}</span> : null}
    </div>
  );
}

// Folder summaries occupy the same columns as the current view's ordinary rows.
// Expanded members reuse those rows without a second heading or column header.
function FindingFolderTableRows({
  row,
  shared,
}: {
  row: FindingCaseListRow;
  shared: Omit<FindingsTableProps, "items" | "caseRows">;
}) {
  const [open, setOpen] = React.useState(false);
  const group = row.case;
  if (!group) return null;
  // Match the current table's ordinary rows, which display the task description.
  const taskLabel = row.task_description;
  const assets = row.assets ?? [];
  const assetCount = row.asset_count ?? assets.length;
  return (
    <>
      <TableRow className={cn(open && "bg-muted/60")}>
        <TableCell />
        <TableCell>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => setOpen((v) => !v)}
            aria-expanded={open}
            aria-controls={`finding-table-members-${group.id}`}
            aria-label={`${open ? "收起" : "展開"}${group.title}`}
          >
            <ChevronRightIcon className={cn("size-4 transition-transform", open && "rotate-90")} />
          </Button>
        </TableCell>
        <TableCell>
          <FindingSeverityCounts counts={group} />
        </TableCell>
        <TableCell>
          <div className="flex min-w-0 items-start gap-2">
            {open ? (
              <FolderOpenIcon className="mt-0.5 size-4 shrink-0 text-primary" />
            ) : (
              <FolderIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
            )}
            <div className="flex min-w-0 flex-col gap-0.5">
              <Link
                className="truncate font-medium hover:text-primary hover:underline"
                title={group.title}
                href={`/function/findings/case?id=${group.id}`}
              >
                {group.title}
              </Link>
              <span className="truncate text-muted-foreground text-xs">
                {group.count} 條上報 · {group.report_version !== group.version ? "統一報告待更新" : "統一報告已生成"}
              </span>
            </div>
          </div>
        </TableCell>
        <TableCell>
          <FindingAssetLabels assets={assets} totalCount={assetCount} />
        </TableCell>
        <TableCell>
          <span className="text-muted-foreground" title="請檢視成員各自的處置狀態">
            —
          </span>
        </TableCell>
        <TableCell>
          {group.task_id ? (
            <Link
              className="inline-flex max-w-full items-center gap-1 text-primary hover:underline"
              title={taskLabel}
              href={`/function/tasks/detail?id=${group.task_id}`}
            >
              <span className="truncate">{taskLabel}</span>
              <ArrowUpRightIcon className="size-3 shrink-0" />
            </Link>
          ) : (
            <span className="text-muted-foreground">—</span>
          )}
        </TableCell>
        <TableCell className="text-muted-foreground text-xs tabular-nums">
          {row.last_found_at ? fmtTime(row.last_found_at) : "—"}
        </TableCell>
        <TableCell>
          <Button asChild size="sm" variant="ghost">
            <Link href={`/function/findings/case?id=${group.id}`}>統一報告</Link>
          </Button>
        </TableCell>
      </TableRow>
      {open ? (
        <TableRow>
          <TableCell colSpan={COLUMN_COUNT} className="whitespace-normal bg-muted/20 px-4 py-4">
            <section id={`finding-table-members-${group.id}`} aria-label={`${group.title}的原始上報`}>
              <FindingCaseMembers
                caseId={group.id}
                version={group.version}
                nested
                renderRecords={(members) => (
                  <FindingsTable {...shared} items={members} matchedIds={row.matched_ids} hideHeader />
                )}
              />
            </section>
          </TableCell>
        </TableRow>
      ) : null}
    </>
  );
}
