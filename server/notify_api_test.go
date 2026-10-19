package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// 本檔案覆蓋推送功能的端到端行為：漏洞落庫 → 事件 → 分派 → 真發 HTTP。
//
// 一個安全上的注意點：這些用例**不呼叫全域的 Notifier.step()**，只對自己建立的
// 渠道呼叫 stepRealtime/stepDigest。原因是 step() 會遍歷庫裡所有啟用渠道——
// 在一個已配好真實釘釘/企微機器人的開發庫上跑測試，全域 step 會把測試期間
// 產生的漏洞真推到那些群裡。逐渠道呼叫把影響面嚴格限制在測試自造的假接收端上。
//
// 清理：用例結束時刪掉本用例產生的事件（級聯刪投遞）與渠道，不給真實渠道留積壓。
//
// 斷言口徑：stepRealtime/stepDigest 不返回值、內部記日誌，因此這裡斷言的是
// **可觀測的外部行為**（假接收端收到了什麼、投遞行落到什麼狀態），而不是函式的
// 返回值——這比對返回值打樁更接近真實呼叫路徑。

// notifyFixture 是本檔案用例的公共裝置。
type notifyFixture struct {
	s       *Server
	pg      *db.DB
	request func(method, path, body string) *httptest.ResponseRecorder
	n       *Notifier
	// 自建的 task/exploration：用例把漏洞記到這裡，與其它用例的資料隔離。
	taskID int64
	expID  int64
	// cleanupMark 之後產生的事件在清理時一併刪除。
	cleanupMark int64
}

func newNotifyFixture(t *testing.T) *notifyFixture {
	t.Helper()
	// 本檔案的所有假接收端都跑在 127.0.0.1 上，而投遞預設拒絕環回地址
	// （防 SSRF 打到同機服務與雲後設資料）。測試顯式開啟這個開關；
	// 守衛「預設拒絕」的行為由 notify 包的 ssrf_test.go 覆蓋。
	t.Setenv(notify.AllowLocalTargetsEnv, "1")
	s, _, request := trafficEvidenceServer(t)
	pg := s.m.pg

	// 自建一個 task：共享裝置 trafficEvidenceServer 造的 task 拿不到 exploration id，
	// 而記錄漏洞必須提供它。
	task, err := s.m.CreateTask("通知推送測試", "推送行為驗證", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Exec(`DELETE FROM tasks WHERE id=$1`, taskID) })

	var mark int64
	if err := pg.QueryRow(`SELECT COALESCE(max(id),0) FROM notification_events`).Scan(&mark); err != nil {
		t.Fatal(err)
	}
	// 讓裝置自成閉環：把建 fixture 之前就存在的事件一次性標記為已分派。
	//
	// 為什麼必須做：FanOutPendingEvents 是**全域**的，會把庫裡所有未分派事件
	// 展開到所有匹配渠道上。而共享裝置 trafficEvidenceServer 自己就會記一條漏洞
	// （正是它返回的那個初始 finding），其它用例也可能有殘留。不隔離的話，
	// 這些雜散事件會被分派到本用例的渠道上，讓「應有 N 條投遞」這類斷言
	// 時對時錯——而且錯法取決於用例執行順序，比直接失敗更難查。
	if _, err := pg.Exec(`UPDATE notification_events SET fanned_out = true WHERE id <= $1 AND NOT fanned_out`, mark); err != nil {
		t.Fatal(err)
	}

	f := &notifyFixture{s: s, pg: pg, request: request, n: newNotifier(s), taskID: taskID, expID: task.ExpID, cleanupMark: mark}
	t.Cleanup(func() {
		if _, err := pg.Exec(`DELETE FROM notification_events WHERE id > $1`, f.cleanupMark); err != nil {
			t.Logf("清理通知事件失敗: %v", err)
		}
	})
	// 總開關必須是開的（其它用例可能關過它）。
	if err := pg.SetBool(settingNotifyEnabled, true); err != nil {
		t.Fatal(err)
	}
	return f
}

// record 走真實的證據寫入路徑落一條漏洞，返回 finding id。
// 這條路會在**同一事務**裡登記推送事件——正是本功能的掛點。
func (f *notifyFixture) record(t *testing.T, vulnclass, severity string) int64 {
	t.Helper()
	out, err := f.s.evidenceStore().Record(context.Background(), db.RecordFindingInput{
		TaskID:        f.taskID,
		ExplorationID: f.expID,
		Worker:        "test",
		VulnClass:     vulnclass,
		Name:          vulnclass,
		Severity:      severity,
		Summary:       vulnclass + " 的摘要",
		Evidence:      "poc",
	}, nil)
	if err != nil {
		t.Fatalf("記錄漏洞失敗: %v", err)
	}
	return out.FindingID
}

