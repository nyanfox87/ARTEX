package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 本檔案是投遞任務的領取與狀態流轉。
//
// 領取用「租約」而非長事務：把行置為 sending 並把 next_attempt_at 推到未來作為
// 租約到期時間，提交事務後再去做網路投遞。這樣投遞期間不持有資料庫鎖——
// 網路請求可能耗時數秒（客戶端超時 15 秒），佔著行鎖不放會拖垮同庫的其它寫操作。
//
// 代價是程序若在投遞途中崩潰，行會停在 sending。這是**可自愈**的：租約到期後
// next_attempt_at 落入過去，下一輪領取會把同一行重新撈起來（見領取條件裡的
// state IN ('pending','sending')）。重試計數在領取時就已 +1，所以崩潰不會造成
// 無限重試——MaxNotifyAttempts 次機會用完後落入 failed 等人工處理。

// MaxNotifyAttempts 是一條投遞的最大嘗試次數（含首次）。
// 定義在這裡而非投遞引擎裡：它是狀態機自身的策略，引擎只是執行者。
const MaxNotifyAttempts = 3

// MaxDigestBatchSize 是單個彙總批次一次最多合併多少條投遞。
//
// 存在的理由是資源：一個彙總週期內如果掃出幾萬個漏洞（完全可能——一次全量掃描
// 就能做到），不設上界的話領取會把全部行讀進記憶體、渲染成一條超長訊息，
// 然後被渠道的長度上限截掉大半——既浪費記憶體，又**靜默丟失**被截掉的那些漏洞。
// 設上界後，超出的部分留在庫裡成為下一個批次，下個週期自然發出去，不會丟。
//
// 取 500 的依據：它是渲染成訊息後在企微 4096 位元組上限內還"有內容可讀"的量級；
// 再大也只是讓截斷髮生在更靠後的位置而已。
const MaxDigestBatchSize = 500

// NotificationDelivery 是一條投遞任務，含渲染所需的渠道配置與事件快照。
type NotificationDelivery struct {
	ID            int64           `json:"id"`
	EventID       int64           `json:"event_id"`
	ChannelID     int64           `json:"channel_id"`
	State         string          `json:"state"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt time.Time       `json:"next_attempt_at"`
	LastError     string          `json:"last_error"`
	BatchID       *int64          `json:"batch_id,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	SentAt        *time.Time      `json:"sent_at,omitempty"`
	Snapshot      json.RawMessage `json:"snapshot,omitempty"`
	// 聯合載入的渲染上下文，不進 JSON（由 server 層組裝 DTO）。
	Channel *NotificationChannel `json:"-"`
	// FindingID/EventKind 從事件帶出，供歷史列表直接跳轉漏洞詳情。
	FindingID int64  `json:"finding_id,string"`
	EventKind string `json:"event_kind"`
	// ChannelName/ChannelKind 是列表展示用的冗餘欄位，省掉前端二次查詢。
	ChannelName string `json:"channel_name"`
	ChannelKind string `json:"channel_kind"`
}

const notificationDeliveryCols = `d.id, d.event_id, d.channel_id, d.state, d.attempts, d.next_attempt_at,
       d.last_error, d.batch_id, d.created_at, d.sent_at`

// joinedDeliveryQuery 是投遞行的統一讀取形狀：投遞 + 事件快照 + 渠道配置。
// 渲染一條訊息三者缺一不可，分開查會寫出三次往返。
const joinedDeliveryQuery = `SELECT ` + notificationDeliveryCols + `,
       e.snapshot, e.kind, e.finding_id,
       c.id, c.name, c.kind, c.enabled, c.config, c.mode, c.filter, c.rate_per_min
FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id
JOIN notification_channels c ON c.id = d.channel_id`

