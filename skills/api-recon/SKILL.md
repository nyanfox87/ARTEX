---
name: api-recon
description: 收集網站API介面時呼叫該skill。
---

# API Recon（前端介面偵察）

在**已授權**前提下，儘可能完整地發現：**後端 API**（路徑、方法、引數、回應體）、**前端路由**、**UI 功能觸發點**（Tab、彈窗、表格操作等）。

---

## 邊界與禁止（Agent 必讀 · 違反即越界）

本 skill **僅做 API / 引數面偵察**，不是漏洞挖掘或滲透利用階段。

### 任務邊界

| 範圍 | 允許 | 禁止 |
|---|---|---|
| **目標** | 列舉 path、method、引數、路由、UI 觸發點 | SQLi/XSS/越權/爆破/fuzz 漏洞、改包攻擊、破壞性操作 |
| **鑑權** | Hook + stub/mock 繞過**客戶端**登入門 | 向使用者索要或猜測帳號密碼；嘗試真實登入表單提交 |
| **執行時** | 無憑據下 hook 介面，用 mock 回應讓 SPA 進入登入後殼層 | 依賴真實後端會話才能繼續的流程 |

### 無憑據動態分析（Phase 3 預設）

1. 通過 `preload.js` / `runtime_harvest.js` **攔截並 stub** 登入、權限、選單等 bootstrap 介面；
2. 對業務查詢介面返回 **結構正確、業務碼成功、資料可為空** 的 mock body；
3. 使前端在無後端或 401 環境下仍能渲染登入後頁面，從而觸發更多 XHR/fetch/WebSocket；
4. **空資料、空白表格、佔位 UI 均屬預期**——勿為此轉向真實登入或漏洞測試。

**一句話**：用 mock 撐開前端路由與元件掛載，**只錄 outbound 請求**；後端返回什麼不重要，重要的是前端**還會發哪些介面**。

### 流程硬禁止

| 禁止 | 替代做法 |
|---|---|
| Phase 1 完成前 grep/curl/Read 主 entry `index-*.js` 提取 API path | 跑 `OUTDIR/harvest_static.py` |
| 手寫 `extract_apis.py` 等替代 harvest 的指令碼 | 改 `OUTDIR/harvest_static.py` 後重跑 |
| 同一 grep/命令失敗 ≥2 次仍重複 | 換策略：讀 tool_logs、改 harvest、查 reference |
| 跳過門禁 A/B，直接跑 `scripts/` 原版 | 複製到 OUTDIR 並按目標改 |
| 真實使用者名稱/密碼、OTP、OAuth 等鑑權 | stub/mock（見上文） |
| 以「拿真實資料」為由跳過 stub，做越權/注入測試 | 只錄 outbound，屬 recon 邊界 |
| 刪除、匯出敏感資料、批次寫等不可逆操作 | coverage 點選亦同 |
| 未完成 runtime + 動態列舉，聲稱已獲全部頁面和介面 | 見「完成定義」或標註侷限 |
| 未完成引數觸發矩陣 + diff，聲稱已掌握全部引數 | Phase 3b 矩陣 + Phase 5 diff |
| 用單一 runtime 樣本推斷必填/可選 | 多樣本 diff 或校驗規則/錯誤反推 |

---

## 兩層模型 + 執行模式

| 層 | 產出 | 上限 |
|---|---|---|
| **靜態**（JS bundle） | 全量 endpoint 路徑、路由草案、組包點欄位候選 | 無 HTTP 方法；引數須 Phase 1b；漏掉執行時拼接 URL |
| **執行時**（活會話） | 方法 + body + 回應 + 動態 URL + WS/SSE；多樣本 diff 補全引數 | 頁面須實際渲染才會發請求；單樣本不足以定必填/可選 |

| 執行模式 | 引擎 | 適用 |
|---|---|---|
| **depth** | `runtime_harvest.js`（Puppeteer） | API 清單、METHOD/params/回應體、WS/SSE、可重現批次跑 |
| **coverage** | browser + `preload.js` | 點 Tab/彈窗/表格，功能點覆蓋更深 |
| **both** | 先 depth 再 coverage | 最完整，耗時最長 |

