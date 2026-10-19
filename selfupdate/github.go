package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Repo 是釋出源。寫死而不是做成配置項：更新源可配等於給任何能改配置的人一條
// 遠端程式碼執行通道，對一個滲透測試平臺來說這個口子開不得。
const Repo = "RuoJi6/ARTEX"

// latestURL 是 GitHub 的"最新正式版"介面。它會自動跳過 prerelease 和 draft。
const latestURL = "https://api.github.com/repos/" + Repo + "/releases/latest"

// allowedHosts 限定升級鏈路能訪問的域名。配合下面的 checkRedirect，
// 任何一跳被重定向到名單外的主機都會直接失敗——這是防止 DNS 汙染 / 中間人
// 把二進位制換掉的第一道閘門，第二道是 SHA256SUMS 比對。
var allowedHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true, // release 資產實際落地的物件儲存
	"release-assets.githubusercontent.com": true,
	"raw.githubusercontent.com":            true,
}

// Release 是 GitHub Release 裡我們關心的欄位。
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []Asset   `json:"assets"`
}

// Asset 是 Release 上掛的一個檔案。
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// NewClient 構造一個只認 GitHub 域名的 HTTP 客戶端。proxy 為空則直連。
//
// 刻意不復用預設 Transport：升級鏈路必須強制走 TLS 且校驗證書，不能被別處
// 設定的 InsecureSkipVerify 之類影響到。
func NewClient(proxy string) *http.Client {
	tr := &http.Transport{
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	if p := strings.TrimSpace(proxy); p != "" {
		if pu, err := url.Parse(p); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   30 * time.Minute, // 下載整包，不能按請求級超時卡死
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("重定向次數過多")
			}
			return checkURL(req.URL)
		},
	}
}

// checkURL 強制 https + 域名白名單。
func checkURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("拒絕非 HTTPS 地址: %s", u.Scheme+"://"+u.Host)
	}
	if !allowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("拒絕非 GitHub 域名: %s", u.Hostname())
	}
	return nil
}

// FetchLatest 查詢最新正式版。
func FetchLatest(ctx context.Context, c *http.Client) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return nil, err
	}
	if err := checkURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "artex-selfupdate")

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("訪問 GitHub 失敗（可在系統設定裡配置全域代理）: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusTooManyRequests:
		// 未認證的 GitHub API 是每 IP 每小時 60 次，共用出口 IP 時很容易撞上。
		return nil, fmt.Errorf("GitHub 介面限流（每小時 60 次），請稍後再試")
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("儲存庫 %s 不可訪問或尚未釋出正式版本", Repo)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub 返回 %d", resp.StatusCode)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("解析 Release 失敗: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return nil, fmt.Errorf("Release 缺少 tag")
	}
	return &rel, nil
}

// AssetName 返回當前平臺對應的釋出包名，與 build.sh 的 package_binary 保持一致：
// artex-<版本>-<os>-<arch>.zip（版本號不帶 v 字首）。
func AssetName(tag, goos, goarch string) string {
	return fmt.Sprintf("artex-%s-%s-%s.zip", strings.TrimPrefix(tag, "v"), goos, goarch)
}

// FindAsset 在 Release 裡按名字找資產。
func (r *Release) FindAsset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return Asset{}, false
}
