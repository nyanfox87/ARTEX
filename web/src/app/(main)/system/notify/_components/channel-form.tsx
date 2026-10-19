"use client";

import { CheckIcon } from "lucide-react";

import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import type { NotificationFilter } from "@/lib/types";

// asText / inputType 是本檔案內的取值輔助（與控制元件渲染強相關），不放在 channel-fields。
import { type FieldDef, type FieldKind, SEVERITY_OPTIONS } from "./channel-fields";

// asText 把任意配置值渲染成輸入框可用的字串。
// config 來自 JSON，值可能是 string / number / boolean / array / null，
// 這裡只關心「能不能塞進文本框」，具體序列化由 buildConfig 負責。
function asText(v: unknown): string {
  if (typeof v === "string") return v;
  if (v === null || v === undefined) return "";
  return String(v);
}

// inputType 把欄位型別對映到 input 的 type 屬性。
function inputType(kind: FieldKind): "text" | "password" | "number" {
  if (kind === "password") return "password";
  if (kind === "number") return "number";
  return "text";
}

// ConfigField 按欄位定義渲染對應的控制元件。
//
// 掩碼欄位的處理是這裡唯一的講究：輸入框**不顯示**掩碼值本身，只顯示一行
// 「已儲存」提示。這樣介面上就只有一個規則——框裡有字就是使用者填的，
// 空框就是空值。若把 "__masked__:…abc123" 塞進輸入框，使用者會以為那是要自己
// 刪掉的佔位文本，反而更容易誤清憑據。
export function ConfigField({
  def,
  value,
  isSecret,
  onChange,
}: {
  def: FieldDef;
  value: unknown;
  isSecret: boolean;
  onChange: (v: unknown) => void;
}) {
  const id = `n-cfg-${def.key}`;
  const raw = asText(value);
  // 後端回顯的掩碼值：形如 "__masked__:…abc123"，尾部是原值的可辨識片段。
  const masked = isSecret && raw.startsWith("__masked__");
  const maskedTail = masked ? (raw.split("…")[1] ?? "") : "";

  if (def.kind === "switch") {
    return (
      <div className="flex items-center gap-2 text-sm">
        <Switch checked={value === true} onCheckedChange={onChange} aria-label={def.label} />
        {def.label}
        {def.help && <span className="text-muted-foreground">（{def.help}）</span>}
      </div>
    );
  }

  if (def.kind === "select") {
    return (
      <div className="grid gap-2">
        <Label>{def.label}</Label>
        <Select value={raw || def.options?.[0]?.value} onValueChange={onChange}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(def.options ?? []).map((o) => (
              <SelectItem key={o.value} value={o.value}>
                {o.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    );
  }

  // 控制元件按欄位型別分派。用 if 鏈而不是巢狀三元，是因為這裡要區分四種控制元件，
  // 三層三元讀起來已經要停下來數括號了。
  function control() {
    if (def.kind === "textarea" || def.kind === "kv") {
      return (
        <Textarea
          id={id}
          className="font-mono"
          placeholder={def.placeholder}
          value={masked ? "" : raw}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    }
    if (def.kind === "list") {
      return (
        <Input
          id={id}
          value={Array.isArray(value) ? (value as string[]).join(", ") : raw}
          onChange={(e) => onChange(e.target.value)}
          placeholder={def.placeholder}
        />
      );
    }
    return (
      <Input
        id={id}
        className={def.kind === "text" ? "font-mono" : ""}
        type={inputType(def.kind)}
        placeholder={def.placeholder}
        value={masked ? "" : raw}
        onChange={(e) => onChange(e.target.value)}
      />
    );
  }

  const hint = masked ? (
    <p className="text-muted-foreground flex items-center gap-1 text-xs">
      <CheckIcon className="size-3" />
      已儲存{maskedTail ? `（尾號 ${maskedTail}）` : ""} · 填入新值即覆蓋，清空則刪除該項
    </p>
  ) : (
    def.help && <p className="text-muted-foreground text-xs">{def.help}</p>
  );

  return (
    <div className="grid gap-2">
      <Label htmlFor={id}>{def.label}</Label>
      {control()}
      {hint}
    </div>
  );
}

// FilterSummary 把過濾條件摘要成一行，讓卡片不用展開就能看出這個渠道推什麼。
export function FilterSummary({ filter }: { filter: NotificationFilter }) {
  const parts: string[] = [];
  if (filter.min_severity) {
    parts.push(SEVERITY_OPTIONS.find((o) => o.value === filter.min_severity)?.label ?? filter.min_severity);
  }
  if (filter.vulnclass_include?.length) parts.push(`型別含 ${filter.vulnclass_include.length} 詞`);
  if (filter.vulnclass_exclude?.length) parts.push(`排除 ${filter.vulnclass_exclude.length} 詞`);
  if (filter.task_ids?.length) parts.push(`${filter.task_ids.length} 個任務`);
  if (filter.asset_ids?.length) parts.push(`${filter.asset_ids.length} 個資產`);
  if (filter.on_status_change) parts.push("含狀態變更");
  if (parts.length === 0) {
    return <p className="text-muted-foreground text-sm">全部漏洞</p>;
  }
  return <p className="text-muted-foreground text-sm">{parts.join(" · ")}</p>;
}
