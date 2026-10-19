package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// 全域設定鍵（存在 settings 鍵值表裡，無需建表）。
const (
	// settingNotifyEnabled 是推送總開關。預設開：它用於維護期一鍵止血，
	// 而不是功能的啟用條件——真正的啟用條件是「有沒有配渠道」。
	settingNotifyEnabled = "notify_enabled"
	// settingNotifyPublicBaseURL 是生成漏洞詳情回鏈的外部訪問地址
	// （如 https://artex.example.com）。留空則訊息裡不帶回鏈按鈕。
	// 專案裡沒有可複用的外部地址配置，所以這裡新增一項。
	settingNotifyPublicBaseURL = "notify_public_base_url"
	// settingNotifyDigestMinutes 是彙總模式的週期（分鐘）。
	settingNotifyDigestMinutes = "notify_digest_interval_min"
)

const (
	// notifyTick 是投遞引擎的輪詢間隔。3 秒是該引擎即時性的上限，
	// 也是「漏洞落庫」到「訊息到達 IM」之間的主要延遲來源。
	notifyTick = 3 * time.Second
	// notifyLease 是領取投遞時的租約時長。必須顯著大於單次投遞的最壞耗時
	// （notify 包的 HTTP 客戶端超時 15 秒），否則會出現同一行被兩個
	// dispatcher 同時投遞。
	notifyLease = 3 * time.Minute
	// notifyFanOutPerTick 限制每輪分派的事件數，避免首次啟用渠道時
	// 一次性把歷史積壓全部展開成投遞任務。
	notifyFanOutPerTick = 200
	// notifyDefaultDigestMinutes 是彙總週期的預設值。
	notifyDefaultDigestMinutes = 30
	// notifyUnlimitedBurstPerTick 是渠道未設限流時的每輪投遞上限。
	// 存在的意義是防止「一個渠道配成不限流 + 一次掃出上千條漏洞」把
	// 單輪迴圈拖成長時間阻塞。
	notifyUnlimitedBurstPerTick = 50
	// notifyMaxSendsPerChannelPerTick 是單渠道每輪最多投遞幾條。
	//
	// 這個上限由**租約時長**倒推：領取時給行打的是租約（notifyLease = 3 分鐘），
	// 若一輪裡序列投遞的條數多到最壞耗時超過租約，後幾條還沒發完租約就過期了。
	// 單程序內無所謂（Run 是單個 goroutine 序列跑，tick 不會重入），但**兩個
	// 程序連同一個庫**時，對端會把租約過期的行重新領走並重復發送，還會把
	// attempts 雙份遞增、在原程序仍在投遞時就判成失敗。
	//
	// 取值：3 分鐘租約 / 30 秒單次超時 = 6 是**剛好用滿租約**、零餘量，
	// 不能取；取 5 讓最壞耗時 150 秒留出 30 秒餘量。這個關係由
	// TestNotifyTickBudgetFitsWithinLease 釘死——改 notifyLease、
	// notifySendTimeout 或本值中的任意一個都會讓那條斷言失敗。
	notifyMaxSendsPerChannelPerTick = 5
	// notifySendTimeout 是單次投遞的超時。它同時決定上一條常量的取值，
	// 兩者相乘不能超過 notifyLease，見 TestNotifyTickBudgetFitsWithinLease。
	notifySendTimeout = 30 * time.Second
)

// notifyBackoff 是失敗重試的退避序列，下標為已嘗試次數。
// 3 次機會（含首次）與 db.MaxNotifyAttempts 對應，兩者必須一起改。
var notifyBackoff = []time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
}

// Notifier 是漏洞推送的投遞引擎。
//
// 與 Scheduler 並列，作為獨立 goroutine 執行（見 server.New）。刻意不復用
// Scheduler 的 tick：推送的即時性要求（3 秒）與觸發器的業務節奏不同，
// 且兩者的失敗互不牽連——推送卡住不該影響 agent 觸發。
type Notifier struct {
	s  *Server
	pg *db.DB

	// mu 保護 buckets。渠道數量少、競爭低，一把互斥鎖足夠，
	// 不值得為它引入更細粒度的結構。
	mu      sync.Mutex
	buckets map[int64]*notifyBucket
}