// channel 讀回渠道配置（供逐渠道呼叫 stepX 使用）。
func (f *notifyFixture) channel(t *testing.T, id int64) *db.NotificationChannel {
	t.Helper()
	ch, err := f.pg.NotificationChannelByID(context.Background(), id)
	if err != nil {
		t.Fatalf("讀渠道失敗: %v", err)
	}
	return ch
}

// deliver 分派事件並只對指定渠道跑一輪投遞。
func (f *notifyFixture) deliver(t *testing.T, chID int64, baseURL string) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := f.pg.FanOutPendingEvents(ctx, 500); err != nil {
		t.Fatalf("分派失敗: %v", err)
	}
	f.n.stepRealtime(ctx, f.channel(t, chID), 50, baseURL)
}

// createChannel 通過 HTTP 介面建渠道，順帶覆蓋介面自身的校驗路徑。
func (f *notifyFixture) createChannel(t *testing.T, payload map[string]any) int64 {
	t.Helper()
	raw, _ := json.Marshal(payload)
	r := f.request("POST", "/api/notify/channels", string(raw))
	if r.Code != 200 {
		t.Fatalf("建渠道失敗 %d: %s", r.Code, r.Body)
	}
	var res struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &res); err != nil || res.ID == 0 {
		t.Fatalf("建渠道返回異常: %s (%v)", r.Body, err)
	}
	t.Cleanup(func() { f.pg.Exec(`DELETE FROM notification_channels WHERE id=$1`, res.ID) })
	return res.ID
}

// fakeWebhook 是記錄收到的請求體的假接收端。
type fakeWebhook struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
}

func newFakeWebhook(t *testing.T) *fakeWebhook {
	t.Helper()
	f := &fakeWebhook{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeWebhook) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func (f *fakeWebhook) body(t *testing.T, i int) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.bodies) {
		t.Fatalf("假接收端只收到 %d 條請求，取不到第 %d 條", len(f.bodies), i)
	}
	return f.bodies[i]
}

func (f *fakeWebhook) last(t *testing.T) map[string]any {
	t.Helper()
	if f.count() == 0 {
		t.Fatal("假接收端沒有收到任何請求")
	}
	return f.body(t, f.count()-1)
}

// markdownText 從請求體裡取出正文，相容各家的欄位名差異：
// 釘釘 markdown 用 `text`、ActionCard 用 `text`、企業微信 markdown 用 `content`。
func markdownText(t *testing.T, body map[string]any) string {
	t.Helper()
	for _, key := range []string{"markdown", "actionCard"} {
		section, ok := body[key].(map[string]any)
		if !ok {
			continue
		}
		for _, field := range []string{"text", "content"} {
			if s, ok := section[field].(string); ok && s != "" {
				return s
			}
		}
	}
	t.Fatalf("請求體裡沒有可識別的正文: %v", body)
	return ""
}

// agePendingBatch 把該渠道的待發投遞催老，用於測試彙總批次到期。
func (f *notifyFixture) agePendingBatch(t *testing.T, chID int64) {
	t.Helper()
	if _, err := f.pg.Exec(`UPDATE notification_deliveries SET created_at = now() - interval '2 hours'
WHERE channel_id=$1 AND state=$2`, chID, db.NotifyStatePending); err != nil {
		t.Fatal(err)
	}
}

