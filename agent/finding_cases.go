package agent

import (
	"context"
	actool "github.com/Autumn-27/norma/tool"
)

type findingReviewKey struct{}

// Historical consolidation has a narrower capability set than new reporting.
func WithFindingCaseReview(ctx context.Context) context.Context {
	return context.WithValue(ctx, findingReviewKey{}, true)
}
func isFindingCaseReview(ctx context.Context) bool {
	v, _ := ctx.Value(findingReviewKey{}).(bool)
	return v
}

func historicalFindingCaseTools(tools []actool.CoreTool) []actool.CoreTool {
	allowed := map[string]bool{}
	for _, name := range []string{"search_finding_duplicates", "get_finding_record", "merge_finding_records", "suggest_finding_merge", "get_finding_case", "update_finding_case_report", "complete_finding_case_review", "get_finding_traffic", "traffic_search", "traffic_get", "traffic_blob", "get_task_node_detail", "list_task_findings", "get_task_graph", "list_task_worker_traces", "get_task_worker_trace", "Read"} {
		allowed[name] = true
	}
	out := make([]actool.CoreTool, 0, len(tools))
	for _, tool := range tools {
		if allowed[tool.Name()] {
			out = append(out, tool)
		}
	}
	return out
}

// This guidance is appended to the runtime prompt, including editable old prompts.
// Never reintroduce unbound tools or recreate a deleted Reporter.
func findingCaseWorkflow(key string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if key != "reporter" {
		return tools, ""
	}
	has := map[string]bool{}
	for _, t := range tools {
		has[t.Name()] = true
	}
	if !has["search_finding_duplicates"] || !has["merge_finding_records"] || !has["get_finding_case"] || !has["update_finding_case_report"] {
		return tools, ""
	}
	return tools, `

【同一漏洞的多次上報整理】保留本次獨立報告，不覆蓋其他原報告。預設報告完成即停止的舊指引在此擴充套件：先儲存本次報告，然後 search_finding_duplicates(finding_id=獨立記錄ID) 分頁讀取同任務候選摘要，按需 get_finding_record 檢視證據。僅憑漏洞型別/標題相似或高中低等級不能歸併；須核對目標、介面、引數、權限邊界、根因及實際驗證證據。
【判斷準則】介面、路徑、後端、讀/寫影響、評級不同，都不能單獨作為不重複的理由；同一登入入口、同一令牌或同一漏洞型別也不能單獨證明同一根因。比較實際缺失的鑑權檢查、物件權限邊界、會話門或共同元件，並引用每條記錄中的具體證據。讀和寫可能是同一權限檢查缺失的不同影響；也可能是獨立 handler 的獨立缺陷。只有證據證明共同實際根因才自動歸併，證據證明不同根因才判獨立；存在關聯但無法確認時必須 suggest_finding_merge，說明已知共同點、缺失證據和待確認問題，不用“路徑不同/影響不同”代替根因判斷。明確同一缺陷時 merge_finding_records(task_id,finding_ids,title,reason)，不確定時 suggest_finding_merge 留待人工確認，不能強行歸併。不重複上報、不刪除記錄、不重新探測目標。人工選擇整理時只能判斷所選記錄，不能加入未選記錄（既有組成員可作為報告上下文）。
歸併成功或已有case_id時，get_finding_case 記錄實際version，分頁讀取全部成員，按需get_finding_record、get_finding_traffic讀取原報告、PoC及已儲存流量。update_finding_case_report儲存完整統一報告（包含有內容的概述、影響、前置條件、重現步驟、證據、PoC、根因分析、修復建議章節，不得半篇截斷）和基於已驗證影響的severity及severity_reason；不能直接取成員最高等級。集中給出前置條件、完整執行順序、輸入輸出如何銜接、可執行的完整指令碼及命令，不把必要步驟分散到原報告；缺失條件、未驗證步驟如實標明。版本衝突重新讀取材料並生成，不能僅換version重試。各成員仍有獨立報告、評級和證據。沒有明確重複則保留獨立記錄。工具被關閉時不繞過配置。`
}

const historicalFindingReviewGuidance = `

【歷史整理模式，優先於舊報告撰寫及自動綁流量指引】本次只整理已經存在的上報。原始報告即使為空或待更新，也只記錄缺失情況，絕不呼叫 update_finding_report、report_finding、bind_finding_traffic，不修改原始標題、評級、狀態、證據或報告，不執行 Bash、寫檔案或重新探測目標。
先逐條 get_finding_record 讀取所選記錄，已有原報告直接作為判斷材料複用，不重複撰寫。只在所選範圍內檢查同一實際缺陷，可按需讀取已存在的證據。按上述根因準則形成明確歸併/獨立/疑似建議；無法確認時儲存建議供人工確認。
已有或新建資料夾必須讀取全部成員並寫入完整、最新的統一報告；統一報告是單獨的材料，不覆蓋原報告。最後必須呼叫 complete_finding_case_review，為每條所選 finding_id 提交 verdict（independent/merged/suggested）、reason 和 root_cause_evidence，並提供總結。根因證據應說明具體相同/不同的檢查及證據來源；疑似建議明確待補證據。服務端驗收通過後才可宣佈完成。工具不可用或驗收失敗時如實報告，不能只用自然語言聲稱完成。`