func scanNotificationDelivery(sc interface{ Scan(...any) error }) (*NotificationDelivery, error) {
	var (
		dl        NotificationDelivery
		lastErr   sql.NullString
		batchID   sql.NullInt64
		sentAt    sql.NullTime
		snapshot  []byte
		eventKind string
		channel   NotificationChannel
		chEnabled bool
	)
	if err := sc.Scan(&dl.ID, &dl.EventID, &dl.ChannelID, &dl.State, &dl.Attempts, &dl.NextAttemptAt,
		&lastErr, &batchID, &dl.CreatedAt, &sentAt,
		&snapshot, &eventKind, &dl.FindingID,
		&channel.ID, &channel.Name, &channel.Kind, &chEnabled, &channel.Config, &channel.Mode, &channel.Filter, &channel.RatePerMin); err != nil {
		return nil, err
	}
	dl.LastError = lastErr.String
	if batchID.Valid {
		dl.BatchID = &batchID.Int64
	}
	if sentAt.Valid {
		dl.SentAt = &sentAt.Time
	}
	dl.Snapshot = json.RawMessage(snapshot)
	dl.EventKind = eventKind
	dl.ChannelName = channel.Name
	dl.ChannelKind = channel.Kind
	channel.Enabled = &chEnabled
	dl.Channel = &channel
	return &dl, nil
}

// claimQuery 描述一次領取：先按 sel 選出候選並加鎖，再把它們置為 sending 並
// 延長租約。sel 裡的 lease 位置由呼叫方用 $n 佔位並自行傳參。
type claimQuery struct {
	sql  string
	args []any
}

// ClaimRealtimeDeliveries 領取某渠道一批到期的即時投遞，最多 limit 條。
//
// 刻意按**單個渠道**領取而不是「全域領一批再挑著發」：限流閘在投遞引擎裡按渠道
// 維護，只有先知道這個渠道這一輪還能發幾條、再去領同樣多的行，限流才不會消耗
// 重試次數。若反過來先領後棄，被限流擋下的行已經被計過一次 attempts，
// 3 次預算會被純粹的等待耗光，最後落進 failed。
//
// 條件含「租約已過期的 sending」——那是崩潰自愈的落點。lease 必須顯著大於單次
// 投遞的最壞耗時（渠道 HTTP 客戶端超時 15 秒），否則同一行會被兩個 dispatcher
// 同時投遞。同時擋掉已停用渠道：停用操作已把存量投遞標記為 skipped，
// 這裡再攔一道，避免停用與領取併發時的漏網。
func (d *DB) ClaimRealtimeDeliveries(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	return d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now()
  AND c.enabled AND c.mode = $4
ORDER BY dd.next_attempt_at, dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $5`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, NotifyModeRealtime, limit},
	}, nil)
}

// DigestBatchDue 報告該渠道是否已攢夠一個到期批次：存在待發投遞，且**最老的那條**
// 年齡已達到彙總週期。
//
// 判定依據是最老投遞的年齡而非牆上時鐘：這樣剛建好的渠道不會因為對齊到整點而
// 立刻吐出一條只有一條的「彙總」，積壓很久的批次也不會再白等一輪。
//
// 與 ClaimDigestBatch 分開是因為語義不同：本函式只回答「該不該發」，
// 而領取要拿走該渠道**全部**待發行（包括尚未滿年齡的那些）——否則一個週期
// 會被拆成多條訊息，彙總就失去意義了。
func (d *DB) DigestBatchDue(ctx context.Context, channelID int64, minAge time.Duration) (bool, error) {
	var due bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM notification_deliveries d
  JOIN notification_channels c ON c.id = d.channel_id
  WHERE d.channel_id = $1 AND d.state IN ($2,$3) AND c.enabled
  GROUP BY d.channel_id
  HAVING min(d.created_at) <= now() - make_interval(secs => $4)
)`, channelID, NotifyStatePending, NotifyStateSending, int64(minAge.Seconds())).Scan(&due)
	return due, err
}