func TestNotifyEndToEndRealtimeDelivery(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "即時推送",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	f.record(t, "SQL注入", "high")
	f.deliver(t, chID, "")

	if hook.count() != 1 {
		t.Fatalf("應發出 1 條訊息，實際 %d", hook.count())
	}
	text := markdownText(t, hook.last(t))
	for _, want := range []string{"SQL注入", "高危", "摘要"} {
		if !strings.Contains(text, want) {
			t.Fatalf("訊息正文缺少 %q:\n%s", want, text)
		}
	}
	// 投遞應流轉為 sent。
	var pending int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1 AND state <> $2`,
		chID, db.NotifyStateSent).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("投遞後仍有 %d 條未標記 sent", pending)
	}
}

func TestNotifyChannelAPIMasksSecretsAndPreservesOnUpdate(t *testing.T) {
	f := newNotifyFixture(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "掩碼用例",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": "https://oapi.dingtalk.com/robot/send?access_token=abc123456", "secret": "SECabcdef123456"},
	})

	r := f.request("GET", "/api/notify/channels", "")
	if r.Code != 200 {
		t.Fatalf("列渠道失敗 %d: %s", r.Code, r.Body)
	}
	if strings.Contains(r.Body.String(), "abc123456") || strings.Contains(r.Body.String(), "SECabcdef123456") {
		t.Fatalf("介面回顯洩露了憑據: %s", r.Body)
	}
	var listed struct {
		Channels []struct {
			ID         int64          `json:"id"`
			Config     map[string]any `json:"config"`
			SecretKeys []string       `json:"secret_keys"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	var mine *struct {
		ID         int64          `json:"id"`
		Config     map[string]any `json:"config"`
		SecretKeys []string       `json:"secret_keys"`
	}
	for i := range listed.Channels {
		if listed.Channels[i].ID == chID {
			mine = &listed.Channels[i]
		}
	}
	if mine == nil {
		t.Fatal("新建的渠道未出現在列表裡")
	}
	if !notify.IsMasked(fmt.Sprint(mine.Config["webhook"])) || !notify.IsMasked(fmt.Sprint(mine.Config["secret"])) {
		t.Fatalf("憑據欄位應為掩碼值: %v", mine.Config)
	}
	if len(mine.SecretKeys) == 0 {
		t.Fatal("介面應告知前端哪些欄位是憑據")
	}

	// PATCH 只改名 + 回傳掩碼憑據：真憑據必須原樣保留。
	body, _ := json.Marshal(map[string]any{
		"name":   "改名後",
		"config": map[string]any{"webhook": fmt.Sprint(mine.Config["webhook"]), "secret": fmt.Sprint(mine.Config["secret"])},
	})
	if r := f.request("PATCH", fmt.Sprintf("/api/notify/channels/%d", chID), string(body)); r.Code != 200 {
		t.Fatalf("更新失敗 %d: %s", r.Code, r.Body)
	}
	cfg := f.channelConfig(t, chID)
	if cfg["webhook"] != "https://oapi.dingtalk.com/robot/send?access_token=abc123456" {
		t.Fatalf("掩碼回傳把真憑據覆蓋了: %v", cfg["webhook"])
	}
	if cfg["secret"] != "SECabcdef123456" {
		t.Fatalf("掩碼回傳把 secret 覆蓋了: %v", cfg["secret"])
	}
	if f.channel(t, chID).Name != "改名後" {
		t.Fatal("名字未更新")
	}

	// 顯式清空 secret 應生效（區別於「回傳掩碼=保持不變」）。
	body, _ = json.Marshal(map[string]any{"config": map[string]any{"secret": ""}})
	if r := f.request("PATCH", fmt.Sprintf("/api/notify/channels/%d", chID), string(body)); r.Code != 200 {
		t.Fatalf("清空 secret 失敗 %d: %s", r.Code, r.Body)
	}
	if _, still := f.channelConfig(t, chID)["secret"]; still {
		t.Fatal("空串應清空 secret")
	}
}

func (f *notifyFixture) channelConfig(t *testing.T, id int64) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(f.channel(t, id).Config, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestNotifyChannelAPICreateValidation(t *testing.T) {
	f := newNotifyFixture(t)
	cases := []struct {
		name    string
		payload map[string]any
		wantSub string
	}{
		{"型別非法", map[string]any{"name": "x", "kind": "nope", "config": map[string]any{}}, "渠道型別無效"},
		{"缺名稱", map[string]any{"kind": notify.KindDingTalk, "config": map[string]any{"webhook": "https://e.com/h"}}, "缺少渠道名稱"},
		{"缺 webhook", map[string]any{"name": "x", "kind": notify.KindDingTalk, "config": map[string]any{}}, "Webhook"},
		{"webhook 協議非法", map[string]any{"name": "x", "kind": notify.KindDingTalk, "config": map[string]any{"webhook": "file:///etc/passwd"}}, "Webhook 地址無效"},
		{"模式非法", map[string]any{"name": "x", "kind": notify.KindDingTalk, "mode": "sometimes", "config": map[string]any{"webhook": "https://e.com/h"}}, "推送模式無效"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.payload)
			r := f.request("POST", "/api/notify/channels", string(raw))
			if r.Code != 400 {
				t.Fatalf("應返回 400，得到 %d: %s", r.Code, r.Body)
			}
			if !strings.Contains(r.Body.String(), tc.wantSub) {
				t.Fatalf("錯誤資訊應提到 %q，得到 %s", tc.wantSub, r.Body)
			}
		})
	}
	if r := f.request("DELETE", "/api/notify/channels/99999999", ""); r.Code != 404 {
		t.Fatalf("刪除不存在的渠道應 404，得到 %d", r.Code)
	}
}

func TestNotifyFilterBlocksBelowThreshold(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "僅嚴重",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
		"filter": map[string]any{"min_severity": "critical"},
	})
	f.record(t, "低危問題", "low")
	if _, _, err := f.pg.FanOutPendingEvents(context.Background(), 500); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("低於閾值的漏洞不該產生投遞，得到 %d 條", n)
	}
	f.n.stepRealtime(context.Background(), f.channel(t, chID), 50, "")
	if hook.count() != 0 {
		t.Fatal("被過濾的漏洞不應發出訊息")
	}
}

