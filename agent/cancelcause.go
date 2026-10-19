package agent

import (
	"context"
	"errors"
	"fmt"
)

// AbortCause names why an agent run's context was cancelled. Every cancellation
// site should attach one so the activity trace can report the real initiator.
type AbortCause struct {
	Code  string
	Short string
	Text  string
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text}
}

// Causef builds a cause that includes runtime-specific detail.
func Causef(code, short, format string, args ...any) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: fmt.Sprintf(format, args...)}
}

var (
	// Task-level execution context.
	AbortPausedBySchedule = cause("paused_by_schedule", "計劃視窗結束，暫停任務", "任務將在下一個計劃視窗恢復，本次執行已停止")
	AbortPausedByUser     = cause("paused_by_user", "使用者暫停了任務",
		"使用者通過任務控制介面（POST /api/tasks/{id}/control，action=pause）暫停了任務。本次 Planner/Worker 執行被主動取消；執行中的意圖會退回 frontier(open)，恢復任務後重新領取並從頭執行")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "編排 Agent 暫停了任務",
		"編排 Agent 呼叫了 pause_task 工具暫停本任務。本次 Planner/Worker 執行被主動取消；執行中的意圖會退回 frontier(open)，恢復後重新執行")
	AbortTaskDeleted = cause("task_deleted", "任務被刪除",
		"任務正在刪除（DELETE /api/tasks/{id}），刪除屏障已取消該任務正在執行的 Planner、Worker 和主 Agent；本次執行結果不會再被使用")
	AbortPausedOnReload = cause("paused_on_reload", "後端恢復了任務的暫停狀態",
		"後端啟動時根據資料庫中持久化的狀態恢復了任務暫停。本次執行被取消；正常情況下恢復階段沒有正在執行的 Agent")
	AbortGoalMet = cause("goal_met", "規劃者判定任務目標已達成",
		"規劃者判定任務目標已達成並將任務置為 done，隨後取消仍在執行的 Worker；這些意圖會標記為 stopped，而不是失敗")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "任務超時收尾的等待時間已用盡",
		"任務到達 timeout 後等待正在執行的 Worker 優雅收尾，但 90 秒 drain 寬限仍不足，因此執行硬取消；意圖會標記為 exhausted，收尾階段已經寫入的事實和資產會保留")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "規劃者終止了這條意圖",
		"規劃者呼叫 kill_work 主動終止了這條意圖，通常表示方向跑偏或已無繼續價值；意圖會標記為 stopped，不會自動重新領取")
	AbortWorkPausedByUser = cause("work_paused_by_user", "使用者暫停了這條 Worker 意圖",
		"使用者暫停了正在執行的 Worker。本次呼叫被取消，意圖轉為 paused；已經登記的意圖、事實、漏洞和活動記錄全部保留，恢復後從頭重新執行")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "使用者刪除了這條 Worker 意圖",
		"使用者刪除了正在執行的 Worker。本次呼叫被取消；Worker 退出寫入區後，服務端按使用者選擇的刪除模式處理該意圖——假刪除僅標記為已刪除並保留全部產出，真刪除會級聯移除該意圖及僅由它支撐的下游節點")
	AbortWorkFinished = cause("work_finished", "Worker 已正常結束並釋放 context",
		"Worker 已正常結束，引擎在 detachWork 中釋放其 context 資源。這不是執行中斷；若它出現在中斷訊息中，說明取消與收場事件發生了競態")
	AbortPausedRaceGuard = cause("paused_race_guard", "任務暫停期間拒絕啟動新執行",
		"任務處於暫停狀態時，引擎拒絕發出新的執行 context，用於防止 claim 與暫停之間的競態導致 Worker 繼續啟動；已領取的意圖會退回 frontier")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "使用者停止了本輪對話",
		"使用者點選了停止，主動中止本輪主 Agent 或會話 Agent 執行。已經產生的活動記錄會保留，可以繼續傳送下一條訊息")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "任務暫停並中止了主 Agent 對話",
		"使用者暫停任務時，正在執行的主 Agent 對話也被同步取消。已經產生的活動記錄會保留；恢復任務後不會自動重放本輪訊息")
	AbortChatTurnFinished = cause("chat_turn_finished", "本輪對話已正常結束並釋放 context",
		"本輪對話已正常結束，服務端正在釋放該輪 context 資源。這不是執行中斷；若它出現在中斷訊息中，說明取消與收場事件發生了競態")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "後端程序正在關閉",
		"後端程序收到 SIGINT 或 SIGTERM，正在重啟、更新或關閉。所有執行中的 Agent 會被取消；重啟後殘留的 running 意圖會重置為 open 並重新執行")
	AbortRunHardTimeout = cause("run_hard_timeout", "單次執行的硬超時兜底已觸發",
		"單次執行超過軟牆鍾預算及額外寬限，說明模型請求或某個工具長時間沒有返回，導致正常的回合邊界收尾無法執行。請重點檢查中斷前最後一個未返回的工具呼叫")
)

// AbortReason resolves the named cause attached to a cancelled run context.
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	c := context.Cause(ctx)
	if c == nil {
		return "", "", "", false
	}
	var ac *AbortCause
	if errors.As(c, &ac) {
		return ac.Code, ac.Short, ac.Text, true
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return "deadline_exceeded", "上游 context 到達 deadline",
			"上游 context 到達 deadline，但設定方沒有通過 WithTimeoutCause 附加具名原因: " + c.Error(), true
	case errors.Is(c, context.Canceled):
		return "canceled_no_cause", "取消方未附加具名原因",
			"上游 context 被取消，但取消方沒有通過 context.WithCancelCause 附加具名原因；請在 agent/cancelcause.go 登記原因並接入該取消點", true
	default:
		return "other", firstLine(c.Error(), 80), c.Error(), true
	}
}
