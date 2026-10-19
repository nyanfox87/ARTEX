# 旁路長對話與上下文預算

2026-09-11 修復。原實現將請求 JSON 的字元數直接視為 token，並繼承主任務的 32K 輸出預留，因此 HTML、JS 和工具結果較多時會提前拒絕正常旁路問題。

## 開源實現核對

- [Grok CLI 旁路上下文](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/agent.ts#L739)：從最近的使用者、助手文本中摘取片段，字元預算約 2000，每條至多擷取 400 字元。未在此路徑維護連續旁路問答歷史。
- [Grok CLI 獨立請求](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/utils/side-question.ts)：獨立取消訊號；模型支援時輸出上限為 2048 tokens；不提供工具。
- [Grok CLI 主會話壓縮](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/compaction.ts)：估算 token、保留近期內容、把新內容更新進舊摘要，並處理跨回合截斷。
- [OpenCode 會話壓縮](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/compaction.ts)：完整請求估算、輸出/緩衝預留、近期內容加滾動摘要、無工具摘要請求；該實現預設近期預算 8000、摘要輸出上限 4096 tokens。
- [OpenCode 溢位恢復](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/runner/llm.ts)：尚未開始助手輸出時才嘗試溢位恢復，恢復後的呼叫不再進入同一溢位恢復路徑。

ARTEX 借鑑獨立輸出預算、近期內容與滾動摘要、有限恢復的做法。保持 norma v0.3.6 的結構化訊息與工具配對，不照搬 Grok 的文本摘錄；不把 OpenCode 的主會話壓縮事件寫入 ARTEX 主 transcript。

## 請求預算與執行

- 訊息沿用 norma 的按內容塊 UTF-8 位元組估算及 4/3 餘量；額外計入系統提示、工具 schema 和訊息封裝開銷。估算不是模型精確 token 計數。
- 旁路輸出預設最多 8192 tokens，也不超過主配置已設的輸出上限。可用服務環境變數 `ARTEX_BTW_MAX_OUTPUT_TOKENS` 設定 256–32768 的上限；不會修改產品預設模型或主任務引數。
- 輸入預算為上下文視窗減去輸出上限和安全餘量；未知視窗使用平臺預設 200K。安全餘量為視窗的 5%，最小 128、最大 8192 tokens。
- 成功問答按遞增序號每批最多載入 20 組。最多保留 20 組原文，其 token 預算最多為輸入預算的 1/4，且不超過 16K。
- 超額問答更新到滾動摘要。摘要帶歷史來源與上下文時間；歷史助手回答不等同於新的工具證據，衝突時優先依據最新主快照。
- 主上下文仍過長時，僅摘要副本中的舊訊息，近期最多保留 8K tokens；擷取點不會拆開工具呼叫與結果。過大的單組會整體進入摘要。
- 摘要輸入按實際剩餘視窗進行 UTF-8 安全分塊，輸出上限 2048 tokens；空摘要、截斷、工具呼叫或超出摘要預算均不寫快取。單次旁路最多 12 次摘要呼叫，並受同一 120 秒超時限制；達到上限會明確失敗，不無限迴圈。
- 若模型首次返回上下文超限且尚未輸出文本或工具呼叫，進一步縮減後最多重試一次；若估算大小沒有下降，立即停止恢復。其他模型錯誤和部分流式輸出不觸發該恢復。
- 所有已取得用量，包括摘要、失敗嘗試和取消時的用量，累加到同一旁路請求。Provider 不返回用量時仍只能記錄零，不能把估算偽裝成實際用量。

## 持久化與介面

`side_question_sessions.memory` 儲存舊問答摘要、覆蓋的 ordinal，以及按快照身份快取的主上下文摘要。`side_question_requests.context_info` 儲存準備階段、實際回放組數、摘要使用情況和預算估算。

摘要只在原請求仍執行且清理版本匹配時儲存；清空同時清除快取，遲到寫入不會恢復已清理資料。新快照不復用舊快照摘要。摘要欄位隨 v3 任務歸檔儲存；恢復舊 v3 缺失欄位時補空物件，v1/v2 繼續相容。

POST 先接納並返回請求，準備與壓縮在後臺執行，不持有準入鎖或資料庫事務。SSE/歷史顯示準備、整理問答、壓縮副本、回答階段；壓縮失敗作為該旁路請求的失敗終態儲存。前端保留錯誤並恢復本次失敗問題的草稿，不用懸浮通知遮擋輸入框；歷史輪詢不再清掉提交錯誤。

## 驗證記錄

- 19/20/21/50 組回放、跨 20 組舊結論保留、摘要快取重啟複用：自動化通過。
- 超長中文回答和程式碼上下文、分塊請求預算、工具配對、快照不變、新快照快取失效：自動化通過。
- 摘要失敗/取消/截斷/超長/工具返回、清空競爭、呼叫上限、僅一次溢位恢復及部分流不重試：自動化通過。
- 獨立 PostgreSQL 的分頁、重啟、v1/v2/v3 歸檔、摘要快取與預算後設資料歸檔恢復、舊 v3 缺少新欄位、20 個父會話共享四個併發名額：通過。
- Go 候選服務構建、前端 TypeScript 檢查、修改元件的 Biome 檢查，以及獨立目錄中的 Next.js 生產構建：通過。
- 內建瀏覽器使用獨立 UI 夾具，在 1280×720 和 390×844 驗證整理階段、摘要範圍提示、失敗後草稿恢復、無懸浮錯誤通知、無橫向溢位和控制台無錯誤。臨時夾具已移除。
- 只讀回放本機現有 Worker 快照，新預算檢查通過；例如 Worker #3 的 293085 字元快照不再被本地字元計數誤判拒絕。此項沒有外部模型呼叫。
- 本次將私有 Worker 快照發送到 Grok 的真實對話測試被自動審批審查拒絕，未執行，不作為通過項。
- 2026-09-11 00:37 按使用者要求重啟本機後端，沿用原資料庫、資料目錄和登入配置。執行檔案與候選二進位制 SHA-256 一致；後端及前端代理的 `/api/health` 均返回正常。

驗證命令（僅使用獨立測試庫）：

```sh
go test -race ./sidequestion ./db ./server -run 'TestSide|TestCheckpoint|TestSnapshot|TestBuildRequest|TestService|TestMainSide|TestTaskArchive' -count=1
go build ./cmd/artex
npx tsc --noEmit
npm run build -- --webpack
```

前端生產構建使用獨立副本，避免覆蓋當前預覽的 `.next`。候選服務位於 `/private/tmp/artex-btw-budget-candidate`，已複製到 `/private/tmp/artex-btw-preview/artex` 並啟動；原二進位制備份為同目錄下的 `artex.before-context-budget`。
