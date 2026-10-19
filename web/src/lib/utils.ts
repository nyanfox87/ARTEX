import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// 抽屜/對話方塊(Sheet/Dialog)的 onInteractOutside 關閉判定輔助。
//
// 背景:抽屜內的 Radix 彈層(Select 下拉、DropdownMenu、Popover 等)會 portal 到抽屜
// 之外。開著彈層時點遮罩/抽屜外想收起它,這一次 pointerdown 會被 Select 和 Sheet 兩個
// DismissableLayer 同時處理;Select 先關閉且是 discrete 事件、React 會同步 flush,於是
// 輪到 Sheet 的處理器時彈層的 data-state 早已翻成 closed —— 在"當下"檢測彈層是否開啟
// 天然不可靠(實測已驗證)。
//
// 正確做法:Radix 的 pointerdown 監聽在冒泡階段;我們在 capture 階段(早於它)先把
// "此刻有沒有彈層開著"記錄下來,onInteractOutside 再讀這個記錄值來決定是否放行關閉。
function isRadixOverlayOpenNow(): boolean {
  if (typeof document === "undefined") return false;
  return !!document.querySelector(
    [
      "[data-slot='select-trigger'][data-state='open']",
      "[data-slot='select-content'][data-state='open']",
      "[role='listbox'][data-state='open']",
      "[data-radix-popper-content-wrapper]",
      "[aria-expanded='true'][data-state='open']",
    ].join(","),
  );
}

let overlayOpenAtLastPointerDown = false;
if (typeof document !== "undefined") {
  document.addEventListener(
    "pointerdown",
    () => {
      overlayOpenAtLastPointerDown = isRadixOverlayOpenNow();
    },
    true, // capture:搶在 Radix 冒泡階段的 pointerdown 處理器之前記錄
  );
}

// radixOverlayWasOpenAtPointerDown 返回"最近一次 pointerdown 發生時是否有 Radix 彈層
// 開著"。抽屜/對話方塊據此:開著彈層時點遮罩 → 只收彈層、不關自身。
export function radixOverlayWasOpenAtPointerDown(): boolean {
  return overlayOpenAtLastPointerDown;
}

// copyText 把文本寫入剪貼簿,返回是否成功。
// 背景:navigator.clipboard 僅在安全上下文(HTTPS / localhost)可用;通過 IP + HTTP
// 訪問時它為 undefined,此時降級到 execCommand("copy")。
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // 繼續走降級方案
    }
  }
  try {
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.style.position = "fixed";
    textarea.style.left = "-9999px";
    textarea.style.top = "0";
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(textarea);
    return ok;
  } catch {
    return false;
  }
}

export const getInitials = (str: string): string => {
  if (typeof str !== "string" || !str.trim()) return "?";

  return (
    str
      .trim()
      .split(/\s+/)
      .filter(Boolean)
      .map((word) => word[0])
      .join("")
      .toUpperCase() || "?"
  );
};

export function formatCurrency(
  amount: number,
  opts?: {
    currency?: string;
    locale?: string;
    minimumFractionDigits?: number;
    maximumFractionDigits?: number;
    noDecimals?: boolean;
  },
) {
  const { currency = "USD", locale = "en-US", minimumFractionDigits, maximumFractionDigits, noDecimals } = opts ?? {};

  const formatOptions: Intl.NumberFormatOptions = {
    style: "currency",
    currency,
    minimumFractionDigits: noDecimals ? 0 : minimumFractionDigits,
    maximumFractionDigits: noDecimals ? 0 : maximumFractionDigits,
  };

  return new Intl.NumberFormat(locale, formatOptions).format(amount);
}
