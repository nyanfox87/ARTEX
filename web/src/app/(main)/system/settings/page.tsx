"use client";

import * as React from "react";

import { CpuIcon, FlaskConicalIcon, KeyboardIcon, RadioTowerIcon, SearchIcon, ShieldAlertIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { CHAT_SEND_MODE_OPTIONS, type ChatSendMode, setChatSendMode, useChatSendMode } from "@/lib/chat-send-mode";
import type { Settings } from "@/lib/types";

import { UpdateCard } from "./_components/update-card";
import { BasicAuthCard } from "./_components/basic-auth-card";

export default function SystemSettingsPage() {
  const [trafficCapture, setTrafficCapture] = React.useState(false);
  const [agentTrafficBinding, setAgentTrafficBinding] = React.useState(false);
  const [webSearch, setWebSearch] = React.useState(false);
  const [backend, setBackend] = React.useState("ddgs");
  const [braveKeySet, setBraveKeySet] = React.useState(false);
  const [braveKeyInput, setBraveKeyInput] = React.useState("");
  const [tavilyKeySet, setTavilyKeySet] = React.useState(false);
  const [tavilyKeyInput, setTavilyKeyInput] = React.useState("");
  const [savingTavilyKey, setSavingTavilyKey] = React.useState(false);
  const [proxyInput, setProxyInput] = React.useState("");
  const [savingProxy, setSavingProxy] = React.useState(false);
  const [globalProxyInput, setGlobalProxyInput] = React.useState("");
  const [savingGlobalProxy, setSavingGlobalProxy] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [savingKey, setSavingKey] = React.useState(false);
  const [pyInterp, setPyInterp] = React.useState("");
  const [workers, setWorkers] = React.useState("3");
  const [savingWorkers, setSavingWorkers] = React.useState(false);
  // 操作約束注入範圍(預設都開)。
  const [injectPlanner, setInjectPlanner] = React.useState(true);
  const [injectWorker, setInjectWorker] = React.useState(true);
  // 實驗功能:noa 上下文壓縮(預設關)。
  const [noaCompaction, setNoaCompaction] = React.useState(false);
  // 純前端偏好：不走 /api/settings，直接讀寫 localStorage。
  const sendMode = useChatSendMode();

  const apply = React.useCallback((s: Settings) => {
    setTrafficCapture(!!s.traffic_capture);
    setAgentTrafficBinding(!!s.agent_traffic_binding);
    setWebSearch(!!s.web_search_enabled);
    setBackend(s.web_search_backend || "ddgs");
    setBraveKeySet(!!s.brave_key_set);
    setTavilyKeySet(!!s.tavily_key_set);
    setProxyInput(s.web_search_proxy ?? "");
    setGlobalProxyInput(s.global_proxy ?? "");
    setPyInterp(s.python_interpreter ?? "");
    setWorkers(String(s.workers ?? 3));
    setInjectPlanner(s.constraints_inject_planner !== false);
    setInjectWorker(s.constraints_inject_worker !== false);
    setNoaCompaction(!!s.noa_compaction);
  }, []);

  const saveWorkers = () => {
    const n = Number(workers);
    if (!Number.isInteger(n) || n <= 0) {
      toast.error("併發數必須是大於 0 的整數");
      return;
    }
    setSavingWorkers(true);
    api
      .setSettings({ workers: n })
      .then((s) => {
        apply(s);
        toast.success("已儲存併發工作 agent 數（對之後啟動的任務生效）");
      })
      .catch((e) => toast.error("儲存失敗：" + (e as Error).message))
      .finally(() => setSavingWorkers(false));
  };

  const savePython = () => {
    setSaving(true);
    api
      .setSettings({ python_interpreter: pyInterp.trim() })
      .then((s) => {
        apply(s);
        toast.success("已儲存 Python 直譯器配置");
      })
      .catch((e) => toast.error("儲存失敗：" + (e as Error).message))
      .finally(() => setSaving(false));
  };
  const detectPython = () => {
    setSaving(true);
    api
      .detectPython()
      .then((r) => setPyInterp(r.python_interpreter))
      .catch(() => undefined)
      .finally(() => setSaving(false));
  };

  React.useEffect(() => {
    api
      .settings()
      .then(apply)
      .catch(() => undefined)
      .finally(() => setLoaded(true));
  }, [apply]);

  const toggleTraffic = (v: boolean) => {
    setTrafficCapture(v); // optimistic
    setSaving(true);
    api
      .setSettings({ traffic_capture: v })
      .then(apply)
      .catch(() => setTrafficCapture(!v)) // revert on failure
      .finally(() => setSaving(false));
  };

  const toggleInjectPlanner = (v: boolean) => {
    setInjectPlanner(v); // optimistic
    api
      .setSettings({ constraints_inject_planner: v })
      .then(apply)
      .catch(() => setInjectPlanner(!v)); // revert on failure
  };

  const toggleAgentTrafficBinding = (v: boolean) => {
    setAgentTrafficBinding(v);
    setSaving(true);
    api
      .setSettings({ agent_traffic_binding: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "已開啟 Agent 自動綁定流量" : "已關閉 Agent 自動綁定流量");
      })
      .catch((e) => {
        setAgentTrafficBinding(!v);
        toast.error(`儲存失敗：${(e as Error).message}`);
      })
      .finally(() => setSaving(false));
  };

  const toggleInjectWorker = (v: boolean) => {
    setInjectWorker(v); // optimistic
    api
      .setSettings({ constraints_inject_worker: v })
      .then(apply)
      .catch(() => setInjectWorker(!v)); // revert on failure
  };

  const toggleNoaCompaction = (v: boolean) => {
    setNoaCompaction(v); // optimistic
    api
      .setSettings({ noa_compaction: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "已開啟 noa 上下文壓縮（對之後啟動的執行生效）" : "已關閉 noa 上下文壓縮（恢復內建壓縮）");
      })
      .catch((e) => {
        setNoaCompaction(!v); // revert on failure
        toast.error(`儲存失敗：${(e as Error).message}`);
      });
  };

  // Persist a web-search patch (enable and/or backend). Optimistic with refetch.
  const saveWebSearch = (patch: Partial<Settings>) => {
    setSaving(true);
    api
      .setSettings(patch)
      .then((s) => {
        apply(s);
        toast.success("已儲存網路搜尋配置");
      })
      .catch((e) => {
        toast.error("儲存失敗：" + (e as Error).message);
        api
          .settings()
          .then(apply)
          .catch(() => undefined);
      })
      .finally(() => setSaving(false));
  };

  const saveBraveKey = () => {
    setSavingKey(true);
    api
      .setSettings({ brave_search_api_key: braveKeyInput })
      .then((s) => {
        apply(s);
        setBraveKeyInput("");
        toast.success("已儲存 Brave API Key");
      })
      .catch((e) => toast.error("儲存失敗：" + (e as Error).message))
      .finally(() => setSavingKey(false));
  };

  const saveTavilyKey = () => {
    setSavingTavilyKey(true);
    api
      .setSettings({ tavily_search_api_key: tavilyKeyInput })
      .then((s) => {
        apply(s);
        setTavilyKeyInput("");
        toast.success("已儲存 Tavily API Key");
      })
      .catch((e) => toast.error("儲存失敗：" + (e as Error).message))
      .finally(() => setSavingTavilyKey(false));
  };

  const saveProxy = () => {
    setSavingProxy(true);
    api
      .setSettings({ web_search_proxy: proxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(proxyInput.trim() ? "已儲存出口代理" : "已清除出口代理（改為直連）");
      })
      .catch((e) => toast.error("儲存失敗：" + (e as Error).message))
      .finally(() => setSavingProxy(false));
  };

  const saveGlobalProxy = () => {
    setSavingGlobalProxy(true);
    api
      .setSettings({ global_proxy: globalProxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(globalProxyInput.trim() ? "已儲存全域代理" : "已清除全域代理（改為直連）");
      })
      .catch((e) => toast.error("儲存失敗：" + (e as Error).message))
      .finally(() => setSavingGlobalProxy(false));
  };

  // Run a real "test" search ("test") against the CURRENT form values (backend +
  // proxy + entered key), falling back to saved values server-side. Toasts result.
  const runTest = () => {
    setTesting(true);
    api
      .testWebSearch({
        web_search_backend: backend,
        web_search_proxy: proxyInput.trim(),
        brave_search_api_key: braveKeyInput,
        tavily_search_api_key: tavilyKeyInput,
      })
      .then((r) => {
        if (r.ok) toast.success(`搜尋測試成功 · ${r.backend} 返回 ${r.count} 條結果`);
        else toast.error("搜尋測試失敗：" + (r.error || "未知錯誤"));
      })
      .catch((e) => toast.error("搜尋測試失敗：" + (e as Error).message))
      .finally(() => setTesting(false));
  };

  // brave-free selected but no key stored and none being entered → tool stays off.
  const braveNeedsKey = webSearch && backend === "brave-free" && !braveKeySet;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">系統配置</h1>
        <p className="text-muted-foreground text-sm">全域執行時開關</p>
      </div>

      {/* 多列而非 grid：網路搜尋卡片比其餘高數倍，且高度隨所選後端變化（brave/tavily
          的 key 輸入是條件渲染）。grid 會按最高的一張撐滿整行、在旁邊留下大片空白，
          多列則自動按內容高度平衡填充。卡片間距靠 mb 而非 gap——多列布局下
          column-gap 只管列間距，行間距要由子元素自己給。 */}
      <div className="columns-1 gap-4 md:gap-6 lg:columns-2">
        <UpdateCard />
        <BasicAuthCard />

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              流量捕獲
            </CardTitle>
            <CardDescription>
              開啟後，所有 Agent 的 HTTP 流量經記錄代理全量落庫，並向 Agent 注入 traffic_search / traffic_get
              工具與代理配置（提示詞含代理說明）。
              <br />
              關閉（預設）時不記錄任何流量：Agent
              <b>不會</b>拿到代理配置與流量工具，提示詞也<b>不含</b>代理相關內容。切換後會即時重建 Agent 生效。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="traffic-capture" className="text-sm font-normal text-muted-foreground">
              {trafficCapture ? "已開啟 · 正在記錄流量並注入代理" : "已關閉 · 不記錄、不注入代理"}
            </Label>
            <Switch
              id="traffic-capture"
              checked={trafficCapture}
              disabled={!loaded || saving}
              onCheckedChange={toggleTraffic}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              Agent 自動綁定流量
            </CardTitle>
            <CardDescription id="agent-traffic-binding-description">
              預設關閉。開啟後，漏洞入庫時觸發的報告 Agent 會核對已有 HTTP 請求/回應，關聯對應流量後再編寫報告。
              <b>查閱資料包及額外的工具呼叫會增加 Token 消耗。</b>
              <br />
              TCP、未抓包或沒有匹配流量時仍可正常上報。此開關不影響流量捕獲、人工綁定及已儲存證據的檢視。 對下一輪 Agent
              生效；關閉後會立即拒絕新的自動綁定。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="agent-traffic-binding" className="text-sm font-normal text-muted-foreground">
              {agentTrafficBinding ? "已開啟 · 會增加 Token 消耗" : "已關閉 · 可繼續人工綁定"}
            </Label>
            <Switch
              id="agent-traffic-binding"
              aria-describedby="agent-traffic-binding-description"
              checked={agentTrafficBinding}
              disabled={!loaded || saving}
              onCheckedChange={toggleAgentTrafficBinding}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              全域代理
            </CardTitle>
            <CardDescription>
              所有 Agent 的<b>目標流量</b>經此代理出網（隱藏源 IP / 走跳板）。支援 <b>http / https / socks5</b>，可帶{" "}
              <code>user:pass</code> 認證。留空=直連。
              <br />
              開啟<b>流量捕獲</b>時，它作為記錄代理的<b>上游</b>（流量仍全量落庫，再經此代理出網）；關閉捕獲時，直接注入
              Agent 的 bash / WebFetch 出網。與網路搜尋代理、LLM 代理相互獨立。
              <br />
              <b>提示</b>：socks5 在<b>關閉捕獲</b>時依賴各命令列工具對 <code>ALL_PROXY</code> 的支援（curl
              可用，部分工具可能忽略）； 若主要用 socks5，建議開啟流量捕獲——此路徑由 MITM
              親自撥號，工具無感知、穩定生效。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            <Label htmlFor="global-proxy" className="text-sm font-normal text-muted-foreground">
              代理地址
            </Label>
            <div className="flex items-center gap-2">
              <Input
                id="global-proxy"
                autoComplete="off"
                placeholder="socks5://user:pass@host:1080 或 http://host:port（留空=直連）"
                value={globalProxyInput}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setGlobalProxyInput(e.target.value)}
              />
              <Button type="button" onClick={saveGlobalProxy} disabled={!loaded || savingGlobalProxy}>
                儲存
              </Button>
            </div>
            <p className="text-muted-foreground text-xs">
              {globalProxyInput.trim() ? "已配置 · 所有目標流量經此代理出網" : "未配置 · 目標流量直連出網"}
            </p>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <ShieldAlertIcon className="size-4" />
              操作約束注入
            </CardTitle>
            <CardDescription>
              開啟後，把每個任務的<b>操作約束</b>（在任務總覽「操作約束」裡維護的 allow/deny 條目）拼進對應 Agent
              的系統提示，用來框定探索邊界（如「僅測當前埠」「禁止爆破」）。
              <br />
              可分別控制注入到 <b>規劃者（planner）</b>與 <b>執行者（worker）</b>
              ；預設都開。切換即時生效（下一輪讀取），無需重建 Agent。關閉後該 Agent 不再看到約束。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-planner" className="text-sm font-normal text-muted-foreground">
                注入規劃者（planner）{injectPlanner ? " · 已開啟" : " · 已關閉"}
              </Label>
              <Switch
                id="inject-planner"
                checked={injectPlanner}
                disabled={!loaded}
                onCheckedChange={toggleInjectPlanner}
              />
            </div>
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-worker" className="text-sm font-normal text-muted-foreground">
                注入執行者（worker）{injectWorker ? " · 已開啟" : " · 已關閉"}
              </Label>
              <Switch
                id="inject-worker"
                checked={injectWorker}
                disabled={!loaded}
                onCheckedChange={toggleInjectWorker}
              />
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <FlaskConicalIcon className="size-4" />
              實驗功能
            </CardTitle>
            <CardDescription>
              尚在驗證中的機制，預設關閉。可能改變 Agent 行為或影響穩定性，請在瞭解影響後啟用。
              <br />
              <b>noa 上下文壓縮</b>：由模型主動壓縮長對話歷史（norma v0.4.0）。開啟後平臺接入的四類 Agent（
              <b>規劃者 / 執行者 / 主 Agent / 對話</b>）改用 noa 接管上下文，取代內建壓縮，
              壓縮原文會歸檔到任務工作目錄下便於回溯。切換即時生效（對之後啟動的執行生效），無需重建 Agent；
              關閉後立即恢復內建壓縮。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="noa-compaction" className="text-sm font-normal text-muted-foreground">
              noa 上下文壓縮{noaCompaction ? " · 已開啟" : " · 已關閉"}
            </Label>
            <Switch
              id="noa-compaction"
              checked={noaCompaction}
              disabled={!loaded}
              onCheckedChange={toggleNoaCompaction}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <SearchIcon className="size-4" />
              網路搜尋
            </CardTitle>
            <CardDescription>
              這是網路搜尋的<b>總開關 + 來源配置</b>。開啟後，才能在<b>每個 Agent 的配置</b>裡單獨選擇是否啟用
              <b>web_search</b>（僅返回標題/連結/摘要，不抓取正文；抓取由 WebFetch 負責）。網路搜尋<b>不走</b>
              記錄代理，獨立於流量捕獲。
              <br />
              來源可選 <b>DuckDuckGo（ddgs）</b>（無需 Key）、<b>Brave（免費版）</b>（需填寫 Brave API Key）、{" "}
              <b>Tavily</b>（需填寫 Tavily API Key）或 <b>DeepSeek</b>（複用當前 LLM 配置）。總開關關閉時，各
              Agent 的網路搜尋開關不可用。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="web-search" className="text-sm font-normal text-muted-foreground">
                {webSearch ? "總開關已開啟 · 可在各 Agent 配置裡單獨啟用" : "已關閉 · 各 Agent 無法啟用網路搜尋"}
              </Label>
              <Switch
                id="web-search"
                checked={webSearch}
                disabled={!loaded || saving}
                onCheckedChange={(v) => {
                  setWebSearch(v); // optimistic
                  saveWebSearch({ web_search_enabled: v });
                }}
              />
            </div>

            {webSearch && (
              <div className="flex items-center justify-between gap-4">
                <Label className="text-sm font-normal text-muted-foreground">搜尋來源</Label>
                <Select
                  value={backend}
                  disabled={!loaded || saving}
                  onValueChange={(v) => {
                    setBackend(v); // optimistic
                    saveWebSearch({ web_search_backend: v });
                  }}
                >
                  <SelectTrigger className="w-48 shrink-0">
                    <SelectValue placeholder="選擇來源" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="ddgs">DuckDuckGo（ddgs · 免費無 Key）</SelectItem>
                    <SelectItem value="brave-free">Brave（免費版 · 需 Key）</SelectItem>
                    <SelectItem value="tavily">Tavily（需 Key）</SelectItem>
                    <SelectItem value="deepseek">DeepSeek（官方）</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            )}

            {webSearch && backend === "deepseek" && (
              <div className="border-border/60 bg-muted/30 flex flex-col gap-2 rounded-md border p-3">
                <p className="text-sm font-medium">DeepSeek 官方聯網搜尋</p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  該來源直接複用<b>當前啟用的 LLM 配置</b>。因此它
                  <b>僅支援 DeepSeek 官方模型</b>，且該配置<b>必須使用 anthropic 協議</b>
                  ——DeepSeek 的 OpenAI 協議端點不支援服務端搜尋。切換 LLM 配置後此來源可能失效。
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  與其它來源不同，搜尋由 <b>DeepSeek 服務端執行</b>：每次搜尋會額外消耗一次模型呼叫（產生 Token
                  費用），搜尋請求<b>不經過上面的出口代理</b>，也<b>不計入流量留痕</b>；返回結果<b>只有標題和連結</b>
                  （無摘要），需要正文時由 WebFetch 抓取。
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  是否滿足上述條件由你自行確認，系統不做攔截；可用下方「測試搜尋」按鈕實際跑一次來驗證。
                </p>
              </div>
            )}

            {webSearch && backend === "brave-free" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="brave-key" className="text-sm font-normal text-muted-foreground">
                  Brave Search API Key
                  {braveKeySet && <span className="ml-2 text-xs text-emerald-500">已配置</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="brave-key"
                    type="password"
                    autoComplete="off"
                    placeholder={braveKeySet ? "已配置（留空則不變）" : "輸入 Brave API Key"}
                    value={braveKeyInput}
                    disabled={!loaded || savingKey}
                    onChange={(e) => setBraveKeyInput(e.target.value)}
                  />
                  <Button
                    type="button"
                    onClick={saveBraveKey}
                    disabled={!loaded || savingKey || braveKeyInput.trim() === ""}
                  >
                    儲存
                  </Button>
                </div>
                {braveNeedsKey && (
                  <p className="text-xs text-amber-500">
                    已選擇 Brave 但尚未配置 Key —— 在儲存 Key 之前，搜尋工具不會啟用。
                  </p>
                )}
                <p className="text-muted-foreground text-xs">
                  免費版額度約 2,000 次/月。前往 https://brave.com/search/api/ 獲取 Key。
                </p>
              </div>
            )}

            {webSearch && backend === "tavily" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="tavily-key" className="text-sm font-normal text-muted-foreground">
                  Tavily Search API Key
                  {tavilyKeySet && <span className="ml-2 text-xs text-emerald-500">已配置</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="tavily-key"
                    type="password"
                    autoComplete="off"
                    placeholder={tavilyKeySet ? "已配置（留空則不變）" : "輸入 Tavily API Key（tvly-…）"}
                    value={tavilyKeyInput}
                    disabled={!loaded || savingTavilyKey}
                    onChange={(e) => setTavilyKeyInput(e.target.value)}
                  />
                  <Button
                    type="button"
                    onClick={saveTavilyKey}
                    disabled={!loaded || savingTavilyKey || tavilyKeyInput.trim() === ""}
                  >
                    儲存
                  </Button>
                </div>
                {webSearch && backend === "tavily" && !tavilyKeySet && (
                  <p className="text-xs text-amber-500">
                    已選擇 Tavily 但尚未配置 Key —— 在儲存 Key 之前，搜尋工具不會啟用。
                  </p>
                )}
                <p className="text-muted-foreground text-xs">前往 https://tavily.com 註冊並獲取 API Key。</p>
              </div>
            )}

            {webSearch && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="ws-proxy" className="text-sm font-normal text-muted-foreground">
                  出口代理（可選）
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="ws-proxy"
                    autoComplete="off"
                    placeholder="http://host:port 或 socks5://host:port（留空=直連）"
                    value={proxyInput}
                    disabled={!loaded || savingProxy}
                    onChange={(e) => setProxyInput(e.target.value)}
                  />
                  <Button type="button" onClick={saveProxy} disabled={!loaded || savingProxy}>
                    儲存
                  </Button>
                </div>
                <p className="text-muted-foreground text-xs">
                  獨立出口代理，僅用於訪問搜尋端點（VPN/SOCKS 等）。與記錄流量的 MITM 代理無關；網路不通時經此代理訪問。
                </p>
              </div>
            )}

            {webSearch && (
              <div className="flex items-center justify-between gap-4 border-t pt-4">
                <p className="text-muted-foreground text-xs">
                  用當前配置（來源 + 代理 + Key）實際搜尋一次「test」，驗證是否可用。
                </p>
                <Button
                  type="button"
                  variant="outline"
                  onClick={runTest}
                  disabled={!loaded || testing}
                  className="shrink-0"
                >
                  {testing ? "測試中…" : "測試搜尋"}
                </Button>
              </div>
            )}
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              自定義指令碼 · Python 直譯器
            </CardTitle>
            <CardDescription>
              自定義 <b>script</b> 型別工具用它跑 Python。開機會自動檢測（python3 優先）；此處可手填 venv /
              特定版本的絕對路徑，留空則執行時自動檢測。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                className="font-mono text-sm"
                placeholder="/usr/bin/python3（留空=自動檢測）"
                value={pyInterp}
                disabled={!loaded || saving}
                onChange={(e) => setPyInterp(e.target.value)}
              />
              <Button variant="outline" onClick={detectPython} disabled={!loaded || saving}>
                重新檢測
              </Button>
              <Button onClick={savePython} disabled={!loaded || saving}>
                儲存
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <CpuIcon className="size-4" />
              工作併發 · Work Agent 數
            </CardTitle>
            <CardDescription>
              每個任務併發執行的工作 agent 數量（預設 3）。數值越大併發探測越多、消耗也越高。修改後
              <b>對之後啟動的任務生效</b>，正在執行的任務不受影響。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={1}
                className="w-32 font-mono text-sm"
                placeholder="3"
                value={workers}
                disabled={!loaded || savingWorkers}
                onChange={(e) => setWorkers(e.target.value)}
              />
              <Button onClick={saveWorkers} disabled={!loaded || savingWorkers}>
                儲存
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <KeyboardIcon className="size-4" />
              會話輸入框傳送鍵位
            </CardTitle>
            <CardDescription>
              對話頁與任務詳情的主 Agent 會話輸入框共用此設定，選擇後立即生效、無需儲存。
              <br />
              該偏好<b>只存在本瀏覽器</b>，不隨帳號同步，換瀏覽器或清理站點資料後需重新設定。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="chat-send-mode" className="text-sm font-normal text-muted-foreground">
              傳送方式
            </Label>
            <Select value={sendMode} onValueChange={(v) => setChatSendMode(v as ChatSendMode)}>
              <SelectTrigger id="chat-send-mode" className="w-72">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CHAT_SEND_MODE_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
