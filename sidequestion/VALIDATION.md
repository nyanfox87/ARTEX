# `/btw` 驗證記錄

日期：2026-09-10。分支：`codex/btw-side-question`。基線：`8dae851b9b622f2ff2631f332fde9719d0b16fba`。

使用獨立 PostgreSQL 測試庫和資料目錄；真實模型憑據只注入獨立測試環境，沒有寫入程式碼或此記錄，也沒有修改產品預設模型。Go 1.26.3、norma v0.3.6、Next.js 16.2.9。

實際模型對話、返回物件、工程斷言及 Qwen 原始審查文本儲存在 [validation-2026-09-10.json](validation-2026-09-10.json)，其中沒有 API 憑據。

## 工程檢查

| 範圍 | 結果 | 證據 |
| --- | --- | --- |
| 結構化訊息、工具引數深複製 | 通過 | `TestCheckpointDeepCopyAndBoundaries` |
| 摘要 / 壓縮請求不覆蓋、完整回覆和終態釋出、半段回覆排除 | 通過 | `TestCheckpointDeepCopyAndBoundaries`、`TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| 實際模型池成員身份 | 通過 | `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| 工具配對、20 組回放、預算裁剪與超限錯誤 | 通過 | `TestBuildRequestCompactionToolPairingAndBudget` |
| 主旁路並行、雙向取消隔離 | 通過 | 阻塞式 Provider，`TestMainSideConcurrencyAndIndependentCancellation` |
| 無工具執行、流式 / 非流式、失敗時已有用量 | 通過 | `TestServiceNoToolsAndUsageOnFailure` |
| 真實 norma ChatAgent + 本地 Read 工具、主 transcript / 活動隔離 | 通過 | `TestSideActualChatCheckpointToolResultAndTranscriptIsolation`，流式和非流式子用例 |
| 持久化、分頁、冪等、重啟保留部分回答 | 通過 | `TestSideHistoryIdempotencyPagingAndRecovery` |
| 清空與遲到寫入競爭、父資源刪除、版本比較 | 通過 | `TestSideClearLateWritersAndDeletedParent` |
| MainAgent / Worker 歸檔與恢復，v1/v2/v3 | 通過 | `TestSideTaskArchiveVersions` |
| 三種父介面、認證、資源歸屬、Worker 邏輯刪除 | 通過 | `TestSideHTTPGlobalLimitTaskWorkerAndDeletion`、`TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| 忙碌主會話可旁路、獨立 SSE 重連 / 斷開、取消、清空 | 通過 | `TestSideHTTPBusyIsolationClearAndReconnect` |
| 每父會話 1 / 全域 4 併發 | 通過 | 兩個 `TestSideHTTP…` 用例 |
| 提交前快照落庫、重啟續問、舊會話不可偽造快照 | 通過 | `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| 快取中的配置被刪除或模型改變後拒絕繼續 | 通過 | `TestSideRejectsDeletedOrChangedCachedProfile` |
| 歸檔前取消並等待最終回答和用量落庫 | 通過 | `TestSideTaskDrainPersistsBeforeArchive` |
| 流式消費者提前取消時只記一次用量及旁路歸屬 | 通過 | `TestSideUsageRecordedOnceOnConsumerCancellation` |
| 重啟自動恢復的 Worker / deadline 執行上下文繼續釋出新快照 | 通過 | `TestSideRestoredWorkerRuntimePublishesNewCheckpoint` |
| 相關包 race 檢查 | 通過 | 下列命令 |
| TypeScript 與生產構建 | 通過 | `npx tsc --noEmit`、`npm run build` |
| 新增前端模組 Biome | 通過 | `biome check`，3 個新增模組 |

在單獨的可丟棄資料庫中配置 `ARTEX_PG_DSN` 後，可以重現自動化檢查（不要指向生產庫）：

```sh
go test -race ./agent ./db ./server ./sidequestion ./llmrec ./llmpool \
  -run 'Test(Side|Checkpoint|Snapshot|BuildRequest|Service|MainSide|CaptureRun|TaskArchive|CompleteForwards|StopIntent|CancelIntent)' -count=1
cd web
npx tsc --noEmit
npx biome check src/lib/side-questions.ts src/hooks/use-side-questions.ts src/components/side-question-workspace.tsx
npm run build
```

全量 Go 迴歸不是全綠：`server` 包有兩個既有測試在臨時目錄清理階段失敗，均報 `TempDir RemoveAll … directory not empty`：

- `TestInheritedActivityDetailAndRelationDeletion`
- `TestTaskMetadataPatchReturnsRenameAndPin`

從上述未修改基線匯出原始碼後，在相同隔離環境重跑 `server` 包，也重現這兩個清理失敗。基線執行另出現 `TestCoreTaskLifecyclePG` 的目標節點數量斷言失敗；最終修改後的 `server` 迴歸沒有該斷言失敗。其他包通過，本次旁路相關用例及 race 檢查通過。沒有將基線問題標為本次驗收通過，也沒有為隱藏問題修改既有斷言。

