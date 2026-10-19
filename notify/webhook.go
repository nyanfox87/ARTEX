package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"
)

// webhookChannel 是通用 Webhook 介面卡：使用者自定 URL、方法、請求頭與 JSON 模板。
// 它的存在讓本功能不必為 Slack / Mattermost / Discord / 自建系統各寫一個實現——
// 那些平臺都能被一個可配模板覆蓋。
type webhookChannel struct{}

func (webhookChannel) Kind() string { return KindWebhook }

// 通用 Webhook 沒有官方限制，返回 0 表示預設不限流，由使用者按對端能力自定。
func (webhookChannel) DefaultRatePerMin() int { return 0 }

// 掩碼 url 與 headers：目標地址本身常帶 token，自定義頭裡通常放著鑑權憑據，
// 兩者都會出現在介面回顯裡，所以都要擋。
// 代價是編輯時若想改動其中一個頭，需要重新填整組頭（掩碼值會被解釋為「保持原值」）——
// 這個取捨是刻意的：寧可多填一次，也不把憑據回顯到瀏覽器。
func (webhookChannel) SecretKeys() []string { return []string{"url", "headers"} }

// 目的地是 url。改 url 時必須重新表態 headers —— 否則原始 Authorization 頭
// 會被原樣發到新地址，這正是掩碼繞過的主路徑。
func (webhookChannel) DestinationKeys() []string { return []string{"url"} }

// webhookDefaultTemplate 是未填模板時的兜底請求體：一個直白的 JSON 結構，
// 覆蓋絕大多數「收一條 JSON 入庫」的自建接收端。
const webhookDefaultTemplate = `{
  "title": {{json .Title}},
  "batch": {{.Batch}},
  "count": {{.Count}},
  "items": [
{{- range $i, $it := .Items}}
{{- if $i}},{{end}}
    {
      "finding_id": {{$it.FindingID}},
      "name": {{json $it.Name}},
      "vulnclass": {{json $it.VulnClass}},
      "severity": {{json $it.Severity}},
      "summary": {{json $it.Summary}},
      "assets": {{json $it.Assets}},
      "detail_url": {{json $it.DetailURL}}
    }
{{- end}}
  ]
}`

// webhookTemplateData 是暴露給使用者模板的上下文。
type webhookTemplateData struct {
	Title   string
	Batch   bool
	Count   int
	Items   []webhookItem
	HomeURL string
	// SentAt 是本次投遞時間（RFC3339），供接收端記錄。
	SentAt string
}

type webhookItem struct {
	FindingID     int64
	Name          string
	VulnClass     string
	Severity      string
	SeverityLabel string
	Summary       string
	Assets        []string
	DetailURL     string
	FromStatus    string
	ToStatus      string
	// StatusLabel 是狀態變更的可讀描述，如「待處理 → 已修復」；非狀態變更時為空。
	StatusLabel string
}

func (webhookChannel) Validate(cfg map[string]any) error {
	raw := cfgString(cfg, "url")
	if raw == "" {
		return errors.New("缺少目標 URL")
	}
	if err := validateHTTPURL(raw); err != nil {
		return fmt.Errorf("目標 URL 無效: %w", err)
	}
	if m := strings.ToUpper(cfgString(cfg, "method")); m != "" && m != http.MethodGet && m != http.MethodPost && m != http.MethodPut && m != http.MethodPatch {
		return fmt.Errorf("不支援的方法 %s（可用 GET/POST/PUT/PATCH）", m)
	}
	if tpl := cfgString(cfg, "body_template"); tpl != "" {
		if _, err := parseWebhookTemplate(tpl); err != nil {
			return fmt.Errorf("請求體模板語法錯誤: %w", err)
		}
	}
	return nil
}

