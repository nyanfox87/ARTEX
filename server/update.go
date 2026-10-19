package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/Autumn-27/artex/selfupdate"
)

// 頁面一鍵更新的 HTTP 面。真正的下載/校驗/換裝邏輯全在 selfupdate 包裡，
// 這裡只負責鑑權邊界、併發互斥、進度廣播，以及把"該退出了"告訴 main。
//
// 重啟不由本程序完成：暫存好新版本後進程以 selfupdate.ExitRestart 退出，
// 由守護指令碼（start.sh / start.bat，Docker 下是 ENTRYPOINT）重新拉起。

// restartCh 在升級就緒或回滾完成後關閉，main 收到後以 ExitRestart 退出。
var (
	restartOnce sync.Once
	restartCh   = make(chan struct{})
)

// RestartRequested 返回一個在"請退出並讓守護程序重新拉起我"時關閉的 channel。
func RestartRequested() <-chan struct{} { return restartCh }

func requestRestart() { restartOnce.Do(func() { close(restartCh) }) }

// bootState 是本次啟動時 selfupdate.Bootstrap 的結論（升級成功 / 剛回滾 /
// 暫存件被丟棄），由 main 注入，供 /api/update/check 如實告訴前端上一次升級的下場。
var (
	bootStateMu sync.Mutex
	bootState   selfupdate.State
)

// SetBootUpdateState 由 main 在啟動時呼叫一次。
func SetBootUpdateState(st selfupdate.State) {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	bootState = st
}

func bootUpdateState() selfupdate.State {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	return bootState
}

// releaseCache 快取 GitHub 的最新版本查詢結果。
//
// 頂欄的"有新版本"提示會在每次整頁載入時查一次，而未認證的 GitHub API 是
// 每 IP 每小時 60 次——不快取的話，多開幾個標籤頁或刷幾次頁面就把配額耗光了，
// 之後真想更新時反而查不動。使用者顯式點"檢查更新"時可以 force 繞過快取。
type releaseCache struct {
	mu  sync.Mutex
	rel *selfupdate.Release
	err error
	at  time.Time
	// fetch 是取數函式，僅為測試留的注入點；為 nil 時走真正的 GitHub 查詢。
	fetch func(context.Context, *http.Client) (*selfupdate.Release, error)
}

const (
	releaseTTL = 30 * time.Minute
	// 失敗結果也快取一小會兒，否則 GitHub 不可達時每次頁面載入都要乾等一次超時；
	// 但 TTL 要短，網路恢復後很快就能自己好。
	releaseErrTTL = 2 * time.Minute
	// 查詢用的超時。NewClient 的 30 分鐘超時是給下載整包用的，查版本不能等那麼久。
	releaseTimeout = 20 * time.Second
)

var relCache = &releaseCache{}

// get 返回最新 Release，命中快取則不訪問網路。
//
// 取數期間一直持有鎖：併發請求會排隊等同一次查詢的結果，而不是各自去打 GitHub
// （頁面剛載入時多個標籤頁同時來查，正是最容易觸發限流的時刻）。
func (c *releaseCache) get(ctx context.Context, client *http.Client, force bool) (*selfupdate.Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !force {
		ttl := releaseTTL
		if c.err != nil {
			ttl = releaseErrTTL
		}
		if !c.at.IsZero() && time.Since(c.at) < ttl {
			return c.rel, c.err
		}
	}

	fetch := c.fetch
	if fetch == nil {
		fetch = selfupdate.FetchLatest
	}
	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	rel, err := fetch(ctx, client)
	// 請求被取消（使用者關了標籤頁）不代表 GitHub 有問題，別把它寫進快取，
	// 否則下一個訪客會拿到一條莫名其妙的"已取消"錯誤。
	if err != nil && ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return c.rel, err
	}
	c.rel, c.err, c.at = rel, err, time.Now()
	return rel, err
}

