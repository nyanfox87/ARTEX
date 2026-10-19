# api-recon — 參考手冊

Grep 配方、`config.json` 模板與排障。所有 grep 針對 `js/` 目錄執行。bundle 單行時可先 `js-beautify` 或 `sed 's/}/}\n/g'`，通常帶上下文視窗的 raw grep 即可。

## 指令碼說明

`scripts/` 內所有檔案均為**參考模板**，執行前必須按目標站點調整。典型改動點：

| 指令碼 | 常見需調整項 |
|---|---|
| `harvest_static.py` | endpoint 正則、webpack/Vite manifest 解析、微前端 publicPath、重試/併發 |
| `runtime_harvest.js` | neutralize 欄位名與成功值、stub 匹配規則與 body 結構、routes 來源、WS 錄製、`waitUntil`/`routeTimeout`/`proxy` |
| `preload.js` | `loginPathRe`、L1 stubs、`neutralize.fields`、`apiPattern`、是否啟用 L3、`recordDetail`、`observe.*`、`neutralizeVueRouter` |
| `spider_mpa.py` | `--exclude` 破壞性連結、cookie、depth/max、同域過濾 |
| `extract_route_map.py` | `routeMap` / `routeLink` 正則、KEY 命名模式 |
| `build_perm_tree.py` | `userRouteAuth` 解析、`ROOTS`/`PREFIX_PARENT` 層級啟發式、stub 外層欄位名 |
| `config.json` | 以上全部站點專屬引數的統一入口 |

調整後的檔案建議放在任務工作目錄（如 `recon/`），報告中註明相對參考指令碼的具體改動。

---

## A. 逆向三道門

### A1. 渲染門 — 「如何判斷已登入？」

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

找鏈路 `isLogin = f(getUser())` → `getUser = decode(storage.read(KEY))`，確定 **儲存鍵**、**容器**（Cookie vs localStorage）、**編碼**：

| 編碼 | config 偽造方式 |
|---|---|
| 明文字串 / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | 無簽名/`alg:none` JWT，或 bundle 內金鑰簽名 |
| 加密（SM2/AES/RSA） | 找硬編碼金鑰；渲染門僅需可解碼 blob 時可 forge；否則靜態兜底 |

→ 寫入 `cookies` / `localStorage`。