func TestNotifyDigestBatchesMultipleFindingsIntoOneMessage(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "彙總推送",
		"kind":   notify.KindDingTalk,
		"mode":   db.NotifyModeDigest,
		"config": map[string]any{"webhook": hook.URL},
	})
	for i := 0; i < 3; i++ {
		f.record(t, fmt.Sprintf("彙總漏洞%d", i+1), "high")
	}
	ctx := context.Background()
	if _, _, err := f.pg.FanOutPendingEvents(ctx, 500); err != nil {
		t.Fatal(err)
	}
	ch := f.channel(t, chID)

	// 未到期：不發。
	f.n.stepDigest(ctx, ch, 50, "")
	if hook.count() != 0 {
		t.Fatal("彙總批次未到期就發了")
	}

	// 催老批次後：三條合成一條訊息。
	f.agePendingBatch(t, chID)
	f.n.stepDigest(ctx, ch, 50, "")
	if got := hook.count(); got != 1 {
		t.Fatalf("三條應彙總成一條訊息，實際發了 %d 條", got)
	}
	text := markdownText(t, hook.last(t))
	if !strings.Contains(text, "近") || !strings.Contains(text, "3 個漏洞") {
		t.Fatalf("彙總訊息缺少條數/時間窗文案:\n%s", text)
	}
	for i := 1; i <= 3; i++ {
		if !strings.Contains(text, fmt.Sprintf("彙總漏洞%d", i)) {
			t.Fatalf("彙總訊息缺少第 %d 條:\n%s", i, text)
		}
	}
	// 同一批次應共享 batch_id。
	var distinct, total int
	if err := f.pg.QueryRow(`SELECT count(DISTINCT batch_id), count(*) FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&distinct, &total); err != nil {
		t.Fatal(err)
	}
	if total != 3 || distinct != 1 {
		t.Fatalf("三條投遞應共享一個 batch_id，得到 distinct=%d total=%d", distinct, total)
	}
}

func TestNotifyDisabledChannelDoesNotSend(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":    "停用渠道",
		"kind":    notify.KindDingTalk,
		"enabled": false,
		"config":  map[string]any{"webhook": hook.URL},
	})
	f.record(t, "停用期間的漏洞", "critical")
	if _, _, err := f.pg.FanOutPendingEvents(context.Background(), 500); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("停用渠道不該產生投遞，得到 %d 條", n)
	}
}

func TestNotifyStatusChangeDelivery(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "狀態變更訂閱",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
		"filter": map[string]any{"on_status_change": true},
	})
	finding := f.record(t, "狀態變更用例", "high")
	r := f.request("PATCH", fmt.Sprintf("/api/exploration/findings/%d", finding), `{"status":"fixed"}`)
	if r.Code != 200 {
		t.Fatalf("改狀態失敗 %d: %s", r.Code, r.Body)
	}
	f.deliver(t, chID, "")

	// 應有兩條：fixed 那一條是狀態變更；finding_created 那條也可能在同一輪發出。
	// 狀態變更的實際上更晚建立，但不依賴順序，全量找。
	found := false
	for i := 0; i < hook.count(); i++ {
		text := markdownText(t, hook.body(t, i))
		if strings.Contains(text, "狀態變更") && strings.Contains(text, "已修復") {
			found = true
		}
	}
	if !found {
		t.Fatalf("沒有收到含「狀態變更 → 已修復」的訊息（共 %d 條）", hook.count())
	}
}

func TestNotifyStatusChangeSuppressedByDefault(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "不訂閱狀態變更",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	finding := f.record(t, "不訂閱變更", "high")
	if r := f.request("PATCH", fmt.Sprintf("/api/exploration/findings/%d", finding), `{"status":"false_positive"}`); r.Code != 200 {
		t.Fatalf("改狀態失敗 %d: %s", r.Code, r.Body)
	}
	if _, _, err := f.pg.FanOutPendingEvents(context.Background(), 500); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id
WHERE d.channel_id=$1 AND e.kind=$2`, chID, notify.EventFindingStatusChanged).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("未訂閱狀態變更的渠道不該收到狀態變更投遞，得到 %d 條", n)
	}
}

func TestNotifyTestMessageEndpoint(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "測試傳送",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	if r := f.request("POST", fmt.Sprintf("/api/notify/channels/%d/test", chID), ""); r.Code != 200 {
		t.Fatalf("測試傳送失敗 %d: %s", r.Code, r.Body)
	}
	if hook.count() != 1 {
		t.Fatalf("假接收端應收到 1 條測試訊息，得到 %d", hook.count())
	}
	// 測試訊息必須一眼能看出是測試，不能被誤當成真實漏洞。
	if text := markdownText(t, hook.last(t)); !strings.Contains(text, "測試") {
		t.Fatalf("測試訊息應標明是測試: %s", text)
	}
	// 配置壞掉時應把渠道的原始錯誤如實回給使用者。
	badID := f.createChannel(t, map[string]any{
		"name":   "壞地址",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": "http://127.0.0.1:1/hook"},
	})
	if r := f.request("POST", fmt.Sprintf("/api/notify/channels/%d/test", badID), ""); r.Code != 502 {
		t.Fatalf("投遞失敗應回 502，得到 %d: %s", r.Code, r.Body)
	}
}