// ClaimDigestBatch 領取某渠道當前到期的待發投遞，作為一個彙總批次，
// 單批最多 MaxDigestBatchSize 條。
//
// 同批次的所有投遞共享 batch_id，用集合裡的最小 id 作批次號（穩定、可讀、
// 無需額外序列）。重試時用 COALESCE 保留原批次號，使「這批 N 條是一起發的」
// 在多次重試後依然成立。
//
// 按 id 升序取前 N 條而非隨機取：最早產生的投遞最先發出去，積壓時不會出現
// 「新漏洞先發、老漏洞永遠排在後面」的飢餓。
func (d *DB) ClaimDigestBatch(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	// limit 是**記憶體上界**，呼叫方傳 MaxDigestBatchSize；這裡再夾一道，
	// 防止呼叫方傳進一個更大的值。
	//
	// 刻意不接受「限流額度」充當批次大小：限流的單位是訊息條數——一個批次只發
	// 一條訊息、消耗一個令牌，由 server 層的 takeTokens 扣除——與「一批裝幾條
	// 漏洞」是兩個不同的量綱。曾經為了讓 rate_per_min 對 digest 生效而把每輪
	// 請求預算傳進來當批次大小，結果 rate=20/min 的渠道每批只裝 1 條漏洞，
	// digest 退化成帶彙總文案的即時推送。要改限流請改 takeTokens 的 want，
	// 不要動這裡。
	if limit > MaxDigestBatchSize {
		limit = MaxDigestBatchSize
	}
	out, err := d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now() AND c.enabled
ORDER BY dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $4`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, limit},
	}, func(tx *sql.Tx, ids []int64) error {
		batchID := ids[0]
		for _, id := range ids {
			if id < batchID {
				batchID = id
			}
		}
		ph, idArgs := placeholders(2, ids)
		_, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET batch_id = COALESCE(batch_id, $1)
WHERE id IN (`+ph+`)`, append([]any{batchID}, idArgs...)...)
		return err
	})
	return out, err
}

// claimDeliveries 執行「選取 + 置 sending 延長租約 + 讀取完整行」，全在一個事務裡。
// postClaim 是可選的附加步驟（彙總批次用它寫入 batch_id）。
func (d *DB) claimDeliveries(ctx context.Context, lease time.Duration, cq claimQuery, postClaim func(*sql.Tx, []int64) error) ([]*NotificationDelivery, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // 提交成功後是 no-op

	ids, err := selectForClaim(ctx, tx, cq.sql, cq.args...)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, tx.Commit()
	}
	// 置 sending 並把 next_attempt_at 推到未來：這個未來時刻即租約到期時間，
	// 「租約未到期」與「未到重試時間」因此共用同一個條件表達，不需要新增列。
	ph, idArgs := placeholders(3, ids)
	if _, err := tx.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=attempts+1, next_attempt_at=now()+make_interval(secs => $2)
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateSending, lease.Seconds()}, idArgs...)...); err != nil {
		return nil, err
	}
	if postClaim != nil {
		if err := postClaim(tx, ids); err != nil {
			return nil, err
		}
	}
	out, err := loadDeliveriesTx(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func selectForClaim(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func loadDeliveriesTx(ctx context.Context, tx *sql.Tx, ids []int64) ([]*NotificationDelivery, error) {
	ph, args := placeholders(1, ids)
	rows, err := tx.QueryContext(ctx, joinedDeliveryQuery+` WHERE d.id IN (`+ph+`) ORDER BY d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dl)
	}
	return out, rows.Err()
}

// MarkDeliveriesSent 把一批投遞標記為已送達。
func (d *DB) MarkDeliveriesSent(ctx context.Context, ids []int64) error {
	ph, args := placeholders(2, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, sent_at=now(), last_error='' WHERE id IN (`+ph+`)`, append([]any{NotifyStateSent}, args...)...)
	return err
}

// RescheduleDeliveries 把一批投遞退回 pending 並推後重試時間。
//
// 退回 pending 而不是引入新的中間狀態，是為了讓「還剩幾次機會」只由一個地方
// 表達（MaxNotifyAttempts），避免狀態機的分支隨重試策略膨脹。
func (d *DB) RescheduleDeliveries(ctx context.Context, ids []int64, delay time.Duration, errMsg string) error {
	ph, args := placeholders(4, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, next_attempt_at=now()+make_interval(secs => $2), last_error=$3
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, delay.Seconds(), truncateNotifyError(errMsg)}, args...)...)
	return err
}