### A2. 攔截器門 — 「什麼觸發跳 /login？」

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(未登入|請重新登入|登入已過期|unauthorized|登入失效|授權|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

確定：**欄位名**、**成功值**（通常 `0` 或 `200`）、**觸發跳轉的失敗值**。用 junk session 驗證：

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

→ 寫入 `neutralize.fields` + `neutralize.success`。

### A3. 內容門 — 「選單/權限從哪來？」

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

**兩層資料**（常見企業後臺）：

| API | 典型 payload | 消費方 |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | 路由守衛、按鈕級 ACL |
| `.../permissions/all` | `tree[{ code, position, children }]` | 側欄選單渲染 |
| bundle 內 `userRouteAuth` | `{ CODE: { url, name? } }` | code → 前端 path |
| bundle 內 `routeMap` | `{ KEY: { name, link } }` | 別名解析（webpack `o.DASHBOARD`） |

讀消費方程式碼確認：`getResultTree(tree, permissions)` 如何過濾、`v-if` / `hasAuth(code)` 檢查哪個欄位。

**手工 forge**（小站點）：構建 permissive payload → `stubs`。

**完整權限樹還原**（大站點，側欄/子模組仍空白）：見 **I 節**。

---

## B. config.json 模板

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "/usr/bin/chromium",

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

欄位說明：
- `runtimeMode`：`depth`（Puppeteer）、`coverage`（browser MCP）、`both`
- `cookies[].value` 字首：`b64json:` → base64(JSON)；`json:` → 原始 JSON；無字首 → 字面量
- `forward: true` 轉發真實請求並改寫碼欄位；`false` 完全離線 stub
- `mockTier`：coverage 模式 preload 啟用層級，如 `L1+L2`、`L1+L2+L3`
- `routes` 來自 `routes.txt`；forge 選單後 harness 自動追加 `<a href>`
- `captureResponses` / `recordWs` 僅 depth 模式有效
- `waitUntil`：大型 SPA 用 `domcontentloaded`，避免 `networkidle2` 掛起
- `routeTimeout`：單路由 `page.goto` 超時（毫秒）
- `proxy`：Puppeteer `--proxy-server`；也可設 `HTTP_PROXY` / `HTTPS_PROXY`

### B1. 雙 stub 模板（role_permissions + permissions/all）

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

外層欄位名（`response_code` / `code` / `data`）須與 A2 攔截器門一致；`permissions` 須覆蓋 tree 中所有 leaf code。

---

## C. coverage 模式：preload 配置

編輯 `scripts/preload.js` 頂部 `CONFIG` 物件，或通過 CDP 注入前替換：

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* 同 config.json stubs */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

驗證：`window.__API_RECON_PRELOAD__ === true` 且 pathname 穩定。

匯出錄製結果：

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. preload / runtime Hook 能力

preload（coverage）與 runtime_harvest（depth）內建的瀏覽器 Hook 能力及覆蓋範圍：

| Hook 能力 | 對 API 發現的價值 | 覆蓋 |
|---|---|---|
| Hook fetch / XHR.open | 錄請求 URL/方法 | ✅ `recordDetail` + `__API_RECON_LOG__` |
| Hook XHR.setRequestHeader | 發現 Authorization 等頭 | ✅ `observe.xhrHeaders` |
| Hook localStorage/cookie 讀 | 確認會話鍵名 | ⚠️ 可選 `observe.storageReads/cookieReads` |
| Vue 獲取路由 | 補全 frontendRoutes | ✅ `__API_RECON_ROUTES__`（已載入路由） |
| Vue 路由守衛中和 / 登入跳轉阻斷 | 撐開模組觸發 API | ✅ `neutralizeVueRouter` + 原生跳轉中和 |
| React 獲取路由 | 補路由 | ⚠️ 靜態 + 點選；無專用 Hook |
| 頁面跳轉阻斷（登入 path） | 留頁分析 | ⚠️ 僅阻斷登入 path，避免擋業務導航 |
| Hook 加密庫（CryptoJS/SM 等） | 加密引數 → 明文 API body | ❌ 須手工 Hook 加密函式入參；結論寫 config |
| 反除錯 bypass | 否則 runtime 錄不到 API | ❌ 須手工處理；靜態仍可用 |

---

## E. Endpoint 提取正則（靜態過少時）

在 `harvest_static.py` 的 `extract_endpoints` 放寬，或手動：

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. 排障

| 現象 | 原因 → 處理 |
|---|---|
| 靜態 API 很少 | endpoint 方言不匹配 → 放寬正則（D 節） |
| chunk 數 ≪ manifest | CSS-only 或未部署 chunk；404 已重試 |
| runtime 仍顯示登入頁 | 渲染門錯誤 → 複查 A1：鍵名、容器、編碼、domain |
| 進殼但模組空白 | 內容門 → forge 選單（A3）；`routes` path 可能不對 |
| 每路由只有 bootstrap/locale | 權限碼不全 → I 節權限樹還原；檢查 `role_permissions` + `permissions/all` 雙 stub |
| 側欄有項但子頁空白 | tree 缺 intermediate 節點或 code 與 `userRouteAuth` 不一致 |
| 每個 API 都跳登入 | 攔截器門 → 確認 `neutralize`；巢狀欄位需擴充套件 walk 邏輯 |
| WS 幀為 0 | 需使用者互動後才 subscribe；加長 `perRouteMs` |
| 回應體空 | 僅 `forward: true` 時有真實回應 |
| Chromium 缺失 | 安裝 chromium 或設定 `config.chromium` / `CHROMIUM` |
| Mock 很多仍回登入 | Hook 太晚或缺 `location.href` setter → document-start + preload |
| 列表全空 | L3 空陣列正常；繼續點 Tab/設定/詳情 |
| 誤把 Redux action 當路由 | 過濾含 get/set/change/clear/toggle/upload 的內部 path |
| Vue 仍跳登入 | preload 非 document-start → 改注入時機；或 `neutralizeVueRouter: false` 時手動清守衛 |
| 回應裡有 URL 但未進 log | 開 `extractUrlsFromResponse`；或從 `__API_RECON_DETAIL__` 人工提取 |
| 不知 Authorization 頭名 | 開 `observe.xhrHeaders` 或 DevTools 檢視請求頭 |
| runtime 極慢 / 超時 | 改 `waitUntil: domcontentloaded`；降 `routeTimeout`；勿用 `networkidle2` |
| 代理連線失敗 | 檢查 `proxy` / 環境變數；Puppeteer 與 curl 代理埠一致 |

---

## G. hardened 目標

服務端逐步校驗會話（不可 forge 的簽名 cookie、服務端渲染且不可 stub 的選單）時，runtime 會在 shell 處卡住。預期行為：

- **靜態足夠做 endpoint 列舉** — 模組 path 在程式碼裡
- 若授權允許，用**真實會話**跑同一 harness：`forward: true`、無需 neutralize，捕獲真實 methods/params/responses

---

## H. 單次任務清單

1. 確認授權範圍
2. **閱讀** `scripts/harvest_static.py` → 按目標調整 → 執行 → 審 `api_static.txt`、`routes.txt`
3. **Phase 1b**：path 錨點擴窗 + 綁定層 → `param_candidates.json`（J 節）
4. 逆向 A1/A2/A3 → 寫站點專屬 `config.json`
5. **閱讀並調整** `runtime_harvest.js` / `preload.js` 後再執行
6. `runtimeMode=depth`：`npm install` → 執行調整後的 harvest 指令碼
7. `runtimeMode=coverage/both`：document-start 注入調整後的 preload → browser MCP 動態列舉 + **引數觸發矩陣**
8. 模組不渲染 → **I 節權限樹還原** → patch stubs → 重跑
9. 引數多樣本 diff + 錯誤反推 → `params_merged.json`
10. 合併 → `site_map.json` + `api_merged.txt`，誠實標註覆蓋、缺口及指令碼改動點

---

## I. 權限樹還原（Phase 4 深化）

當 forge 簡單 `menus: [{ path, show: true }]` 無效、子模組仍不 mount 時使用。

### I1. 定位 auth 模組

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

記錄：**權限 API path**、**回應欄位名**、**消費 chunk 檔名**。

### I2. 提取 routeMap

```bash
python3 scripts/extract_route_map.py recon/js recon/
# 產出 recon/route_map.json
```

若 `[!] no routeMap pattern found`：放寬 `extract_route_map.py` 中正則，或手工 grep：

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. 構建權限樹 + stub

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

指令碼邏輯：
1. 解析 `userRouteAuth={MONITOR:{url:...},...}`（含 webpack 別名 `He=o.DASHBOARD`）
2. 用 `route_map.json` 解析 alias → 真實 path
3. 按 code 字首推斷 parent（`MONITOR_ALERT` → `MONITOR`）
4. 輸出 `permissions_tree.json`、`permissions_all_stub.json`、`role_permissions_stub.json`
5. `--config` 時自動寫入 `config.json` 的 `stubs` 與擴充套件 `routes`

**按目標調整**（在指令碼頂部）：
- `DEFAULT_ROOTS`：頂級模組 code 列表
- `DEFAULT_PREFIX_PARENT`：`PREFIX_` → parent 對映
- `DEFAULT_EXTRA_PARENT`：非字首關係的 orphan 節點

### I4. 校驗 stub 一致性

```bash
# permissions 數量應 ≈ userRouteAuth 條目數
wc -l recon/perm_codes_all.txt
# routes 應覆蓋 route_map 全部 link
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. 重跑 runtime 並對比

```bash
node recon/runtime_harvest.js recon/config.json
# 對比 forge 前後 runtime_api.json 條數；檢查 /attack、/asset 等是否出現模組 API
```

| forge 前 | forge 後（成功） |
|---|---|
| 每路由相同 3–5 條 bootstrap | 不同路由觸發不同 module API |
| 僅 `/api/locale/language` | 出現 `/api/web/...` 模組 endpoint |
| `routes.txt` 個位路由 | `routes` 80–110+ 來自 route_map |

### I6. 仍失敗時

- **coverage 模式**：點選側欄 + Tab，權限 gating 可能在互動後才請求
- **stub 欄位**：對比真實 API（curl + 真實 session）與 stub 的 nesting
- **額外守衛**：grep `hasPermission|checkRole|func.` 等按鈕級檢查，擴充套件 `role_permissions.permissions`
- **靜態兜底**：模組 API path 仍在 `api_static.txt`，runtime 僅補 METHOD/body；引數保留 `param_candidates.json` + 已錄樣本

---

## J. 引數逆向（Phase 1b / 5b / 5c）

**方法論，非通用指令碼。** 找 path 用正則；找引數用錨點擴窗 + UI 綁定鏈 + 多樣本 diff + 錯誤反推。

### J1. 錨點擴窗 — 從 path 找組包物件

```bash
# 以 Phase 1 已知 path 為錨
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. 包裝層與傳輸形態

```bash
# axios / 統一 request
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# 路徑引數
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. 校驗門 — 必填 / 格式 / 列舉

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. 綁定層 — 表單 → API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

runtime 補位：DevTools → Network → 請求 → **發起程式**（call stack）從 `fetch`/`send` 往上追組包函式。

### J5. 加密引數

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

**勿在密文上猜欄位** — Hook 加密函式**入參**，在加密前錄 plaintext payload；結論寫 `config.json` / `param_candidates.json`。

### J6. 引數觸發矩陣（Phase 3 必做）

對每模組按操作各錄一次，diff 請求 body/query：

| 操作 | 關注 |
|---|---|
| 列表首屏 | 分頁預設值 |
| 搜尋 | keyword、filters |
| 高階篩選 | optional 欄位 |
| 新建/編輯 | 完整 entity |
| 批次/匯出 | `ids[]`、`exportType` |
| 排序/翻頁 | `sortField`、`order` |

產出 `param_samples.json`：`[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. 置信度規則

| 置信度 | 條件 |
|---|---|
| **高** | 靜態 callsite + runtime ≥2 樣本一致 |
| **中** | 僅靜態，或僅 1 次 runtime |
| **低** | 回應/錯誤反推，未二次驗證 |
| **待觸發** | 靜態已知欄位，UI/權限未跑到 |

### J8. 場景快配

| 場景 | 順序 |
|---|---|
| REST 列表頁 | J1 組包物件 → J6 四次 diff → J3 rules |
| 新建/編輯表單 | J3 Form name → J4 submit 鏈 → runtime 提交 + 故意留空看 400 |
| GraphQL | J2 variables 宣告 → runtime 各 operation 錄 variables |
| 加密 body | J5 Hook 入參 → 加密前欄位即真實 params |

### J9. 與 api-recon 階段對映

| api-recon | 引數 recon |
|---|---|
| Phase 1 靜態 | J1 錨點擴窗 |
| Phase 2 A2 攔截器 | 全域注入欄位（tenantId、sign） |
| Phase 3 runtime | J6 觸發矩陣 + `param_samples.json` |
| Phase 4 權限樹 | 不同模組表單不同 → 權限夠才觸發全欄位 |
| Phase 5 合併 | `params_merged.json` + 置信度；勿單樣本定必填 |

### J10. 排障

| 現象 | 處理 |
|---|---|
| 靜態有欄位名 runtime 從未出現 | 標註「待觸發」；補權限樹 / 點高階篩選 / 聯動 select 各 option |
| 同 path 不同 body 形狀 | 正常 — 按 `action` 分條記錄，勿強行合併 schema |
| stub 回應假但想看 params | **看 outbound 請求** body/headers，勿從 stub 回應反推 |
| 400 報 nested field | 注意外層包裝 `data`/`bizData`/`variables` |
| GraphQL 只見 operation 名 | 展開 `variables` JSON；靜態找 `$var: Type` |

---