// notifyBucket 是單渠道的令牌桶。
//
// 用令牌桶而不是「每分鐘計數後清零」的滑動視窗，是因為後者的邊界效應很糟：
// 在視窗末尾發滿 20 條、下一瞬間再發 20 條，對平臺來說是一秒內 40 條，
// 會被限流；令牌桶以恆定速率補充，天然避免這種突發。
type notifyBucket struct {
	tokens   float64
	lastFill time.Time
}

func newNotifier(s *Server) *Notifier {
	return &Notifier{s: s, pg: s.m.pg, buckets: map[int64]*notifyBucket{}}
}

// Run 迴圈直到 ctx 結束。由 server.New 啟動一次。
func (n *Notifier) Run(ctx context.Context) {
	if n.pg == nil {
		return
	}
	t := time.NewTicker(notifyTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.step(ctx)
		}
	}
}

// step 跑一輪：先分派新事件，再投遞到期的任務。
//
// 任何一步失敗都只記日誌、不中斷迴圈——通知系統的故障絕不能升級成程序級問題。
// 每個 tick 都是獨立的，下一輪會自然重試。
func (n *Notifier) step(ctx context.Context) {
	if !n.enabled() {
		return
	}
	if _, _, err := n.pg.FanOutPendingEvents(ctx, notifyFanOutPerTick); err != nil {
		log.Printf("[notify] 分派事件失敗: %v", err)
		return
	}
	channels, err := n.pg.ListNotificationChannels(ctx)
	if err != nil {
		log.Printf("[notify] 讀取渠道失敗: %v", err)
		return
	}
	baseURL := n.publicBaseURL()
	for _, ch := range channels {
		if !ch.IsEnabled() {
			continue
		}
		// 令牌桶的計量單位是**訊息條數**（等價於 HTTP 請求數），不是漏洞條數。
		// 即時模式下兩者相同（一條漏洞一條訊息）；彙總模式下一整批漏洞合成
		// 一條訊息，所以只消耗一個令牌。
		//
		// 兩種模式都先問令牌桶、再按額度去領——順序不能反，否則被限流擋下的
		// 投遞已經消耗過重試次數。
		now := time.Now()
		if ch.Mode == db.NotifyModeDigest {
			tokens, claimLimit := digestTickPlan()
			if n.takeTokens(ch.ID, ch.RatePerMin, tokens, now) <= 0 {
				continue
			}
			n.stepDigest(ctx, ch, claimLimit, baseURL)
			continue
		}
		allow := n.takeTokens(ch.ID, ch.RatePerMin, notifyMaxSendsPerChannelPerTick, now)
		if allow <= 0 {
			continue
		}
		n.stepRealtime(ctx, ch, allow, baseURL)
	}
}

// digestTickPlan 返回彙總渠道本輪的令牌消耗與批次大小上界。
//
// 兩個返回值是**兩個不同的量綱**，這正是獨立成函式的理由：
//
//   - tokens 是訊息條數。一批漏洞合成一條訊息、發一次 HTTP 請求，所以恆為 1。
//     rate_per_min 因此仍然對 digest 生效（每分鐘最多這麼多條彙總訊息）。
//   - claimLimit 是這一批最多裝幾條漏洞。它只受記憶體上界約束，與請求預算無關。
//
// 曾經為了讓 rate_per_min 對 digest 生效，把每輪請求預算
// （notifyMaxSendsPerChannelPerTick，由租約倒推而來）直接當批次大小傳下去。
// 後果是 rate_per_min=20 的渠道在 3 秒的 tick 裡只補到 1 個令牌，於是每條彙總
// 訊息只裝 1 個漏洞——digest 退化成「帶彙總文案的即時推送」，讀者收到的是一串
// 「近 30 分鐘新增 1 個漏洞」，而 db.MaxDigestBatchSize 永不可達。
//
// 這個症狀在端到端測試裡不容易發現（現有用例都手動傳一個夠大的 limit 給
// stepDigest，繞過了 step 裡的額度計算），所以把決策收在這裡由
// TestDigestTickPlanDecouplesBatchSizeFromSendBudget 直接釘住。
func digestTickPlan() (tokens, claimLimit int) {
	return 1, db.MaxDigestBatchSize
}

