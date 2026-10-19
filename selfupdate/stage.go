package selfupdate

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"runtime"
	"strings"
	"time"
)

// sumsAsset 是 release.yml 生成的校驗和清單，覆蓋 Release 裡全部 zip。
const sumsAsset = "SHA256SUMS"

// maxBinarySize 限制解壓出來的二進位制體積，防止畸形 zip 把磁碟寫滿。
const maxBinarySize = 512 << 20 // 512 MiB

// Phase 是升級過程中的階段，直接用作 SSE 事件裡的 phase 欄位。
type Phase string

const (
	PhaseIdle     Phase = "idle"
	PhaseDownload Phase = "downloading"
	PhaseVerify   Phase = "verifying"
	PhaseExtract  Phase = "extracting"
	PhaseStaged   Phase = "staged"
	PhaseFailed   Phase = "failed"
)

// Progress 由呼叫方提供，用來把進度推給前端。pct 僅在下載階段有意義（0-100），
// 其餘階段傳 -1。
type Progress func(ph Phase, pct int, msg string)

// Stage 下載指定 Release 的當前平臺釋出包，校驗後把新二進位制暫存為 artex.new。
//
// 走的是完整 zip 而不是裸二進位制，理由有兩個：現有 Release 的 SHA256SUMS 本來就
// 只覆蓋 zip，走 zip 不需要改 CI，也能相容已經發布出去的歷史版本；zip 裡還帶著
// skills/，為將來同步內建 skill 留了口子。代價只是多下載 skills 那幾百 KB。
//
// 函式返回即代表暫存完成，呼叫方隨後優雅關閉並以 ExitRestart 退出。
func Stage(ctx context.Context, c *http.Client, rel *Release, currentVersion string, prog Progress) error {
	if prog == nil {
		prog = func(Phase, int, string) {}
	}
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	if err := checkWritable(p.Dir); err != nil {
		return err
	}

	name := AssetName(rel.TagName, runtime.GOOS, runtime.GOARCH)
	asset, ok := rel.FindAsset(name)
	if !ok {
		return fmt.Errorf("該版本沒有提供 %s/%s 的釋出包（缺少 %s）", runtime.GOOS, runtime.GOARCH, name)
	}

	prog(PhaseDownload, 0, "獲取校驗和清單…")
	sums, err := fetchSums(ctx, c, rel)
	if err != nil {
		return err
	}
	want, ok := sums[name]
	if !ok {
		return fmt.Errorf("%s 未收錄 %s，拒絕安裝未經校驗的二進位制", sumsAsset, name)
	}

	// 臨時檔案全部落在目標目錄裡，保證最後的 rename 是同一檔案系統內的原子操作
	// （跨裝置 rename 會失敗，而 /tmp 常常是獨立掛載點）。
	zipPath := p.New + ".zip.part"
	binPath := p.New + ".part"
	defer func() {
		_ = os.Remove(zipPath)
		_ = os.Remove(binPath)
	}()

	prog(PhaseDownload, 0, fmt.Sprintf("下載 %s（%s）…", name, humanSize(asset.Size)))
	got, err := download(ctx, c, asset, zipPath, prog)
	if err != nil {
		return err
	}

	prog(PhaseVerify, -1, "校驗 SHA256…")
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("SHA256 不匹配：期望 %s，實際 %s（下載損壞或被篡改）", short(want), short(got))
	}

	prog(PhaseExtract, -1, "解壓並冒煙測試…")
	if err := extractBinary(zipPath, binPath); err != nil {
		return err
	}
	if err := smokeTest(binPath); err != nil {
		return fmt.Errorf("新版本無法在當前系統上執行: %w", err)
	}

	// 暫存件自己的 sha256 單獨存一份：下次啟動換裝前還要再校驗一次，
	// 防止暫存後到重啟前這段時間裡檔案被改動或寫壞。
	binSum, err := fileSHA256(binPath)
	if err != nil {
		return fmt.Errorf("計算新二進位制校驗和: %w", err)
	}
	if err := os.WriteFile(p.Sum, []byte(binSum), 0o644); err != nil {
		return fmt.Errorf("寫入校驗和: %w", err)
	}
	if err := os.Rename(binPath, p.New); err != nil {
		_ = os.Remove(p.Sum)
		return fmt.Errorf("暫存新版本: %w", err)
	}

	if err := writeMarker(p.Marker, marker{
		From:     currentVersion,
		To:       strings.TrimPrefix(rel.TagName, "v"),
		StagedAt: time.Now().Unix(),
	}); err != nil {
		// 標記隻影響自動回滾能力，暫存件本身已就位，不因此中斷升級。
		prog(PhaseStaged, -1, "警告：寫入升級標記失敗，本次升級將沒有自動回滾保護")
	}

	prog(PhaseStaged, 100, "新版本已就緒，正在重啟…")
	return nil
}

