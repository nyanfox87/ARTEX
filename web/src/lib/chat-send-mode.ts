"use client";

import * as React from "react";

import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";

// 會話輸入框的傳送/換行鍵位。純前端偏好：只落 localStorage，不入庫、不隨帳號同步，
// 因此換瀏覽器需要重設。見 issue #39——0.3.2 把 Ctrl+Enter 傳送改成了 Enter 傳送，
// 這裡把舊鍵位還回來作為可選項。
export type ChatSendMode = "enter" | "ctrl-enter";

export const CHAT_SEND_MODE_KEY = "artex_chat_send_mode";
export const DEFAULT_CHAT_SEND_MODE: ChatSendMode = "enter";

export const CHAT_SEND_MODE_OPTIONS: { value: ChatSendMode; label: string }[] = [
  { value: "enter", label: "Enter 傳送，Shift+Enter 換行" },
  { value: "ctrl-enter", label: "Ctrl+Enter 傳送，Enter 換行" },
];

function parseMode(raw: string | null): ChatSendMode {
  return raw === "ctrl-enter" || raw === "enter" ? raw : DEFAULT_CHAT_SEND_MODE;
}

// 同一標籤頁內的訂閱者集合。localStorage 的 storage 事件只在「其他」標籤頁觸發，
// 本頁在設定裡改完後要靠 emit 通知同頁的輸入框，否則得重新整理才生效。
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

// 返回的是字串字面量，Object.is 按值比較，不會讓 useSyncExternalStore 陷入迴圈。
function getSnapshot(): ChatSendMode {
  return parseMode(getLocalStorageValue(CHAT_SEND_MODE_KEY));
}

// 服務端沒有 localStorage，先渲染預設值，hydrate 後 getSnapshot 再糾正。
function getServerSnapshot(): ChatSendMode {
  return DEFAULT_CHAT_SEND_MODE;
}

export function useChatSendMode(): ChatSendMode {
  return React.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

export function setChatSendMode(mode: ChatSendMode) {
  setLocalStorageValue(CHAT_SEND_MODE_KEY, mode);
  for (const listener of listeners) listener();
}

// shouldSubmitOnKey 判斷一次按鍵是否應當傳送。
// isComposing / keyCode 229 是中文等輸入法正在選字，必須放行，否則回車選詞會誤傳送。
// enter 模式只排除 Shift，與 0.3.2 的行為逐字保持一致——不改設定的使用者手感不變。
// ctrl-enter 模式同時接受 Ctrl 與 Cmd（macOS）。
export function shouldSubmitOnKey(e: React.KeyboardEvent, mode: ChatSendMode): boolean {
  if (e.key !== "Enter") return false;
  if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return false;
  if (mode === "ctrl-enter") return e.ctrlKey || e.metaKey;
  return !e.shiftKey;
}
