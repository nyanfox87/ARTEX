package server

import (
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// 重試策略的服務端解析，見 docs/LLM重試設計.md。五層裡：
//   - 建連 / 空回應 / 同 provider 安全視窗 是「跟著端點走」的，每個 LLM 配置可以覆蓋
//     全域預設（profile 的某項留空就繼承全域，全域也沒配就用內建預設）；
//   - 熔斷 / 意圖重跑 是程序級的，只有全域一份。
//
// 全域策略讀一次 DB 一行 settings，呼叫點都在低頻路徑（構建 provider、work 收尾、
// 儲存配置），不值得再加一層快取；熔斷引數是例外——它在失敗路徑上每次都要讀，所以
// 由 applyRetryPolicy 推給 Registry 儲存。

// retryPolicy reads the global policy; a nil DB yields the zero policy (all
// layers on their built-in defaults).
func (s *Server) retryPolicy() db.LLMRetryPolicy {
	if s.m == nil || s.m.pg == nil {
		return db.LLMRetryPolicy{}
	}
	return s.m.pg.LLMRetryPolicy()
}

// resolveRetry layers one profile's override on top of the global policy and
// converts the result into the form agent.Config carries. Rules combine field by
// field, so a profile that only pins an interval still inherits the global count.
func resolveRetry(o db.RetryOverride, pol db.LLMRetryPolicy) agent.RetryConfig {
	connect := o.Connect.Or(pol.Connect)
	empty := o.Empty.Or(pol.Empty)
	stream := o.Stream.Or(pol.Stream)
	return agent.RetryConfig{
		// 次數在這裡保持「0=預設 / 負=關閉」的原始語義:SDK 的 MaxRetries /
		// EmptyResponseRetries 與之完全同構,交給它自己解析即可。
		ConnectAttempts: connect.Attempts, ConnectInterval: connect.Interval(),
		EmptyAttempts: empty.Attempts, EmptyInterval: empty.Interval(),
		StreamAttempts: stream.Attempts, StreamInterval: stream.Interval(),
	}
}

// applyProfileRetry fills cfg.Retry for a profile read from the DB.
func (s *Server) applyProfileRetry(cfg *agent.Config, p *db.LLMProfile) {
	if p == nil {
		return
	}
	cfg.Retry = resolveRetry(p.Retry, s.retryPolicy())
}

// 熔斷(輪詢冷卻)的預設值,與 llmpool 內建的一致 —— 這裡只在「使用者配了值」時才覆蓋。
// 意圖重跑的預設值見 engine.go 的 modelErrorRetries / modelErrorRetryBackoff。

// applyRetryPolicy pushes the process-wide layers of the policy into the objects
// that consume them on a hot path: the circuit-breaker registry. Called at
// startup and whenever the policy is saved.
func (s *Server) applyRetryPolicy() {
	pol := s.retryPolicy()
	if s.llmHealth != nil {
		s.llmHealth.SetPolicy(pol.Breaker.Attempts, pol.Breaker.Interval())
	}
}

// modelErrorRetryPolicy resolves the intent-level replay knobs (layer ⑤): how
// many times a model_error work is re-run and how long to back off between runs.
func (e *Engine) modelErrorRetryPolicy() (retries int, backoff time.Duration) {
	retries, backoff = modelErrorRetries, modelErrorRetryBackoff
	if e == nil || e.m == nil || e.m.pg == nil {
		return retries, backoff
	}
	rule := e.m.pg.LLMRetryPolicy().Intent
	if rule.Attempts != 0 {
		retries = max(rule.Attempts, 0)
	}
	if d := rule.Interval(); d > 0 {
		backoff = d
	}
	return retries, backoff
}

// emptyTurnNudgeLimit resolves how many empty-turn continuations one work may
// inject (see steerHooks.Stop). It deliberately reuses layer ②'s knob —— 「空回應
// 重試次數」:兩者是同一件事的兩種手段。SDK 那層管「一個內容塊都沒有」，手段是把
// 同一個請求原樣重發;這裡管「只有思考、既無正文也無工具」，手段是追加一條指令讓
// 模型帶著已有思考接著做(原樣重發對這種由上下文形狀決定的空轉沒有意義)。判空口徑
// 不同是因為 SDK 以「有沒有 yield 過事件」為準，而思考增量本身就是事件——但使用者配
// 「空回應重試幾次」時想表達的是「模型沒產出實質內容就再來一次」，兩層共用一個次數
// 才對得上這個心智。
//
// 讀全域策略而不是某個 profile 的覆蓋:一個 run 中途可能因故障轉移換 profile，而這
// 是整條意圖的總量閘，不該跟著換端點而變。語義與 SDK 的 emptyRetries() 同構:
// 0 = 預設 defaultEmptyTurnNudges;-1(負) = 關閉空轉續跑;>0 = 用該值。
func (e *Engine) emptyTurnNudgeLimit() int {
	if e == nil || e.m == nil || e.m.pg == nil {
		return defaultEmptyTurnNudges
	}
	switch n := e.m.pg.LLMRetryPolicy().Empty.Attempts; {
	case n == 0:
		return defaultEmptyTurnNudges
	case n < 0:
		return 0
	default:
		return n
	}
}
