import {
  Activity,
  Ban,
  BellRing,
  CalendarClock,
  Bot,
  Brain,
  Bug,
  ClipboardList,
  FolderOpen,
  FolderSync,
  LayoutDashboard,
  type LucideIcon,
  MessageSquare,
  Network,
  Plug,
  Radio,
  ScrollText,
  Settings2,
  ShieldAlert,
  Sparkles,
  Target,
  Terminal,
  Wrench,
} from "lucide-react";

export type NavBadge = "new" | "soon";

export interface NavSubItem {
  id: string;
  title: string;
  url: string;
  icon?: LucideIcon;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
}

interface NavItemBase {
  id: string;
  title: string;
  icon?: LucideIcon;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
}

export interface NavMainLinkItem extends NavItemBase {
  url: string;
  subItems?: never;
}

export interface NavMainParentItem extends NavItemBase {
  subItems: NavSubItem[];
}

export type NavMainItem = NavMainLinkItem | NavMainParentItem;

export interface NavGroup {
  id: number;
  label?: string;
  items: NavMainItem[];
}

export const sidebarItems: NavGroup[] = [
  {
    id: 1,
    label: "功能",
    items: [
      { id: "dashboard", title: "儀表盤", url: "/dashboard", icon: LayoutDashboard },
      { id: "chat", title: "對話", url: "/chat", icon: MessageSquare },
      { id: "tasks", title: "任務", url: "/function/tasks", icon: Target },
      { id: "schedules", title: "計劃任務", url: "/function/schedules", icon: CalendarClock },
      { id: "findings", title: "發現", url: "/function/findings", icon: Bug },
      { id: "traffic", title: "流量", url: "/function/traffic", icon: Activity },
      { id: "commands", title: "工具執行", url: "/function/commands", icon: Terminal },
      { id: "llm-records", title: "LLM 錄製", url: "/function/llm-records", icon: Radio },
      { id: "assets", title: "資產", url: "/function/assets", icon: Network },
      { id: "sync", title: "資產同步", url: "/function/sync", icon: FolderSync },
      { id: "workspace", title: "工作空間", url: "/function/workspace", icon: FolderOpen },
    ],
  },
  {
    id: 2,
    label: "系統",
    items: [
      { id: "llm", title: "LLM", url: "/system/llm", icon: Brain },
      { id: "agents", title: "Agent", url: "/system/agents", icon: Bot },
      { id: "mcp", title: "MCP", url: "/system/mcp", icon: Plug },
      { id: "skills", title: "Skill", url: "/system/skills", icon: Sparkles },
      { id: "tools", title: "工具", url: "/system/tools", icon: Wrench },
      { id: "notify", title: "通知推送", url: "/system/notify", icon: BellRing },
      { id: "intercept", title: "攔截規則", url: "/system/intercept", icon: ShieldAlert },
      { id: "asset-intercept", title: "資產攔截", url: "/system/intercept/assets", icon: Ban },
      { id: "approvals", title: "審批記錄", url: "/system/intercept/approvals", icon: ClipboardList },
      { id: "logs", title: "日誌", url: "/system/logs", icon: ScrollText },
      { id: "settings", title: "系統配置", url: "/system/settings", icon: Settings2 },
    ],
  },
];
