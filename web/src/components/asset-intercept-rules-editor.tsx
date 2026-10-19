"use client";

import * as React from "react";

import { PlusIcon, Trash2Icon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import type { AssetInterceptKind, AssetInterceptRuleInput } from "@/lib/types";

// 用 NativeSelect（原生 <select>）而非 shadcn Select：這個編輯器會用在 Sheet 抽屜內，
// shadcn Select 的下拉 portal 到 body、點選外部會觸發抽屜的「點選外部關閉」誤關；原生下拉無此問題。
export const ASSET_INTERCEPT_KIND_OPTIONS: {
  value: AssetInterceptKind;
  label: string;
  placeholder: string;
}[] = [
  { value: "exact_domain", label: "域名(全等)", placeholder: "example.gov.cn" },
  { value: "exact_ip", label: "IP(全等)", placeholder: "203.0.113.10" },
  { value: "exact_url", label: "URL(全等)", placeholder: "https://example.com/login" },
  { value: "fuzzy_domain", label: "域名(模糊)", placeholder: ".gov.cn" },
  { value: "fuzzy_ip", label: "IP(模糊)", placeholder: "203.0.113." },
  { value: "fuzzy_url", label: "URL(模糊)", placeholder: "/admin" },
  { value: "cidr", label: "CIDR 網段", placeholder: "192.168.0.0/16" },
];

// AssetInterceptRulesEditor 是「攔截/允許規則」的受控多行編輯區（攔截block/允許allow +
// 型別 + 匹配內容 + 備註），不自帶持久化——由父元件決定何時提交。
export function AssetInterceptRulesEditor({
  value,
  onChange,
}: {
  value: AssetInterceptRuleInput[];
  onChange: (v: AssetInterceptRuleInput[]) => void;
}) {
  function update(i: number, patch: Partial<AssetInterceptRuleInput>) {
    onChange(value.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
  }
  function remove(i: number) {
    onChange(value.filter((_, idx) => idx !== i));
  }
  function add() {
    onChange([...value, { action: "block", kind: "fuzzy_domain", pattern: "", note: "", enabled: true }]);
  }
  return (
    <div className="grid gap-2">
      {value.map((r, i) => {
        const ph = ASSET_INTERCEPT_KIND_OPTIONS.find((o) => o.value === r.kind)?.placeholder ?? "";
        return (
          // biome-ignore lint/suspicious/noArrayIndexKey: 行無穩定 id，按索引受控即可
          <div key={i} className="flex items-center gap-2">
            <NativeSelect
              size="sm"
              className="w-[84px] shrink-0"
              value={r.action}
              onChange={(e) => update(i, { action: e.target.value as "block" | "allow" })}
            >
              <NativeSelectOption value="block">攔截</NativeSelectOption>
              <NativeSelectOption value="allow">允許</NativeSelectOption>
            </NativeSelect>
            <NativeSelect
              size="sm"
              className="w-[120px] shrink-0"
              value={r.kind}
              onChange={(e) => update(i, { kind: e.target.value as AssetInterceptKind })}
            >
              {ASSET_INTERCEPT_KIND_OPTIONS.map((o) => (
                <NativeSelectOption key={o.value} value={o.value}>
                  {o.label}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <Input
              className="flex-1"
              placeholder={ph}
              value={r.pattern}
              onChange={(e) => update(i, { pattern: e.target.value })}
            />
            <Input
              className="w-[120px] shrink-0"
              placeholder="備註(可選)"
              value={r.note}
              onChange={(e) => update(i, { note: e.target.value })}
            />
            <Button
              type="button"
              size="icon"
              variant="ghost"
              className="text-destructive hover:text-destructive size-8 shrink-0"
              onClick={() => remove(i)}
            >
              <Trash2Icon className="size-4" />
            </Button>
          </div>
        );
      })}
      <Button type="button" size="sm" variant="outline" className="w-fit" onClick={add}>
        <PlusIcon className="size-4" /> 新增一條
      </Button>
    </div>
  );
}
