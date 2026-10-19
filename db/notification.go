package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Autumn-27/artex/notify"
)

// 本檔案是 IM 推送的渠道配置與事件層。投遞任務的領取與狀態流轉見
// db/notification_delivery.go。
//
// 兩條不變數，改這個檔案時務必保持：
//
//  1. 寫漏洞的事務(RecordFindingTx)只調用 InsertNotificationEventTx 做一次盲插，
//     不讀任何通知相關的表、不做過濾匹配。任何在這裡引入的讀操作都可能因為
//     使用者配錯的過濾條件而汙染甚至中止漏洞寫入事務。
//  2. 過濾匹配永不報錯：配置畸形一律按「命中」處理(見 notify.Match)。寧可多推，
//     不可漏推。

// ErrNotificationChannelNotFound 渠道不存在。
var ErrNotificationChannelNotFound = errors.New("通知渠道不存在")

// 投遞狀態。
const (
	NotifyStatePending = "pending" // 待發
	NotifyStateSending = "sending" // 已被某個 dispatcher 領取，租約未到期
	NotifyStateSent    = "sent"    // 已送達
	NotifyStateFailed  = "failed"  // 重試耗盡或永久失敗，可手動重發
	NotifyStateSkipped = "skipped" // 渠道已停用，不再發送
)

// 推送模式。
const (
	NotifyModeRealtime = "realtime"
	NotifyModeDigest   = "digest"
)

// ValidNotifyMode 白名單校驗推送模式（與 findings.status 同理：不用 DB CHECK，
// 便於後續擴充套件）。
func ValidNotifyMode(m string) bool {
	return m == NotifyModeRealtime || m == NotifyModeDigest
}

