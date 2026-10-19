package agent

import (
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

const findingIDGuidance = "\n\n**漏洞編號約定**：finding_id 是獨立漏洞記錄 ID；finding_node_id 是探索節點 ID。list_findings / list_task_findings / node_detail / get_task_node_detail 的 id 保留為探索節點 ID，應從同一返回的 finding_id 讀取獨立編號。get_finding_traffic / bind_finding_traffic 用獨立 finding_id。舊 update_finding_report 的 finding_id 引數仍傳 finding_node_id。不要把 report_finding 第一行的數字用於證據工具，也不要遇到編號錯誤後猜測其他數字。"

// The server supplies the persisted setting. A missing setting/host is off.
// Consulted at assembly and again on writes so an already-running session
// cannot keep binding after the user switches the feature off.
var FindingTrafficBindingEnabled func() bool

func findingTrafficBindingEnabled() bool {
	return FindingTrafficBindingEnabled != nil && FindingTrafficBindingEnabled()
}

// Applied after ToolResolve: user descriptions and prompts remain intact, while
// all actual reporters (including Planner and custom chat agents) see the same
// API contract. Disabled/unbound tools are never reintroduced here.
func findingWorkflowTools(agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if !findingTrafficBindingEnabled() {
		out := make([]actool.CoreTool, 0, len(tools))
		for _, tool := range tools {
			if tool.Name() == "bind_finding_traffic" {
				continue
			}
			if agentKey == "reporter" && (tool.Name() == "traffic_search" || tool.Name() == "traffic_get" || tool.Name() == "traffic_blob") {
				continue
			}
			switch tool.Name() {
			case "report_finding", "add_hint", "add_task_hint":
				// Work on a copy: toggling back on must restore the original schema.
				raw, _ := json.Marshal(tool.InputSchema())
				var schema map[string]any
				if json.Unmarshal(raw, &schema) == nil {
					stripTrafficParameters(schema)
					tool = DecorateTool(tool, tool.Description(), schema)
				}
			}
			out = append(out, tool)
		}
		return out, ""
	}
	out := append([]actool.CoreTool(nil), tools...)
	has := map[string]bool{}
	for i, tool := range out {
		has[tool.Name()] = true
		note := ""
		switch tool.Name() {
		case "report_finding":
			note = "\n預設由報告 Agent 在編寫報告前核對並綁定流量。上報者在 evidence 中保留驗證命令、關鍵輸出、已有的真實流量 ID 及其用途，供報告 Agent 對照執行記錄核實；無需為綁定額外查包。相容顯式即時綁定：traffic_refs 或 evidence_hint_id 可提交已核實的引用，後者讀取本任務指定 hint 的結構化引用；任一無效則本次上報全部失敗。TCP/無包不需要這些可選引數。返回 finding_id 與 finding_node_id 分別表示獨立記錄和探索節點。"
		case "add_hint", "add_task_hint":
			note = "\n交接已確認漏洞時，在對應提示的 traffic_refs 中保留已核實流量的 ID、用途、說明和順序（單條放頂層，批次放對應 hints 元素），並在 text 中說明它證明的具體漏洞。呼叫方不能只交接文字而丟棄已有流量引用。未核實的候選不能作為證據傳遞。"
		case "get_finding_traffic", "bind_finding_traffic", "list_findings", "list_task_findings", "node_detail", "get_task_node_detail", "update_finding_report":
			note = findingIDGuidance
		}
		if note != "" {
			out[i] = DecorateTool(tool, tool.Description()+note, tool.InputSchema())
		}
	}
	guidance := ""
	if has["report_finding"] || has["add_task_hint"] || has["add_hint"] {
		guidance = "\n\n**流量證據交接（可選）**：自動綁定預設由報告 Agent 在漏洞入庫後、編寫報告前完成。上報者應在 evidence 保留驗證命令、關鍵輸出、已有真實流量 ID 及其用途，任務中帶 intent_id，便於報告 Agent 追溯；不必為了綁定額外查包。Auto / Planner 代為上報時不要丟棄執行者已有的引用。add_hint / add_task_hint 可用 traffic_refs 交接；顯式即時綁定仍相容 report_finding 的 traffic_refs / evidence_hint_id。TCP 或無包時正常登記，不能猜測 ID，也不能僅為補包重複探測。"
		if has["add_task_hint"] && !has["add_hint"] {
			guidance += "\n平臺對話沒有任務上下文時，不直接呼叫 report_finding；通過 add_task_hint 向已有對應任務交接，由任務 Agent 登記，並用 list_task_findings 核對結果。"
		}
		if has["prove_goal"] || has["goal_met"] {
			guidance += "\n判定目標完成前，先完成本次已有證據的上報/交接。不要在證據交接尚未完成時僅因文字漏洞已登記就結束任務、取消 Worker；無包不要求等待或強行抓包。"
		}
	}
	if has["update_finding_report"] && has["bind_finding_traffic"] && has["get_finding_traffic"] {
		guidance += "\n\n**報告前自動關聯流量（已開啟）**：你負責為本次觸發的漏洞核對並綁定流量，再撰寫報告。先從 report_finding 返回 JSON 或 get_task_node_detail / list_task_findings 取得明確的 finding_id 與 finding_node_id。讀取漏洞詳情、對應意圖的執行記錄及已有證據清單，優先使用上報者交接的真實 ID。若本次驗證為 HTTP 且流量工具可用，用 traffic_search 篩選候選，再用 traffic_get 逐條核實請求/回應確實支援該漏洞；域名和時間只用於篩選，不證明歸屬。將確認的證據按重現順序用 bind_finding_traffic(finding_id, traffic_refs) 關聯，選擇 baseline / proof / verification / supporting 並說明用途。只能操作本次漏洞，不重複建立漏洞或重新探測目標。綁定成功後重新呼叫 get_finding_traffic 獲取最新 version，讀取所需正文，再將實際讀取的 version 作為 evidence_version 傳給 update_finding_report（其 finding_id 引數仍用 finding_node_id）。已有綁定不必重複追加。TCP、未採集、工具不可用或沒有確切匹配時，跳過自動綁定，依據文字/命令證據正常寫報告並說明原因，不得為湊齊流量而猜測。綁定失敗不宣稱成功；保留已有證據並在報告說明未綁定原因。"
	}
	if guidance != "" || has["get_finding_traffic"] || has["update_finding_report"] {
		guidance += findingIDGuidance
	}
	return out, guidance
}

func stripTrafficParameters(schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	delete(props, "traffic_refs")
	delete(props, "evidence_hint_id")
	if required, ok := schema["required"].([]any); ok {
		kept := required[:0]
		for _, key := range required {
			if key != "traffic_refs" && key != "evidence_hint_id" {
				kept = append(kept, key)
			}
		}
		schema["required"] = kept
	}
	if hints, ok := props["hints"].(map[string]any); ok {
		if items, ok := hints["items"].(map[string]any); ok {
			stripTrafficParameters(items)
		}
	}
}

// HintTrafficSchema is shared by the task-local and cross-task hint tools.
func HintTrafficSchema() map[string]any {
	return map[string]any{"type": "array", "description": "可選：已核實且對應本提示中具體漏洞的流量引用，保留順序；交接後 report_finding 可傳 evidence_hint_id 攜帶這些引用。", "items": obj(map[string]any{"traffic_id": str("真實流量 ID"), "role": str("baseline / proof / verification / supporting"), "note": str("該流量支援什麼結論")}, "traffic_id")}
}

func (t *ToolSet) findingRefsFromHint(hintID int64, explicit []db.TrafficRef) ([]db.TrafficRef, error) {
	if hintID <= 0 {
		return db.NormalizeTrafficRefs(explicit)
	}
	n, err := t.ts.GetNode(hintID) // local store only: inherited hints cannot supply evidence
	if err != nil {
		return nil, err
	}
	if n == nil || n.Kind != db.KindHint {
		return nil, fmt.Errorf("evidence_hint_id=%d 必須是本任務的提示節點（繼承提示不可直接用於綁定）", hintID)
	}
	var payload struct {
		Refs []db.TrafficRef `json:"traffic_refs"`
	}
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		return nil, err
	}
	return db.NormalizeTrafficRefs(append(append([]db.TrafficRef{}, explicit...), payload.Refs...))
}
