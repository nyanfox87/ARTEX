"use client";

import * as React from "react";
import {
  DownloadIcon,
  FileIcon,
  FolderIcon,
  FolderPlusIcon,
  HardDriveIcon,
  RefreshCwIcon,
  SaveIcon,
  Trash2Icon,
  UploadIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Sheet,
  SheetContent,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import { api } from "@/lib/api";
import type { WorkspaceEntry, WorkspaceFile } from "@/lib/types";

function fmtSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
}
function fmtTime(ms: number): string {
  return new Date(ms).toLocaleString("zh-CN", {
    year: "2-digit",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

type EditState = {
  file: WorkspaceFile;
  content: string;
  dirty: boolean;
  saving: boolean;
};

export default function WorkspacePage() {
  const [path, setPath] = React.useState("");
  const [entries, setEntries] = React.useState<WorkspaceEntry[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [edit, setEdit] = React.useState<EditState | null>(null);
  const [mkdirOpen, setMkdirOpen] = React.useState(false);
  const [mkdirName, setMkdirName] = React.useState("");
  const uploadRef = React.useRef<HTMLInputElement>(null);

  const load = React.useCallback(
    (p: string) => {
      setLoading(true);
      api
        .workspaceList(p)
        .then((r) => {
          setEntries(r.entries);
          setPath(r.path);
        })
        .catch((e) => toast.error(`讀取目錄失敗：${(e as Error).message}`))
        .finally(() => setLoading(false));
    },
    [],
  );

  React.useEffect(() => {
    load("");
  }, [load]);

  const crumbs = React.useMemo(() => {
    const parts = path ? path.split("/") : [];
    const acc: { name: string; path: string }[] = [{ name: "工作空間", path: "" }];
    let cur = "";
    for (const part of parts) {
      cur = cur ? `${cur}/${part}` : part;
      acc.push({ name: part, path: cur });
    }
    return acc;
  }, [path]);

  const openFile = (e: WorkspaceEntry) => {
    api
      .workspaceRead(e.path)
      .then((f) => setEdit({ file: f, content: f.content ?? "", dirty: false, saving: false }))
      .catch((err) => toast.error(`開啟檔案失敗：${(err as Error).message}`));
  };

  const saveFile = () => {
    if (!edit) return;
    setEdit({ ...edit, saving: true });
    api
      .workspaceWrite(edit.file.path, edit.content)
      .then(() => {
        toast.success("已儲存");
        setEdit((cur) => (cur ? { ...cur, dirty: false, saving: false } : cur));
        load(path);
      })
      .catch((err) => {
        toast.error(`儲存失敗：${(err as Error).message}`);
        setEdit((cur) => (cur ? { ...cur, saving: false } : cur));
      });
  };

  const del = (e: WorkspaceEntry) => {
    if (!window.confirm(`確認刪除 ${e.dir ? "目錄" : "檔案"} “${e.name}”？${e.dir ? "（含其下所有內容）" : ""}`)) return;
    api
      .workspaceDelete(e.path)
      .then(() => {
        toast.success("已刪除");
        load(path);
      })
      .catch((err) => toast.error(`刪除失敗：${(err as Error).message}`));
  };

  const doUpload = (files: FileList | null) => {
    if (!files || files.length === 0) return;
    api
      .workspaceUpload(path, Array.from(files))
      .then((r) => {
        toast.success(`已上傳 ${r.uploaded} 個檔案`);
        load(path);
      })
      .catch((err) => toast.error(`上傳失敗：${(err as Error).message}`))
      .finally(() => {
        if (uploadRef.current) uploadRef.current.value = "";
      });
  };

  const doMkdir = () => {
    const name = mkdirName.trim();
    if (!name) return;
    const target = path ? `${path}/${name}` : name;
    api
      .workspaceMkdir(target)
      .then(() => {
        toast.success("已建立目錄");
        setMkdirOpen(false);
        setMkdirName("");
        load(path);
      })
      .catch((err) => toast.error(`建立失敗：${(err as Error).message}`));
  };

  return (
    <div className="flex flex-col gap-4">
      {/* 頭部：麵包屑 + 操作 */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-1 text-sm">
          <HardDriveIcon className="text-muted-foreground mr-1 size-4 shrink-0" />
          {crumbs.map((c, i) => (
            <React.Fragment key={c.path}>
              {i > 0 && <span className="text-muted-foreground">/</span>}
              <button
                type="button"
                onClick={() => load(c.path)}
                className={cn(
                  "max-w-[160px] truncate rounded px-1.5 py-0.5 hover:bg-accent",
                  i === crumbs.length - 1 ? "text-foreground font-medium" : "text-muted-foreground",
                )}
              >
                {c.name}
              </button>
            </React.Fragment>
          ))}
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => setMkdirOpen(true)}>
            <FolderPlusIcon /> 新建資料夾
          </Button>
          <Button variant="outline" size="sm" onClick={() => uploadRef.current?.click()}>
            <UploadIcon /> 上傳
          </Button>
          <Button variant="ghost" size="icon" className="size-8" onClick={() => load(path)} title="重新整理">
            <RefreshCwIcon className={cn("size-4", loading && "animate-spin")} />
          </Button>
          <input
            ref={uploadRef}
            type="file"
            multiple
            className="hidden"
            onChange={(e) => doUpload(e.target.files)}
          />
        </div>
      </div>

      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>名稱</TableHead>
                <TableHead className="w-28 text-right">大小</TableHead>
                <TableHead className="w-40">修改時間</TableHead>
                <TableHead className="w-24 text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {entries.length === 0 && (
                <TableRow>
                  <TableCell colSpan={4} className="text-muted-foreground py-10 text-center text-sm">
                    {loading ? "載入中…" : "空目錄"}
                  </TableCell>
                </TableRow>
              )}
              {entries.map((e) => (
                <TableRow key={e.path}>
                  <TableCell>
                    <button
                      type="button"
                      onClick={() => (e.dir ? load(e.path) : openFile(e))}
                      className="flex items-center gap-2 text-left hover:underline"
                    >
                      {e.dir ? (
                        <FolderIcon className="size-4 shrink-0 text-blue-500" />
                      ) : (
                        <FileIcon className="text-muted-foreground size-4 shrink-0" />
                      )}
                      <span className="truncate font-mono text-sm">{e.name}</span>
                    </button>
                  </TableCell>
                  <TableCell className="text-muted-foreground text-right font-mono text-xs">
                    {e.dir ? "—" : fmtSize(e.size)}
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs">{fmtTime(e.mtime)}</TableCell>
                  <TableCell>
                    <div className="flex items-center justify-end gap-1">
                      {!e.dir && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="size-7"
                          title="下載"
                          onClick={() => api.workspaceDownload(e.path).catch((err) => toast.error(`下載失敗：${(err as Error).message}`))}
                        >
                          <DownloadIcon className="size-3.5" />
                        </Button>
                      )}
                      <Button
                        variant="ghost"
                        size="icon"
                        className="text-destructive size-7"
                        title="刪除"
                        onClick={() => del(e)}
                      >
                        <Trash2Icon className="size-3.5" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      {/* 檔案檢視 / 編輯 */}
      <Sheet open={edit !== null} onOpenChange={(o) => !o && setEdit(null)}>
        <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 sm:max-w-2xl">
          {edit && (
            <>
              <SheetHeader className="border-b p-4">
                <SheetTitle className="flex items-center gap-2 truncate font-mono text-sm">
                  <FileIcon className="size-4 shrink-0" />
                  <span className="truncate" title={edit.file.path}>
                    {edit.file.path}
                  </span>
                </SheetTitle>
                <span className="text-muted-foreground text-xs">{fmtSize(edit.file.size)}</span>
              </SheetHeader>

              {edit.file.binary || edit.file.too_large ? (
                <div className="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-center">
                  <p className="text-muted-foreground text-sm">
                    {edit.file.too_large ? "檔案過大，不支援線上預覽/編輯。" : "二進位制檔案，不支援線上預覽/編輯。"}
                  </p>
                  <Button variant="outline" onClick={() => api.workspaceDownload(edit.file.path)}>
                    <DownloadIcon /> 下載檔案
                  </Button>
                </div>
              ) : (
                <>
                  <div className="min-h-0 flex-1 p-3">
                    <Textarea
                      value={edit.content}
                      onChange={(ev) => setEdit({ ...edit, content: ev.target.value, dirty: true })}
                      spellCheck={false}
                      className="h-full min-h-[50vh] resize-none font-mono text-xs leading-relaxed"
                    />
                  </div>
                  <SheetFooter className="flex-row items-center justify-between border-t p-3">
                    <span className="text-muted-foreground text-xs">{edit.dirty ? "未儲存的修改" : "已同步"}</span>
                    <div className="flex gap-2">
                      <Button
                        variant="outline"
                        onClick={() => api.workspaceDownload(edit.file.path)}
                      >
                        <DownloadIcon /> 下載
                      </Button>
                      <Button onClick={saveFile} disabled={!edit.dirty || edit.saving}>
                        <SaveIcon /> {edit.saving ? "儲存中…" : "儲存"}
                      </Button>
                    </div>
                  </SheetFooter>
                </>
              )}
            </>
          )}
        </SheetContent>
      </Sheet>

      {/* 新建資料夾 */}
      <Dialog open={mkdirOpen} onOpenChange={setMkdirOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>新建資料夾</DialogTitle>
          </DialogHeader>
          <Input
            autoFocus
            value={mkdirName}
            onChange={(e) => setMkdirName(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && doMkdir()}
            placeholder="資料夾名稱"
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setMkdirOpen(false)}>
              取消
            </Button>
            <Button onClick={doMkdir} disabled={!mkdirName.trim()}>
              建立
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