// fetchSums 下載並解析 SHA256SUMS，返回 檔名 → 十六進位制摘要。
func fetchSums(ctx context.Context, c *http.Client, rel *Release) (map[string]string, error) {
	asset, ok := rel.FindAsset(sumsAsset)
	if !ok {
		return nil, fmt.Errorf("該 Release 沒有 %s，無法校驗完整性，拒絕升級", sumsAsset)
	}
	body, err := get(ctx, c, asset.URL)
	if err != nil {
		return nil, fmt.Errorf("下載 %s: %w", sumsAsset, err)
	}
	defer body.Close()

	raw, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("讀取 %s: %w", sumsAsset, err)
	}
	out := parseSums(string(raw))
	if len(out) == 0 {
		return nil, fmt.Errorf("%s 內容為空或格式無法識別", sumsAsset)
	}
	return out, nil
}

// parseSums 解析 sha256sum 風格的清單，返回 檔名 → 十六進位制摘要。
//
// 第一個欄位必須是 64 位十六進位制才收錄。只按"恰好兩個欄位"判斷是不夠的——
// 任意一行兩個單詞的說明文字都會被當成合法條目，把垃圾值塞進摘要表，
// 真正的資產反而可能匹配到錯誤的摘要。
func parseSums(raw string) map[string]string {
	out := map[string]string{}
	for line := range strings.Lines(raw) {
		// 格式為 "<sha256>  <filename>"（sha256sum 用雙空格；shasum 的二進位制
		// 模式會給檔名加 * 字首）。
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 || !isHexSHA256(fields[0]) {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == "" {
			continue
		}
		out[name] = strings.ToLower(fields[0])
	}
	return out
}

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// download 把資產寫入 dst，同時計算 SHA256 並按 Content-Length 彙報進度。
func download(ctx context.Context, c *http.Client, a Asset, dst string, prog Progress) (string, error) {
	body, err := get(ctx, c, a.URL)
	if err != nil {
		return "", fmt.Errorf("下載 %s: %w", a.Name, err)
	}
	defer body.Close()

	f, err := os.Create(dst)
	if err != nil {
		return "", fmt.Errorf("建立臨時檔案: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	pw := &progressWriter{total: a.Size, prog: prog, name: a.Name, last: time.Now()}
	if _, err := io.Copy(io.MultiWriter(f, h, pw), body); err != nil {
		return "", fmt.Errorf("下載中斷: %w", err)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("落盤失敗: %w", err)
	}
	if a.Size > 0 && pw.written != a.Size {
		return "", fmt.Errorf("下載不完整：期望 %d 位元組，實際 %d 位元組", a.Size, pw.written)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// get 發起一個受白名單約束的 GET，返回回應體。
func get(ctx context.Context, c *http.Client, rawURL string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if err := checkURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "artex-selfupdate")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// extractBinary 從釋出包裡取出 artex 執行檔。
//
// 包內結構是 artex-<版本>-<os>-<arch>/artex，但這裡按**基名**匹配而不是拼完整
// 路徑：版本號在包名裡出現過一次，拼錯一個字元就整個升級失敗，按基名找更耐改。
func extractBinary(zipPath, dst string) error {
	want := "artex"
	if runtime.GOOS == "windows" {
		want = "artex.exe"
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("打開發布包: %w", err)
	}
	defer zr.Close()

	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() || !strings.EqualFold(path.Base(entry.Name), want) {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return fmt.Errorf("讀取 %s: %w", entry.Name, err)
		}
		defer rc.Close()

		f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return fmt.Errorf("寫出新二進位制: %w", err)
		}
		defer f.Close()

		n, err := io.Copy(f, io.LimitReader(rc, maxBinarySize+1))
		if err != nil {
			return fmt.Errorf("解壓 %s: %w", entry.Name, err)
		}
		if n > maxBinarySize {
			return fmt.Errorf("釋出包內的執行檔超過 %s，拒絕解壓", humanSize(maxBinarySize))
		}
		if n == 0 {
			return fmt.Errorf("釋出包內的 %s 是空檔案", want)
		}
		return f.Sync()
	}
	return fmt.Errorf("釋出包裡沒有找到 %s", want)
}

// checkWritable 提前確認目錄可寫。沒有這一步，非 root 執行、或二進位制被放在系統
// 目錄時，會在下載完幾十 MB 之後才在換裝那一刻失敗。
func checkWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".artex-update-probe-*")
	if err != nil {
		return fmt.Errorf("程式目錄 %s 不可寫，無法自動更新（請檢查權限或改用手動升級）: %w", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

// progressWriter 統計已寫位元組並限頻彙報，避免每個 32KiB 分塊都推一條 SSE。
type progressWriter struct {
	total   int64
	written int64
	name    string
	prog    Progress
	last    time.Time
}

func (w *progressWriter) Write(b []byte) (int, error) {
	w.written += int64(len(b))
	if time.Since(w.last) < 300*time.Millisecond {
		return len(b), nil
	}
	w.last = time.Now()
	pct := -1
	if w.total > 0 {
		pct = int(w.written * 100 / w.total)
	}
	w.prog(PhaseDownload, pct, fmt.Sprintf("下載中 %s / %s", humanSize(w.written), humanSize(w.total)))
	return len(b), nil
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12] + "…"
	}
	return sum
}
