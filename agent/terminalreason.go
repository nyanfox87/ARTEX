package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/norma/harness"
)

// runTrace retains the latest tool call so an interrupted run can identify the
// operation that was still in flight.
type runTrace struct {
	startedAt time.Time
	id        string
	name      string
	input     string
	at        time.Time
	pending   bool
}

func (t *runTrace) start(id, name, input string) {
	t.id, t.name, t.input, t.at, t.pending = id, name, input, time.Now(), true
}

func (t *runTrace) done(id string) {
	if id == t.id {
		t.pending = false
	}
}

var reasonHint = map[harness.TerminalReason]string{
	harness.ReasonCompleted:         "模型正常結束了本輪，但沒有留下文字總結；事實和資產以本輪工具呼叫記錄為準",
	harness.ReasonMaxTurns:          "達到步數上限(MaxTurns)：SDK 已執行收尾並寫回事實和資產，意圖會標記為 exhausted，供規劃者換方向繼續，而不是作為失敗處理",
	harness.ReasonTimeout:           "達到單次執行的牆鍾預算(MaxDuration)：到點會打斷在跑的工具並就地進收尾，把已識別的事實和資產寫回，意圖會標記為 exhausted",
	harness.ReasonModelError:        "模型或 API 呼叫失敗（網路、鑑權、限流、供應商 5xx 等），重試用盡後意圖示記為 blocked——傳輸層故障導致這條意圖基本沒真正探成；查其執行過程（get_worker_trace）後再決定重派或換法",
	harness.ReasonBlockingLimit:     "上下文長度達到硬上限，請求在發出前被攔截；應收窄意圖粒度或壓縮工具返回",
	harness.ReasonPromptTooLong:     "提示詞過長且上下文壓縮重試已經用盡，無法繼續執行",
	harness.ReasonImageError:        "當前模型不支援本輪多模態內容；請切換支援視覺的模型或避免工具返回圖片",
	harness.ReasonStopHookPrevented: "Stop 鉤子阻止本輪結束，隨後未能繼續；請檢查任務 Guard 規則是否過嚴",
	harness.ReasonHookStopped:       "工具或鉤子主動停止繼續執行，例如越界目標或停用命令；請檢查最後一條 tool_result 的攔截說明",
	harness.ReasonAbortedStreaming:  "執行在模型輸出流式生成階段被取消",
	harness.ReasonAbortedTools:      "執行在工具執行階段被取消",
}

// terminalText renders a terminal event with no final text into a compact summary
// and a Markdown detail block.
func terminalText(ctx context.Context, term *harness.Terminal, tr *runTrace) (string, string) {
	reason := term.Reason
	aborted := reason == harness.ReasonAbortedStreaming || reason == harness.ReasonAbortedTools
	// Prompt may return ctx.Err directly without a terminal event. Preserve the
	// cancellation cause instead of falling back to an empty/unknown terminal reason.
	if reason == "" && ctx.Err() != nil {
		aborted = true
	}

	var sum string
	if aborted {
		_, short, _, ok := AbortReason(ctx)
		if !ok {
			short = "未能取得取消原因"
		}
		stage := "執行過程中"
		switch reason {
		case harness.ReasonAbortedStreaming:
			stage = "模型輸出階段"
		case harness.ReasonAbortedTools:
			stage = "工具執行階段"
		}
		sum = "（執行被中斷：" + short + "；停在" + stage + progressSuffix(term, tr) + "，未完成）"
	} else if reason == harness.ReasonMaxTurns || reason == harness.ReasonTimeout {
		sum = "（達到執行預算上限(" + string(reason) + ")，已收尾寫回事實" + progressSuffix(term, tr) + "；本次無文字總結）"
	} else {
		hint := terminalReasonHint(reason)
		sum = "（無文字總結，終態 " + terminalReasonLabel(reason) + "：" + firstLine(hint, 80) + "）"
	}

	var b strings.Builder
	b.WriteString(sum)
	b.WriteString("\n\n")
	displayReason := terminalReasonLabel(reason)
	fmt.Fprintf(&b, "- **終態**: `%s` - %s\n", displayReason, terminalReasonHint(reason))
	if aborted {
		code, _, why, ok := AbortReason(ctx)
		if ok {
			fmt.Fprintf(&b, "- **中斷原因** (`%s`): %s\n", code, why)
		} else {
			b.WriteString("- **中斷原因**: 無法取得；取消方可能沒有通過 context.WithCancelCause 附加具名原因\n")
		}
	}
	if term.Err != nil {
		fmt.Fprintf(&b, "- **底層錯誤**: `%v`\n", term.Err)
	}
	if aborted && strings.TrimSpace(term.Text) != "" {
		b.WriteString("- **取消前已生成的部分輸出**:\n\n")
		b.WriteString(term.Text)
		b.WriteString("\n\n")
	}
	if term.Turns > 0 {
		fmt.Fprintf(&b, "- **已執行**: %d 輪模型回合\n", term.Turns)
	}
	if !tr.startedAt.IsZero() {
		fmt.Fprintf(&b, "- **本次執行耗時**: %s\n", roundDur(time.Since(tr.startedAt)))
	}
	if u := term.Usage; u.InputTokens+u.OutputTokens+u.CacheReadTokens+u.CacheWriteTokens > 0 {
		fmt.Fprintf(&b, "- **累計 token**: 輸入 %d / 輸出 %d / 快取讀 %d / 快取寫 %d\n",
			u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)
	}
	if tr.name == "" {
		b.WriteString("- **工具呼叫**: 本次執行還沒有發出工具呼叫就結束了\n")
	} else if tr.pending {
		fmt.Fprintf(&b, "- **中斷時正在執行的工具**: `%s`（已執行 %s，**未返回結果**）\n\n  ```json\n  %s\n  ```\n",
			tr.name, roundDur(time.Since(tr.at)), firstLine(tr.input, 300))
	} else {
		fmt.Fprintf(&b, "- **中斷前最後一個工具**: `%s`（已正常返回）\n", tr.name)
	}
	return sum, b.String()
}

func terminalReasonLabel(reason harness.TerminalReason) string {
	if reason == "" {
		return "context_canceled"
	}
	return string(reason)
}

func terminalReasonHint(reason harness.TerminalReason) string {
	if hint := reasonHint[reason]; hint != "" {
		return hint
	}
	if reason == "" {
		return "執行的 context 已取消，但底層沒有產生 Terminal 事件"
	}
	return "未知終態；harness 可能新增了 TerminalReason，請補充 reasonHint"
}

func progressSuffix(term *harness.Terminal, tr *runTrace) string {
	var parts []string
	if term.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d 輪", term.Turns))
	}
	if !tr.startedAt.IsZero() {
		parts = append(parts, roundDur(time.Since(tr.startedAt)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "，已執行 " + strings.Join(parts, " / ")
}

func roundDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	case d < time.Hour:
		return d.Round(time.Second).String()
	default:
		return d.Round(time.Minute).String()
	}
}
