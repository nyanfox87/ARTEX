package server

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

// 空轉回合(僅思考、無正文無工具)的識別與續跑，見 steerHooks.Stop。

func assistantThinking(text string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: text, Signature: "sig"},
	}}
}

func TestIsThinkingOnlyTurn(t *testing.T) {
	toolUse := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: "先掃埠"},
		{Type: llm.BlockToolUse, ID: "t1", Name: "run_nuclei"},
	}}
	cases := []struct {
		name string
		msgs []llm.Message
		want bool
	}{
		{"僅思考", []llm.Message{llm.UserText("開始"), assistantThinking("想想")}, true},
		{"思考+工具", []llm.Message{llm.UserText("開始"), toolUse}, false},
		{"思考+正文", []llm.Message{assistantThinking("想想"), {
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "x"}, llm.TextBlock("結論")},
		}}, false},
		{"正文只有空白字元", []llm.Message{{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "x"}, llm.TextBlock("  \n ")},
		}}, true},
		{"完全空的 assistant 回合", []llm.Message{{Role: llm.RoleAssistant}}, true},
		// 工具結果是 user 角色，判定必須回溯到它前面那條 assistant，而不是就近誤判。
		{"最後一條是工具結果", []llm.Message{toolUse, {
			Role:    llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.BlockToolResult, ToolUseID: "t1"}},
		}}, false},
		{"沒有 assistant 訊息", []llm.Message{llm.UserText("開始")}, false},
		{"空歷史", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isThinkingOnlyTurn(c.msgs); got != c.want {
				t.Fatalf("isThinkingOnlyTurn = %v, want %v", got, c.want)
			}
		})
	}
}

// fakeHooks 是一個可程式設計的 inner HookRunner，用來驗證 steerHooks 對 inner 決定的尊重。
type fakeHooks struct {
	prevent  bool
	blocking []string
	msg      string
}

func (f fakeHooks) PreToolUse(context.Context, string, []byte) (bool, string, []byte) {
	return false, "", nil
}
func (f fakeHooks) PostToolUse(context.Context, string, []byte, []byte, bool) {}
func (f fakeHooks) Stop(context.Context, []llm.Message) (bool, []string, string) {
	return f.prevent, f.blocking, f.msg
}

func TestSteerHooksStopNudgesEmptyTurn(t *testing.T) {
	empty := []llm.Message{assistantThinking("我應該先列舉子域名")}

	t.Run("空轉回合注入續跑指令", func(t *testing.T) {
		h := steerHooks{nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges, label: "worker-1 · #1"}
		prevent, blocking, _ := h.Stop(context.Background(), empty)
		if prevent {
			t.Fatal("空轉回合不應硬停")
		}
		if len(blocking) != 1 || blocking[0] != emptyTurnNudge {
			t.Fatalf("blocking = %v, want [emptyTurnNudge]", blocking)
		}
	})

	t.Run("有正文或工具時不介入", func(t *testing.T) {
		h := steerHooks{nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges}
		normal := []llm.Message{{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{llm.TextBlock("已完成掃描，未發現開放埠")},
		}}
		if _, blocking, _ := h.Stop(context.Background(), normal); blocking != nil {
			t.Fatalf("正常收場被誤判為空轉: %v", blocking)
		}
		if n := h.nudges.Load(); n != 0 {
			t.Fatalf("未介入時不應計數, got %d", n)
		}
	})

	t.Run("達到上限後放行收場", func(t *testing.T) {
		const limit = 5 // 使用者把「空回應重試次數」配成 5
		h := steerHooks{nudges: &atomic.Int64{}, limit: limit}
		for i := 1; i <= limit; i++ {
			if _, blocking, _ := h.Stop(context.Background(), empty); len(blocking) != 1 {
				t.Fatalf("第 %d 次應仍在配額內, blocking = %v", i, blocking)
			}
		}
		if _, blocking, _ := h.Stop(context.Background(), empty); blocking != nil {
			t.Fatalf("超出上限仍在注入: %v", blocking)
		}
	})

	// 「空回應重試次數」配 -1 = 關掉這層，emptyTurnNudgeLimit 解析成 0。
	t.Run("配置關閉時不介入", func(t *testing.T) {
		h := steerHooks{nudges: &atomic.Int64{}, limit: 0}
		if _, blocking, _ := h.Stop(context.Background(), empty); blocking != nil {
			t.Fatalf("已關閉仍在注入: %v", blocking)
		}
	})

	t.Run("inner 決定硬停時不疊加", func(t *testing.T) {
		h := steerHooks{inner: fakeHooks{prevent: true, msg: "guard 拒絕收場"}, nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges}
		prevent, blocking, msg := h.Stop(context.Background(), empty)
		if !prevent || msg != "guard 拒絕收場" || blocking != nil {
			t.Fatalf("inner 的硬停被改寫: prevent=%v blocking=%v msg=%q", prevent, blocking, msg)
		}
		if n := h.nudges.Load(); n != 0 {
			t.Fatalf("讓位給 inner 時不應消耗配額, got %d", n)
		}
	})

	t.Run("inner 已要續跑時不疊加", func(t *testing.T) {
		h := steerHooks{inner: fakeHooks{blocking: []string{"guard 的續跑理由"}}, nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges}
		_, blocking, _ := h.Stop(context.Background(), empty)
		if len(blocking) != 1 || blocking[0] != "guard 的續跑理由" {
			t.Fatalf("inner 的續跑訊息被改寫: %v", blocking)
		}
	})

	t.Run("未裝計數器時行為不變", func(t *testing.T) {
		h := steerHooks{limit: defaultEmptyTurnNudges} // 例如未來其他呼叫點忘了傳 nudges
		if _, blocking, _ := h.Stop(context.Background(), empty); blocking != nil {
			t.Fatalf("無計數器時不應注入: %v", blocking)
		}
	})
}
