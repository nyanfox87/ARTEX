package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// allowLocalTargets 決定是否允許把訊息投遞到環回 / 鏈路本地地址。
//
// 預設拒絕。這幾段地址不是 IM 機器人或公網郵件伺服器會出現的地方，而它們能
// 打到的東西很敏感：同機另一個服務的管理埠、以及雲環境的後設資料端點
// （169.254.169.254，可讀出例項憑據）。投遞地址是管理員配的，但一個被 XSS/CSRF
// 借用的管理會話、或共用同一 JWT 的第二個人，都能靠改配置把回應內容讀回來
// ——doJSON 會把 4xx/5xx 的回應體前 200 位元組寫進 last_error，而投遞歷史介面
// 會把它回顯出來，這就是一條半盲讀原語。
//
// 但「本機 SMTP 中繼」（127.0.0.1:25 上的 postfix）是自建郵件的常見配置，
// 一刀切會把人卡住。所以留一個顯式逃生口而不是硬編碼放行：
// 設定 ARTEX_NOTIFY_ALLOW_LOCAL=1 即允許。
//
// 匯出為 AllowLocalTargetsEnv 是為了讓測試能明確地開啟它——本包與 server 包的
// 用例大量使用 127.0.0.1 上的 httptest 假接收端，不開啟就全部被守衛攔下。
const AllowLocalTargetsEnv = "ARTEX_NOTIFY_ALLOW_LOCAL"

func allowLocalTargets() bool {
	v := strings.TrimSpace(os.Getenv(AllowLocalTargetsEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// isBlockedDialIP 報告目標 IP 是否屬於「預設不允許投遞」的地址段。
//
// 只拒絕環回、鏈路本地（含雲後設資料 169.254.169.254）、未指定與組播。
// **不拒絕** RFC1918 私網：內網自建 Mattermost / SMTP 中繼是很常見的合法用法，
// 把它們一併擋掉會讓功能在真實環境裡直接不可用。這條取捨是刻意的——
// 防護要擋住真正敏感的目標，同時不能把正常部署一起廢掉。
func isBlockedDialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4-mapped IPv6（::ffff:127.0.0.1）要還原成 IPv4 再判，否則繞過檢查。
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// blockInternalDial 是 http.Transport 撥號器的 Control 鉤子，在**連線建立時**
// 檢查目標地址。
//
// 為什麼設在撥號階段而不是隻在儲存配置時校驗：這裡才是最終生效點。
// 它同時覆蓋兩種繞過配置校驗的情形——DNS 重綁定（校驗時解析到公網 IP、
// 真正連線時解析到內網）與重定向（雖然我們已拒絕跨主機跳轉，但同主機跳轉
// 仍可能把路徑指到別處）。
func blockInternalDial(_, address string, _ syscall.RawConn) error {
	if allowLocalTargets() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("無法解析目標地址 %q", host)
	}
	if isBlockedDialIP(ip) {
		return fmt.Errorf("拒絕投遞到本機/鏈路本地地址 %s（如確需投遞到本機服務，設定 %s=1）", ip, AllowLocalTargetsEnv)
	}
	return nil
}

// notifyTransport 在預設 Transport 的基礎上只加一個撥號守衛。
// 用 Clone 保留預設的全部調優（連線池、HTTP/2、超時、proxy 等），
// 避免為了加一個檢查而改動其它行為。
var notifyTransport = func() *http.Transport {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{}
	}
	clone := t.Clone()
	clone.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: blockInternalDial}).DialContext
	return clone
}()

// httpClient 是所有渠道投遞共用的客戶端。
//
// 刻意**不**複用專案的全域出口代理（server 側的 GlobalProxy）：那個代理是給滲透
// 目標流量用的，常是不穩定的隧道，而通知的可用性不該被目標網路的抖動綁架。
// IM 推送直連即可。超時設為 15 秒——比這更慢的對端實際上已經是故障狀態。
//
// 拒絕跨主機重定向：本功能的投遞地址都是「一個固定 endpoint」形態，正常不會
// 重定向到別的主機；而這幾家的憑據（釘釘的 access_token、企微的 key、Telegram 的
// bot token）**就在 URL 裡**，跟隨跨主機跳轉等於把憑據交給重定向目標。同主機
// 的跳轉（如末尾補斜槓）仍允許。
var httpClient = &http.Client{
	Timeout:   15 * time.Second,
	Transport: notifyTransport,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("重定向次數過多")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("拒絕跨主機重定向（%s → %s）", via[0].URL.Host, req.URL.Host)
		}
		return nil
	},
}

// respBodyLimit 限制讀取回應體的大小。對端異常時可能吐回超大內容，而我們只需要
// 錯誤碼和一小段錯誤描述用於展示在投遞歷史裡。
const respBodyLimit = 8 << 10

