package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// 總覽「目標管理」的人工 CRUD 介面。與 agent 側的 set_goals 工具寫同一批 goal 節點,
// 但入口是人類在 UI 上直接增刪改;新增/修改後複用「復活任務」邏輯(admitTask resume:
// 終態→running、解除暫停、必要時排隊),刪除不復活(按產品決策)。每個變更 handler 都走
// beginTaskOperation/decInflight,避免與任務刪除競態(與意圖 CRUD 一致)。

// listGoals 返回本任務的全部目標(text/vulnclass/state 拆好),供目標管理卡片渲染。
func (s *Server) listGoals(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	goals, err := t.Store.ListByKind(db.KindGoal, 10000)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"goals": goalDTOs(goals)})
}

// addGoal 人工新增一個目標:落庫(掛到任務根 spawns 下)→ 記一條「新增了目標」觸發喚醒
// planner → 復活任務,讓規劃者據新目標重判是否達成。
func (s *Server) addGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任務正在刪除,無法新增目標")
		return
	}
	defer s.engine.decInflight(t.ID)

	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "目標內容不能為空")
		return
	}
	payload := map[string]any{"text": text}
	if vc := strings.TrimSpace(body.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	id, err := t.Store.AddGoal(payload, "human")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if of, _ := t.Store.OriginFactID(); of > 0 && id > 0 {
		_ = t.Store.Link(of, db.RelSpawns, id) // goal descends from the task root (origin fact)
	}
	t.NotifyGoal([]string{text}) // 記「人新增了目標:…」觸發並喚醒 planner
	s.reviveTask(t)              // 把已完成/暫停的任務拉回執行態繼續跑
	node, _ := t.Store.GetNode(id)
	if node == nil {
		writeErr(w, 500, "目標寫入後讀取失敗")
		return
	}
	writeJSON(w, 200, goalDTO(node))
}

// editGoal 人工修改一個目標文本(及 vulnclass):改庫 → 記「使用者修改了目標由 old 變為 new」
// 觸發喚醒 planner → 復活任務,讓規劃者據新目標調整方向。
func (s *Server) editGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任務正在刪除,無法修改目標")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "目標內容不能為空")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, "目標不存在")
		return
	}
	oldText := goalDTO(node).Text
	if err := t.Store.UpdateGoalPayload(gid, text, strings.TrimSpace(body.VulnClass)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalEdited(oldText, text) // 記「人修改了目標由 old 變為 new」觸發並喚醒 planner
	s.reviveTask(t)                   // 與新增一致:復活任務據新目標重判
	updated, _ := t.Store.GetNode(gid)
	if updated == nil {
		writeErr(w, 500, "目標更新後讀取失敗")
		return
	}
	writeJSON(w, 200, goalDTO(updated))
}

// deleteGoal 人工刪除一個目標(硬刪除,級聯刪邊/錨點):刪庫 → 記「使用者刪除了該目標 X」
// 觸發喚醒 planner 據此重判剩餘目標。按產品決策,刪除【不】復活任務。
func (s *Server) deleteGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任務正在刪除,無法刪除目標")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, "目標不存在")
		return
	}
	text := goalDTO(node).Text
	if err := t.Store.DeleteGoal(gid); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalDeleted(text) // 記「人刪除了該目標:…」觸發並喚醒 planner(不復活任務)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