// DeferDeliveries 把一批投遞退回 pending、立即可再領，並**撤銷領取時計的那一次嘗試**。
//
// 用途只有一個：彙總訊息按渠道長度上限分段傳送時，沒裝進本條的條目要留到下一批。
// 那不是失敗，所以不該消耗重試預算——領取時 attempts 已經樂觀地 +1 了，
// 這裡必須減回去。否則一個 500 條的積壓會按每段 20 條切成 25 段，
// 尾部條目在第 3 段就被 MaxNotifyAttempts 判成 failed，而它們從未出過任何錯。
//
// GREATEST(...,0) 兜住「有人手工重發把 attempts 清零後又走到這裡」的情況，
// 不讓計數變成負數。
func (d *DB) DeferDeliveries(ctx context.Context, ids []int64, reason string) error {
	ph, args := placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=GREATEST(attempts-1, 0), next_attempt_at=now(), last_error=$2
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, truncateNotifyError(reason)}, args...)...)
	return err
}

// FailDeliveries 把一批投遞標記為最終失敗，等待人工在投遞歷史裡重發。
func (d *DB) FailDeliveries(ctx context.Context, ids []int64, errMsg string) error {
	// 佔位符從 $3 開始：$1 是 state、$2 是 last_error。
	ph, args := placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries SET state=$1, last_error=$2 WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateFailed, truncateNotifyError(errMsg)}, args...)...)
	return err
}

// RetryNotificationDelivery 手動重發一條投遞：重置為 pending、清零重試計數、
// 立即到期。清計數是刻意的——人工點「重發」意味著前幾次失敗的原因已被處理，
// 再拿舊計數限制它沒有道理。
func (d *DB) RetryNotificationDelivery(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$2, attempts=0, next_attempt_at=now(), last_error=''
WHERE id=$1 AND state IN ($3,$4)`, id, NotifyStatePending, NotifyStateFailed, NotifyStateSkipped)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("投遞 %d 不存在或當前狀態不允許重發", id)
	}
	return nil
}

// NotificationDeliveryFilter 是投遞歷史的查詢條件。
type NotificationDeliveryFilter struct {
	ChannelID int64
	State     string
	EventKind string
}

func (f NotificationDeliveryFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.ChannelID > 0 {
		args = append(args, f.ChannelID)
		conds = append(conds, fmt.Sprintf("d.channel_id=$%d", len(args)))
	}
	if f.State != "" {
		args = append(args, f.State)
		conds = append(conds, fmt.Sprintf("d.state=$%d", len(args)))
	}
	if f.EventKind != "" {
		args = append(args, f.EventKind)
		conds = append(conds, fmt.Sprintf("e.kind=$%d", len(args)))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListNotificationDeliveries 分頁返回投遞歷史，新的在前。
func (d *DB) ListNotificationDeliveries(ctx context.Context, f NotificationDeliveryFilter, page, pageSize int) ([]*NotificationDelivery, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	where, args := f.where()

	var total int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := fmt.Sprintf("%s%s ORDER BY d.id DESC LIMIT $%d OFFSET $%d",
		joinedDeliveryQuery, where, len(args)+1, len(args)+2)
	rows, err := d.QueryContext(ctx, q, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, dl)
	}
	return out, total, rows.Err()
}

// truncateNotifyError 把錯誤資訊截到列可接受的長度。渠道返回的回應體可能很長
// （通用 Webhook 打到自建服務時尤甚），不截斷會讓歷史列表的載荷膨脹。
func truncateNotifyError(msg string) string {
	const max = 500
	if len(msg) <= max {
		return msg
	}
	// 按字元邊界回退，避免留下半個 UTF-8 字元讓前端顯示成亂碼。
	cut := max
	for cut > 0 && !isUTF8Start(msg[cut]) {
		cut--
	}
	return msg[:cut] + "…"
}

func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }

// placeholders 生成從 start 開始的 $n 佔位串及對應引數，供 IN (...) 使用。
// 例如 start=3, ids=[7,8] → "$3,$4", [7,8]。
func placeholders(start int, ids []int64) (string, []any) {
	ph := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		ph = append(ph, fmt.Sprintf("$%d", start+i))
		args = append(args, id)
	}
	return strings.Join(ph, ","), args
}
