// 渠道欄位表與配置值的解析工具。
//
// 與頁面拆開是因為這一份是**資料**而不是檢視：它描述每種渠道有哪些欄位、
// 各自該用什麼控制元件，以及表單文本到配置值（JSON）的雙向轉換。
// 單獨放一個檔案後，新增渠道只需要動這裡，頁面本身不必改。
// 渠道型別的展示名與簡介。放在前端是因為它隻影響文案，後端不需要知道。
export const KIND_LABEL: Record<string, string> = {
  dingtalk: "釘釘",
  feishu: "飛書",
  wecom: "企業微信",
  webhook: "通用 Webhook",
  telegram: "Telegram",
  email: "郵件",
};

// 各渠道的配置欄位定義。
//
// 這裡刻意保留一份前端欄位表，而不是讓後端下發 schema：後端只負責
// Validate（必填/格式），UI 需要的是佈局與控制元件型別，兩者關注的不是同一件事。
// 唯一的耦合點是 secret_keys —— 哪些欄位該渲染成密碼框由後端給出，
// 因為只有渠道實現自己清楚哪些值算憑據（企業微信的整個 Webhook 就是憑據，
// 而釘釘的只是其中一個 secret）。新增渠道時這裡少一個條目只會讓表單變空白，
// 不會靜默出錯（下面的 hasFields 會提示）。
export type FieldKind = "text" | "password" | "number" | "select" | "textarea" | "switch" | "kv" | "list";
export interface FieldDef {
  key: string;
  label: string;
  kind: FieldKind;
  placeholder?: string;
  help?: string;
  options?: { value: string; label: string }[];
}
export const CHANNEL_FIELDS: Record<string, FieldDef[]> = {
  dingtalk: [
    {
      key: "webhook",
      label: "Webhook 地址",
      kind: "text",
      placeholder: "https://oapi.dingtalk.com/robot/send?access_token=...",
    },
    {
      key: "secret",
      label: "加簽金鑰",
      kind: "password",
      help: "機器人安全設定選「加簽」時填寫；選「自定義關鍵詞」或未開啟安全設定則留空",
    },
  ],
  feishu: [
    {
      key: "webhook",
      label: "Webhook 地址",
      kind: "text",
      placeholder: "https://open.feishu.cn/open-apis/bot/v2/hook/...",
    },
    { key: "secret", label: "簽名校驗金鑰", kind: "password", help: "機器人開啟「簽名校驗」時填寫，否則留空" },
  ],
  wecom: [
    {
      key: "webhook",
      label: "Webhook 地址",
      kind: "text",
      placeholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...",
    },
  ],
  webhook: [
    { key: "url", label: "目標 URL", kind: "text", placeholder: "https://your-endpoint.example.com/hook" },
    {
      key: "method",
      label: "請求方法",
      kind: "select",
      options: [
        { value: "POST", label: "POST（帶請求體）" },
        { value: "PUT", label: "PUT（帶請求體）" },
        { value: "PATCH", label: "PATCH（帶請求體）" },
        { value: "GET", label: "GET（不帶請求體）" },
      ],
    },
    { key: "headers", label: "自定義請求頭", kind: "kv", help: "每行 KEY=VALUE，例如 Authorization=Bearer xxx" },
    {
      key: "body_template",
      label: "請求體模板",
      kind: "textarea",
      help:
        "留空用內建預設模板。變數：{{.Title}} {{.Batch}} {{.Count}} {{.HomeURL}} {{.SentAt}}，" +
        "以及 range .Items 下的 .Name/.VulnClass/.Severity/.Summary/.Assets/.DetailURL/.StatusLabel。" +
        "插入字串請用 {{json .Xxx}} 而不是 {{.Xxx}}，否則標題裡的引號會破壞 JSON。",
    },
  ],
  telegram: [
    { key: "bot_token", label: "Bot Token", kind: "password", placeholder: "123456:ABC-DEF..." },
    { key: "chat_id", label: "Chat ID", kind: "text", placeholder: "-1001234567890" },
    {
      key: "base_url",
      label: "API 地址",
      kind: "text",
      placeholder: "https://api.telegram.org",
      help: "留空用官方地址；自建 Bot API 反代時填寫",
    },
  ],
  email: [
    { key: "host", label: "SMTP 伺服器", kind: "text", placeholder: "smtp.example.com" },
    {
      key: "port",
      label: "埠",
      kind: "number",
      placeholder: "587",
      help: "587 走 STARTTLS；465 請把「隱式 TLS」開啟",
    },
    { key: "username", label: "帳號", kind: "text" },
    { key: "password", label: "密碼 / 授權碼", kind: "password" },
    { key: "from", label: "發件人", kind: "text", placeholder: "artex@example.com" },
    { key: "to", label: "收件人", kind: "list", help: "多個地址用逗號分隔" },
    { key: "tls", label: "隱式 TLS", kind: "switch", help: "465 埠開啟；587 保持關閉（會自動 STARTTLS）" },
  ],
};

export const SEVERITY_OPTIONS = [
  { value: "", label: "不限" },
  { value: "low", label: "低危及以上" },
  { value: "medium", label: "中危及以上" },
  { value: "high", label: "高危及以上" },
  { value: "critical", label: "僅嚴重" },
];

export type ChannelForm = {
  name: string;
  kind: string;
  mode: "realtime" | "digest";
  enabled: boolean;
  ratePerMin: string;
  config: Record<string, unknown>;
  minSeverity: string;
  includeText: string;
  excludeText: string;
  taskIDsText: string;
  assetIDsText: string;
  onStatusChange: boolean;
};

export const emptyForm = (kind: string): ChannelForm => ({
  name: "",
  kind,
  mode: "realtime",
  enabled: true,
  ratePerMin: "",
  config: {},
  minSeverity: "",
  includeText: "",
  excludeText: "",
  taskIDsText: "",
  assetIDsText: "",
  onStatusChange: false,
});

// parseKV 解析「每行 KEY=VALUE」的文本域。
export function parseKV(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const t = line.trim();
    if (!t) continue;
    const i = t.indexOf("=");
    if (i > 0) out[t.slice(0, i).trim()] = t.slice(i + 1).trim();
  }
  return out;
}
// parseIDs 解析逗號/空白分隔的 id 列表。
export function parseIDs(text: string): number[] {
  return text
    .split(/[\s,，]+/)
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => Number(s))
    .filter((n) => Number.isFinite(n) && n > 0);
}
// parseKeywords 解析行/逗號分隔的關鍵詞列表（漏洞型別名可能含空格，所以按行或逗號切）。
export function parseKeywords(text: string): string[] {
  return text
    .split(/[\n,，]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}
