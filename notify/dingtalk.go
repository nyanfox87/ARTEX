package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"
)

// dingTalkChannel 實現釘釘自定義機器人。
//
// 平臺特性（決定了這裡的實現取捨）：
//   - 單機器人限流 20 條/分鐘，超發會被靜默丟棄（HTTP 仍可能 200），
//     所以限流必須在客戶端做，見 DefaultRatePerMin。
//   - 安全設定三選一：加簽 / 自定義關鍵詞 / IP 白名單。加簽是唯一不依賴
//     訊息內容的方案，所以只支援加簽（也支援三者都不開的裸 webhook）。
//   - 成功/失敗都返回 HTTP 200，靠 body 裡的 errcode 區分——不檢查 errcode
//     會把投遞失敗記成成功。
type dingTalkChannel struct{}

func (dingTalkChannel) Kind() string { return KindDingTalk }

func (dingTalkChannel) DefaultRatePerMin() int { return 20 }

// 釘釘的 Webhook 地址裡帶 access_token，本身就是憑據，因此整體掩碼。
func (dingTalkChannel) SecretKeys() []string { return []string{"webhook", "secret"} }

// 目標是釘釘的 Webhook 地址本身；改地址必須同時對新地址重新表態加簽金鑰。
func (dingTalkChannel) DestinationKeys() []string { return []string{"webhook"} }

func (dingTalkChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("缺少 Webhook 地址")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("Webhook 地址無效: %w", err)
	}
	return nil
}

// Send 投遞一次訊息。有回鏈且是單條時用 ActionCard（帶按鈕），否則用 markdown。
func (c dingTalkChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	hook := cfgString(cfg, "webhook")
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	endpoint, err := dingTalkSignedURL(hook, cfgString(cfg, "secret"), time.Now())
	if err != nil {
		return 0, Permanent(err)
	}

	title := markdownTitle(m)
	// 釘釘 markdown 正文無明確位元組上限，但仍做上限保護，避免證據欄位異常膨脹。
	text, kept := markdownBody(m, 20000)

	var payload any
	if !m.Batch && len(m.Items) == 1 && m.Items[0].DetailURL != "" {
		payload = map[string]any{
			"msgtype": "actionCard",
			"actionCard": map[string]any{
				"title":          title,
				"text":           text,
				"btnOrientation": "0",
				"singleTitle":    "檢視詳情",
				"singleURL":      m.Items[0].DetailURL,
			},
		}
	} else {
		payload = map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]any{"title": title, "text": text},
		}
	}

	raw, err := doJSON(ctx, "POST", endpoint, nil, payload)
	if err != nil {
		return 0, err
	}
	// 釘釘把業務錯誤藏在 200 回應裡。
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("解析釘釘回應失敗: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 301000 是簽名校驗失敗、310000 是關鍵詞不匹配——都是配置錯誤，
		// 重試不會自愈。
		return 0, Permanent(fmt.Errorf("釘釘返回錯誤 %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}

// dingTalkSignedURL 按官方加簽規則給 webhook 追加 timestamp 與 sign 引數。
//
// 規則：待籤串 = timestamp + "\n" + secret，HMAC-SHA256 的**金鑰也是 secret**，
// 結果 base64 後 URL 編碼。timestamp 是毫秒。secret 為空時原樣返回，
// 以支援未開啟加簽的機器人。
func dingTalkSignedURL(hook, secret string, now time.Time) (string, error) {
	if secret == "" {
		return hook, nil
	}
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	u, err := url.Parse(hook)
	if err != nil {
		// 不透傳 err：url.Parse 的錯誤文本裡帶完整地址（含 access_token）。
		return "", fmt.Errorf("解析 Webhook 地址失敗: %s", redactRequestTarget(hook))
	}
	q := u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", sign)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// validateHTTPURL 校驗地址可用、協議受支援，並對字面 IP 目標做內網判斷。
//
// 兩點講究：
//
//  1. **錯誤資訊必須脫敏**。url.Parse 自己返回的是 *url.Error，它的 Error() 帶
//     **完整原始地址**，而本功能這幾家的地址裡就嵌著憑據（釘釘 access_token、
//     企微 key、Telegram 的 bot token、飛書 hook id）。曾經這裡直接 `return err`，
//     於是「地址格式非法」這條錯誤就把憑據帶了出去，流向測試介面的 400 回應、
//     每次投遞落庫的 last_error、服務端日誌與投遞歷史介面。
//
//  2. **字面 IP 直接判內網**，域名留給撥號階段判（blockInternalDial 才是最終
//     生效點，也能覆蓋 DNS 重綁定）。這裡做一次是為了讓儲存配置時就能得到提示，
//     而不是等到第一次投遞失敗。
//
// 限制協議是防禦性的：file:///gopher:// 之類會讓 http.Client 產生意料之外的
// 行為（雖已被 scheme 檢查擋下，但沒有理由放開這個面）。
func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("地址無法解析（%s）", redactRequestTarget(raw))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("只支援 http/https，收到 %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("缺少主機名")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && isBlockedDialIP(ip) && !allowLocalTargets() {
		return fmt.Errorf("拒絕投遞到本機/鏈路本地地址 %s（如確需投遞到本機服務，設定 %s=1）", ip, AllowLocalTargetsEnv)
	}
	return nil
}