// stepRealtime 領取並投遞某渠道的即時任務，一條漏洞一條訊息。
func (n *Notifier) stepRealtime(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	deliveries, err := n.pg.ClaimRealtimeDeliveries(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] 領取即時投遞失敗 channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("渠道型別 %q 未註冊", ch.Kind))
		return
	}
	for _, dl := range deliveries {
		msg, err := n.renderSingle(ctx, dl, baseURL)
		if err != nil {
			// 渲染失敗是本地資料問題，重試不會變好。
			_ = n.pg.FailDeliveries(ctx, []int64{dl.ID}, err.Error())
			continue
		}
		n.send(ctx, channel, cfg, msg, []*db.NotificationDelivery{dl})
	}
}

// stepDigest 在批次到期時把某渠道的待發投遞聚合成一條訊息發出。
func (n *Notifier) stepDigest(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	window := n.digestInterval()
	due, err := n.pg.DigestBatchDue(ctx, ch.ID, window)
	if err != nil {
		log.Printf("[notify] 判斷彙總批次失敗 channel=%d: %v", ch.ID, err)
		return
	}
	if !due {
		return
	}
	deliveries, err := n.pg.ClaimDigestBatch(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] 領取彙總批次失敗 channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("渠道型別 %q 未註冊", ch.Kind))
		return
	}
	msg, included, err := n.renderBatch(ctx, deliveries, baseURL, int(window.Minutes()))
	if err != nil {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), err.Error())
		return
	}
	// 快照壞掉、沒能進訊息的那些投遞要顯式判失敗。不這麼做的話它們會留在
	// included 之外、既不進訊息也不進失敗列表——傳送成功時它們的狀態會被
	// 之後的批次標記漏掉，永遠停在 sending 直到租約過期被反覆領取。
	if skipped := excludeDeliveries(deliveries, included); len(skipped) > 0 {
		reason := "事件快照無法解析，本條漏洞無法渲染成訊息"
		if fErr := n.pg.FailDeliveries(ctx, deliveryIDs(skipped), reason); fErr != nil {
			log.Printf("[notify] 標記壞快照投遞失敗 channel=%s ids=%v: %v", ch.Kind, deliveryIDs(skipped), fErr)
		}
		log.Printf("[notify] 跳過 %d 條快照無法解析的投遞 channel=%d", len(skipped), ch.ID)
	}
	// 只把進了訊息的那些交給 send：included[i] 與 msg.Items[i] 嚴格對應，
	// send 依賴這個對應關係把「渠道回報裝下了前 K 條」落到正確的投遞行上。
	n.send(ctx, channel, cfg, msg, included)
}