Next.js 構建輸出已有的多 lockfile / workspace root 推斷警告；構建完成且所有頁面生成成功。

## 瀏覽器檢查

使用 Codex In-app Browser，連線獨立本地 Go 服務和 Next.js 開發伺服器。桌面與 390 × 844 窄屏完成以下人工自動化操作，檢查截圖和瀏覽器日誌：

- 普通聊天執行期間輸入 `/btw`，主內容和旁路同時顯示；桌面側欄正常。
- 連續追問；旁路停止後保留已生成部分；主流程繼續。
- 關閉面板時請求繼續，重開後恢復完成的回答；重新整理頁面後空 `/btw` 恢復歷史。
- 窄屏 Drawer 的輸入、按鈕、歷史和關閉操作正常，無橫向溢位。
- 清空使用確認彈窗，清空後歷史消失，主 transcript 和快照保留。
- 任務 MainAgent 與兩個 Worker 分別提問並切換，Agent 標籤和歷史未串話。
- 阻塞式本地模型夾具保持 Worker 執行；從 Worker 主輸入框提交 `/btw`，停止旁路後 Worker 仍顯示即時執行和自己的暫停按鈕，旁路儲存部分回答。
- 瀏覽器錯誤 / 警告日誌為空。

可控夾具用於精確驗證併發時序，不依賴真實模型的輸出速度。除錯期間兩次 Worker 執行時檢查未形成有效併發視窗（任務已結束 / 回答提前結束），修正夾具後重做並通過；不將這些初始操作記作有效通過。

## 真實模型對話

優先探測 `grok-4.6`，OpenAI 相容介面 `http://127.0.0.1:12580/tingly/openai`。探測 HTTP 200，返回模型名 `grok-4.6` 和 `READY`，耗時 2.82 秒。首選可用，因此沒有啟用 Tingly `glm` 或智譜 `glm-5.3` 備用鏈；這兩個備用服務本次沒有驗證。

| 場景 | 實際結果 |
| --- | --- |
| 主會話執行期間詢問資產、目標、標記 | 返回 `redhaze.top`、首頁讀取和總結目標、`BTW-REAL-0910`；旁路完成，16.97 秒 |
| 主會話完成首頁讀取後詢問工具依據 | 正確引用 WebFetch 200、curl 跳轉 301 → 302 → 200、頁面標題；7.24 秒 |
| 旁路要求 Bash 建立測試檔案 | 拒絕執行，目標檔案未建立；7.74 秒 |
| 完成後的旁路不改變主上下文 | 主 transcript SHA-256 與主活動記錄保持一致；旁路工具執行次數為 0 |
| 真正停止 / 重啟 Go 服務後續問 | 保留先前 3 條旁路歷史，直接從持久化快照回答資產、標記和標題，未重跑主 Agent |
| 新會話使用 Grok 非流式配置 | 正確回答資產和 `ATOMIC-0910`；返回並儲存用量：input 11734、output 138、cache_read 11520 |

資產案例的主會話使用 WebFetch 和 Bash/curl 讀取公開首頁，落地頁為 `https://id.redhaze.top/home`，標題為“紅幕科技 RedHaze Group · 全球綜合集團門戶”。Bash 把回應暫存於本地測試檔案；未向遠端執行寫入。該事實與“旁路沒有執行工具”分開核驗。

主 transcript 校驗值：`e7e61f135a4a120954b539f357e8c4205d7d5cd7460dcaf3dc0fd066463e1d00`。

**用量限制：** Tingly 的 Grok 流式回應沒有返回 usage。另行直接傳送 `stream_options.include_usage=true` 驗證，HTTP 200、12 個數據幀、0 個 usage 幀。因此流式測試中的 0 表示端點沒有提供用量，不能解釋為沒有計費。非流式用量以及夾具的失敗 / 取消用量都正確儲存。

## Qwen 審查

審查模型 `qwen-flash`，OpenAI 相容介面 `https://dashscope.aliyuncs.com/compatible-mode/v1`，HTTP 200。提供了前三項真實旁路對話、主會話工具依據及工程斷言；返回 `verdict: accept`、`concerns: []`，認為回答與資產、標記、頁面讀取證據一致，旁路工具拒絕符合約束。審查用量：prompt 6625、completion 312、total 6937。

這次 Qwen 審查範圍不包含後來追加的服務重啟和非流式測試。Qwen 對“無寫入”的概括過寬：主會話 curl 確實建立了本地回應臨時檔案，上文已明確記錄。併發、零工具執行和 transcript 隔離由工程斷言判斷，模型審查只輔助評估答案質量。