**引數方法論**（無通用指令碼）：path 用 harvest/正則；引數用 **錨點擴窗 + UI 綁定鏈 + 多樣本 diff + 錯誤反推**（grep 配方見 [reference.md](reference.md) J 節）。

---

## 完成定義

全部滿足方可聲稱 recon 完成：

- [ ] **靜態**：Phase 1 harvest 產出 `api_static.txt`、`routes.txt`、`js/`
- [ ] **執行時**：至少 depth 或 coverage 之一；coverage/both 須 **Hook 生效 + 動態列舉環**
- [ ] **進殼**：訪問業務 path 時非 `/login`（注意 hash 路由）
- [ ] **引數**：coverage/both 完成引數觸發矩陣 + `param_samples.json`；Phase 5 合併 `params_merged.json`
- [ ] **深度**（若模組頁空白）：Phase 4 權限樹還原並重跑，直至出現 **module 級 API**（非僅 locale/bootstrap）
- [ ] **交付**：Phase 5 產出齊全（見 Phase 5 產出表）；`insert_assets` 寫入服務與端點資產

---

## 指令碼與門禁

`scripts/` 僅為參考模板，**禁止**直接跑原版並當最終結果。

**規則**：先讀 → 按目標改 → 寫入 `OUTDIR`（如 `recon/`）→ 記 `CHANGES.md`；不匹配則按方法論重寫，只借結構。

| 門禁 | 何時 | 參考指令碼 → OUTDIR 副本 | 常見必改項 |
|---|---|---|---|
| **A（靜態）** | Phase 0 後、**第一次**跑 harvest/spider 前 | `harvest_static.py` / `spider_mpa.py` | **多數站點預設 regex 可直接跑**；僅 manifest/方言不匹配時改 endpoint 正則、webpack/Vite `publicPath`、MPA exclude/cookie |
| **B（執行時）** | Phase 2 後、跑 depth/coverage 前 | `runtime_harvest.js` / `preload.js` + `config.json` | Cookie/localStorage 鍵、neutralize 成功值、stubs、login 正則、api 字首、hash/history |

**SPA 強制順序**（不可交換；Phase 編號優先於「先探索再指令碼」）：

