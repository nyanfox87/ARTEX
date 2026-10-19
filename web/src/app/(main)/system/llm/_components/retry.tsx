"use client";

// LLM 重試配置的共用件：五層重試各自的「次數 + 間隔」。
//
// 五層從內到外：建連(SDK) → 空回應(SDK) → 同 provider 安全視窗 → 輪詢熔斷 → 意圖重跑。
// 前三層跟著端點走，所以每個模型配置都能覆蓋全域預設；後兩層是程序級的，只有全域一份。
//
// 所有輸入都遵循同一套「留空 = 不配置」語義，與後端 db.RetryRule 一致：
//   次數   空/0 = 用內建預設 | -1 = 關閉這層重試 | >0 = 用這個次數
//   間隔   空/0 = 用這層原本的指數退避 | >0 = 改用這個固定毫秒間隔

import * as React from "react";

import { Loader2Icon, SaveIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { LLMRetryOverride, LLMRetryPolicy, LLMRetryRule } from "@/lib/types";

export const ZERO_RULE: LLMRetryRule = { attempts: 0, interval_ms: 0 };
export const ZERO_OVERRIDE: LLMRetryOverride = {
  connect: ZERO_RULE,
  empty: ZERO_RULE,
  stream: ZERO_RULE,
};
const ZERO_POLICY: LLMRetryPolicy = {
  ...ZERO_OVERRIDE,
  breaker: ZERO_RULE,
  intent: ZERO_RULE,
};

type LayerMeta = {
  title: string;
  /** 這層重試發生在哪、由誰執行 */
  where: string;
  /** 什麼樣的錯誤會走到這層——具體到狀態碼，別讓人猜 */
  trigger: string;
  /** 長得像但【不】走這層的錯誤，省得填了沒反應還以為是 bug */
  skips?: string;
  desc: string;
  attemptsLabel: string;
  /** 次數留空時的預設值，用於佔位符 */
  defAttempts: number;
  /** 間隔留空時的預設策略，用於佔位符 */
  defInterval: string;
  /** 次數填 -1 的含義 */
  offHint: string;
};

export const RETRY_LAYERS = {
  connect: {
    title: "建連重試",
    where: "SDK · 拿到 200 之前",
    trigger:
      "連不上或還沒拿到 200：連線重置 / 讀寫超時 / DNS 失敗等網路層錯誤，以及 HTTP 408、429、500、502、503、504。",
    skips: "其餘狀態碼（400 / 401 / 403 / 404 / 413 / 422 等）都是確定性拒絕，重發也一樣失敗，直接上拋。",
    desc: "原樣重發同一個請求。流一旦開始（已經拿到 200），中途斷開就不歸這層管了。",
    attemptsLabel: "重試次數",
    defAttempts: 3,
    defInterval: "0.5s→1s→2s 指數（封頂 8s）",
    offHint: "-1 = 一次都不重試，失敗立刻上拋",
  },
  empty: {
    title: "空回應重試",
    where: "SDK · 僅 openai 格式",
    trigger:
      "HTTP 200、finish_reason 是正常 stop，但整條回應一個內容塊都沒有——閘道器空幀、思考欄位丟幀、取樣打嗝都會長這樣。",
    skips: "因 max_tokens 截斷而沒有內容的不算（那要靠調高輸出上限解決，重發只會再撞一次）。",
    desc: "重發整個 prompt，所以在長上下文上比較貴，次數不宜給大。",
    attemptsLabel: "重試次數",
    defAttempts: 2,
    defInterval: "0.5s→1s→2s 指數（封頂 8s）",
    offHint: "-1 = 空回應直接原樣交出",
  },
  stream: {
    title: "同 provider 安全視窗重試",
    where: "本專案 · 未交付輸出前",
    trigger:
      "流已經建立（拿到 200）之後才出問題：連線中途斷開、供應商 overloaded、流內的 429 / 5xx 錯誤事件——且一個 token 都還沒交給呼叫方。",
    skips:
      "額度耗盡（402 / insufficient_quota，交給輪詢換配置）、上下文過長（413 / context length，交給壓縮）、400 / 401 / 403 / 404 / 422 確定性拒絕，都不重試。",
    desc: "在同一個配置上重放同一個請求。因為還沒交付任何輸出，重放不會重複模型輸出或工具執行。",
    attemptsLabel: "重試次數",
    defAttempts: 2,
    defInterval: "0.5s→1s 指數（封頂 4s）",
    offHint: "-1 = 斷流直接交給外層的意圖重跑",
  },
  breaker: {
    title: "輪詢熔斷",
    where: "本專案 · 程序級，全域一份",
    trigger:
      "瞬時失敗（429、5xx、網路錯誤）連續累計到閾值時熔斷；餘額不足（402）、金鑰失效（401 / 403）、模型不存在（404）這類確定性失敗不看閾值，第一次就熔斷。",
    skips: "成功一次即清零，所以偶爾抽風的配置不會被慢慢攢到熔斷。",
    desc: "熔斷後進入冷卻，冷卻期內輪詢直接跳過這個配置。狀態落庫，重啟不丟。",
    attemptsLabel: "連續失敗幾次熔斷",
    defAttempts: 3,
    defInterval: "1min→5min→30min 梯度",
    offHint: "-1 = 瞬時失敗永不熔斷（確定性失敗仍然熔斷）",
  },
  intent: {
    title: "意圖重跑",
    where: "本專案 · 程序級，全域一份",
    trigger:
      "前面幾層都沒兜住：worker 以 model_error 收場——內層重試全部用盡，或者流已經開始交付輸出後才斷掉（那時重放不安全，只能整條重來）。",
    skips: "額度耗盡已經由輪詢換配置處理，不在這裡重跑；任務被暫停 / 終止 / 進入收尾時立即讓位，不佔用退避時間。",
    desc: "整條意圖從頭再跑一遍。它是最外層，一次重跑意味著裡面幾層的次數會再乘一遍。",
    attemptsLabel: "重跑次數",
    defAttempts: 2,
    defInterval: "固定 3s",
    offHint: "-1 = 不重跑，該意圖直接判為 blocked",
  },
} satisfies Record<string, LayerMeta>;

type LayerKey = keyof typeof RETRY_LAYERS;

/** 毫秒的人話，只用於在輸入框旁邊回顯，免得數零。 */
function humanMs(ms: number) {
  if (!Number.isFinite(ms) || ms <= 0) return "";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${Number((ms / 1000).toFixed(2))}s`;
  return `${Number((ms / 60_000).toFixed(2))}min`;
}

/** 受控數字輸入：空串 ↔ 0，中間態（"-"、"1e"）原樣留在本地，不打擾父級。 */
function NumField({
  id,
  value,
  onChange,
  placeholder,
  min,
}: {
  id: string;
  value: number;
  onChange: (n: number) => void;
  placeholder: string;
  min: number;
}) {
  const [text, setText] = React.useState(value === 0 ? "" : String(value));
  // 父級換了一整套值（讀取到策略、切換配置）時跟上；自己敲字時不會走到這裡，
  // 因為那時 value 已經等於本地文本 parse 後的結果。
  React.useEffect(() => {
    const incoming = value === 0 ? "" : String(value);
    setText((cur) => (Number(cur || 0) === value ? cur : incoming));
  }, [value]);
  return (
    <Input
      id={id}
      type="number"
      min={min}
      className="w-28 shrink-0"
      value={text}
      placeholder={placeholder}
      onChange={(e) => {
        setText(e.target.value);
        const n = Number(e.target.value);
        onChange(e.target.value.trim() === "" || !Number.isFinite(n) ? 0 : Math.trunc(n));
      }}
    />
  );
}

/** 一層重試的兩個旋鈕。idPrefix 用來在同一頁出現多次時保住 label 的 htmlFor。 */
export function RetryRuleFields({
  layer,
  idPrefix,
  value,
  onChange,
  compact,
}: {
  layer: LayerKey;
  idPrefix: string;
  value: LLMRetryRule;
  onChange: (r: LLMRetryRule) => void;
  /** true = 配置抽屜裡的緊湊版：省掉展開說明，只留「什麼錯誤會走到這層」這一句 */
  compact?: boolean;
}) {
  const meta = RETRY_LAYERS[layer];
  const human = humanMs(value.interval_ms);
  return (
    <div className={compact ? "grid gap-2" : "grid gap-3 rounded-lg border p-3"}>
      <div className="grid gap-0.5">
        <div className="flex flex-wrap items-baseline gap-2">
          <Label className="text-sm">{meta.title}</Label>
          <span className="text-muted-foreground text-xs">{meta.where}</span>
        </div>
        {/* 哪些錯誤會走到這層，具體到狀態碼——填了旋鈕卻看不到效果，多半是錯誤壓根不落在這層。 */}
        <p className="text-muted-foreground text-xs">
          <span className="font-medium text-foreground">觸發</span>：{meta.trigger}
        </p>
        {!compact && meta.skips && (
          <p className="text-muted-foreground text-xs">
            <span className="font-medium text-foreground">不走這層</span>：{meta.skips}
          </p>
        )}
        {!compact && <p className="text-muted-foreground text-xs">{meta.desc}</p>}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-n`} className="text-muted-foreground text-xs">
            {meta.attemptsLabel}
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-n`}
            min={-1}
            value={value.attempts}
            placeholder={`預設 ${meta.defAttempts}`}
            onChange={(n) => onChange({ ...value, attempts: n })}
          />
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-ms`} className="text-muted-foreground text-xs">
            間隔 ms
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-ms`}
            min={0}
            value={value.interval_ms}
            placeholder="預設退避"
            onChange={(n) => onChange({ ...value, interval_ms: n })}
          />
          <span className="text-muted-foreground text-xs">{human ? `固定 ${human}` : meta.defInterval}</span>
        </div>
      </div>
      {!compact && <p className="text-muted-foreground text-xs">留空 = 用預設；{meta.offHint}。</p>}
    </div>
  );
}

/** 模型配置抽屜裡的三層覆蓋（跟著端點走的那三層）。 */
export function ProfileRetryFields({
  value,
  onChange,
}: {
  value: LLMRetryOverride;
  onChange: (o: LLMRetryOverride) => void;
}) {
  return (
    <div className="grid gap-3 rounded-lg border p-3">
      <div className="grid gap-0.5">
        <Label className="text-sm">重試覆蓋</Label>
        <p className="text-muted-foreground text-xs">
          只對這個配置生效，覆蓋「重試與退避」裡的全域預設。每格留空 = 跟隨全域；次數填 -1 = 關掉這層重試；
          間隔填了就用固定間隔取代指數退避。熔斷與意圖重跑是程序級的，只能在全域那頁調。
        </p>
      </div>
      {(["connect", "empty", "stream"] as const).map((k) => (
        <div key={k} className="border-t pt-3 first:border-t-0 first:pt-0">
          <RetryRuleFields
            compact
            layer={k}
            idPrefix="pf"
            value={value[k]}
            onChange={(r) => onChange({ ...value, [k]: r })}
          />
        </div>
      ))}
    </div>
  );
}

/** 「重試與退避」tab：五層的全域預設值。 */
export function RetryPolicyPanel() {
  const [policy, setPolicy] = React.useState<LLMRetryPolicy>(ZERO_POLICY);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const p = await api.llmRetryPolicy();
      setPolicy({ ...ZERO_POLICY, ...p });
    } catch (e) {
      toast.error(`讀取重試策略失敗：${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function save() {
    if (saving) return;
    setSaving(true);
    try {
      // 後端會把越界值夾回區間並回傳，直接用回傳值重新整理，所見即所存。
      const saved = await api.saveLLMRetryPolicy(policy);
      setPolicy({ ...ZERO_POLICY, ...saved });
      toast.success("已儲存，即時生效（正在跑的這一輪呼叫仍用舊引數）");
    } catch (e) {
      toast.error(`儲存失敗：${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  const set = (k: LayerKey) => (r: LLMRetryRule) => setPolicy((p) => ({ ...p, [k]: r }));

  if (loading) {
    return (
      <div className="flex items-center gap-2 rounded-lg border border-dashed p-10 text-muted-foreground text-sm">
        <Loader2Icon className="size-4 animate-spin" /> 讀取重試策略…
      </div>
    );
  }

  return (
    <div className="grid gap-4">
      <div className="rounded-lg border bg-muted/30 p-3 text-muted-foreground text-xs leading-relaxed">
        一次模型呼叫的失敗會依次經過五層重試，由內到外：
        <span className="text-foreground"> 建連 → 空回應 → 同 provider 安全視窗 → 輪詢熔斷 → 意圖重跑</span>
        。內層用盡才輪到外層，所以次數是
        <span className="text-foreground">相乘</span>
        的——把每層都拉滿，一次抖動能燒掉幾十次請求。
        全部留空即當前預設值，與沒有這頁時的行為完全一致。前三層可以在每個模型配置裡單獨覆蓋。
      </div>

      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        {(Object.keys(RETRY_LAYERS) as LayerKey[]).map((k) => (
          <RetryRuleFields key={k} layer={k} idPrefix="gl" value={policy[k]} onChange={set(k)} />
        ))}
      </div>

      <div className="flex gap-2">
        <Button onClick={save} disabled={saving}>
          {saving ? <Loader2Icon className="animate-spin" /> : <SaveIcon />}
          儲存
        </Button>
        <Button variant="outline" onClick={() => setPolicy(ZERO_POLICY)} disabled={saving}>
          全部恢復預設
        </Button>
      </div>
    </div>
  );
}
