package agent

import (
	"context"

	"github.com/Autumn-27/artex/db"
)

// FindingRecorder is injected by the host; agents never synthesize or copy
// evidence bodies themselves. Its implementation owns the atomic write.
type FindingRecorder interface {
	Record(context.Context, db.RecordFindingInput, []db.TrafficRef) (*db.RecordedFinding, error)
}

// Tool-use guidance is appended without replacing the user's editable prompt.
// It does not require capture or claim that unavailable traffic tools exist.
const findingTrafficGuidance = "\n\n**漏洞流量證據（可選）**：呼叫 report_finding 上報漏洞時，如有已檢視並確認支援漏洞結論的 HTTP 請求/回應，可用 traffic_refs 按重現順序綁定真實 ID；域名和時間只作候選篩選，不推定關聯。TCP 等非 HTTP 漏洞、未採集或無確切匹配時省略或傳 []，在 evidence 保留命令輸出、日誌等其他可驗證證據，建議說明未綁定原因。不要猜測 ID，也不要僅為補包重複探測。"

func (t *ToolSet) SetFindingRecorder(r FindingRecorder)   { t.findingRecorder = r }
func (w *Worker) SetFindingRecorder(r FindingRecorder)    { w.findingRecorder = r }
func (p *Planner) SetFindingRecorder(r FindingRecorder)   { p.findingRecorder = r }
func (m *MainAgent) SetFindingRecorder(r FindingRecorder) { m.findingRecorder = r }
