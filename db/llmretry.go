package db

import (
	"encoding/json"
	"time"
)

// LLM 重試策略：五層重試的「次數 + 間隔」全域配置，見 docs/LLM重試設計.md。
// 存在 settings 表的一個 JSON 值裡 —— 它是整機一份的執行引數，不值得為它開一張表；
// 讀取走內建預設兜底，所以鍵不存在(全新庫/從未配置過)時行為與寫死常量時代完全一致。

const settingLLMRetryPolicy = "llm_retry_policy"

// RetryRule is one layer's knob pair. The zero value means "unset":
//
//	Attempts   0 = 用內建預設次數; -1 = 關閉該層重試; >0 = 用該值
//	IntervalMS 0 = 用該層原本的間隔策略(通常是指數退避); >0 = 改用固定毫秒間隔
//
// -1 是「顯式關掉」而不是「0 次」，因為 0 已經被「未配置」佔用了。
type RetryRule struct {
	Attempts   int `json:"attempts"`
	IntervalMS int `json:"interval_ms"`
}

// Interval returns the configured fixed interval, or 0 when unset (caller keeps
// its own default ladder).
func (r RetryRule) Interval() time.Duration {
	if r.IntervalMS <= 0 {
		return 0
	}
	return time.Duration(r.IntervalMS) * time.Millisecond
}

// Or returns the rule with each unset field filled in from fallback. Used to
// layer a profile override on top of the global policy field by field, so a
// profile that only pins the interval still inherits the global count.
func (r RetryRule) Or(fallback RetryRule) RetryRule {
	if r.Attempts == 0 {
		r.Attempts = fallback.Attempts
	}
	if r.IntervalMS == 0 {
		r.IntervalMS = fallback.IntervalMS
	}
	return r
}

// retry knob bounds. A count above the cap turns a blip into a token bonfire;
// an interval above an hour outlives any transient failure worth waiting out.
const (
	maxRetryAttempts   = 20
	maxRetryIntervalMS = 3600_000 // 1h
)

// Clamped returns the rule with out-of-range values pulled back into the sane
// band (attempts within [-1, 20], interval within [0, 1h]).
func (r RetryRule) Clamped() RetryRule {
	if r.Attempts < -1 {
		r.Attempts = -1
	}
	if r.Attempts > maxRetryAttempts {
		r.Attempts = maxRetryAttempts
	}
	if r.IntervalMS < 0 {
		r.IntervalMS = 0
	}
	if r.IntervalMS > maxRetryIntervalMS {
		r.IntervalMS = maxRetryIntervalMS
	}
	return r
}

// Clamped bounds a profile's override the same way the global policy is bounded,
// so a hand-crafted API payload can't land a value the CHECK constraint rejects.
func (o RetryOverride) Clamped() RetryOverride {
	o.Connect, o.Empty, o.Stream = o.Connect.Clamped(), o.Empty.Clamped(), o.Stream.Clamped()
	return o
}

// LLMRetryPolicy holds the五層 retry configuration. Connect/Empty/Stream are the
// per-request layers (a profile may override them, see LLMProfile.Retry);
// Breaker and Intent are process-wide by nature and live only here.
type LLMRetryPolicy struct {
	// Connect：SDK 建連重試(連線重置/超時/429/5xx，流開始前)。預設 3 次、指數退避。
	Connect RetryRule `json:"connect"`
	// Empty：SDK 空回應重試(完成但無 content block，僅 openai 格式)。預設 2 次、指數退避。
	Empty RetryRule `json:"empty"`
	// Stream：同 provider 安全視窗重試(未交付輸出前的斷流重放)。預設 2 次、0.5s 起指數(封頂 4s)。
	Stream RetryRule `json:"stream"`
	// Breaker：輪詢熔斷。Attempts=連續幾次瞬時失敗觸發熔斷(預設 3，-1=瞬時失敗不熔斷，
	// 硬失敗如餘額不足/金鑰失效仍立即熔斷)；IntervalMS=固定冷卻時長(0=預設 1/5/30min 梯度)。
	Breaker RetryRule `json:"breaker"`
	// Intent：worker 以 model_error 收場後的整條意圖重跑。預設 2 次、固定 3s。
	Intent RetryRule `json:"intent"`
}

// Clamped returns the policy with every rule clamped.
func (p LLMRetryPolicy) Clamped() LLMRetryPolicy {
	p.Connect, p.Empty, p.Stream = p.Connect.Clamped(), p.Empty.Clamped(), p.Stream.Clamped()
	p.Breaker, p.Intent = p.Breaker.Clamped(), p.Intent.Clamped()
	return p
}

// LLMRetryPolicy reads the global retry policy. A missing or unparseable value
// yields the zero policy — i.e. every layer on its built-in default.
func (d *DB) LLMRetryPolicy() LLMRetryPolicy {
	var p LLMRetryPolicy
	if d == nil {
		return p
	}
	raw, ok, err := d.GetSetting(settingLLMRetryPolicy)
	if err != nil || !ok || raw == "" {
		return p
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return LLMRetryPolicy{}
	}
	return p.Clamped()
}

// SetLLMRetryPolicy persists the global retry policy (values are clamped first).
func (d *DB) SetLLMRetryPolicy(p LLMRetryPolicy) error {
	raw, err := json.Marshal(p.Clamped())
	if err != nil {
		return err
	}
	return d.SetSetting(settingLLMRetryPolicy, string(raw))
}