// NotificationChannel 是一個渠道例項配置。Config 與 Filter 保持原始 JSON，
// 解析交給 notify 包——db 層不理解它們的欄位含義。
type NotificationChannel struct {
	ID     int64           `json:"id"`
	Name   string          `json:"name"`
	Kind   string          `json:"kind"`
	Mode   string          `json:"mode"`
	Config json.RawMessage `json:"config"`
	Filter json.RawMessage `json:"filter"`
	// Enabled 用指標是為了區分「沒傳這個欄位」與「顯式傳 false」——
	// 前端開關控制元件只提交被改動的欄位。
	Enabled    *bool     `json:"enabled,omitempty"`
	RatePerMin int       `json:"rate_per_min"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// IsEnabled 返回渠道是否啟用；Enabled 為 nil（未載入）時按啟用處理。
func (c *NotificationChannel) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// NotificationEvent 是一條事件事實。
type NotificationEvent struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	FindingID int64           `json:"finding_id"`
	Snapshot  json.RawMessage `json:"snapshot"`
	CreatedAt time.Time       `json:"created_at"`
}

const notificationChannelCols = `id, name, kind, enabled, config, mode, filter, rate_per_min, created_at, updated_at`

func scanNotificationChannel(sc interface{ Scan(...any) error }) (*NotificationChannel, error) {
	var c NotificationChannel
	var enabled bool
	if err := sc.Scan(&c.ID, &c.Name, &c.Kind, &enabled, &c.Config, &c.Mode, &c.Filter, &c.RatePerMin, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Enabled = &enabled
	return &c, nil
}

// ListNotificationChannels 返回全部渠道例項，啟用的排在前面、同級按 id。
// 排序放在 SQL 裡是為了讓 UI 與 dispatcher 看到同一個穩定順序。
func (d *DB) ListNotificationChannels(ctx context.Context) ([]*NotificationChannel, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+notificationChannelCols+` FROM notification_channels
ORDER BY enabled DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationChannel{}
	for rows.Next() {
		c, err := scanNotificationChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NotificationChannelByID 取單個渠道。
func (d *DB) NotificationChannelByID(ctx context.Context, id int64) (*NotificationChannel, error) {
	row := d.QueryRowContext(ctx, `SELECT `+notificationChannelCols+` FROM notification_channels WHERE id=$1`, id)
	c, err := scanNotificationChannel(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotificationChannelNotFound
	}
	return c, err
}

// SaveNotificationChannel 新建或更新一個渠道。
//
// 更新時只覆蓋呼叫方顯式給出的欄位（非 nil / 非空），這樣前端可以提交區域性
// 修改的抽屜表單，而不必回傳 config 裡那些它沒展示的欄位——回傳反而會造成
// 「掩碼值把真實金鑰覆蓋掉」的事故。
func (d *DB) SaveNotificationChannel(ctx context.Context, c *NotificationChannel) (int64, error) {
	if c.Mode == "" {
		c.Mode = NotifyModeRealtime
	}
	// 這裡刻意**不**對 0 做任何加工：0 是合法配置，含義是「不限流」。
	//
	// 曾經寫成 `if c.RatePerMin <= 0 { c.RatePerMin = 預設值 }`，本意是「未指定時
	// 給個安全預設」，但那把「顯式設成 0」也一起吞掉了——文件、UI 提示與
	// takeTokens 都把 0 解釋為不限流，唯獨這裡悄悄改成 20（釘釘/企微/Telegram）
	// 或 100（飛書），操作者以為放開了限流、實際被 20/分鐘卡著且沒有任何提示。
	//
	// 「未指定」與「顯式 0」的區別只有呼叫方知道（請求體裡欄位預設 vs 明確傳 0），
	// 所以預設值由 server 層在欄位預設時填，見 notifyCreateChannel。
	if c.RatePerMin < 0 {
		return 0, errors.New("限流值不能為負")
	}
	if c.Config == nil {
		c.Config = json.RawMessage(`{}`)
	}
	if c.Filter == nil {
		c.Filter = json.RawMessage(`{}`)
	}
	enabled := c.IsEnabled()

	if c.ID == 0 {
		var id int64
		err := d.QueryRowContext(ctx, `INSERT INTO notification_channels(name,kind,enabled,config,mode,filter,rate_per_min)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			c.Name, c.Kind, enabled, string(c.Config), c.Mode, string(c.Filter), c.RatePerMin).Scan(&id)
		return id, err
	}
	res, err := d.ExecContext(ctx, `UPDATE notification_channels
SET name=$2, kind=$3, enabled=$4, config=$5, mode=$6, filter=$7, rate_per_min=$8
WHERE id=$1`,
		c.ID, c.Name, c.Kind, enabled, string(c.Config), c.Mode, string(c.Filter), c.RatePerMin)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotificationChannelNotFound
	}
	return c.ID, nil
}

// SetNotificationChannelEnabled 切換啟停。
//
// 停用一個渠道時，把它尚未發出的投遞一併標記為 skipped：否則重新啟用後
// 會突然收到一批「停用期間積壓」的舊漏洞，時效已失且容易誤判為新增。
func (d *DB) SetNotificationChannelEnabled(ctx context.Context, id int64, enabled bool) error {
	return d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE notification_channels SET enabled=$2 WHERE id=$1`, id, enabled)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotificationChannelNotFound
		}
		if !enabled {
			if _, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET state=$2, last_error=$3
WHERE channel_id=$1 AND state IN ($4,$5)`,
				id, NotifyStateSkipped, "渠道已停用", NotifyStatePending, NotifyStateSending); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteNotificationChannel 刪除渠道。其投遞歷史隨外部索引鍵級聯刪除
// （渠道配置都沒了，歷史無從解讀）。
func (d *DB) DeleteNotificationChannel(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx, `DELETE FROM notification_channels WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotificationChannelNotFound
	}
	return nil
}

// RecordNotificationEventTx 在呼叫方的事務裡**盡力**寫入一條推送事件。
//
// 這是漏洞寫入路徑上唯一的通知相關改動：一次 INSERT，不讀任何表、不認識渠道、
// 不跑過濾。事務提交即保證「漏洞落庫」與「推送任務存在」原子一致，
// 不存在提交成功卻沒入隊、訊息永久丟失的視窗。
//
// 兩個關鍵設計，都不是隨手寫的：
//
//  1. **為什麼用 SAVEPOINT**：PostgreSQL 裡事務內任一語句報錯會讓整個事務進入
//     aborted 狀態，此後所有語句（含 COMMIT）一律失敗。所以「忽略這條 INSERT
//     的錯誤、讓呼叫方繼續提交」在 PG 裡是做不到的——除非用儲存點把錯誤隔離在
//     這一條語句上。沒有儲存點，就只剩「整筆回滾」這一個選項。
//
//  2. **為什麼整筆回滾是錯的**：推送是便利功能，漏洞記錄才是產品本身。一個通知
//     表的問題（舊庫未遷移、磁碟瞬時故障）不該讓高危漏洞存不進庫。所以這裡隔離
//     錯誤、記日誌、返回 false，讓漏洞寫入照常提交——代價是丟掉這一條推送。
//     返回 bool 而非 error 是刻意的：呼叫方不該把它當作會影響寫入成敗的錯誤。
func RecordNotificationEventTx(ctx context.Context, tx *sql.Tx, kind string, findingID int64, snap notify.Snapshot) bool {
	raw, err := json.Marshal(snap)
	if err != nil {
		log.Printf("[notify] 序列化推送事件失敗 finding=%d: %v", findingID, err)
		return false
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT notify_event`); err != nil {
		log.Printf("[notify] 建立儲存點失敗 finding=%d: %v", findingID, err)
		return false
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,$2,$3)`,
		kind, findingID, string(raw)); err != nil {
		log.Printf("[notify] 寫入推送事件失敗 finding=%d（漏洞記錄不受影響）: %v", findingID, err)
		// 回滾到儲存點，把事務從 aborted 狀態裡救回來。
		if _, rbErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT notify_event`); rbErr != nil {
			log.Printf("[notify] 回滾到儲存點失敗 finding=%d: %v", findingID, rbErr)
		}
		return false
	}
	// 釋放儲存點，避免長事務裡積攢無用的儲存點。
	_, _ = tx.ExecContext(ctx, `RELEASE SAVEPOINT notify_event`)
	return true
}

// AddNotificationEvent 是 InsertNotificationEventTx 的獨立事務版本，供不在
// 既有事務裡的呼叫點使用（如渠道的「傳送測試訊息」，它沒有真實 finding）。
func (d *DB) AddNotificationEvent(ctx context.Context, kind string, findingID int64, snap notify.Snapshot) (int64, error) {
	raw, err := json.Marshal(snap)
	if err != nil {
		return 0, fmt.Errorf("序列化通知事件快照失敗: %w", err)
	}
	var id int64
	err = d.QueryRowContext(ctx, `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,$2,$3) RETURNING id`,
		kind, findingID, string(raw)).Scan(&id)
	return id, err
}

// FanOutPendingEvents 把尚未分派的漏洞事件按當前啟用的渠道展開成投遞任務，
// 返回本輪處理的事件數與新建的投遞數。
//
// 整輪操作在一個事務裡：事件用 FOR UPDATE SKIP LOCKED 領取，多個程序同時跑
// 也各自領到不同的行（專案裡歸檔佇列的領取用的是同一套手法，見
// db/task_archives.go 的 completeNextArchiveJob）。
//
// 過濾匹配刻意放在 Go 側而非 SQL：渠道的過濾條件是一組可選欄位的 JSONB，
// 用 SQL 表達六種組合的匹配會讓查詢難以維護，而渠道數量是「人手配的幾條」，
// 全量載入後在記憶體裡逐條比對更快也更好測。
//
// 未命中任何渠道的事件同樣會被標記 fanned_out ——否則它會永遠留在待分派集合裡，
// 每個 tick 被重掃一遍。
func (d *DB) FanOutPendingEvents(ctx context.Context, limit int) (eventCount, deliveryCount int, err error) {
	if limit <= 0 {
		limit = 200
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck // 提交成功後是 no-op

	channels, err := listEnabledNotificationChannelsTx(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, kind, finding_id, snapshot FROM notification_events
WHERE NOT fanned_out ORDER BY id FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
	if err != nil {
		return 0, 0, err
	}
	var (
		events      []NotificationEvent
		parsedSnaps []notify.Snapshot
	)
	for rows.Next() {
		var ev NotificationEvent
		if err := rows.Scan(&ev.ID, &ev.Kind, &ev.FindingID, &ev.Snapshot); err != nil {
			rows.Close()
			return 0, 0, err
		}
		var snap notify.Snapshot
		// 快照是我們自己寫的，理論上必定可解析；解析失敗不阻斷投遞流程，
		// 但這條事件會因欄位全空而被所有帶過濾條件的渠道跳過——寧可少推一條
		// 也不讓一個壞行卡死整個佇列。
		_ = json.Unmarshal(ev.Snapshot, &snap)
		// kind 以行內值為準：快照裡那份是渲染用的副本，可能被舊版本寫過。
		snap.Kind = ev.Kind
		events = append(events, ev)
		parsedSnaps = append(parsedSnaps, snap)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if len(events) == 0 {
		return 0, 0, tx.Commit()
	}

	type pending struct {
		eventID   int64
		channelID int64
	}
	var toInsert []pending
	for i, snap := range parsedSnaps {
		for _, ch := range channels {
			if !notify.Match(notify.ParseFilter(ch.Filter), snap) {
				continue
			}
			toInsert = append(toInsert, pending{eventID: events[i].ID, channelID: ch.ID})
		}
	}
	if len(toInsert) > 0 {
		var (
			vals []string
			args []any
		)
		for _, p := range toInsert {
			vals = append(vals, fmt.Sprintf("($%d,$%d)", len(args)+1, len(args)+2))
			args = append(args, p.eventID, p.channelID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_deliveries(event_id,channel_id) VALUES `+strings.Join(vals, ","), args...); err != nil {
			return 0, 0, err
		}
	}

	// 標記本輪事件已分派。未命中任何渠道的事件也一起標記（見函式註釋）。
	ids := make([]string, 0, len(events))
	markArgs := make([]any, 0, len(events))
	for _, ev := range events {
		markArgs = append(markArgs, ev.ID)
		ids = append(ids, fmt.Sprintf("$%d", len(markArgs)))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notification_events SET fanned_out=true WHERE id IN (`+strings.Join(ids, ",")+`)`, markArgs...); err != nil {
		return 0, 0, err
	}
	return len(events), len(toInsert), tx.Commit()
}

// listEnabledNotificationChannelsTx 在事務裡取啟用中的渠道。數量很少，
// 不做分頁也不加快取——快取會引入「改了配置何時生效」這個額外的時序問題。
func listEnabledNotificationChannelsTx(ctx context.Context, tx *sql.Tx) ([]*NotificationChannel, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, kind, config, mode, filter, rate_per_min
FROM notification_channels WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationChannel{}
	for rows.Next() {
		var c NotificationChannel
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Config, &c.Mode, &c.Filter, &c.RatePerMin); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// NotificationAssetNames 把資產 id 解析成簡短展示名，供推送訊息使用。
//
// 返回順序與入參一致、長度可能小於入參（不存在的 id 被跳過）。保持入參順序是
// 為了讓同一條漏洞的訊息在多次投遞裡資產順序穩定——否則重試後收到的訊息裡
// 資產次序變了，會被誤讀成「資產變了」。
func (d *DB) NotificationAssetNames(ctx context.Context, ids []int64) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph, args := placeholders(1, ids)
	rows, err := d.QueryContext(ctx, `SELECT id, type, domain, ip, url, app_name, bundle_id FROM assets WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := map[int64]string{}
	for rows.Next() {
		var (
			id                int64
			typ               string
			domain, ip, url   sql.NullString
			appName, bundleID sql.NullString
		)
		if err := rows.Scan(&id, &typ, &domain, &ip, &url, &appName, &bundleID); err != nil {
			return nil, err
		}
		labels[id] = assetDisplayName(typ, domain.String, ip.String, url.String, appName.String, bundleID.String)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if label, ok := labels[id]; ok && label != "" {
			out = append(out, label)
		}
	}
	return out, nil
}

// assetDisplayName 按資產型別挑選最具辨識度的標識。
// 兜底返回空串，由呼叫方決定怎麼呈現「名字取不到的資產」——本函式不臆造佔位符，
// 否則「資產#42」這種噪音會混進推送訊息裡，讀者還以為是真實域名。
func assetDisplayName(typ, domain, ip, url, appName, bundleID string) string {
	pick := func(vals ...string) string {
		for _, v := range vals {
			if strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
	switch typ {
	case "root_domain", "subdomain":
		return domain
	case "ip":
		return ip
	case "app":
		return pick(appName, bundleID)
	case "service", "endpoint":
		return pick(url, domain, ip)
	default:
		return pick(domain, ip, url, appName)
	}
}

// SetFindingStatusWithNotify 更新漏洞處置狀態，並在同一事務裡登記一條狀態變更
// 推送事件。
//
// 返回 from=變更前的狀態；found=漏洞是否存在；notified=事件是否登記成功。
//
// 三條刻意的行為：
//   - 狀態未實際變化時不登記事件。前端抽屜重複提交同一個值、或自動化指令碼
//     冪等重放，都不該產出推送噪音。
//   - 漏洞不存在時返回 found=false 且不做任何寫入，由呼叫方翻譯成 404。
//   - 事件登記失敗不影響狀態更新（見 RecordNotificationEventTx 的儲存點說明），
//     所以 notified=false 時狀態已經改成功了，呼叫方不應因此報錯。
func (d *DB) SetFindingStatusWithNotify(ctx context.Context, id int64, status string) (from string, found bool, notified bool, err error) {
	err = d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		from, found, _, notified, txErr = SetFindingStatusTx(ctx, tx, id, status)
		return txErr
	})
	return from, found, notified, err
}

// SetFindingStatusTx 在**呼叫方的事務**內更新漏洞狀態並登記狀態變更推送事件。
//
// 抽成事務級函式是為了讓所有改狀態的路徑共用同一套語義——此前只有
// patchFinding 走帶通知的版本，而**複測結論為「已修復」時**（finding_retests
// 裡那條 `UPDATE findings SET status=...`）是直接寫庫的，於是配了
// `on_status_change` 的渠道對這類狀態流轉完全收不到推送：介面上狀態悄悄變了，
// 運維要到開啟平臺才發現。
//
// 返回 from=變更前狀態、found=漏洞是否存在、changed=狀態是否真的變了、
// notified=事件是否登記成功（登記失敗不影響狀態更新，見 RecordNotificationEventTx）。
func SetFindingStatusTx(ctx context.Context, tx *sql.Tx, id int64, status string) (from string, found bool, changed bool, notified bool, err error) {
	var (
		vulnclass, name, severity, summary string
		taskID                             sql.NullInt64
		assetIDs                           []byte
	)
	scanErr := tx.QueryRowContext(ctx, `SELECT vulnclass, name, severity, summary, task_id, asset_ids, status
FROM findings WHERE id=$1 FOR UPDATE`, id).
		Scan(&vulnclass, &name, &severity, &summary, &taskID, &assetIDs, &from)
	if scanErr == sql.ErrNoRows {
		return "", false, false, false, nil
	}
	if scanErr != nil {
		return "", false, false, false, scanErr
	}
	found = true
	if from == status {
		// 狀態沒有真的變化就不登記事件：重複提交同一個值、冪等重放都不該
		// 產生推送噪音。
		return from, true, false, false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE findings SET status=$2 WHERE id=$1`, id, status); err != nil {
		return from, true, false, false, err
	}
	var assets []int64
	_ = json.Unmarshal(assetIDs, &assets)
	notified = RecordNotificationEventTx(ctx, tx, notify.EventFindingStatusChanged, id, notify.Snapshot{
		Kind:       notify.EventFindingStatusChanged,
		FindingID:  id,
		TaskID:     taskID.Int64,
		VulnClass:  vulnclass,
		Name:       name,
		Severity:   severity,
		Summary:    summary,
		AssetIDs:   assets,
		FromStatus: from,
		ToStatus:   status,
	})
	return from, true, true, notified, nil
}

// NotificationStats 是通知頁頂部的概覽計數。
type NotificationStats struct {
	Channels     int   `json:"channels"`
	ChannelsOn   int   `json:"channels_on"`
	Pending      int   `json:"pending"`
	Failed       int   `json:"failed"`
	SentToday    int   `json:"sent_today"`
	BacklogAgeMS int64 `json:"backlog_age_ms"` // 最老的待發投遞距今毫秒數
}

// NotificationStatsSnapshot 彙總通知系統的健康度。
// BacklogAgeMS 是「推送是不是卡住了」最直接的指標——比 pending 計數有用得多，
// 因為積壓 3 條和積壓 3 條的差別可以是從 3 秒到 3 小時。
func (d *DB) NotificationStatsSnapshot(ctx context.Context) (*NotificationStats, error) {
	var s NotificationStats
	if err := d.QueryRowContext(ctx, `SELECT
    (SELECT count(*) FROM notification_channels),
    (SELECT count(*) FROM notification_channels WHERE enabled),
    (SELECT count(*) FROM notification_deliveries WHERE state IN ($1,$2)),
    (SELECT count(*) FROM notification_deliveries WHERE state=$3),
    (SELECT count(*) FROM notification_deliveries WHERE state=$4 AND sent_at >= date_trunc('day', now())),
    COALESCE((SELECT EXTRACT(EPOCH FROM (now() - min(created_at))) * 1000 FROM notification_deliveries WHERE state=$1), 0)::bigint`,
		NotifyStatePending, NotifyStateSending, NotifyStateFailed, NotifyStateSent).
		Scan(&s.Channels, &s.ChannelsOn, &s.Pending, &s.Failed, &s.SentToday, &s.BacklogAgeMS); err != nil {
		return nil, err
	}
	return &s, nil
}
