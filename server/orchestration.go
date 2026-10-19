package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// jsonResult marshals v to a JSON tool result.
func jsonResult(v any) (actool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// 本檔案實現 P2「跨任務編排工具集」(docs/跑分編排 §2 P2)。這些是 host 工具——需要
// 訪問 Manager(任意任務的 Store)、Engine(暫停)、以及建任務流程,所以住在 server 層。
// 讀類工具把「現有 per-task 工具」重定向到目標任務的 store 上跑(建一個臨時 ToolSet
// 並 Call 其對應工具),從而複用完全相同的邏輯;控制類(spawn/pause)直接調 Manager/Engine。
// 它們像流量工具一樣 seed 進 tools 表、按 agent 綁定(只綁給編排 agent 才可見)。

// hostTools is the runtime host-tool provider fed to ToolAugment: traffic tools
// (gated by capture) + cross-task orchestration tools + user-defined custom tools.
// The second return is the names of custom tools flagged `deferred` (schema
// withheld, routed via SearchExtraTools/ExecuteExtraTool). Per-agent binding still
// decides who actually sees any of them.
//
//nolint:unused // used as the hostTools provider in wireAgentAugment
func (s *Server) hostTools() ([]actool.CoreTool, map[string][]string) {
	tools := append(s.m.HostTools(), s.orchestrationTools()...)
	tools = append(tools, s.findingRetestTools()...)
	tools = append(tools, s.platformTools()...) // 平臺操作工具(建改 skill/工具/MCP，給 Auto 用)
	custom, err := s.customTools()
	if err != nil {
		log.Printf("[custom-tool] 載入失敗: %v", err)
		return tools, nil
	}
	tools = append(tools, custom...)
	// deferred custom tools → name -> its bound agent keys. ToolAugment turns a
	// name into a deferred entry only for agents it's actually bound to (so we don't
	// advertise a tool the per-agent binding will drop from the callable set).
	deferred := map[string][]string{}
	rows, _ := s.m.pg.ListCustomTools()
	for _, t := range rows {
		if t.Deferred && t.Enabled {
			deferred[t.Key] = t.Agents
		}
	}
	return tools, deferred
}

// orchestrationTools returns the cross-task tool set. Bound per-agent via the
// tools table (default: no binding — opt-in for orchestration agents).
func (s *Server) orchestrationTools() []actool.CoreTool {
	return append([]actool.CoreTool{
		s.toolListTasks(),
		s.toolListLLMProfiles(),
		s.toolSpawnTask(),
		s.toolPauseTask(),
		s.toolGetTaskGraph(),
		s.toolListTaskFindings(),
		s.toolAddHint(),
		s.toolGetWorkerTrace(),
		s.toolListWorkerTraces(),
		s.toolSearchWorkerTraces(),
		s.toolGetTaskNodeDetail(),
		s.toolUpdateFindingReport(),
		s.toolGetFindingTraffic(),
		s.toolBindFindingTraffic(),
	}, s.findingCaseTools()...)
}

// --- schema helpers ---

func strParam(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// parseProfileID reads an LLM profile id from a tool arg that may arrive as a JSON
// number (5) or a numeric string ("5"); returns 0 when absent/unparseable.
func parseProfileID(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(str), 10, 64)
		return v
	}
	return 0
}

func objSchema(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		m["required"] = req
	}
	return m
}

func roTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:   func(json.RawMessage) bool { return true },
		Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func wrTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// delegateToTask resolves the `task_id` in the input, builds a ToolSet bound to
// that task's store, strips task_id, and calls the chosen per-task tool — so the
// cross-task read reuses the exact in-task logic against another task.
func (s *Server) delegateToTask(ctx context.Context, in json.RawMessage, pick func(*agent.ToolSet) actool.CoreTool) (actool.Result, error) {
	var head struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(in, &head)
	if strings.TrimSpace(head.TaskID) == "" {
		return actool.Errorf("task_id 為必填"), nil
	}
	t, ok := s.m.Task(head.TaskID)
	if !ok {
		return actool.Errorf("task 不存在: " + head.TaskID), nil
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(in, &m)
	delete(m, "task_id")
	inner, _ := json.Marshal(m)
	tsx := agent.NewToolSet(t.Store, "orchestrator")
	if s.m.Assets() != nil {
		tsx.SetAssetStore(s.m.Assets(), s.m.Assets().Companies())
	}
	tsx.SetNotify(t.Notify)         // 通用喚醒（無專用回撥的寫操作走它；讀工具為 no-op）
	tsx.SetNotifyHint(t.NotifyHint) // add_hint → 記一條「人新增了 N 條戰略提示：…」觸發並喚醒 planner
	return pick(tsx).Call(ctx, inner, nil)
}

// --- tools ---

func (s *Server) toolListTasks() actool.CoreTool {
	return roTool("list_tasks",
		"列出所有任務(id/描述/目標/狀態/執行時長/父任務/LLM 配置)，編排 agent 用它掌握全域、看哪些任務卡太久、各自用哪個 LLM。執行時長：執行中=建立→現在，終態=建立→最後活動(秒)。llm_profile：任務 planner/worker 用的配置名，(啟用配置)=跟隨全域啟用。",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			lastAct, _ := s.m.PG().LastActivityAll()
			// id -> name to resolve each task's pinned LLM profile.
			profName := map[int64]string{}
			if profs, err := s.m.pg.ListProfiles(); err == nil {
				for _, p := range profs {
					profName[p.ID] = p.Name
				}
			}
			out := make([]map[string]any, 0)
			for _, t := range s.m.List() {
				status := s.deriveTaskStatus(t)
				end := lastAct[t.ExpID]
				if live := s.engine.LastActivity(t.ID); live > end {
					end = live
				}
				dur := int64(0)
				if status == "running" {
					dur = time.Now().Unix() - t.CreatedAt
				} else if end > t.CreatedAt {
					dur = end - t.CreatedAt
				}
				row := map[string]any{"id": t.ID, "description": t.Description, "goal": t.Goal, "status": status, "run_seconds": dur}
				if t.ParentRef != "" {
					row["parent_ref"] = t.ParentRef
				}
				llmState := t.llmStateSnapshot()
				if llmState.ProfileID == nil {
					row["llm_profile"] = "(啟用配置)"
				} else if n, ok := profName[*llmState.ProfileID]; ok {
					row["llm_profile"] = n
				} else {
					row["llm_profile"] = fmt.Sprintf("#%d(已刪除)", *llmState.ProfileID)
				}
				out = append(out, row)
			}
			return jsonResult(out)
		})
}