// doJSON 傳送一次請求並返回回應體（已限長）。
//
// payload 為 nil 時傳送空 body（用於 GET 或平臺不要求 body 的場景）。
// headers 裡的鍵值原樣附加，用於通用 Webhook 的自定義頭。
//
// 錯誤分類是這個函式的核心職責：網路層失敗與 5xx/408/429 歸為「可重試」，
// 其餘 4xx 歸為「永久失敗」——重試一個 403 只是把同一個錯誤刷 3 遍日誌。
func doJSON(ctx context.Context, method, url string, headers map[string]string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			// 序列化失敗是本地 bug（配置欄位型別不對），重試也不會變好。
			return nil, Permanent(fmt.Errorf("構造請求體失敗: %w", err))
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		// URL 非法——多半是使用者把地址填錯了，屬於永久失敗。
		// 這裡同樣不能透傳 err：url.Parse 的錯誤文本里含完整地址。
		return nil, Permanent(fmt.Errorf("請求地址非法: %s", redactRequestTarget(url)))
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// 連線被拒、DNS 失敗、超時——多為瞬時故障，交給退避重試。
		//
		// 錯誤文本必須脫敏後再往外傳。原因：http.Client.Do 返回的是 *url.Error，
		// 它的 Error() 是 `Op "完整URL": 底層錯誤`，而本功能這幾家的憑據**就在 URL 裡**
		// （釘釘 access_token、企微 key、飛書 hook id、Telegram /bot<token>/）。
		// 不脫敏的話，憑據會順著這條錯誤串流到四個地方：notification_deliveries
		// 的 last_error（明文落庫）、投遞歷史介面的回應（**繞過渠道配置的掩碼**）、
		// 服務端日誌、以及測試傳送介面回給前端的 502 文本。
		return nil, fmt.Errorf("請求失敗: %s", redactTransportError(err))
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, respBodyLimit))
	if readErr != nil {
		return nil, fmt.Errorf("讀取回應失敗: %w", readErr)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, nil
	}
	// 429（限流）與 408（超時）值得重試；其餘 4xx 是配置或權限問題，重試無意義。
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
		return nil, fmt.Errorf("對方限流或超時 (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("對方服務異常 (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	return nil, Permanent(fmt.Errorf("對方拒絕請求 (HTTP %d): %s", resp.StatusCode, snippet(raw)))
}

// snippet 把回應體壓成一行短文本，用於錯誤資訊。回應裡可能帶換行與大量空白，
// 直接塞進 last_error 會讓投遞歷史頁面排版崩掉。
func snippet(raw []byte) string {
	return OneLine(string(raw), 200)
}

// redactRequestTarget 把投遞地址壓成「scheme://host/…」，用於錯誤資訊。
//
// 這是本包唯一的地址脫敏口徑，刻意做得**足夠粗暴**：除了 scheme 與 host，
// 其餘一律丟棄。原因是沒有一個「通用且安全」的方式判斷 URL 的哪一段是憑據：
//
//	釘釘   憑據在 query      /robot/send?access_token=xxx
//	企微   憑據在 query      /cgi-bin/webhook/send?key=xxx
//	飛書   憑據在**路徑末段** /open-apis/bot/v2/hook/<hook_id>
//	Telegram 憑據在**路徑中段** /bot<token>/sendMessage
//
// 想「只保留有用部分」就得按渠道打補丁，而漏掉任何一家就是一次憑據洩露。
// 保留 host 已經夠用於排查（DNS 解析不了、連不上、證書不對都能定位），
// 具體是哪個機器人由渠道配置裡的掩碼尾號提示去認。
//
// 解析失敗時返回固定佔位符——絕不把原始串回顯出去。
func redactRequestTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(地址不可解析)"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// redactTransportError 從傳輸層錯誤裡剝掉地址，只保留底層原因。
//
// *url.Error 的結構是 {Op, URL, Err}，Error() 會把 URL 一起打出來。
// 這裡顯式取 Err 欄位，繞開它的 Error() ——比事後做字串替換更可靠，
// 因為替換要正確應對 URL 編碼/轉義後的各種變體，容易漏。
func redactTransportError(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		host := ""
		if u, parseErr := url.Parse(uerr.URL); parseErr == nil {
			host = u.Host
		}
		if uerr.Err != nil {
			return fmt.Sprintf("%s %s: %s", uerr.Op, host, uerr.Err)
		}
		return fmt.Sprintf("%s %s: 未知錯誤", uerr.Op, host)
	}
	// 非 *url.Error（如重定向策略返回的錯誤）也可能帶地址，統一走脫敏。
	return redactURLsInText(err.Error())
}

// redactURLsInText 把一段文本里出現的 http(s) 地址替換成脫敏形態。
//
// 用於兜底那些拿不到結構化欄位的錯誤（重定向策略錯誤、第三方庫的自定義錯誤）。
// 只識別 http/https 字首，按空白與引號切分——地址不會包含這兩類字元。
func redactURLsInText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		rest := s[i:]
		if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
			end := len(rest)
			if j := strings.IndexAny(rest, " \t\n\"'"); j >= 0 {
				end = j
			}
			b.WriteString(redactRequestTarget(rest[:end]))
			i += end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