func (c webhookChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	method := strings.ToUpper(cfgString(cfg, "method"))
	if method == "" {
		method = http.MethodPost
	}

	// GET 不帶請求體：把內容塞進 query 超出模板能力範圍，也不符合 GET 語義，
	// 所以 GET 只適合「命中即觸發鉤子」這類接收端。
	var payload any
	if method != http.MethodGet {
		body, err := renderWebhookBody(cfgString(cfg, "body_template"), m)
		if err != nil {
			return 0, Permanent(err)
		}
		// 模板渲染出的是字串形式的 JSON，這裡轉成 json.RawMessage 原樣發出，
		// 避免二次轉義把使用者精心構造的結構套進一個 JSON 字串裡。
		if !json.Valid([]byte(body)) {
			return 0, Permanent(errors.New("請求體模板渲染結果不是合法 JSON"))
		}
		payload = json.RawMessage(body)
	}

	headers := cfgMap(cfg, "headers")
	if ct := cfgString(cfg, "content_type"); ct != "" {
		// 允許覆蓋，但放在 headers 之後應用，保證顯式配置優先。
		if headers == nil {
			headers = map[string]string{}
		}
		headers["Content-Type"] = ct
	}
	if _, err := doJSON(ctx, method, cfgString(cfg, "url"), headers, payload); err != nil {
		return 0, err
	}
	// 通用 Webhook 不截斷正文（接收端是使用者自己的服務，體積由 body_template 決定），
	// 因此整批都算送達。
	return len(m.Items), nil
}

// renderWebhookBody 用使用者模板（或預設模板）渲染請求體。
func renderWebhookBody(tpl string, m Message) (string, error) {
	if strings.TrimSpace(tpl) == "" {
		tpl = webhookDefaultTemplate
	}
	t, err := parseWebhookTemplate(tpl)
	if err != nil {
		return "", fmt.Errorf("請求體模板語法錯誤: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, newWebhookTemplateData(m)); err != nil {
		return "", fmt.Errorf("渲染請求體模板失敗: %w", err)
	}
	return buf.String(), nil
}

// parseWebhookTemplate 解析模板。
//
// missingkey=zero 讓缺失的 map 鍵渲染成零值而不是報錯——但本檔案的上下文是結構體，
// 主要作用是讓 .Items 為空時 range 不出錯。真正需要防的是 .Items 為 nil。
func parseWebhookTemplate(tpl string) (*template.Template, error) {
	return template.New("body").Funcs(webhookTemplateFuncs).Option("missingkey=zero").Parse(tpl)
}

// webhookTemplateFuncs 是暴露給模板的輔助函式。
var webhookTemplateFuncs = template.FuncMap{
	// json 把任意值序列化成 JSON。
	//
	// 這個函式不是錦上添花而是必需的：省去它，使用者只能寫 {{.Title}} 直接插值，
	// 而漏洞標題裡只要有引號或換行，整段請求體就不再是合法 JSON——接收端會
	// 拒收，且報錯資訊指向「JSON 解析失敗」，完全聯想不到是標題裡有個引號。
	"json": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	},
	// jsons 用於把 JSON 片段嵌進另一段 JSON 字串值內部（做一層字串轉義）。
	"jsons": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		quoted, err := json.Marshal(string(raw))
		if err != nil {
			return "", err
		}
		// 去掉外層引號：呼叫方自己決定要不要加引號。
		return string(quoted[1 : len(quoted)-1]), nil
	},
}

func newWebhookTemplateData(m Message) webhookTemplateData {
	d := webhookTemplateData{
		Title:   markdownTitle(m),
		Batch:   m.Batch,
		Count:   len(m.Items),
		HomeURL: m.HomeURL,
		SentAt:  time.Now().Format(time.RFC3339),
		Items:   make([]webhookItem, 0, len(m.Items)),
	}
	for _, it := range m.Items {
		wi := webhookItem{
			FindingID:     it.FindingID,
			Name:          it.Name,
			VulnClass:     it.VulnClass,
			Severity:      it.Severity,
			SeverityLabel: SeverityLabel(it.Severity),
			Summary:       it.Summary,
			Assets:        append([]string{}, it.Assets...),
			DetailURL:     it.DetailURL,
			FromStatus:    it.FromStatus,
			ToStatus:      it.ToStatus,
		}
		if it.IsStatusChange() {
			wi.StatusLabel = StatusLabel(it.FromStatus) + " → " + StatusLabel(it.ToStatus)
		}
		d.Items = append(d.Items, wi)
	}
	return d
}