func TestNotifyDeliveriesHistoryAndRetry(t *testing.T) {
	f := newNotifyFixture(t)
	// 指向必然失敗的地址，製造 failed 投遞。
	chID := f.createChannel(t, map[string]any{
		"name":   "失敗重試",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": "http://127.0.0.1:1/hook"},
	})
	f.record(t, "會失敗的推送", "high")
	ctx := context.Background()
	if _, _, err := f.pg.FanOutPendingEvents(ctx, 500); err != nil {
		t.Fatal(err)
	}
	ch := f.channel(t, chID)
	// 連投到耗盡重試預算。
	for i := 0; i < db.MaxNotifyAttempts; i++ {
		f.n.stepRealtime(ctx, ch, 50, "")
		if _, err := f.pg.Exec(`UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE channel_id=$1`, chID); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	if err := f.pg.QueryRow(`SELECT state FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != db.NotifyStateFailed {
		t.Fatalf("重試耗盡後應為 failed，得到 %s", state)
	}

	r := f.request("GET", fmt.Sprintf("/api/notify/deliveries?channel_id=%d&state=failed", chID), "")
	if r.Code != 200 {
		t.Fatalf("查歷史失敗 %d: %s", r.Code, r.Body)
	}
	var hist struct {
		Deliveries []struct {
			ID        int64  `json:"id"`
			State     string `json:"state"`
			LastError string `json:"last_error"`
			Attempts  int    `json:"attempts"`
			Title     string `json:"title"`
		} `json:"deliveries"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &hist); err != nil {
		t.Fatal(err)
	}
	if hist.Total != 1 || len(hist.Deliveries) != 1 {
		t.Fatalf("應查到 1 條失敗投遞，得到 total=%d len=%d", hist.Total, len(hist.Deliveries))
	}
	if hist.Deliveries[0].LastError == "" {
		t.Fatal("歷史裡應帶上失敗原因，否則使用者無法排查")
	}
	if hist.Deliveries[0].Attempts < db.MaxNotifyAttempts {
		t.Fatalf("嘗試次數應被記錄，得到 %d", hist.Deliveries[0].Attempts)
	}
	if hist.Deliveries[0].Title != "會失敗的推送" {
		t.Fatalf("歷史應帶出漏洞標題，得到 %q", hist.Deliveries[0].Title)
	}

	// 手動重發：應回到 pending 且清零計數。
	if r := f.request("POST", fmt.Sprintf("/api/notify/deliveries/%d/retry", hist.Deliveries[0].ID), ""); r.Code != 200 {
		t.Fatalf("重發失敗 %d: %s", r.Code, r.Body)
	}
	var attempts int
	if err := f.pg.QueryRow(`SELECT state, attempts FROM notification_deliveries WHERE id=$1`, hist.Deliveries[0].ID).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != db.NotifyStatePending || attempts != 0 {
		t.Fatalf("重發後應為 pending 且 attempts=0，得到 %s/%d", state, attempts)
	}
}

func TestNotifyMetaAndSettingsRoundTrip(t *testing.T) {
	f := newNotifyFixture(t)
	r := f.request("GET", "/api/notify/meta", "")
	if r.Code != 200 {
		t.Fatalf("meta 失敗: %s", r.Body)
	}
	var meta struct {
		Kinds []struct {
			Kind       string   `json:"kind"`
			SecretKeys []string `json:"secret_keys"`
		} `json:"kinds"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.Kinds) != len(notify.Kinds()) {
		t.Fatalf("meta 應列出全部 %d 個渠道，得到 %d", len(notify.Kinds()), len(meta.Kinds))
	}
	for _, k := range meta.Kinds {
		if len(k.SecretKeys) == 0 {
			t.Errorf("渠道 %s 未上報憑據欄位", k.Kind)
		}
	}

	// 三項全域設定往返。尾部斜槓應被規範化掉，否則回鏈會拼出 "//function/..."。
	if r := f.request("PUT", "/api/settings", `{"notify_public_base_url":"https://artex.example.com/","notify_digest_interval_min":15,"notify_enabled":true}`); r.Code != 200 {
		t.Fatalf("寫設定失敗 %d: %s", r.Code, r.Body)
	}
	t.Cleanup(func() {
		f.pg.Exec(`DELETE FROM settings WHERE key IN ($1,$2)`, settingNotifyPublicBaseURL, settingNotifyDigestMinutes)
	})
	payload := f.s.settingsPayload()
	if payload["notify_public_base_url"] != "https://artex.example.com" {
		t.Fatalf("回鏈地址未規範化: %v", payload["notify_public_base_url"])
	}
	if payload["notify_digest_interval_min"] != 15 {
		t.Fatalf("彙總週期未生效: %v", payload["notify_digest_interval_min"])
	}

	// 非法值應被拒。
	for _, body := range []string{
		`{"notify_public_base_url":"ftp://x"}`,
		`{"notify_digest_interval_min":0}`,
		`{"notify_digest_interval_min":99999}`,
	} {
		if r := f.request("PUT", "/api/settings", body); r.Code != 400 {
			t.Errorf("%s 應返回 400，得到 %d", body, r.Code)
		}
	}
}

// TestNotifyDeepLinkUsesPublicBaseURL 覆蓋回鏈拼接：配了 public_base_url 時
// 單條訊息必須用帶按鈕的 ActionCard，且連結指向漏洞詳情頁。
func TestNotifyDeepLinkUsesPublicBaseURL(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "回鏈",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	finding := f.record(t, "帶回鏈的漏洞", "high")
	f.deliver(t, chID, "https://artex.example.com")

	body := hook.last(t)
	card, _ := body["actionCard"].(map[string]any)
	if card == nil {
		t.Fatalf("有回鏈時應用 ActionCard，得到 msgtype=%v", body["msgtype"])
	}
	want := fmt.Sprintf("https://artex.example.com/function/findings/detail?id=%d", finding)
	if card["singleURL"] != want {
		t.Fatalf("回鏈不對\n期望 %s\n得到 %v", want, card["singleURL"])
	}
}

// TestNotifyNoDeepLinkWithoutBaseURL 反向覆蓋：沒配外部地址時不該產生壞連結
// （比如指向 localhost 或相對路徑），應退回純 markdown。
func TestNotifyNoDeepLinkWithoutBaseURL(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "無回鏈",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	f.record(t, "無回鏈的漏洞", "high")
	f.deliver(t, chID, "")

	body := hook.last(t)
	if body["msgtype"] != "markdown" {
		t.Fatalf("未配外部地址時應發 markdown，得到 %v", body["msgtype"])
	}
	if text := markdownText(t, body); strings.Contains(text, "檢視詳情") {
		t.Fatalf("未配外部地址時不該出現詳情連結:\n%s", text)
	}
}

// TestNotifyDigestSegmentsAndDefersRemainder 是「靜默丟失」修復的端到端證據。
//
// 彙總訊息受渠道長度上限約束（企微 4096 位元組），一批裝不下時必須**按整條**切分：
// 裝進本條的那些標記已送達，其餘回到佇列等下一條。曾經的實現是把整批標記
// 成功——被截掉的那些既不在訊息裡、也不在失敗列表裡，投遞歷史還顯示成功，
// 漏洞就這麼沒了。
//
// 斷言四件事：① 只標記了實際裝下的條數 ② 其餘仍是待發 ③ 被推遲的條目
// **沒有消耗重試次數** ④ 再跑一輪能把剩下的發出去（不會卡死）。
func TestNotifyDigestSegmentsAndDefersRemainder(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	// 用企業微信：markdown 上限 4096 位元組，是六個渠道里最緊的。
	chID := f.createChannel(t, map[string]any{
		"name":   "分段彙總",
		"kind":   notify.KindWeCom,
		"mode":   db.NotifyModeDigest,
		"config": map[string]any{"webhook": hook.URL},
	})
	const total = 60
	// 標題取長一點，保證 60 條遠超 4096 位元組，必然分段。
	longName := strings.Repeat("超長漏洞名稱", 6)
	for i := 0; i < total; i++ {
		f.record(t, longName+strconv.Itoa(i+1), "high")
	}
	ctx := context.Background()
	if _, _, err := f.pg.FanOutPendingEvents(ctx, 500); err != nil {
		t.Fatal(err)
	}
	f.agePendingBatch(t, chID)
	ch := f.channel(t, chID)

	f.n.stepDigest(ctx, ch, 50, "")
	if hook.count() != 1 {
		t.Fatalf("應只發出一條訊息，得到 %d", hook.count())
	}

	var sent, pending int
	if err := f.pg.QueryRow(`SELECT
    count(*) FILTER (WHERE state=$2),
    count(*) FILTER (WHERE state=$3)
  FROM notification_deliveries WHERE channel_id=$1`, chID, db.NotifyStateSent, db.NotifyStatePending).
		Scan(&sent, &pending); err != nil {
		t.Fatal(err)
	}
	if sent == 0 {
		t.Fatal("應有條目被標記為已送達")
	}
	if pending == 0 {
		t.Fatalf("一批 %d 條不可能全裝進 4096 位元組，應有剩餘待發；sent=%d", total, sent)
	}
	if sent+pending != total {
		t.Fatalf("條目數對不上：sent=%d pending=%d total=%d（既沒送達也沒待發=丟失）", sent, pending, total)
	}
	// 訊息正文必須如實告知還有多少條沒包含在本條裡。
	if text := markdownText(t, hook.last(t)); !strings.Contains(text, "其餘") {
		t.Fatalf("訊息應說明還有條目未包含在本條:\n%.400s", text)
	}

	// 被推遲的條目不得消耗重試預算：領取時 attempts 已樂觀 +1，推遲時要減回去。
	var maxAttempts int
	if err := f.pg.QueryRow(`SELECT COALESCE(max(attempts),0) FROM notification_deliveries
WHERE channel_id=$1 AND state=$2`, chID, db.NotifyStatePending).Scan(&maxAttempts); err != nil {
		t.Fatal(err)
	}
	if maxAttempts > 0 {
		t.Fatalf("被推遲的條目不該消耗重試次數（否則幾條之後就會被判失敗），得到 attempts=%d", maxAttempts)
	}

	// 反覆跑直到收斂。斷言的是**最終全部送達**且中途確實分了多輪——
	// 這比「第二輪發完」更強：它證明分段不會卡死、也不會把剩餘條目丟掉。
	rounds := 0
	for {
		var undelivered int
		if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries
WHERE channel_id=$1 AND state <> $2 AND state <> $3`, chID, db.NotifyStateSent, db.NotifyStateFailed).
			Scan(&undelivered); err != nil {
			t.Fatal(err)
		}
		if undelivered == 0 {
			break
		}
		rounds++
		if rounds > total+5 {
			t.Fatalf("分段投遞不收斂：跑了 %d 輪仍有 %d 條懸而未決", rounds, undelivered)
		}
		before := hook.count()
		f.n.stepDigest(ctx, ch, 50, "")
		if hook.count() == before {
			t.Fatalf("第 %d 輪沒有任何進展，剩餘 %d 條會永久卡住", rounds, undelivered)
		}
	}
	if rounds < 2 {
		t.Fatalf("一條 4096 位元組的訊息裝不下 %d 條長標題漏洞，應分多輪發出，實際只用了 %d 輪", total, rounds)
	}
	// 首輪之後的每一輪都應是**純續發**，不存在被渠道拒絕的條目。
	var failed int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1 AND state=$2`,
		chID, db.NotifyStateFailed).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 0 {
		t.Fatalf("假接收端始終返回成功，不該有失敗條目，得到 %d", failed)
	}
}

// TestNotifyBackoffTableMatchesAttemptBudget 是防漂移斷言。
//
// 重試預算（db.MaxNotifyAttempts）與退避序列表（notifyBackoff）分居兩個包：
// 前者是狀態機的策略、後者是引擎的執行節拍。若只改其中一個——比如把預算提到 5
// 次而忘了加退避檔位——程式碼不會報錯，只會讓第 4、5 次重試沿用最後一檔間隔，
// 表現為「重試節奏莫名變慢」，排查時很難聯想到是這裡。
// 斷言兩者長度一致，讓這種漂移在 CI 裡就暴露。
func TestNotifyBackoffTableMatchesAttemptBudget(t *testing.T) {
	if len(notifyBackoff) != db.MaxNotifyAttempts {
		t.Fatalf("退避檔位數(%d)與最大嘗試次數(%d)不一致——改一個必須同時改另一個",
			len(notifyBackoff), db.MaxNotifyAttempts)
	}
	// 退避間隔必須單調不減，否則重試會越試越急，反而加劇限流。
	for i := 1; i < len(notifyBackoff); i++ {
		if notifyBackoff[i] < notifyBackoff[i-1] {
			t.Fatalf("退避間隔必須單調不減：第 %d 檔 %v < 第 %d 檔 %v",
				i, notifyBackoff[i], i-1, notifyBackoff[i-1])
		}
	}
}

// TestNotifyRateLimitDoesNotConsumeRetryBudget 鎖住「先取令牌再領取」的順序。
// 若反了（先領後棄），被限流擋下的投遞已經計過一次 attempts，
// 預算會被純粹的等待耗光，最後落進 failed。
func TestNotifyRateLimitDoesNotConsumeRetryBudget(t *testing.T) {
	// 只測令牌桶本身，不需要 Server（也不該為它造一個）。
	n := &Notifier{buckets: map[int64]*notifyBucket{}}
	now := time.Now()
	// 每分鐘 1 條：滿桶時最多 1 條。
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now); got != 1 {
		t.Fatalf("滿桶時每分鐘 1 條應取 1 個令牌，得到 %d", got)
	}
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(time.Millisecond)); got != 0 {
		t.Fatalf("令牌耗盡後應立即返回 0，得到 %d", got)
	}
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(30*time.Second)); got != 0 {
		t.Fatalf("半程不應補滿一個令牌，得到 %d", got)
	}
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(time.Minute)); got != 1 {
		t.Fatalf("滿一個週期應補回 1 個令牌，得到 %d", got)
	}
	// 不限流渠道走有限上限，避免單輪被無限積壓拖住。
	if got := n.takeTokens(2, 0, notifyUnlimitedBurstPerTick+10, now); got != notifyUnlimitedBurstPerTick {
		t.Fatalf("不限流應返回每輪上限 %d，得到 %d", notifyUnlimitedBurstPerTick, got)
	}
	// 渠道之間的令牌桶互相獨立。
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(time.Millisecond)); got != 0 {
		t.Fatalf("渠道 1 的桶應仍然為空，得到 %d", got)
	}
}

// TestNotifyTakeTokensKeepsUnusedTokens 鎖住「只取 want 個」的語義。
//
// 曾經的實現把桶整個抽空後才由呼叫方截斷，於是 rate=100/min 的渠道攢滿桶、
// 一輪只用 5 條，剩下 95 個令牌直接丟棄；渠道這一輪沒有待發投遞時同樣照扣。
// 結果是註釋聲稱的「積壓時可以一次性衝 rate_per_min 條」在任何情況下都做不到。
func TestNotifyTakeTokensKeepsUnusedTokens(t *testing.T) {
	n := &Notifier{buckets: map[int64]*notifyBucket{}}
	now := time.Now()
	// 桶初始為滿（100），本輪只要 5 個。
	if got := n.takeTokens(1, 100, 5, now); got != 5 {
		t.Fatalf("want=5 時應恰好取 5 個令牌，得到 %d", got)
	}
	// 關鍵斷言：餘下的 95 個必須還在桶裡，而不是被抽空丟棄。
	// 不推進時間，確保取到的只可能來自存量而非補充。
	if got := n.takeTokens(1, 100, 95, now); got != 95 {
		t.Fatalf("剩餘令牌應仍可取用（期望 95），得到 %d——桶被整輪抽空了", got)
	}
	if got := n.takeTokens(1, 100, 1, now); got != 0 {
		t.Fatalf("桶已取盡，應返回 0，得到 %d", got)
	}
	// want<=0 不應扣減任何令牌（空輪不收費）。
	n2 := &Notifier{buckets: map[int64]*notifyBucket{}}
	if got := n2.takeTokens(1, 20, 0, now); got != 0 {
		t.Fatalf("want=0 應返回 0，得到 %d", got)
	}
	if got := n2.takeTokens(1, 20, 20, now); got != 20 {
		t.Fatalf("want=0 的那次不該消耗令牌，應仍可取滿 20，得到 %d", got)
	}
}

// TestDigestTickPlanDecouplesBatchSizeFromSendBudget 釘住彙總模式的兩個量綱。
//
// 彙總批次的大小一旦跟每輪請求預算掛上，rate_per_min=20 的渠道就只能在每個
// 3 秒 tick 裡補到 1 個令牌，於是每條彙總訊息只裝 1 個漏洞——功能上等於沒有
// 彙總，而訊息頭部還寫著「近 30 分鐘新增 1 個漏洞」。這個退化不會報錯，
// 現有的端到端用例也看不出來（它們手動給 stepDigest 傳一個夠大的 limit，
// 繞過了 step 裡的額度計算），所以在這裡直接斷言決策本身。
func TestDigestTickPlanDecouplesBatchSizeFromSendBudget(t *testing.T) {
	tokens, claimLimit := digestTickPlan()
	// 一批 = 一條訊息 = 一次請求 = 一個令牌。令牌的單位是訊息，不是漏洞。
	if tokens != 1 {
		t.Fatalf("彙總一批只發一條訊息，應恰好消耗 1 個令牌，得到 %d", tokens)
	}
	if claimLimit != db.MaxDigestBatchSize {
		t.Fatalf("彙總批次大小應為記憶體上界 db.MaxDigestBatchSize=%d，得到 %d",
			db.MaxDigestBatchSize, claimLimit)
	}
	// 關鍵關係：批次大小必須遠大於每輪請求預算。兩者一旦同量級，
	// 說明又把「發幾條訊息」和「一批裝幾條漏洞」混成了一個數。
	if claimLimit <= notifyMaxSendsPerChannelPerTick {
		t.Fatalf("彙總批次大小 %d 不應受每輪請求預算 %d 約束——"+
			"請求預算是由租約倒推的「發幾次請求」，與「一批裝幾條漏洞」是兩個量綱",
			claimLimit, notifyMaxSendsPerChannelPerTick)
	}
}

// TestNotifyTickBudgetFitsWithinLease 是又一條防漂移斷言。
//
// 單渠道每輪的投遞條數上限（notifyMaxSendsPerChannelPerTick）是從租約時長倒推的：
// 一輪裡序列投遞的最壞耗時必須 < 租約，否則後幾條還沒發完租約就過期，
// 多例項部署時對端會把它們重新領走、重複傳送。這三個常量分處不同位置，
// 改任意一個都可能打破關係而不會有任何報錯——所以在這裡釘死。
func TestNotifyTickBudgetFitsWithinLease(t *testing.T) {
	worst := time.Duration(notifyMaxSendsPerChannelPerTick) * notifySendTimeout
	if worst >= notifyLease {
		t.Fatalf("單渠道一輪的最壞耗時 %v 不應達到或超過租約 %v"+
			"（notifyMaxSendsPerChannelPerTick=%d × notifySendTimeout=%v）——"+
			"改這三個常量中的任意一個都要同步檢查另外兩個",
			worst, notifyLease, notifyMaxSendsPerChannelPerTick, notifySendTimeout)
	}
}