// toolListLLMProfiles lists the available LLM profiles (name/model/active) so an
// orchestration agent can pick one for spawn_task's llm_profile. Never leaks keys.
func (s *Server) toolListLLMProfiles() actool.CoreTool {
	return roTool("list_llm_profiles",
		"列出可用的 LLM 配置(profile)：id、名稱、模型、格式、是否為當前啟用配置。用 id 給 spawn_task 的 llm_profile_id 引數指定子任務專屬 LLM（如偵察用便宜模型、利用用強模型）。不含 API Key。",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			profs, err := s.m.pg.ListProfiles()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out := make([]map[string]any, 0, len(profs))
			for _, p := range profs {
				out = append(out, map[string]any{
					"id": p.ID, "name": p.Name, "model": p.Model, "format": p.Format, "is_active": p.IsDefault,
				})
			}
			return jsonResult(map[string]any{"profiles": out})
		})
}

func (s *Server) toolSpawnTask() actool.CoreTool {
	return wrTool("spawn_task",
		"新建一個子任務並啟動探索引擎，返回 task_id。用於把一件事(如一道題/一個目標)派成獨立任務。parent_ref 可選：填當前編排關聯的父任務 id 做父子關聯。",
		objSchema(map[string]any{
			"description":            strParam("任務描述(簡短標題)"),
			"goal":                   strParam("任務目標(要達成什麼)"),
			"parent_ref":             strParam("可選：父任務 id(做父子關聯)"),
			"source_task_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": fmt.Sprintf("可選：只讀繼承的來源任務 id 列表(最多 %d 個)。子任務可只讀引用這些任務已探明的資產/結論作為起點；與 parent_ref 的純父子指標不同，這是內容繼承。", db.MaxTaskSourceCount)},
			"llm_profile_id":         map[string]any{"type": "integer", "description": "可選：指定本子任務 planner/worker 用的 LLM 配置 id(見 list_llm_profiles)；留空則繼承父任務、再回退全域啟用配置"},
			"timeout_seconds":        map[string]any{"type": "integer", "description": "可選：任務級超時(秒)。到點後觸發優雅收尾並進入 timeout 終態；留空或 0 = 不限時"},
			"plan_heartbeat_seconds": map[string]any{"type": "integer", "description": "可選：planner 心跳觸發間隔(秒)。距上輪規劃結束/任務開始滿該值且期間無觸發 → 觸發一輪規劃(兜底死鎖 + 喚醒去監督飛行中的 worker)。留空或 0 = 預設 600(10min)；"},
			"seed_first_intent":      map[string]any{"type": "boolean", "description": "可選：對於簡單任務可開啟，建立時直接下發一條種子意圖(內容=描述+目標)讓 worker 免等首輪 planner 直接開跑測試；預設 false(走標準先規劃再執行)。"},
		}, "description", "goal"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Description          string          `json:"description"`
				Goal                 string          `json:"goal"`
				ParentRef            string          `json:"parent_ref"`
				SourceTaskIDs        []string        `json:"source_task_ids"`
				LLMProfileID         json.RawMessage `json:"llm_profile_id"`
				TimeoutSeconds       int             `json:"timeout_seconds"`
				PlanHeartbeatSeconds int             `json:"plan_heartbeat_seconds"`
				SeedFirstIntent      bool            `json:"seed_first_intent"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Description) == "" {
				a.Description = "未命名任務"
			}
			if strings.TrimSpace(a.Goal) == "" {
				return actool.Errorf("goal 為必填"), nil
			}
			if a.TimeoutSeconds < 0 {
				a.TimeoutSeconds = 0
			}
			// 只讀繼承來源任務：數量上限 + 每個 id 有效/去重/存在，校驗規則與 HTTP 建任務一致。
			if len(a.SourceTaskIDs) > db.MaxTaskSourceCount {
				return actool.Errorf(fmt.Sprintf("關聯任務最多選擇 %d 個", db.MaxTaskSourceCount)), nil
			}
			sourceIDs := make([]int64, 0, len(a.SourceTaskIDs))
			seenSources := map[int64]bool{}
			for _, raw := range a.SourceTaskIDs {
				id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
				if err != nil || id <= 0 || seenSources[id] {
					return actool.Errorf("關聯任務 id 無效或重複"), nil
				}
				if _, ok := s.m.Task(strconv.FormatInt(id, 10)); !ok {
					return actool.Errorf(fmt.Sprintf("關聯任務 #%d 不存在", id)), nil
				}
				seenSources[id] = true
				sourceIDs = append(sourceIDs, id)
			}
			// LLM profile resolution: explicit id > inherit parent's pin > active(nil).
			var pin *int64
			if id := parseProfileID(a.LLMProfileID); id > 0 {
				if _, ok := s.loadProfileConfig(id); !ok {
					return actool.Errorf(fmt.Sprintf("LLM 配置 #%d 不存在或未設定 API Key", id)), nil
				}
				pin = &id
			} else if a.ParentRef != "" {
				if pt, ok := s.m.Task(a.ParentRef); ok {
					pin = pt.LLMProfileID
				}
			}
			var llmIDs []int64
			if pin != nil {
				llmIDs = []int64{*pin}
			}
			t, err := s.m.CreateTaskWithOptions(a.Description, a.Goal, db.TaskCreateOptions{
				SourceTaskIDs:        sourceIDs,
				LLMProfileIDs:        llmIDs,
				TimeoutSeconds:       a.TimeoutSeconds,
				PlanHeartbeatSeconds: a.PlanHeartbeatSeconds,
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if a.ParentRef != "" {
				t.ParentRef = a.ParentRef
				if id, e := strconv.ParseInt(t.ID, 10, 64); e == nil {
					_ = s.m.PG().SetParentRef(id, a.ParentRef)
				}
			}
			// 共享的建後流程,與 HTTP 建任務(server.go createTask)複用同一段 launchTask:
			// seed + 後臺可見地做目標分解(第0輪/LLM步驟/逐條goal) + engine.Run。
			// seed_first_intent 預設 false(標準先規劃再執行);簡單任務可開啟直接下發一 work 測試。
			s.launchTask(t, a.Description+" "+a.Goal, a.SeedFirstIntent)
			return actool.Text(fmt.Sprintf("task created: %s", t.ID)), nil
		})
}

func (s *Server) toolPauseTask() actool.CoreTool {
	return wrTool("pause_task", "暫停指定任務(停止其 planner/worker 迴圈)。",
		objSchema(map[string]any{"task_id": strParam("要暫停的任務 id")}, "task_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				TaskID string `json:"task_id"`
			}
			_ = json.Unmarshal(in, &a)
			t, ok := s.m.Task(a.TaskID)
			if !ok {
				return actool.Errorf("task 不存在: " + a.TaskID), nil
			}
			if _, err := s.applyTaskControlWithCause(t, "pause", agent.AbortPausedByOrchestrator); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text("task paused: " + a.TaskID), nil
		})
}

func (s *Server) toolGetTaskGraph() actool.CoreTool {
	return roTool("get_task_graph", "讀指定任務的探索圖總覽(同 graph_overview：資產計數/frontier/發現/覆蓋等)，用 task_id 指定任務。",
		objSchema(map[string]any{"task_id": strParam("任務 id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).GraphOverviewTool)
		})
}

func (s *Server) toolListTaskFindings() actool.CoreTool {
	return roTool("list_task_findings", "讀指定任務的確認漏洞(含 flag/PoC；每條帶 id/task_id/intent_id/vulnclass/severity/摘要/狀態)，用 task_id 指定任務。",
		objSchema(map[string]any{"task_id": strParam("任務 id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListFindingsTool)
		})
}

func (s *Server) toolAddHint() actool.CoreTool {
	return wrTool("add_task_hint", "給指定任務注入戰略提示(該任務的 planner 下輪生成意圖時會讀到)。\n"+
		"★優先批次：多條提示放進 hints 陣列一次提交（返回 ids 陣列，與 hints 等長同序，失敗項 id=0）；單條則省略 hints 直接給頂層 text。",
		objSchema(map[string]any{
			"task_id":      strParam("任務 id"),
			"hints":        map[string]any{"type": "array", "description": "【優先用這個】提示陣列，每個元素欄位同頂層（text/asset_ids/traffic_refs）。", "items": objSchema(map[string]any{"text": strParam("提示內容"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": agent.HintTrafficSchema()})},
			"text":         strParam("[單條] 提示內容"),
			"traffic_refs": agent.HintTrafficSchema(),
			"asset_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "錨定的資產 id（可選，0/1/多個；該任務內的資產 id）"},
		}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).AddHintTool)
		})
}

func (s *Server) toolGetWorkerTrace() actool.CoreTool {
	return roTool("get_task_worker_trace",
		"看指定任務裡某個 work(意圖)的執行過程：get_task_worker_trace(task_id, intent_id) 看步驟摘要；再帶 step_ids=[...] 取那幾步完整內容(一次最多 5 個,多傳只返回前 5 個)。",
		objSchema(map[string]any{
			"task_id":   strParam("任務 id"),
			"intent_id": map[string]any{"type": "integer", "description": "意圖 id(該任務裡的 work)"},
			"step_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "可選：要取完整內容的步驟 id(一次最多 5 個,多傳只返回前 5 個,其餘在 omitted_step_ids 裡列出)"},
		}, "task_id", "intent_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).GetWorkerTraceTool)
		})
}

func (s *Server) toolListWorkerTraces() actool.CoreTool {
	return roTool("list_task_worker_traces", "列出指定任務裡跑過哪些 work(意圖) + 各自步數，用於發現哪些 work 值得翻看(再用 get_task_worker_trace)。",
		objSchema(map[string]any{"task_id": strParam("任務 id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListWorkerTracesTool)
		})
}

func (s *Server) toolSearchWorkerTraces() actool.CoreTool {
	return roTool("search_task_worker_traces", "在指定任務裡按關鍵字搜尋所有 work 的執行過程(返回命中步驟摘要 + intent_id)。",
		objSchema(map[string]any{"task_id": strParam("任務 id"), "q": strParam("搜尋關鍵字")}, "task_id", "q"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).SearchWorkerTracesTool)
		})
}

func (s *Server) toolGetTaskNodeDetail() actool.CoreTool {
	return roTool("get_task_node_detail",
		"讀指定任務裡某個探索圖節點的完整內容(發現/事實/意圖/目標：摘要 + 詳情/證據/PoC)。id 為探索節點 id(如 report_finding 返回、或 list_task_findings 裡的 id)。寫漏洞報告前用它取該漏洞的完整證據。",
		objSchema(map[string]any{
			"task_id": strParam("任務 id"),
			"id":      map[string]any{"type": "integer", "description": "探索圖節點 id(非資產 id)"},
		}, "task_id", "id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).NodeDetailTool)
		})
}

// toolUpdateFindingReport writes/overwrites a finding's detailed Markdown report.
// finding_id is the id report_finding returned ("finding recorded: <id>", the
// finding node id). The write (SetFindingReportByNodeID) is keyed by node_id and
// task-agnostic, so this host tool needs no task_id / exploration store.
func (s *Server) toolUpdateFindingReport() actool.CoreTool {
	return wrTool("update_finding_report",
		"為已登記的漏洞寫入/更新【詳細報告】(Markdown 全文,整段覆蓋舊內容)。finding_id 傳 report_finding 返回的那個 id(\"finding recorded: <id>\" 裡的數字)。報告建議包含:漏洞概述、影響與危害、重現步驟、證據/PoC、修復建議。",
		objSchema(map[string]any{
			"finding_id":       map[string]any{"type": "integer", "description": "目標漏洞 id(report_finding 返回的 id)"},
			"report":           strParam("詳細報告全文,Markdown 格式"),
			"evidence_version": map[string]any{"type": "integer", "description": "get_finding_traffic 返回的證據 version；用於防止報告覆蓋新的證據變更"},
		}, "finding_id", "report"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				EvidenceVersion *int64          `json:"evidence_version"`
				FindingID       json.RawMessage `json:"finding_id"`
				Report          string          `json:"report"`
			}
			_ = json.Unmarshal(in, &a)
			nodeID := parseProfileID(a.FindingID) // 複用「數字或數字字串」解析
			if nodeID <= 0 {
				return actool.Errorf("finding_id 無效"), nil
			}
			fid, err := s.m.pg.FindingIDByNodeID(nodeID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if fid > 0 {
				if err := s.checkCaseReviewScope(ctx, "update_finding_report", findingCaseRequest{FindingID: json.RawMessage(i64s(fid))}); err != nil {
					return actool.Errorf(err.Error()), nil
				}
			}
			n, err := s.m.pg.SetFindingReportVersionByNodeID(ctx, nodeID, a.Report, a.EvidenceVersion)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == 0 {
				return actool.Errorf(fmt.Sprintf("未找到 finding_id=%d 對應的漏洞記錄(先用 report_finding 登記)", nodeID)), nil
			}
			return actool.Text(fmt.Sprintf("finding %d report updated (%d chars)", nodeID, len(a.Report))), nil
		})
}

// deriveTaskStatus mirrors listTasks' status derivation for the list_tasks tool.
func (s *Server) deriveTaskStatus(t *Task) string {
	lifecycle := t.lifecycleSnapshot()
	switch {
	case isTerminalStatus(lifecycle.Status):
		return lifecycle.Status
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		return "paused"
	case s.engine.ReadyFor(t) && s.engine.Started(t.ID):
		return "running"
	}
	return "created"
}

// orchestrationToolSeeds seeds the cross-task tools into the tools table so they
// are bindable per-agent (default: bound to nobody — opt-in for orchestration
// agents). First-insert only, like the traffic seeds.
func (s *Server) seedOrchestrationTools() {
	// task-op + platform tools default-bind to the built-in Auto agent (它天生用來
	// 操作平臺)。SeedTool 首插入生效;老庫已 seed 的行由 seedAutoDefaultBindings 補綁。
	autoAgents, _ := json.Marshal([]string{"auto"})
	for _, t := range s.orchestrationTools() {
		schema, _ := json.Marshal(t.InputSchema())
		bindings := autoAgents
		if t.Name() == "bind_finding_traffic" || strings.Contains(t.Name(), "finding_case") || t.Name() == "search_finding_duplicates" || t.Name() == "get_finding_record" || t.Name() == "merge_finding_records" || t.Name() == "suggest_finding_merge" {
			bindings = json.RawMessage(`["reporter"]`)
		}
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, bindings)
	}
	for _, t := range s.platformTools() {
		schema, _ := json.Marshal(t.InputSchema())
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, autoAgents)
	}
	s.refreshBuiltinToolSchemas()
	s.seedAutoDefaultBindings()
	s.seedPlannerDefaultBindings()
	s.seedPlannerListAssetsBinding()
	s.seedCompanyScopeRebind()
	s.seedWorkerReadToolsUnbind() // list_facts/list_companies/list_worker_traces 從 worker 預設解綁(一次性)
	s.seedWorkerReadbackRebind()  // 修復舊遷移誤刪：把 search_all_worker_traces/get_worker_trace/node_detail 補綁回 worker(一次性)
	s.seedAutoReportFindingBinding()
	s.unbindGoalMetDefault()
	s.reseedGoalsPrompt()             // goals 提示詞加入「抽操作約束」步 → 舊庫追加一版新預設(一次性)
	s.reseedMainAgentPrompt()         // mainagent 提示詞加入「目標達成後 add_intent 反問是否建目標」(一次性)
	s.reseedPlannerPrompt()           // planner 提示詞:重寫「0 意圖」正當理由 + 加量化驗收核對(一次性)
	s.reseedWorkerPrompt()            // worker 提示詞:加否定結論證據門檻(一次性)
	s.seedReporterAgent()             // 預置「報告撰寫」agent + 工具綁定 + finding 觸發器(一次性)
	s.upgradeReporterTriggerMessage() // 老庫補遷移:讓 reporter 回傳 evidence_version(一次性)
	s.seedFindingTrafficTools()       // 增加可選證據引數及只讀證據工具，保留使用者配置
	s.seedFindingWorkflowTools()
	s.seedFindingCaseTools()
	// 注：pentest 的預設工具綁定無需遷移——BuiltinToolSeeds 在全新初始化時就把
	// list_assets/insert_assets/report_finding/list_findings/list_companies 連同
	// pentest 一起 seed 好了（專案尚無舊庫，不做遷移）。
}

// refreshBuiltinToolSchemas propagates code schema/description changes on the
// orchestration + platform tools into already-seeded rows ONCE per version flag —
// SeedTool is first-insert-only, so a new param (e.g. spawn_task 的 llm_profile) never
// reaches an old DB otherwise. Preserves each tool's agent binding + enabled flag.
// Bump the flag whenever these tools' schemas/descriptions change in code.
func (s *Server) refreshBuiltinToolSchemas() {
	const flag = "tool_schema_refresh_v7_list_facts_paging"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	tools := append(s.orchestrationTools(), s.platformTools()...)
	for _, t := range tools {
		schema, _ := json.Marshal(t.InputSchema())
		if err := s.m.pg.RefreshToolDefaults(t.Name(), t.Description(), schema); err != nil {
			log.Printf("[tools] refresh %s schema failed: %v", t.Name(), err)
		}
	}
	// 同時把部分內建 agent 工具刷成程式碼預設：
	//   - goal_met：舊庫 seed 的描述帶“結束本輪規劃”的誤導，會讓 planner 把它當成
	//     “結束空輪”的手段、剛開跑就誤判整個任務完成。
	//   - insert_assets：新增 related 入參(標記資產是否與當前任務相關、決定是否入覆蓋度)，
	//     SeedTool 首插入only，舊庫已 seed 的 schema 否則收不到這個新引數。
	//   - list_facts：改為分頁，新增 limit/before/q 入參；舊庫已 seed 的空 schema 否則
	//     在工具管理頁顯示「無引數」，模型也拿不到這幾個引數說明。
	refreshBuiltin := map[string]bool{"goal_met": true, "insert_assets": true, "list_facts": true}
	for _, sd := range agent.BuiltinToolSeeds() {
		if !refreshBuiltin[sd.Key] {
			continue
		}
		schema, _ := json.Marshal(sd.Schema)
		if err := s.m.pg.RefreshToolDefaults(sd.Key, sd.Desc, schema); err != nil {
			log.Printf("[tools] refresh %s desc failed: %v", sd.Key, err)
		}
	}
	_ = s.m.pg.SetSetting(flag, "true")
	log.Printf("[tools] 已重新整理 orchestration/platform 工具 schema 到程式碼預設(一次性)")
}

// unbindGoalMetDefault removes goal_met's default "planner" binding ONCE (guarded by
// a settings flag), so existing DBs match the new default of NO agent. goal_met bypasses
// per-goal prove_goal to declare the whole task done — powerful/risky and redundant with
// the prove_goal→auto-complete path — so it ships unbound; users can re-bind it per agent
// in the UI. A user's own binding to another agent is untouched (we only strip planner).
func (s *Server) unbindGoalMetDefault() {
	const flag = "goal_met_unbind_default_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.RemoveAgentFromTool("planner", "goal_met"); err != nil {
		log.Printf("[tools] goal_met 解綁 planner 失敗: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// reseedGoalsPrompt 把 goals 目標拆解器的提示詞刷成【當前程式碼預設】——因為預設正文新增了
// 「先抽操作約束(set_constraints)再拆目標」這一步,而 SeedPromptIfEmpty 首插入only,舊庫
// 已有的 version 1 收不到這步。這裡用版本管理【追加一個新版本】並切過去(ResetPromptToDefault),
// 舊的版本仍保留在歷史裡,使用者若自定義過可從版本記錄找回。settings flag 守衛 → 只做一次;
// 以後預設再變就 bump 這個 flag。全新庫無需處理(SeedPromptIfEmpty 已 seed 最新預設)。
func (s *Server) reseedGoalsPrompt() {
	const flag = "goals_prompt_constraint_step_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 無論成功與否只嘗試一次
	a, err := s.m.pg.GetAgentByKey("goals")
	if err != nil || a == nil {
		return // 全新庫尚未建 agent 行時,seedPrompts 會直接 seed 最新預設,無需此遷移
	}
	tmpl := agent.BuiltinPromptSeeds()["goals"]
	if tmpl == "" {
		return
	}
	// 全新庫 seedPrompts 已 seed 最新預設 → 當前版本已等於程式碼預設,不必再追加重複版本。
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] goals 提示詞重刷為新預設失敗: %v", err)
		return
	}
	log.Printf("[prompts] goals 提示詞已追加新預設版本(加入抽操作約束步,一次性)")
}

// reseedMainAgentPrompt 把 mainagent 提示詞刷成【當前程式碼預設】——預設正文新增了「目標全部
// 達成後 add_intent 直投意圖時,反問人是否登記為正式目標」這段引導,而 SeedPromptIfEmpty 首插入
// only,舊庫已有版本收不到。用版本管理【追加一個新版本】並切過去(ResetPromptToDefault),舊版本仍
// 保留在歷史裡,使用者若自定義過可從版本記錄找回。settings flag 守衛 → 只做一次。全新庫無需處理
// (SeedPromptIfEmpty 已 seed 最新預設)。與 reseedGoalsPrompt 完全同構。
func (s *Server) reseedMainAgentPrompt() {
	const flag = "mainagent_prompt_goalless_intent_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 無論成功與否只嘗試一次
	a, err := s.m.pg.GetAgentByKey("mainagent")
	if err != nil || a == nil {
		return // 全新庫尚未建 agent 行時,seedPrompts 會直接 seed 最新預設,無需此遷移
	}
	tmpl := agent.BuiltinPromptSeeds()["mainagent"]
	if tmpl == "" {
		return
	}
	// 全新庫 seedPrompts 已 seed 最新預設 → 當前版本已等於程式碼預設,不必再追加重複版本。
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] mainagent 提示詞重刷為新預設失敗: %v", err)
		return
	}
	log.Printf("[prompts] mainagent 提示詞已追加新預設版本(加入目標達成後反問建目標,一次性)")
}

// reseedPlannerPrompt 把 planner 提示詞刷成【當前程式碼預設】——預設正文做了精簡重構,並把「剋制」降級為
// 僅去重、新增「深度優先於覆蓋度」「硬底線:目標未達成且無在跑意圖必須產出」、給否定結論複核加上界。
// 每次預設有實質變更就 bump 下面的 flag(當前 v2)讓存量舊庫再刷一次。SeedPromptIfEmpty 首插入only,舊庫已有版本收不到,故用版本管理
// 【追加一個新版本】並切過去(ResetPromptToDefault),舊版本仍保留在歷史裡,使用者若自定義過可從版本記錄
// 找回。settings flag 守衛 → 只做一次。全新庫無需處理(SeedPromptIfEmpty 已 seed 最新預設)。與
// reseedGoalsPrompt 完全同構。
func (s *Server) reseedPlannerPrompt() {
	const flag = "planner_prompt_compact_realistic_v2"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 無論成功與否只嘗試一次
	a, err := s.m.pg.GetAgentByKey("planner")
	if err != nil || a == nil {
		return // 全新庫尚未建 agent 行時,seedPrompts 會直接 seed 最新預設,無需此遷移
	}
	tmpl := agent.BuiltinPromptSeeds()["planner"]
	if tmpl == "" {
		return
	}
	// 全新庫 seedPrompts 已 seed 最新預設 → 當前版本已等於程式碼預設,不必再追加重複版本。
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] planner 提示詞重刷為新預設失敗: %v", err)
		return
	}
	log.Printf("[prompts] planner 提示詞已追加新預設版本(精簡重構+剋制降級去重+深度優先+否定複核上界,一次性)")
}

// reseedWorkerPrompt 把 worker 提示詞刷成【當前程式碼預設】——預設正文 record_fact 段刪掉了「否定類結論
// 寫觀察+試探性讀法」整句、並把 confidence(observed/inferred)與「是否窮盡本意圖手段」解耦(這些易誤導規劃者),
// 同時把 facts 陣列分條收緊為「彼此完全獨立、無法歸併」的極少數例外。bump flag 至 v3 讓存量舊庫再刷一次。
// SeedPromptIfEmpty 首插入only,舊庫已有版本收不到,故用版本管理【追加一個新版本】並切過去,舊版本仍保留在歷史裡可找回。
// settings flag 守衛 → 只做一次。全新庫無需處理。與 reseedGoalsPrompt 完全同構。
func (s *Server) reseedWorkerPrompt() {
	const flag = "worker_prompt_compact_v4"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 無論成功與否只嘗試一次
	a, err := s.m.pg.GetAgentByKey("worker")
	if err != nil || a == nil {
		return // 全新庫尚未建 agent 行時,seedPrompts 會直接 seed 最新預設,無需此遷移
	}
	tmpl := agent.BuiltinPromptSeeds()["worker"]
	if tmpl == "" {
		return
	}
	// 全新庫 seedPrompts 已 seed 最新預設 → 當前版本已等於程式碼預設,不必再追加重複版本。
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] worker 提示詞重刷為新預設失敗: %v", err)
		return
	}
	log.Printf("[prompts] worker 提示詞已追加新預設版本(查上下文段收斂為 list_assets/list_findings,去掉 list_facts/node_detail/asset_neighbors,一次性)")
}

// reporterToolCallMessage 必須無條件要求先讀一次 get_finding_traffic 再寫報告。
// 該工具是隻讀的、「不依賴捕獲開關」,自動綁定關不關都能讀到人工綁定的證據。若這裡
// 寫成「啟用自動綁定才讀」,預設關閉配置下 reporter 就不會傳 evidence_version,
// SetFindingReportVersionByNodeID 便按 legacy 語義寫 -1,漏洞詳情與 Markdown 匯出
// 從此常駐「證據已變更，報告待更新」,而 UI 上沒有任何入口能把它清掉。
const reporterToolCallMessage = "上面剛有一個漏洞被 report_finding 登記。請讀取返回 JSON 的 finding_id（獨立漏洞記錄 ID）與 finding_node_id（探索節點 ID），" +
	"先用 get_finding_traffic(finding_id) 讀取當前證據清單及其 version（空清單是正常情況，照常寫報告）；" +
	"若執行指引啟用自動綁定，在讀取前先核實並關聯本次漏洞的流量。節點詳情使用 finding_node_id。" +
	"最後呼叫 update_finding_report(finding_id=finding_node_id, report, evidence_version=實際讀取版本) 儲存，" +
	"evidence_version 必須傳，否則報告會被永久標記為待更新。不要混用兩種編號。"

// 舊版觸發訊息(0.3.8 及更早)。只有仍與它逐字相同的記錄才會被遷移覆蓋，使用者改過的保持原樣。
const reporterToolCallMessageV1 = "上面剛有一個漏洞被 report_finding 登記。請從觸發上下文裡取出 finding_id" +
	"（工具返回 \"finding recorded: <id>\" 裡的數字）與任務 id，按你的職責撰寫該漏洞的詳細報告，" +
	"最後呼叫 update_finding_report(finding_id, report) 儲存。"

// upgradeReporterTriggerMessage 把老庫裡仍是預設文案的 reporter 觸發訊息刷成新版本。
// seedReporterAgent 受 reporter_agent_seed_v1 守衛且只在新建 agent 時寫觸發器，所以
// 升級上來的庫拿不到新文案 —— 工具 schema 由 seedFindingTrafficTools 補齊了
// evidence_version，但沒有任何東西告訴 reporter 去用它。一次性，且只覆蓋未被改動的文案。
func (s *Server) upgradeReporterTriggerMessage() {
	const flag = "reporter_trigger_evidence_version_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 只嘗試一次
	triggers, err := s.m.pg.ListTriggersFor("reporter")
	if err != nil {
		log.Printf("[reporter] 讀取觸發器失敗: %v", err)
		return
	}
	for _, t := range triggers {
		if !t.OnToolCall || t.ToolCallMessage != reporterToolCallMessageV1 {
			continue // 使用者改過或不是 finding 觸發器，不動。
		}
		t.ToolCallMessage = reporterToolCallMessage
		if err := s.m.pg.UpdateTrigger(t); err != nil {
			log.Printf("[reporter] 升級觸發訊息失敗: %v", err)
			return
		}
		log.Printf("[reporter] 觸發訊息已升級為讀取並回傳 evidence_version")
	}
}

// seedReporterAgent 預置一個「報告撰寫」自定義 agent(builtin=false，可在 UI 編輯/刪除)：
// 綁定 update_finding_report + 任務查詢工具，並掛一個「report_finding 被呼叫即觸發」的
// 觸發器 —— 每登記一個漏洞就喚起它寫詳細報告。一次性(settings flag 守衛)：使用者刪掉後不再重建。
// 依賴：orchestration 工具已在本函式上方 SeedTool 入庫，故綁定得上。
func (s *Server) seedReporterAgent() {
	const flag = "reporter_agent_seed_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 無論成功與否只嘗試一次

	if exist, _ := s.m.pg.GetAgentByKey("reporter"); exist != nil {
		return // key 已被佔用(使用者手建過)——不覆蓋
	}
	a, err := s.m.pg.CreateAgent("reporter", "報告撰寫",
		"漏洞詳細報告撰寫：發現漏洞時自動觸發，查取證據與執行過程後寫 Markdown 報告並回寫。")
	if err != nil {
		log.Printf("[reporter] 建立 agent 失敗: %v", err)
		return
	}
	if err := s.m.pg.SeedPromptIfEmpty(a.ID, agent.ReporterDefaultPrompt); err != nil {
		log.Printf("[reporter] seed prompt 失敗: %v", err)
	}
	// 觸發執行策略：parallel + none —— 一漏洞一報告、多個 finding 併發各寫各的。
	// merge 必須為 none：否則(預設 all)一波 finding 會被合併成一次執行，並行就沒意義。
	// maxParallel=5：同時最多 5 個報告會話，避免瞬時太多 LLM 呼叫。
	if err := s.m.pg.SetAgentTriggerBehavior("reporter", "parallel", "none", 5); err != nil {
		log.Printf("[reporter] 設定觸發執行策略失敗: %v", err)
	}
	// 綁定它需要的工具：寫報告 + 讀證據/執行過程/態勢。
	if err := s.m.pg.AddAgentToToolBinding("reporter", []string{
		"update_finding_report", "get_task_node_detail", "list_task_findings",
		"get_task_worker_trace", "list_task_worker_traces", "search_task_worker_traces",
		"get_task_graph",
	}); err != nil {
		log.Printf("[reporter] 綁定工具失敗: %v", err)
	}
	// 觸發器：report_finding 被呼叫即觸發（工具返回 "finding recorded: <id>" 帶上 finding_id，
	// 任務 id 也在觸發訊息裡）。
	if _, err := s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey:        "reporter",
		Enabled:         true,
		OnToolCall:      true,
		ToolNames:       []string{"report_finding"},
		ToolCallMessage: reporterToolCallMessage,
	}); err != nil {
		log.Printf("[reporter] 建立觸發器失敗: %v", err)
	}
	log.Printf("[reporter] 已預置「報告撰寫」agent + finding 觸發器")
}

// seedAutoReportFindingBinding adds "auto" to report_finding's binding ONCE so
// conversation-context agents can call it without requiring an intent_id.
func (s *Server) seedAutoReportFindingBinding() {
	const flag = "auto_report_finding_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("auto", []string{"report_finding"}); err != nil {
		log.Printf("[auto] report_finding 預設綁定失敗: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedPlannerDefaultBindings adds "planner" to report_finding's binding ONCE
// (guarded by a settings flag), so existing DBs — whose report_finding row was
// seeded as worker-only — also let the planner record findings. Fresh DBs already
// get it via PlannerTools(); this only backfills without overriding a user unbind.
func (s *Server) seedPlannerDefaultBindings() {
	const flag = "planner_report_finding_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"report_finding"}); err != nil {
		log.Printf("[planner] report_finding 預設綁定失敗: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedPlannerListAssetsBinding adds "planner" to list_assets's binding ONCE
// (guarded by a settings flag), so existing DBs — whose list_assets row was seeded
// as auto/pentest-only — also let the planner query the asset store by DSL. Fresh
// DBs already get it via PlannerTools(); this only backfills without overriding a
// user unbind.
func (s *Server) seedPlannerListAssetsBinding() {
	const flag = "planner_list_assets_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"list_assets"}); err != nil {
		log.Printf("[planner] list_assets 預設綁定失敗: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedCompanyScopeRebind changes add_company_scope's default binding ONCE on
// existing DBs (guarded by a settings flag): the tool moves off worker and onto
// planner — defining a company's asset scope is a planning/main/auto concern, not
// something a worker does mid-exploration. Fresh DBs already get planner via
// PlannerTools() and lack worker via WorkerTools(); this only backfills old rows.
// One-shot + flag-guarded so a user who later re-binds worker isn't overridden.
func (s *Server) seedCompanyScopeRebind() {
	const flag = "company_scope_rebind_v1" // worker→planner 預設綁定切換
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"add_company_scope"}); err != nil {
		log.Printf("[planner] add_company_scope 預設綁定失敗: %v", err)
		return
	}
	if err := s.m.pg.RemoveAgentFromTool("worker", "add_company_scope"); err != nil {
		log.Printf("[worker] add_company_scope 解綁失敗: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedWorkerReadToolsUnbind strips the read-context tools off worker's default
// binding ONCE on existing DBs (guarded by a settings flag): a worker executes one
// intent and writes back — reading facts/companies and listing all workers' traces is
// a planning/main concern, not the executor's. Fresh DBs already lack these via
// WorkerTools(); this only backfills old rows without overriding a user who
// deliberately re-binds worker. Each RemoveAgentFromTool is per-tool +
// membership-guarded, so planner/mainagent bindings of the same tool are untouched.
//
// NOTE: search_all_worker_traces / get_worker_trace / node_detail are intentionally NOT
// unbound — worker owns them for cross-work look-back + node drill-down (see WorkerTools).
// They used to be in this list back when worker lacked them; seedWorkerReadbackRebind
// repairs DBs whose old run stripped them.
func (s *Server) seedWorkerReadToolsUnbind() {
	const flag = "worker_readtools_unbind_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	for _, k := range []string{
		"list_facts", "list_companies", "list_worker_traces",
	} {
		if err := s.m.pg.RemoveAgentFromTool("worker", k); err != nil {
			log.Printf("[worker] %s 從 worker 解綁失敗: %v", k, err)
			return // 出錯則不落 flag，下次啟動重試
		}
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedWorkerReadbackRebind re-binds the cross-work look-back / drill-down tools onto
// worker ONCE (guarded by a settings flag): an earlier seedWorkerReadToolsUnbind wrongly
// stripped search_all_worker_traces / get_worker_trace / node_detail from worker after
// they had been added to WorkerTools(), so any DB that ran that migration lost them.
// Fresh DBs already have them via WorkerTools() and this is a harmless no-op there.
// One-shot + flag-guarded so a user who later deliberately unbinds them isn't overridden.
func (s *Server) seedWorkerReadbackRebind() {
	const flag = "worker_readback_rebind_v2" // v2: 追加 node_detail
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("worker", []string{
		"search_all_worker_traces", "get_worker_trace", "node_detail",
	}); err != nil {
		log.Printf("[worker] 回看/詳情工具補綁失敗: %v", err)
		return // 出錯則不落 flag，下次啟動重試
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedAutoDefaultBindings adds "auto" to the task-op + platform tools' bindings
// ONCE (guarded by a settings flag), so existing DBs whose tool rows were seeded
// before Auto existed still give Auto its default toolset — without re-adding it
// after a user deliberately unbinds.
func (s *Server) seedAutoDefaultBindings() {
	const flag = "auto_default_bindings_v3" // v3: 替換舊資產工具名，加入 insert_assets/add_company_scope
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	keys := make([]string, 0, len(platformToolKeys)+12)
	for _, t := range s.orchestrationTools() {
		keys = append(keys, t.Name())
	}
	keys = append(keys, platformToolKeys...)
	// 資產工具：Auto 操作平臺常要看/登記資產、管理公司範圍。
	keys = append(keys, "insert_assets", "add_company_scope", "list_assets")
	if err := s.m.pg.AddAgentToToolBinding("auto", keys); err != nil {
		log.Printf("[auto] 預設綁定失敗: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}
