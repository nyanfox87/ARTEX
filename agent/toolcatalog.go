package agent

import (
	"context"
	"encoding/json"

	actool "github.com/Autumn-27/norma/tool"
)

// 本檔案把「內建工具」從純程式碼變成可列舉、可被 DB 覆蓋的目錄：
//   - BuiltinToolSeeds()：把三個執行 agent 的內建工具集展開成 seed 記錄（key +
//     描述 + 引數 schema + 預設綁定的 agent），供服務端開機冪等播種進 tools 表。
//   - ToolResolve 鉤子：執行時按 DB 裡的 tools 行對已裝配的工具做「按 agent 過濾 +
//     覆蓋描述/schema + 注入引數預設值」。key/handler 仍在程式碼層，DB 只改「散文與預設值」。
// handler（Call 行為）永遠來自程式碼——DB 改不了它，只能改模型看到的說明與預設入參。

// ToolSeed 是一個內建工具的可播種快照：key 即 CoreTool.Name()（與 handler 死綁，
// UI 只讀），Desc/Schema 取自程式碼裡的工具定義，Agents 是程式碼預設把它給了哪些 agent。
type ToolSeed struct {
	Key    string         // = CoreTool.Name()，主鍵，不可改
	Desc   string         // 頂層描述（可在 UI 覆蓋）
	Schema map[string]any // 引數 JSON-Schema（結構只讀，description/default 可在 UI 改）
	Agents []string       // 預設綁定的 agent key（worker/planner/mainagent）
}

// builtinToolsByAgent 用一個「只讀空殼」ToolSet（nil stores）構造每個執行 agent 的
// 領域工具集。工具建構函式只把閉包塞進 Spec、構造期不解引用 store，所以 nil 安全——
// 這些工具在這裡只用來讀 Name()/Description()/InputSchema()，絕不 Call。
//
// 刻意【不含】SDK 通用工具 actool.DefaultTools()（Read/Write/Edit/MultiEdit/LS/Glob/
// Grep/Bash）：它們每個 agent 都固定擁有、沒有「綁定到誰」的取捨，且說明大多在 Prompt()
// 裡（本表只覆蓋 Description()，會造成半覆蓋誤導）。不 seed → 無 DB 行 → ToolResolve
// 原樣放行、不覆蓋，行為與從前一致。只有 artex 自己的領域工具入表可管。
func builtinToolsByAgent() map[string][]actool.CoreTool {
	ts := NewToolSet(nil, "")
	return map[string][]actool.CoreTool{
		"mainagent": ts.MainAgentTools(),
		"planner":   ts.PlannerTools(),
		"worker":    ts.WorkerTools(),
		// goals（目標拆解器）預設綁 set_goals + set_constraints：靠它們把拆出的目標、
		// 抽出的操作約束寫進庫。與 mainagent 共用同一受管工具，web 端可改描述/schema、按 agent 勾選。
		"goals": {ts.setGoals(), ts.setConstraints()},
		// auto 預設綁漏洞上報 + 資產管理工具，其他域工具可在 UI 按需勾選。
		// 新庫由此 seed 寫入；老庫由 seedAutoDefaultBindings 遷移。
		"auto": {ts.addFinding(), ts.insertAssets(), ts.addCompanyScope(), ts.listAssets(), ts.listCompanies()},
		// pentest（獨立滲透 agent）預設綁：查資產 / 插資產 / 報漏洞 / 查漏洞 / 查企業。
		// 新庫由此 seed 寫入；老庫由 seedPentestDefaultBindings 遷移。
		"pentest": {ts.listAssets(), ts.insertAssets(), ts.addFinding(), ts.listFindings(), ts.listCompanies()},
	}
}

// defaultUnbound：這些 system 工具會照常入目錄（web 端可見、可手動按 agent 勾選），但
// 預設【不綁任何 agent】——ToolResolve 對空綁定的工具對所有 agent 一律丟棄,須顯式 opt-in。
// 之所以仍留在某個 agent 的 base 工具集裡(如 goal_met 在 PlannerTools):一是讓 seed 能
// 構造它拿到 desc/schema,二是使用者手動綁回後執行時 base 裡有它、ToolResolve 才留得住。
//
// goal_met：繞過逐個 prove_goal、直接從全域宣佈【整個任務完成】,權重大且有誤判風險,又與
// 「prove_goal 標記最後一個目標 → 自動收官」重複,故預設不給任何 agent,需要時再手動綁。
var defaultUnbound = map[string]bool{"goal_met": true}