// send 投遞並按結果流轉狀態。
//
// 同一批投遞（彙總模式下可能幾十條）共享一個傳送結果：要麼送達、要麼整批重試。
// 不做逐條重試——彙總訊息是一條，重發其中一部分會讓批次語義錯亂。
//
// 唯一的例外是**渠道長度上限導致的分段**：渠道回報實際只裝下了前 K 條，
// 那麼第 K+1 條起必須留到下一批，而不是跟著一起被標記成功。否則被截掉的
// 那些漏洞既不在訊息裡、也不在失敗列表裡，徹底消失。
func (n *Notifier) send(ctx context.Context, channel notify.Channel, cfg map[string]any, msg notify.Message, deliveries []*db.NotificationDelivery) {
	// 單次投遞設上限，避免某個渠道卡住把這一輪剩餘渠道全部拖住。
	sendCtx, cancel := context.WithTimeout(ctx, notifySendTimeout)
	defer cancel()
	delivered, err := channel.Send(sendCtx, cfg, msg)
	if err == nil && delivered > 0 {
		if delivered > len(deliveries) {
			// 渠道回報的條數不可能超過投遞數；真發生了說明渲染層算錯了，
			// 按全部送達處理並把問題記下來，總好過把記錄寫亂。
			log.Printf("[notify] 渠道回報送達條數 %d 超過投遞數 %d channel=%s，按全部送達處理",
				delivered, len(deliveries), channel.Kind())
			delivered = len(deliveries)
		}
		sent, rest := deliveries[:delivered], deliveries[delivered:]
		if err := n.pg.MarkDeliveriesSent(ctx, deliveryIDs(sent)); err != nil {
			log.Printf("[notify] 標記已送達失敗 channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(sent), err)
		}
		if len(rest) > 0 {
			// 本條訊息已達渠道長度上限：剩下的立刻回隊，由下一個 tick 續發。
			// 用 DeferDeliveries 而非 RescheduleDeliveries —— 這不是失敗，
			// 不該消耗重試預算（領取時已經樂觀 +1 了，那裡會減回去）。
			if err := n.pg.DeferDeliveries(ctx, deliveryIDs(rest),
				fmt.Sprintf("本條訊息已達渠道長度上限，僅送達前 %d 條，其餘留待下一批", delivered)); err != nil {
				log.Printf("[notify] 分段續發排隊失敗 channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(rest), err)
			}
		}
		return
	}
	if err == nil {
		// 渠道既沒報錯也沒說送達了多少條。按失敗處理（走退避），
		// 免得這條投遞被反覆領取卻永遠標記不掉。
		err = fmt.Errorf("渠道未報告送達條數（delivered=%d）", delivered)
	}

	// 失敗處置**逐條**決定，而不是拿整批的最大嘗試次數做判斷。
	//
	// 曾經是 `if maxAttempts(deliveries) >= MaxNotifyAttempts` 整批判死，但批次裡
	// 各條的嘗試次數並不相同：一個已經重試兩次的老投遞（attempts=2）會把同一批裡
	// 全新的投遞（attempts=1）一起拖進 failed——新漏洞一條重試都沒用上就永久丟了，
	// 與「不讓老行拖新行下水」的初衷正好相反。
	permanent := notify.IsPermanent(err)
	var failIDs, exhaustedIDs []int64
	byDelay := map[time.Duration][]int64{}
	for _, dl := range deliveries {
		switch {
		case permanent:
			failIDs = append(failIDs, dl.ID)
		case dl.Attempts >= db.MaxNotifyAttempts:
			exhaustedIDs = append(exhaustedIDs, dl.ID)
		default:
			delay := notifyBackoff[min(dl.Attempts, len(notifyBackoff)-1)]
			byDelay[delay] = append(byDelay[delay], dl.ID)
		}
	}

	if len(failIDs) > 0 {
		if fErr := n.pg.FailDeliveries(ctx, failIDs, err.Error()); fErr != nil {
			log.Printf("[notify] 標記失敗狀態出錯 channel=%s ids=%v: %v", channel.Kind(), failIDs, fErr)
		}
	}
	if len(exhaustedIDs) > 0 {
		reason := fmt.Sprintf("重試 %d 次後仍失敗: %s", db.MaxNotifyAttempts, err)
		if fErr := n.pg.FailDeliveries(ctx, exhaustedIDs, reason); fErr != nil {
			log.Printf("[notify] 標記失敗狀態出錯 channel=%s ids=%v: %v", channel.Kind(), exhaustedIDs, fErr)
		}
	}
	// 按延遲分組重排：只有 3 檔退避，分組數天然很小，不必為每條單獨發一次
	// UPDATE（那會讓一個 500 條的批次產生 500 次往返）。
	for delay, group := range byDelay {
		if rErr := n.pg.RescheduleDeliveries(ctx, group, delay, err.Error()); rErr != nil {
			log.Printf("[notify] 重排投遞失敗 channel=%s ids=%v: %v", channel.Kind(), group, rErr)
		}
	}
	if len(failIDs)+len(exhaustedIDs) > 0 {
		log.Printf("[notify] 投遞失敗 channel=%d kind=%s 永久失敗=%d 重試耗盡=%d 待重試=%d: %s",
			deliveries[0].ChannelID, channel.Kind(), len(failIDs), len(exhaustedIDs), len(byDelay), err)
	}
}

// excludeDeliveries 返回 all 中不在 keep 裡的那些（按指標身份比較）。
// 用於找出「沒能進訊息」的投遞——它們必須被顯式處置，不能留在灰色地帶。
func excludeDeliveries(all, keep []*db.NotificationDelivery) []*db.NotificationDelivery {
	inKeep := make(map[*db.NotificationDelivery]bool, len(keep))
	for _, dl := range keep {
		inKeep[dl] = true
	}
	var out []*db.NotificationDelivery
	for _, dl := range all {
		if !inKeep[dl] {
			out = append(out, dl)
		}
	}
	return out
}

// adapt 取渠道實現並解析其配置。
// 返回 ok=false 表示型別未註冊，投遞應直接判失敗而不是無限重試。
func (n *Notifier) adapt(ch *db.NotificationChannel) (notify.Channel, map[string]any, bool) {
	channel, ok := notify.Get(ch.Kind)
	if !ok {
		return nil, nil, false
	}
	var cfg map[string]any
	if len(ch.Config) > 0 {
		// 配置解析失敗時給一個空 map：渠道自身的 Validate 會報出「缺哪個欄位」，
		// 那個錯誤比 JSON 解析錯誤更能指導使用者修復。
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return channel, cfg, true
}

// renderSingle 渲染單條漏洞訊息。
func (n *Notifier) renderSingle(ctx context.Context, dl *db.NotificationDelivery, baseURL string) (notify.Message, error) {
	snap, err := parseSnapshot(dl)
	if err != nil {
		return notify.Message{}, err
	}
	item, err := n.itemFor(ctx, snap, baseURL)
	if err != nil {
		return notify.Message{}, err
	}
	return notify.Message{Items: []notify.Item{item}, HomeURL: baseURL}, nil
}

// renderBatch 渲染彙總訊息。逐條解析快照——單條壞了只跳過那一條，
// 不讓它把整批匯總拖沒。
//
// 返回值 included 與 msg.Items **嚴格一一對應**（第 i 個投遞 ↔ 第 i 個條目）。
// 這個對應關係是硬要求：呼叫方按「渠道回報裝下了前 K 條」來決定前 K 個投遞
// 標記已送達。若這裡跳過了壞快照卻不把跳過的投遞從 included 裡剔除，
// 下標就會錯位——本該失敗的壞條目會被標成已送達，而好條目被誤判為未送達。
// 壞掉的那些由呼叫方顯式標記失敗，見 stepDigest。
func (n *Notifier) renderBatch(ctx context.Context, deliveries []*db.NotificationDelivery, baseURL string, windowMinutes int) (notify.Message, []*db.NotificationDelivery, error) {
	items := make([]notify.Item, 0, len(deliveries))
	included := make([]*db.NotificationDelivery, 0, len(deliveries))
	for _, dl := range deliveries {
		snap, err := parseSnapshot(dl)
		if err != nil {
			// 壞快照不進訊息，也不進 included——它的處置由呼叫方負責
			// （顯式標記失敗，而不是混在「已送達」裡矇混過關）。
			log.Printf("[notify] 彙總批次中跳過無法解析的快照 delivery=%d: %v", dl.ID, err)
			continue
		}
		item, err := n.itemFor(ctx, snap, baseURL)
		if err != nil {
			return notify.Message{}, nil, err
		}
		items = append(items, item)
		included = append(included, dl)
	}
	if len(items) == 0 {
		return notify.Message{}, nil, fmt.Errorf("彙總批次 %d 條投遞全部無法解析", len(deliveries))
	}
	return notify.Message{
		Items:         items,
		Batch:         true,
		WindowMinutes: windowMinutes,
		HomeURL:       baseURL,
	}, included, nil
}

// itemFor 把事件快照渲染成待推送條目，順帶解析資產名與詳情回鏈。
func (n *Notifier) itemFor(ctx context.Context, snap notify.Snapshot, baseURL string) (notify.Item, error) {
	assets, err := n.pg.NotificationAssetNames(ctx, snap.AssetIDs)
	if err != nil {
		// 資產名解析失敗不該阻止推送：讀不到名字比收不到通知輕得多，
		// 訊息裡少一行資產而已。
		log.Printf("[notify] 解析資產名失敗 finding=%d: %v", snap.FindingID, err)
	}
	item := notify.Item{
		FindingID:  snap.FindingID,
		Name:       snap.Name,
		VulnClass:  snap.VulnClass,
		Severity:   snap.Severity,
		Summary:    snap.Summary,
		Assets:     assets,
		FromStatus: snap.FromStatus,
		ToStatus:   snap.ToStatus,
	}
	if baseURL != "" {
		// 詳情頁路由見 web/src/app/(main)/function/findings/detail/page.tsx，
		// 它從 query 引數 id 讀取漏洞 id。
		item.DetailURL = fmt.Sprintf("%s/function/findings/detail?id=%d", baseURL, snap.FindingID)
	}
	return item, nil
}

// takeTokens 從渠道令牌桶裡取走**最多 want 個**令牌，返回實際取到的數量。
//
// 一個令牌 = 一條訊息（一次 HTTP 請求）。即時模式下呼叫方要幾條就傳幾條；
// 彙總模式下一整批漏洞只發一條訊息，傳 1。
//
// 桶容量為該渠道每分鐘上限，按恆定速率補充。ratePerMin<=0 表示不限流，
// 返回一個有限但足夠大的值，防止單輪迴圈被無限積壓拖住。
//
// want 這個上限是必需的：沒有它就只能把桶整個抽空，而呼叫方自己還有每輪上限，
// 多取的令牌既用不上、又在下次補充前憑空消失——攢下來的突發容量永遠不可達，
// 連「這一輪沒有任何待發投遞」都會照扣一筆。
func (n *Notifier) takeTokens(channelID int64, ratePerMin, want int, now time.Time) int {
	if want <= 0 {
		return 0
	}
	if ratePerMin <= 0 {
		return min(want, notifyUnlimitedBurstPerTick)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	b := n.buckets[channelID]
	if b == nil {
		b = &notifyBucket{tokens: float64(ratePerMin), lastFill: now}
		n.buckets[channelID] = b
	}
	// 按經過的真即時間補充，速率是 ratePerMin/60 每秒。
	if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		b.tokens = minF(float64(ratePerMin), b.tokens+elapsed*float64(ratePerMin)/60)
		b.lastFill = now
	}
	// 加一個極小 epsilon 再取整：令牌數是浮點累加出來的，分兩次補滿時
	// 0.5 + 0.5 可能得到 0.9999999999，直接 int() 會被截成 0——
	// 數學上已滿的桶卻取不出令牌。1e-9 遠小於一個令牌，不會放過真正的欠額。
	take := min(int(b.tokens+1e-9), want)
	if take <= 0 {
		return 0
	}
	b.tokens -= float64(take)
	return take
}

// enabled 讀取總開關。
func (n *Notifier) enabled() bool {
	return n.pg.GetBool(settingNotifyEnabled, true)
}

// publicBaseURL 返回回鏈用的外部地址，去掉尾部斜槓。
func (n *Notifier) publicBaseURL() string {
	v, ok, err := n.pg.GetSetting(settingNotifyPublicBaseURL)
	if err != nil || !ok {
		return ""
	}
	return trimTrailingSlash(v)
}

// digestInterval 返回彙總週期，非法或未配置時回落到預設值。
func (n *Notifier) digestInterval() time.Duration {
	v, ok, err := n.pg.GetSetting(settingNotifyDigestMinutes)
	if err != nil || !ok {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	m := 0
	if _, err := fmt.Sscanf(v, "%d", &m); err != nil || m <= 0 {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	return time.Duration(m) * time.Minute
}

// parseSnapshot 解析投遞對應事件的快照。
func parseSnapshot(dl *db.NotificationDelivery) (notify.Snapshot, error) {
	var snap notify.Snapshot
	if len(dl.Snapshot) == 0 {
		return snap, fmt.Errorf("投遞 %d 的事件快照為空", dl.ID)
	}
	if err := json.Unmarshal(dl.Snapshot, &snap); err != nil {
		return snap, fmt.Errorf("解析投遞 %d 的事件快照失敗: %w", dl.ID, err)
	}
	if snap.Kind == "" {
		// 事件型別以事件行為準，快照裡那份可能由舊版本寫過。
		snap.Kind = dl.EventKind
	}
	return snap, nil
}

func deliveryIDs(deliveries []*db.NotificationDelivery) []int64 {
	out := make([]int64, 0, len(deliveries))
	for _, dl := range deliveries {
		out = append(out, dl.ID)
	}
	return out
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
