// Package selfupdate implements ARTEX 的頁面一鍵更新：從 GitHub Release 拉取新版
// 二進位制、校驗、暫存，並在下次啟動時原子換裝。
//
// 整體分工（見 start.sh / start.bat）：
//
//	啟動指令碼  = 傻瓜守護迴圈，只負責"程序退出後按退出碼決定是否再拉起"
//	本包      = 全部易錯邏輯（下載 / SHA256 校驗 / 冒煙 / 換裝 / 失敗回滾）
//
// 之所以把換裝放在 Go 而不是腳本里，是因為 sha256 校驗和冒煙測試在 sh 和 bat 上
// 要寫兩套（sha256sum / shasum / certutil），而這恰恰是最不能出錯的一環——換上一個
// 跑不起來的二進位制，守護程序會忠實地反覆拉起它，使用者只能上機器手工救。
//
// 一次完整升級經過三次程序啟動：
//
//	① 舊版 server 收到 /api/update/apply → 下載校驗 → 暫存 artex.new → exit 75
//	② 指令碼重新拉起舊版 → Bootstrap 發現 artex.new → 校驗+冒煙 → 換裝 → exit 75
//	③ 指令碼重新拉起，此時已是新版 → Bootstrap 記一次嘗試 → 啟動成功後清除標記
//
// 任何一步失敗都退回舊版：② 校驗不過就刪掉暫存件繼續跑舊版；③ 連續 3 次沒活到
// 清除標記（起不來就崩）則自動把 artex.old 換回去。
package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExitRestart 是"請守護程序重新拉起我"的退出碼（EX_TEMPFAIL）。啟動指令碼看到它
// 就立刻重跑，不計入崩潰退避。0 表示使用者正常停止（指令碼退出迴圈），其餘均視為崩潰。
const ExitRestart = 75

// maxAttempts 是換裝後允許的啟動嘗試次數。新版每次啟動都會把計數 +1，活過
// settleDelay 則清除標記；連崩 maxAttempts 次說明新版根本起不來，自動回滾。
const maxAttempts = 3

// Paths 是一次升級涉及的全部檔案，統一掛在**執行檔所在目錄**下。
// 刻意不用 CWD：服務化執行時工作目錄可能是 / 或任意路徑，用 CWD 會讓暫存件落到
// 別處，換裝邏輯直接失效。
type Paths struct {
	Dir     string // 執行檔所在目錄
	Current string // 當前執行的二進位制        artex      / artex.exe
	New     string // 暫存的新版本            artex.new  / artex.new.exe
	Sum     string // 新版本的 sha256（hex）  artex.new.sha256 / artex.new.exe.sha256
	Old     string // 換裝前備份的舊版本      artex.old  / artex.old.exe
	Marker  string // 升級狀態標記            artex.upgrade.json
}

// ResolvePaths 按當前執行檔推導全部升級路徑。
//
// Windows 上 .new/.old 也必須帶 .exe 字尾，否則冒煙測試和換裝後的執行都會失敗，
// 所以先把字尾摘掉再拼，兩個平臺的命名才對稱。
func ResolvePaths() (Paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return Paths{}, fmt.Errorf("定位執行檔: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	name := filepath.Base(exe)
	ext := filepath.Ext(name) // Windows 上是 ".exe"，Unix 上通常為空
	stem := strings.TrimSuffix(name, ext)

	join := func(suffix string) string { return filepath.Join(dir, stem+suffix+ext) }
	return Paths{
		Dir:     dir,
		Current: exe,
		New:     join(".new"),
		Sum:     join(".new") + ".sha256",
		Old:     join(".old"),
		Marker:  filepath.Join(dir, stem+".upgrade.json"),
	}, nil
}

// marker 記錄一次換裝的進度，用來在新版起不來時觸發自動回滾。
type marker struct {
	From     string `json:"from"`     // 升級前的版本
	To       string `json:"to"`       // 目標版本
	Attempts int    `json:"attempts"` // 換裝後已嘗試啟動的次數
	StagedAt int64  `json:"staged_at"`
}

func readMarker(path string) (marker, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil {
		return marker{}, false
	}
	return m, true
}

func writeMarker(path string, m marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// cleanStaged 清掉暫存件。換裝成功、校驗失敗、使用者取消都走它，避免殘留的
// artex.new 在下次啟動時被重新嘗試。
func cleanStaged(p Paths) {
	_ = os.Remove(p.New)
	_ = os.Remove(p.Sum)
}

// CompareVersions 比較兩個版本號，返回 -1/0/1（a<b / a==b / a>b）。
// ok=false 表示至少一邊不是可比較的版本號（例如本地開發構建的 "dev" 或
// git describe 產出的 "0.3.7-2-gabc1234-dirty"），此時呼叫方應停用一鍵更新，
// 否則會把開發中的構建"升級"成正式版、覆蓋掉未提交的改動。
func CompareVersions(a, b string) (int, bool) {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range 3 {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseVersion 解析 "v0.3.7" / "0.3.7" 形式的版本號為 [3]int。
//
// 只接受純淨的三段式：build.sh 在非 tag 構建時用 git describe 產出
// "0.3.7-2-gabc1234" 這類帶字尾的版本，它們必須被判為不可比較，而不是被當成
// 0.3.7 —— 否則開發構建會被誤判為"已是最新"或被正式版覆蓋。
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// InDocker 報告程序是否跑在容器裡。Docker 下換裝寫的是容器可寫層，
// `docker compose up -d` 重建容器會退回映像自帶的版本——這是預期行為
// （那時使用者本來就在拉新映像），但前端要能據此把話說清楚。
func InDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd")
}
