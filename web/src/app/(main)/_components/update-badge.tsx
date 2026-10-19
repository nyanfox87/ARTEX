"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowUpCircleIcon } from "lucide-react";

import { api } from "@/lib/api";

/**
 * 頂欄的"有新版本"提示：整頁載入時查一次，有更新就在版本號旁邊亮出來，
 * 點選直達系統配置頁的「版本與更新」卡片。
 *
 * 後端對 GitHub 的查詢結果有 30 分鐘快取，所以這裡每次掛載都查一次是安全的
 * ——未認證的 GitHub API 只有 60 次/小時/IP，沒有那層快取的話，多開幾個標籤頁
 * 就會把配額耗光，之後真想更新反而查不動。
 *
 * 查詢失敗一律靜默：頂欄不是報錯的地方，使用者進設定頁點「檢查更新」會看到原因。
 */
export function UpdateBadge() {
  const [latest, setLatest] = React.useState("");

  React.useEffect(() => {
    let alive = true;
    api
      .checkUpdate()
      .then((r) => {
        // has_update 已經包含了"版本號可比較"的判斷，開發構建不會亮這個提示。
        if (alive && r.has_update && r.latest) setLatest(r.latest.replace(/^v(?=\d)/, ""));
      })
      .catch(() => {
        // 靜默：沒網 / GitHub 限流都不該在頂欄彈錯誤。
      });
    return () => {
      alive = false;
    };
  }, []);

  if (!latest) return null;

  return (
    <Link
      href="/system/settings"
      title={`發現新版本 ${latest}，點選前往更新`}
      className="inline-flex items-center gap-1.5 rounded-full bg-primary px-2.5 py-1 font-medium text-primary-foreground text-xs transition-opacity hover:opacity-90"
    >
      {/* 呼吸點：頂欄元素很多，純文字容易被忽略，動效讓它一眼可見。 */}
      <span className="relative flex size-1.5">
        <span className="absolute inline-flex size-full animate-ping rounded-full bg-primary-foreground opacity-75" />
        <span className="relative inline-flex size-1.5 rounded-full bg-primary-foreground" />
      </span>
      <ArrowUpCircleIcon className="size-3.5" />
      <span className="hidden sm:inline">新版本 {latest}</span>
      <span className="sm:hidden">新版本</span>
    </Link>
  );
}
