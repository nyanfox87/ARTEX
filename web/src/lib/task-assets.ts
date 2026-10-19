import type { NewAssetType } from "@/lib/types";

const ASSET_TYPE_LABELS: Record<NewAssetType, string> = {
  app: "應用",
  endpoint: "介面",
  ip: "IP",
  root_domain: "根域名",
  service: "服務",
  subdomain: "子域名",
};

const TASK_ASSET_SOURCE_LABELS: Record<string, string> = {
  agent: "Agent 發現",
  anchor: "黑板錨點",
  api: "資產 API",
  company: "企業關聯",
  legacy: "歷史關聯",
  manual: "人工加入",
  system: "系統關聯",
  task: "任務初始化",
};

export function taskAssetTypeLabel(type: NewAssetType): string {
  return ASSET_TYPE_LABELS[type];
}

export function taskAssetSourceLabel(source: string): string {
  return TASK_ASSET_SOURCE_LABELS[source] ?? source;
}