| 步驟 | 必須 | 禁止 |
|---|---|---|
| Phase 0 完成後 | 下一條 Bash = `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | curl/grep/Read 主 entry `index-*.js`（通常 >500KB） |
| 門禁 A | 複製指令碼 → 按需小改 → **立刻執行** | 先手工提取 API 再決定是否 harvest |
| Phase 1 完成前 | `wc -l` 校驗產出；404 改 harvest 重試 | 手寫 extract 指令碼；對未下載 URL 反覆 grep |
| Phase 1b 起 | grep 僅 `OUTDIR/js/*.js` | 用主 bundle 代替 harvest |

- ✅ 複製 `harvest_static.py` → （可選）改 regex → **立即執行**
- ❌ curl 主 bundle → grep 多次 → 寫臨時 extract → 最後才 harvest
- **MPA**：Phase 0 後下一條 Bash = `python3 OUTDIR/spider_mpa.py ...`

---

## 工具與輸出約束

| 約束 | 說明 |
|---|---|
| 大檔案 | >100KB 的 `index-*.js` **禁止** Read/grep 進上下文；用 OUTDIR 指令碼批處理 |
| grep 輸出 | 必須 `\| head -20` 或 `-m 5`；對話只保留 path 摘要，勿貼 bundle 片段 |
| 校驗 | 用 `wc -l`、`ls \| wc -l`；勿 Read 整目錄 |
| regex 初探 | 可選、≤1 次、僅 ≤50KB 小 chunk 或 HTML；正式靜態以 harvest 為準 |
| reference | 配方/模板/排障見 [reference.md](reference.md)，勿重複 inline 全文 |

---

## 執行路線圖

```
Phase 0 分類 + OUTDIR
  → 門禁 A → Phase 1 harvest（★ 立刻執行 ★）
  → Phase 1b 引數逆向
  → Phase 2 鑑權三道門 → config.json
  → 門禁 B → Phase 3 執行時 + 引數矩陣
  → Phase 4 權限樹（必要時）→ 重跑 Phase 3
  → Phase 5 合併報告 + insert_assets批次插入所有發現的服務、端點api資產，無論如何插入時不允許漏掉已發現的資產
```

按序勾選；**前一項未完成不得進入下一 Phase**。

1. [ ] **Phase 0**：初探 SPA/MPA；建立 `OUTDIR` → [Phase 0](#phase-0--分類)
2. [ ] **門禁 A + Phase 1**：複製指令碼 → **立刻** harvest → `wc -l` 校驗 → [Phase 1](#phase-1--靜態)
3. [ ] **Phase 1b**：錨點擴窗 + 綁定層 → `param_candidates.json` → [Phase 1b](#phase-1b--引數逆向)
4. [ ] **Phase 2**：鑑權三道門 → `config.json` → [Phase 2](#phase-2--鑑權三道門)
5. [ ] **門禁 B**：調整 runtime 指令碼 → [Phase 3](#phase-3--執行時)
6. [ ] **Phase 3**：depth / coverage / both；確認進殼；引數觸發矩陣 → `param_samples.json`
7. [ ] **Phase 4**（若需要）：權限樹 → patch stubs → 重跑 Phase 3 → [Phase 4](#phase-4--權限樹還原)
8. [ ] **Phase 5**：合併產出 + 報告 + `insert_assets` → [Phase 5](#phase-5--合併與報告)

---

## Phase 0 — 分類

拉取入口 HTML，**建立 `OUTDIR`**（勿改 skill 內 `scripts/`）：

- **SPA**：空殼 + `<div id=app>` + chunk → Phase 1–5
- **MPA**：SSR + `<form>`、無 endpoint bundle → 門禁 A 後：

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

產出 `forms.txt`、`links.txt`、`api_inline.txt`。SPA 若 forms ≈ 0 → 切 Phase 1。

---

## Phase 1 — 靜態

遵守 [指令碼與門禁](#指令碼與門禁) · [工具與輸出約束](#工具與輸出約束)。

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

harvest：解析 HTML script → webpack/Vite manifest → 下載全部 lazy chunk → 產出 `js/`、`api_static.txt`、`routes.txt`、`chunkmap.txt`。

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- chunk 數 vs manifest：404 須改 harvest 重試，勿手工 curl 逐個 chunk
- `api_static.txt` 過少 → 放寬 OUTDIR 內 endpoint 正則後重跑（見 reference）

### Phase 1b — 引數逆向

path 來自 Phase 1；引數欄位須單獨 recon。grep 規則見 [工具與輸出約束](#工具與輸出約束)。

**完成標準**：重要介面能答——欄位名、傳輸位置、型別推斷、是否必填、樣本值、置信度。

#### 1b.0 — 傳輸形態

| 形態 | 引數在哪 | 靜態優先看 |
|---|---|---|
| REST JSON | body + query | path 錨點旁 `(params\|data\|body)\s*:\s*\{` |
| GraphQL | `variables` | gql 模板、`$page: Int` |
| 傳統 form | urlencoded | `<form>`、`FormData` |
| 檔案上傳 | multipart | `FormData.append` |
| 路徑引數 | `/user/:id` | 路由表 + `useParams` / `$route.params` |
| 加密/簽名 | 包進 `sign`/`data` | Hook 加密函式入參（reference D 節） |

產出：每介面標註 `transport: query|json|form|graphql|encrypted`。

#### 1b.1 — 錨點擴窗

以已知 path 為錨，擴視窗找組包物件：

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| 包裝層 | 引數線索 |
|---|---|
| axios 例項 | `data` / `params` |
| 統一 request | 攔截器注入全域欄位 |
| OpenAPI 客戶端 | 生成 method 簽名 |
| React Query / SWR | hook 第二引數 |
| Vue composable | composable 入參 |

型別殘留：`yup`/`zod`/rules、`Form.Item name=`、內嵌 Swagger。

→ `param_candidates.json`：`{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — 綁定層

```
Form field → onFinish/handleSubmit → transform → API payload
```

| 綁定源 | 手法 |
|---|---|
| 表單 submit | 跟 submit → transform → API |
| 表格搜尋 | `getFieldsValue()` → `params` |
| 路由 | `:id` / `?tab=` |
| 攔截器 | 全域 `tenantId`、分頁、sign |
| 列舉 select | `options` → API 列舉值 |

DevTools call stack 從 `fetch`/`XHR.send` 往上追組包函式。

#### 1b.3 — 組包三問（≠ Phase 2 鑑權三門）

| 問 | 要答什麼 |
|---|---|
| **組裝** | payload 在哪 build、transform 痕跡 |
| **校驗** | required、pattern、enum |
| **傳輸** | path / query / body / multipart / 頭 |

攔截器門（Phase 2）順帶讀全域注入欄位（Authorization、`X-Tenant-Id`、sign）。

#### 1b.4 — 與 Phase 3 銜接

候選欄位來自靜態/綁定層；**必填/可選/條件依賴**須 Phase 3 引數矩陣 + diff + Phase 5 錯誤反推。

---

## Phase 2 — 鑑權三道門

在 `OUTDIR/js/` grep（帶 `head`），寫入 `config.json`（配方見 reference）：

| 門 | 問題 | 關鍵詞 |
|---|---|---|
| **渲染門** | 如何判斷已登入？ | `isLogin`、`getToken`、Cookie/localStorage |
| **攔截器門** | 什麼觸發跳 `/login`？ | `response_code`、`errno`、axios interceptor |
| **內容門** | 選單/權限從哪來？ | `menu`、`permission`、`role`、`acl`、`routes` |

禁止把 localStorage 鍵名當憑據——須從 chunk/請求鏈確認。

**出口 = 門禁 B**：結論落到 `config.json`，並改 `OUTDIR/runtime_harvest.js` / `preload.js`。

### Phase 2b — API 觀察（可選）

用 OUTDIR 內 `preload.js` 確認會話鍵名、Authorization、巢狀 API URL：

| 配置 | 產出 |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | headers 觀察 |
| `extractUrlsFromResponse: true` | 回應內子 API |
| `observe.storageReads/cookieReads: true` | 回填 config |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

coverage 每輪匯出：`__API_RECON_LOG__`、`__API_RECON_DETAIL__`、`__API_RECON_ROUTES__`、`__API_RECON_OBSERVE__`。

---

## Phase 3 — 執行時

須已過門禁 B；遵守 [邊界與禁止](#邊界與禁止agent-必讀--違反即越界) · 無憑據 mock 策略。

`config.json` 設定 `"runtimeMode": "depth" | "coverage" | "both"`（模板見 reference）。

### Hook 與 stub（depth + coverage 共用）

| 層 | 範圍 | 目的 |
|---|---|---|
| L1 精確 | auth/權限/bootstrap stub | 過首屏鑑權 |
| L2 負向修正 | 所有 JSON 回應 | 未登入碼 → 成功 |
| L3 兜底 | 未命中 L1 的 `/api` 等 | 空成功體，撐開 UI |

- **depth**：fake auth + `forward` 改業務碼 + `stubs`；遍歷 `routes`（hash/history）；產出 `runtime_api.json`
- **coverage**：**document-start** 注入 `preload.js`（CDP `addScriptToEvaluateOnNewDocument` 或 Userscript）

驗證：`window.__API_RECON_PRELOAD__` 存在；業務 path 不回 `/login`。

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — coverage 動態列舉（必做）

1. 主導航/側欄 — 每項點選，等網路 1–3s
2. Tab — `role=tab`、`.ant-tabs-tab`
3. 表格 — 首行檢視/編輯/詳情
4. 工具欄 — 匯出、篩選、新建（**避免不可逆刪除**）
5. 每進模組 — 合併 API/路由
6. SPA — 對 `routes.txt` 未覆蓋 path 受控 `pushState`（MPA 禁止）

**引數觸發矩陣**（必做）：每模組按操作型別各錄一次，**diff 多樣本**：

| 操作 | 通常多出的引數 |
|---|---|
| 列表首屏 | 分頁 + 預設篩選 |
| 點搜尋 | keyword、filter |
| 高階篩選 | 更多 optional |
| 新建/編輯 | 完整 entity |
| 批次/匯出/排序 | `ids[]`、`exportType`、`sortField` |

**stub 下 outbound body/headers 仍真實**——以請求為準。錄製 → `scan_raw.json`、`param_samples.json`、`api_detail.json`。

- **Vue**：`neutralizeVueRouter: true` + document-start preload
- **React**：`routes.txt` + 側欄點選 + `pushState`
- **both**：先 3a depth，再 3b coverage

---

## Phase 4 — 權限樹還原

**觸發**：模組頁空白 / 每路由僅 bootstrap（如 locale）→ 內容門未過。

| 現象 | 含義 |
|---|---|
| 進殼成功 | 渲染門 + 攔截器門已過 |
| 側欄缺項/點選空白 | stub shape 或權限碼不全 |
| 每路由 API 相同且極少 | `v-if permission` 未通過 |
| `routes.txt` 遠少於 bundle | 須從 auth 模組補全 |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

典型鏈：`role_permissions`（flat codes）+ `permissions/all`（tree）→ `getResultTree` → `userRouteAuth[CODE].url`。

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

中間產出：`route_map.json`、`userRouteAuth.json`、`permissions_tree.json`、`*_stub.json`、`perm_codes_all.txt`。

stub 檢查：外層 `response_code` 與攔截器門一致；flat codes 與 tree 對齊；`routes` 覆蓋 `route_map` 全部 link。

更新 `config.json` 後**重跑 Phase 3**。大型 SPA 可調 `waitUntil`、`routeTimeout`、`perRouteMs`（見 reference A3/I 節）。

---

## Phase 5 — 合併與報告

### 產出表

| 檔案 | 階段 | 內容 |
|---|---|---|
| `js/`、`api_static.txt`、`routes.txt`、`chunkmap.txt` | 1 | 靜態 bundle 與 path |
| `param_candidates.json` | 1b | 靜態引數欄位候選 |
| `config.json` | 2 | 三道門 + runtime 配置 |
| `runtime_api.json` | 3a | depth 詳細錄製（含 WS/SSE） |
| `param_samples.json`、`scan_raw.json`、`api_detail.json` | 3b | 多樣本、點選日誌、detail |
| `route_map.json` 等 | 4 | 權限樹中間檔案（若執行） |
| `params_merged.json` | 5 | 合併引數欄位 + 置信度 |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | 路由、API、params、功能點、侷限 |
| **insert_assets** | 5 | 將所有服務、端點資產寫入資產庫 |

### 5b — 引數合併

從 `param_samples.json` diff，**無通用合併指令碼**。置信度規則見 reference J7（高/中/低/待觸發）。

### 5c — 錯誤反推

授權範圍內可發不完整請求讀 400（**屬引數 recon，非漏洞測試**）：`field 'x' is required`、列舉錯誤等。注意 `data` 包裝、`variables`、加密前 `bizData`。

報告須註明：runtimeMode、靜態/執行時 API 數、引數置信度、未覆蓋模組、相對參考指令碼的 `CHANGES.md` 摘要。

`site_map.json` 建議結構：

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

更多欄位與 grep 配方見 [reference.md](reference.md)。

---

## 通用說明

- **框架無關**：webpack/Vite/Angular lazy load 方法相同
- **傳輸**：REST/JSON、GraphQL、WebSocket、SSE；gRPC-web 不在範圍
- **SSR**：客戶端 fetch 可錄；RSC/Server Actions 不完全可列舉
- **盲區**：JSVMP、WASM、HMAC/mTLS 強校驗 → 靜態 + 標註侷限
- **引數盲區**：條件聯動、hidden params、WASM 組包 → 「待觸發」/「不可達」
- **靜態是安全網**：runtime 被擋時靜態仍能列舉 endpoint

---

## 附加資源

- Grep 配方、`config.json` 模板、排障、Hook、引數逆向 J 節、site_map 模板：**[reference.md](reference.md)**
- 參考指令碼路徑見 [指令碼與門禁](#指令碼與門禁) 表