// updateProgress 是推給前端的一條進度。
type updateProgress struct {
	Phase   selfupdate.Phase `json:"phase"`
	Percent int              `json:"percent"` // 僅下載階段有意義；其餘為 -1
	Message string           `json:"message"`
	Version string           `json:"version,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// updateHub 持有一次升級的進度並廣播給 SSE 訂閱者。
//
// running 同時充當互斥：升級期間再次 POST /api/update/apply 直接 409，
// 避免兩個 goroutine 同時往同一個 artex.new 寫。
type updateHub struct {
	mu      sync.Mutex
	running bool
	cur     updateProgress
	subs    map[chan updateProgress]struct{}
}

var updHub = &updateHub{
	cur:  updateProgress{Phase: selfupdate.PhaseIdle, Percent: -1},
	subs: map[chan updateProgress]struct{}{},
}

// begin 搶佔升級權限，已在進行中則返回 false。
func (h *updateHub) begin(version string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return false
	}
	h.running = true
	h.cur = updateProgress{Phase: selfupdate.PhaseDownload, Percent: 0, Message: "準備中…", Version: version}
	h.fanout(h.cur)
	return true
}

// finish 結束一次升級。err 為 nil 表示暫存成功，等待重啟。
func (h *updateHub) finish(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running = false
	if err != nil {
		h.cur = updateProgress{Phase: selfupdate.PhaseFailed, Percent: -1, Message: "更新失敗", Error: err.Error(), Version: h.cur.Version}
	} else {
		h.cur = updateProgress{Phase: selfupdate.PhaseStaged, Percent: 100, Message: "新版本已就緒，正在重啟…", Version: h.cur.Version}
	}
	h.fanout(h.cur)
}

func (h *updateHub) publish(ph selfupdate.Phase, pct int, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cur = updateProgress{Phase: ph, Percent: pct, Message: msg, Version: h.cur.Version}
	h.fanout(h.cur)
}

// fanout 必須在持有 h.mu 時呼叫。訂閱者 channel 是有緩衝的，滿了就丟——
// 進度是可丟棄的瞬時資訊，絕不能讓一個卡住的 SSE 連線阻塞升級本身。
func (h *updateHub) fanout(p updateProgress) {
	for ch := range h.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

func (h *updateHub) snapshot() (updateProgress, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur, h.running
}

func (h *updateHub) subscribe() (<-chan updateProgress, func()) {
	ch := make(chan updateProgress, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
}

// updateCheck 查詢 GitHub 上的最新正式版並與當前版本比較。
//
// 前端也會直連 api.github.com（GitHub 的 CORS 是 *），但**以本介面為準**：
// 下載是後端做的，只有後端能訪問 GitHub 才談得上更新。瀏覽器能連、伺服器連不上
// 的情況很常見（伺服器在內網、或代理只配在瀏覽器上），那時點更新必然失敗，
// 不如在檢查這一步就如實報錯。
func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	current := BuildVersion
	mode := "binary"
	if selfupdate.InDocker() {
		mode = "docker"
	}
	boot := bootUpdateState()
	out := map[string]any{
		"current":     current,
		"mode":        mode,
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"has_backup":  selfupdate.HasBackup(),
		"repo":        selfupdate.Repo,
		"boot_notice": boot.Detail,
		"rolled_back": boot.RolledBack,
	}

	// 頂欄提示走快取（預設）；使用者點"檢查更新"時帶 force=1 強制回源。
	force := r.URL.Query().Get("force") != ""
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(r.Context(), client, force)
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, 200, out)
		return
	}

	latest := rel.TagName
	out["latest"] = latest
	out["notes"] = rel.Body
	out["html_url"] = rel.HTMLURL
	if !rel.PublishedAt.IsZero() {
		out["published_at"] = rel.PublishedAt.Format(time.RFC3339)
	}

	asset := selfupdate.AssetName(latest, runtime.GOOS, runtime.GOARCH)
	out["asset"] = asset
	if a, ok := rel.FindAsset(asset); ok {
		out["asset_available"] = true
		out["size"] = a.Size
	} else {
		out["asset_available"] = false
	}

	cmp, comparable := selfupdate.CompareVersions(current, latest)
	out["comparable"] = comparable
	out["has_update"] = comparable && cmp < 0
	if !comparable {
		// 開發構建（dev / git describe 帶字尾）沒有可比較的版本號。放行只會
		// 用正式版覆蓋掉本地正在除錯的二進位制，所以直接不給更新。
		out["reason"] = fmt.Sprintf("當前版本 %q 不是正式釋出版本，已停用一鍵更新", current)
	}
	writeJSON(w, 200, out)
}

// updateApply 下載並暫存新版本，完成後讓程序退出交給守護指令碼重啟。
//
// 立刻返回 202，實際工作在後臺 goroutine 上跑：整包下載可能要幾分鐘，
// 掛在請求上會被反代超時掐斷。進度走 /api/update/stream。
func (s *Server) updateApply(w http.ResponseWriter, r *http.Request) {
	current := BuildVersion

	// 走快取：確保裝上的就是使用者在介面上看到並確認的那個版本。
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(r.Context(), client, false)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	cmp, comparable := selfupdate.CompareVersions(current, rel.TagName)
	if !comparable {
		writeErr(w, 400, fmt.Sprintf("當前版本 %q 不是正式釋出版本，已停用一鍵更新", current))
		return
	}
	if cmp >= 0 {
		writeErr(w, 400, fmt.Sprintf("當前已是最新版本 %s", current))
		return
	}
	if !updHub.begin(rel.TagName) {
		writeErr(w, 409, "已有一個更新正在進行中")
		return
	}

	go func() {
		// 刻意用 s.ctx 而不是請求的 ctx：HTTP 回應一返回請求就結束了，
		// 掛在它上面下載會立刻被取消。
		err := selfupdate.Stage(s.ctx, client, rel, current, func(ph selfupdate.Phase, pct int, msg string) {
			updHub.publish(ph, pct, msg)
		})
		updHub.finish(err)
		if err != nil {
			log.Printf("[update] 更新失敗：%v", err)
			return
		}
		log.Printf("[update] %s → %s 已暫存，即將退出以完成換裝", current, rel.TagName)
		// 留一點時間把最後一條進度推給前端，再觸發退出。
		time.Sleep(1500 * time.Millisecond)
		requestRestart()
	}()

	writeJSON(w, 202, map[string]any{"ok": true, "target": rel.TagName})
}

// updateRollback 主動退回上一版本（換裝前備份的 artex.old）。
func (s *Server) updateRollback(w http.ResponseWriter, r *http.Request) {
	if _, running := updHub.snapshot(); running {
		writeErr(w, 409, "更新正在進行中，無法回滾")
		return
	}
	if err := selfupdate.Rollback(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("[update] 已手動回滾到上一版本，即將退出以完成切換")
	writeJSON(w, 202, map[string]any{"ok": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		requestRestart()
	}()
}

// updateStream 以 SSE 推送更新進度。
func (s *Server) updateStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := updHub.subscribe()
	defer unsub()

	send := func(p updateProgress) {
		b, _ := json.Marshal(p)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	// 先補一條當前狀態，頁面重新整理後能立刻看到進行中的升級。
	cur, _ := updHub.snapshot()
	send(cur)

	ctx := r.Context()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-ch:
			if !ok {
				return
			}
			send(p)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
