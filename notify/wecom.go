package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// weComMarkdownLimit 是企微群機器人 markdown content 的硬上限（位元組，非字元）。
// 這是全部六個渠道里最緊的限制，也是 TruncateBytes 存在的主要原因。
const weComMarkdownLimit = 4096

// weComChannel 實現企業微信群機器人。
//
// 平臺特性：
//   - 唯一通過 URL 上的 key 鑑權，不支援加簽——所以 webhook 地址本身就是全部憑據。
//   - markdown content 上限 4096 **位元組**，超長整條被拒（不是截斷）。中文 3 位元組/字，
//     意味著正文只有一千多字可寫，必須客戶端截斷。
//   - 限流 20 條/分鐘，同樣靠客戶端限流兜住。
type weComChannel struct{}

func (weComChannel) Kind() string { return KindWeCom }

func (weComChannel) DefaultRatePerMin() int { return 20 }

// 企業微信只有 Webhook 一處憑據（URL 上的 key），且它不支援加簽——
// 整個地址就是全部憑據，沒有別的欄位需要掩碼。
func (weComChannel) SecretKeys() []string { return []string{"webhook"} }

// 企微只有 Webhook 一處欄位，它既是目的地也是憑據，因此沒有「改地址後殘留的憑據」可言。
func (weComChannel) DestinationKeys() []string { return []string{"webhook"} }

func (weComChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("缺少 Webhook 地址")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("Webhook 地址無效: %w", err)
	}
	return nil
}

func (c weComChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	// 彙總批可能很長（50 條 × 每條一行 + 字首），4096 位元組很容易超。
	// 截斷在這裡做而不是靠平臺報錯：被拒意味著這一批全丟，而截斷至少送達前若干條。
	content, kept := markdownBody(m, weComMarkdownLimit)
	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": content},
	}
	raw, err := doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("解析企業微信回應失敗: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 45009 是介面呼叫超過限制——平臺的限流視窗會滾動，退避後重試是有效的，
		// 所以顯式歸為可重試。走到這裡說明客戶端 rate_per_min 配得過於激進，
		// 重試只是兜底，真正的修法是調低該渠道的限流值。
		if res.ErrCode == 45009 {
			return 0, fmt.Errorf("企業微信限流 %d: %s", res.ErrCode, res.ErrMsg)
		}
		// 93000 是 webhook key 無效——永久失敗，重試不會自愈。
		return 0, Permanent(fmt.Errorf("企業微信返回錯誤 %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}
