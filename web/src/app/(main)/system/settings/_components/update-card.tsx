"use client";

import * as React from "react";

import {
  CheckCircle2Icon,
  DownloadIcon,
  ExternalLinkIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { api, openSSE } from "@/lib/api";
import type { UpdateCheck, UpdateProgress } from "@/lib/types";

/** 等待新版本上線的最長時間。一次升級要經過三次程序啟動（暫存 → 換裝 → 新版），
 *  每次都是秒級，三分鐘足夠覆蓋慢磁碟和 Docker 容器重建。 */
const RESTART_TIMEOUT_MS = 180_000;

function humanSize(n?: number): string {
  if (!n || n <= 0) return "";
  const units = ["B", "KB", "MB", "GB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export function UpdateCard() {
  const [info, setInfo] = React.useState<UpdateCheck | null>(null);
  const [checking, setChecking] = React.useState(true);
  const [progress, setProgress] = React.useState<UpdateProgress | null>(null);
  // 與 progress 分開：暫存完成後程序就沒了，SSE 會斷，此時要切到輪詢 /api/health。
  const [restarting, setRestarting] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  // quiet 同時決定要不要繞過後端快取：進頁面時的自動檢查用快取（頂欄剛查過），
  // 使用者手動點「檢查更新」則強制回源，否則剛釋出的版本要等快取過期才看得到。
  const check = React.useCallback((quiet = false) => {
    setChecking(true);
    api
      .checkUpdate(!quiet)
      .then((r) => {
        setInfo(r);
        if (!quiet) {
          if (r.error) toast.error("檢查更新失敗：" + r.error);
          else if (r.has_update) toast.success(`發現新版本 ${r.latest}`);
          else if (r.comparable) toast.success("當前已是最新版本");
        }
      })
      .catch((e) => {
        if (!quiet) toast.error("檢查更新失敗：" + (e as Error).message);
      })
      .finally(() => setChecking(false));
  }, []);

  React.useEffect(() => {
    check(true);
  }, [check]);

  // 輪詢 /api/health 直到版本號變化。
  //
  // 判據必須是"版本變了"而不是"能連上了"：換裝過程中舊版本會短暫地重新起來一次
  // （那一次只負責把 artex.new 換上去然後立刻退出），只看連通性會誤判成功。
  const waitForNewVersion = React.useCallback(async (fromVersion: string) => {
    setRestarting(true);
    const deadline = Date.now() + RESTART_TIMEOUT_MS;
    while (Date.now() < deadline) {
      await sleep(2000);
      try {
        const r = await fetch("/api/health", { cache: "no-store" });
        if (r.ok) {
          const j = (await r.json()) as { version?: string };
          if (j.version && j.version !== fromVersion) {
            toast.success(`已更新到 ${j.version}，正在重新載入頁面`);
            await sleep(800);
            window.location.reload();
            return;
          }
        }
      } catch {
        // 重啟視窗內連不上是預期的，繼續輪詢。
      }
    }
    setRestarting(false);
    toast.error("等待服務重啟超時。請檢查後端日誌，或確認 artex 是通過 start.sh / start.bat 啟動的。");
  }, []);

  // 訂閱更新進度。SSE 不走 Next 的 /api 重寫（那層會緩衝，事件推不出來）。
  const openStream = React.useCallback(
    (fromVersion: string) => {
      const es = openSSE("/api/update/stream");
      es.onmessage = (ev) => {
        let p: UpdateProgress;
        try {
          p = JSON.parse(ev.data) as UpdateProgress;
        } catch {
          return;
        }
        setProgress(p);
        if (p.phase === "failed") {
          es.close();
          setBusy(false);
          toast.error("更新失敗：" + (p.error || p.message));
          return;
        }
        if (p.phase === "staged") {
          es.close();
          void waitForNewVersion(fromVersion);
        }
      };
      es.onerror = () => {
        // 程序退出時 SSE 必然斷開。如果已經進入等待重啟，這屬於正常現象，
        // 交給 /api/health 輪詢繼續判定即可。
        es.close();
      };
      return es;
    },
    [waitForNewVersion],
  );

  const doUpdate = () => {
    if (!info) return;
    const from = info.current;
    const ok = window.confirm(
      `確定更新到 ${info.latest}？\n\n` +
        "更新會重啟程式，正在執行的任務會被中斷。\n" +
        (info.mode === "docker"
          ? "\n注意：容器內更新只替換程式本身，不會更新映像裡的 playwright / nmap 等工具鏈；" +
            "若新版本依賴新工具，請改用 docker compose pull。"
          : ""),
    );
    if (!ok) return;

    setBusy(true);
    setProgress({ phase: "downloading", percent: 0, message: "準備中…" });
    const es = openStream(from);
    api.applyUpdate().catch((e) => {
      es.close();
      setBusy(false);
      setProgress(null);
      toast.error("啟動更新失敗：" + (e as Error).message);
    });
  };

  const doRollback = () => {
    if (!info) return;
    if (
      !window.confirm(
        "回滾到上一版本？\n\n程式會重啟，正在執行的任務會被中斷。\n注意：資料庫結構不會回退，舊版本可能無法識別新版寫入的資料。",
      )
    )
      return;
    const from = info.current;
    setBusy(true);
    api
      .rollbackUpdate()
      .then(() => {
        toast.success("已切換到上一版本，正在重啟…");
        void waitForNewVersion(from);
      })
      .catch((e) => {
        setBusy(false);
        toast.error("回滾失敗：" + (e as Error).message);
      });
  };

  const phase = progress?.phase;
  const showProgress = busy || restarting;
  // 只有下載階段拿得到真實百分比（按 Content-Length 算）。校驗/解壓/等待重啟都是
  // 時長不可知的階段，進度條填滿並加個脈衝動畫表示"在忙但說不準還要多久"。
  const downloading = !restarting && phase === "downloading";
  const pct = downloading ? Math.max(progress?.percent ?? 0, 0) : 100;

  return (
    // 設定頁是多列瀑布流佈局，卡片自己負責行間距並禁止跨列斷開（見 page.tsx 的註釋）。
    <Card className="mb-4 break-inside-avoid md:mb-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <DownloadIcon className="size-4" />
          版本與更新
        </CardTitle>
        <CardDescription>從 GitHub 檢查並安裝新版本。更新會重啟程式，正在執行的任務會被中斷。</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-muted-foreground">當前版本</span>
          <Badge variant="secondary" className="font-mono">
            {info?.current ?? "…"}
          </Badge>
          {info && (
            <>
              <Badge variant="outline" className="font-mono">
                {info.os}/{info.arch}
              </Badge>
              <Badge variant="outline">{info.mode === "docker" ? "Docker" : "獨立程式"}</Badge>
            </>
          )}
          {info?.latest && (
            <>
              <span className="text-muted-foreground">最新版本</span>
              <Badge variant={info.has_update ? "default" : "secondary"} className="font-mono">
                {info.latest}
              </Badge>
            </>
          )}
          {info?.html_url && (
            <a
              href={info.html_url}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-xs text-muted-foreground underline-offset-4 hover:underline"
            >
              更新日誌 <ExternalLinkIcon className="size-3" />
            </a>
          )}
        </div>

        {info?.boot_notice && (
          <p className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-2 text-xs text-amber-700 dark:text-amber-400">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            {info.boot_notice}
          </p>
        )}

        {info?.error && (
          <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            無法連線 GitHub：{info.error}
            {"　"}可在上方配置全域代理後重試。
          </p>
        )}

        {info && !info.comparable && info.reason && <p className="text-xs text-muted-foreground">{info.reason}</p>}

        {info?.has_update && info.asset_available === false && (
          <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            {info.latest} 沒有提供 {info.os}/{info.arch} 的釋出包（缺少 {info.asset}），無法自動更新。
          </p>
        )}

        {info?.has_update && info.asset_available !== false && (
          <p className="text-xs text-muted-foreground">
            將下載 <span className="font-mono">{info.asset}</span>
            {info.size ? `（${humanSize(info.size)}）` : ""}，校驗 SHA256 並冒煙測試後才會替換，失敗自動保留當前版本。
          </p>
        )}

        {info && !info.has_update && info.comparable && !info.error && (
          <p className="flex items-center gap-2 text-xs text-muted-foreground">
            <CheckCircle2Icon className="size-3.5 text-emerald-600" />
            當前已是最新版本。
          </p>
        )}

        {info?.mode === "docker" && info.has_update && (
          <p className="text-xs text-muted-foreground">
            Docker 下的更新只替換程式本身，不更新映像裡的 playwright / nmap 等工具鏈，且
            <span className="font-mono"> docker compose up -d </span>
            重建容器後會退回映像自帶的版本。需要連映像一起升級請執行
            <span className="font-mono"> docker compose pull artex &amp;&amp; docker compose up -d artex</span>。
          </p>
        )}

        {showProgress && (
          <div className="space-y-1.5">
            <Progress value={pct} className={downloading ? undefined : "animate-pulse"} />
            <p className="text-xs text-muted-foreground">
              {restarting ? "正在重啟並應用新版本，請稍候（頁面會自動重新整理）…" : progress?.message}
            </p>
          </div>
        )}

        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={() => check(false)} disabled={checking || busy || restarting}>
            <RefreshCwIcon className={checking ? "size-4 animate-spin" : "size-4"} />
            檢查更新
          </Button>
          <Button
            size="sm"
            onClick={doUpdate}
            disabled={busy || restarting || !info?.has_update || info?.asset_available === false}
          >
            <DownloadIcon className="size-4" />
            {info?.has_update ? `更新到 ${info.latest}` : "立即更新"}
          </Button>
          {info?.has_backup && (
            <Button variant="ghost" size="sm" onClick={doRollback} disabled={busy || restarting}>
              <RotateCcwIcon className="size-4" />
              回滾到上一版本
            </Button>
          )}
        </div>

        <p className="text-xs text-muted-foreground">
          一鍵更新依賴守護指令碼重啟程式。請通過 <span className="font-mono">start.sh</span>（Windows 為
          <span className="font-mono"> start.bat</span>）啟動 ARTEX；直接執行 artex 本體時，程式退出後不會被自動拉起。
        </p>
      </CardContent>
    </Card>
  );
}