// BuiltinToolSeeds 把各 agent 的內建工具集去重合併成 seed 列表：同名工具（如 list_assets
// 多個 agent 都有）合成一條，Agents 取並集；defaultUnbound 裡的工具則強制綁定為空。
func BuiltinToolSeeds() []ToolSeed {
	byAgent := builtinToolsByAgent()
	order := []string{"mainagent", "goals", "planner", "worker", "auto", "pentest"}

	type acc struct {
		tool   actool.CoreTool
		agents []string
	}
	m := map[string]*acc{}
	var keys []string
	for _, ak := range order {
		for _, t := range byAgent[ak] {
			a, ok := m[t.Name()]
			if !ok {
				a = &acc{tool: t}
				m[t.Name()] = a
				keys = append(keys, t.Name())
			}
			a.agents = append(a.agents, ak)
		}
	}

	out := make([]ToolSeed, 0, len(keys))
	for _, k := range keys {
		a := m[k]
		agents := a.agents
		if defaultUnbound[k] {
			agents = []string{} // 入目錄、可手動綁，但預設不給任何 agent（存 [] 而非 null，與其它工具一致）
		}
		out = append(out, ToolSeed{
			Key:    k,
			Desc:   a.tool.Description(),
			Schema: a.tool.InputSchema(),
			Agents: agents,
		})
	}
	return out
}

// ToolResolve, if set, post-processes an agent's fully-assembled tool list against
// the DB tools table: it drops tools not bound to this agent (or globally disabled)
// and wraps the rest so the model sees the DB-overridden description/schema and
// 預設入參 get injected. Tools with no matching DB row (MCP/skill/host tools like
// traffic) pass through untouched. nil = tools unchanged. Wired in server/assembly.go.
var ToolResolve func(ctx context.Context, agentKey string, tools []actool.CoreTool) []actool.CoreTool

// DecorateTool wraps t so Description()/InputSchema() report the DB overrides and
// Call() injects scalar parameter defaults (from schema's "default" props) whenever
// the model omitted them. Name/Prompt/permission/scheduler flags delegate to t, so
// the tool's identity and handler are unchanged. Empty desc/schema fall back to t's.
func DecorateTool(t actool.CoreTool, desc string, schema map[string]any) actool.CoreTool {
	if desc == "" {
		desc = t.Description()
	}
	if len(schema) == 0 {
		schema = t.InputSchema()
	}
	return &overriddenTool{CoreTool: t, desc: desc, schema: schema}
}

// overriddenTool is a CoreTool decorator: it embeds the original (so all behavioral
// methods — Prompt/IsReadOnly/IsConcurrencySafe/CheckPermissions/Name — delegate)
// and overrides only the model-facing description/schema plus default injection.
type overriddenTool struct {
	actool.CoreTool
	desc   string
	schema map[string]any
}

func (o *overriddenTool) Description() string         { return o.desc }
func (o *overriddenTool) InputSchema() map[string]any { return o.schema }

func (o *overriddenTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	return o.CoreTool.Call(ctx, injectDefaults(in, o.schema), tc)
}

// injectDefaults fills scalar parameter defaults declared in the (possibly edited)
// schema into the input JSON whenever the model omitted the field or left it empty/
// null. Structure (names/types/required) is untouched — only預設值 are merged in.
func injectDefaults(in json.RawMessage, schema map[string]any) json.RawMessage {
	defs := scalarDefaults(schema)
	if len(defs) == 0 {
		return in
	}
	m := map[string]json.RawMessage{}
	if len(in) > 0 {
		if err := json.Unmarshal(in, &m); err != nil {
			return in // non-object input: don't touch it
		}
	}
	changed := false
	for k, dv := range defs {
		if cur, ok := m[k]; !ok || isEmptyJSON(cur) {
			m[k] = dv
			changed = true
		}
	}
	if !changed {
		return in
	}
	b, err := json.Marshal(m)
	if err != nil {
		return in
	}
	return b
}

// scalarDefaults extracts properties[k]["default"] for scalar params (string/
// integer/number/boolean). Array/object defaults are skipped: merging them is
// ambiguous and not worth the surprise.
func scalarDefaults(schema map[string]any) map[string]json.RawMessage {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	out := map[string]json.RawMessage{}
	for name, raw := range props {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		dv, ok := p["default"]
		if !ok || dv == nil {
			continue
		}
		switch p["type"] {
		case "string", "integer", "number", "boolean":
			if b, err := json.Marshal(dv); err == nil {
				out[name] = b
			}
		}
	}
	return out
}

func isEmptyJSON(raw json.RawMessage) bool {
	s := string(raw)
	return s == "null" || s == `""`
}
