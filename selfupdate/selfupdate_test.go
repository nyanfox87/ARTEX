package selfupdate

import (
	"archive/zip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testPaths 造一個隔離的升級目錄。不能直接用 ResolvePaths()——那會指向測試
// 二進位制本身，一跑就把 go test 的執行檔改名了。
func testPaths(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	return Paths{
		Dir:     dir,
		Current: filepath.Join(dir, "artex"),
		New:     filepath.Join(dir, "artex.new"),
		Sum:     filepath.Join(dir, "artex.new.sha256"),
		Old:     filepath.Join(dir, "artex.old"),
		Marker:  filepath.Join(dir, "artex.upgrade.json"),
	}
}

// fakeBin 寫一個可執行的殼指令碼冒充 artex。smokeTest 只是用 -h 拉起它看退出碼，
// 指令碼完全夠用，而且比編譯一個真二進位制快得多。
func fakeBin(t *testing.T, path, marker string, exitCode int) {
	t.Helper()
	script := "#!/bin/sh\necho " + marker + "\nexit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("寫入假二進位制 %s: %v", path, err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return string(rune('0' + n))
}

// stage 把 bin 佈置成"已暫存待換裝"的樣子：寫好 artex.new 和它的校驗和。
func stage(t *testing.T, p Paths, marker string, exitCode int) {
	t.Helper()
	fakeBin(t, p.New, marker, exitCode)
	sum, err := fileSHA256(p.New)
	if err != nil {
		t.Fatalf("計算校驗和: %v", err)
	}
	if err := os.WriteFile(p.Sum, []byte(sum), 0o644); err != nil {
		t.Fatalf("寫入校驗和: %v", err)
	}
}

func readAll(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("讀取 %s: %v", path, err)
	}
	return string(b)
}

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("假二進位制用的是 sh 指令碼，Windows 上跑不了")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b       string
		want       int
		comparable bool
	}{
		{"0.3.7", "0.3.8", -1, true},
		{"0.3.8", "0.3.7", 1, true},
		{"0.3.7", "0.3.7", 0, true},
		{"v0.3.7", "0.3.8", -1, true}, // build.sh 去掉 v，tag 帶 v，兩邊都要認
		{"0.3.7", "v0.3.7", 0, true},
		{"0.9.0", "0.10.0", -1, true}, // 按數字比而不是字典序
		{"1.0.0", "0.99.99", 1, true},
		// 開發構建必須判為不可比較，否則會被正式版覆蓋掉未提交的改動。
		{"dev", "0.3.8", 0, false},
		{"0.3.7-2-gabc1234", "0.3.8", 0, false},
		{"0.3.7-dirty", "0.3.8", 0, false},
		{"0.3", "0.3.8", 0, false},
		{"", "0.3.8", 0, false},
	}
	for _, c := range cases {
		got, ok := CompareVersions(c.a, c.b)
		if ok != c.comparable {
			t.Errorf("CompareVersions(%q,%q) comparable=%v, 期望 %v", c.a, c.b, ok, c.comparable)
			continue
		}
		if ok && got != c.want {
			t.Errorf("CompareVersions(%q,%q)=%d, 期望 %d", c.a, c.b, got, c.want)
		}
	}
}

func TestResolvePathsNaming(t *testing.T) {
	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	// 關鍵不變數：所有升級檔案都和執行檔同目錄。落到 CWD 會讓服務化執行
	// （工作目錄可能是 /）時的換裝徹底失效。
	for name, path := range map[string]string{"New": p.New, "Sum": p.Sum, "Old": p.Old, "Marker": p.Marker} {
		if filepath.Dir(path) != p.Dir {
			t.Errorf("%s 不在執行檔目錄下: %s (期望 %s)", name, path, p.Dir)
		}
	}
	// Windows 上 .new/.old 必須保留 .exe，否則冒煙測試和換裝後的執行都會失敗。
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(p.New, ".exe") || !strings.HasSuffix(p.Old, ".exe") {
			t.Errorf("Windows 上 .new/.old 必須以 .exe 結尾: new=%s old=%s", p.New, p.Old)
		}
	}
}

func TestVerifyStagedRejectsTamperedBinary(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	stage(t, p, "new", 0)

	// 校驗和寫好之後再改動檔案，模擬下載損壞 / 被掉包。
	fakeBin(t, p.New, "tampered", 0)
	if err := verifyStaged(p); err == nil {
		t.Fatal("期望 SHA256 不匹配被拒絕，卻通過了")
	}
}

func TestVerifyStagedRejectsUnrunnableBinary(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	stage(t, p, "broken", 1) // 能執行但退出碼非 0

	if err := verifyStaged(p); err == nil {
		t.Fatal("期望冒煙測試失敗被拒絕，卻通過了")
	}
}

func TestApplyStagedHappyPath(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "old", 0)
	stage(t, p, "new", 0)
	if err := writeMarker(p.Marker, marker{From: "0.3.7", To: "0.3.8"}); err != nil {
		t.Fatalf("寫標記: %v", err)
	}

	action, st := applyStaged(p)
	if action != Restart {
		t.Fatalf("期望 Restart，得到 %v", action)
	}
	if !st.Pending {
		t.Error("換裝後狀態應為 Pending")
	}
	if !strings.Contains(readAll(t, p.Current), "new") {
		t.Error("artex 應已被替換為新版本")
	}
	if !strings.Contains(readAll(t, p.Old), "old") {
		t.Error("舊版本應備份到 artex.old")
	}
	if _, err := os.Stat(p.New); !os.IsNotExist(err) {
		t.Error("換裝後 artex.new 應已消失")
	}
	if _, err := os.Stat(p.Sum); !os.IsNotExist(err) {
		t.Error("換裝後校驗和檔案應已清理")
	}
	// 標記必須留著，下一次啟動（跑的是新版）靠它計數、必要時回滾。
	if _, ok := readMarker(p.Marker); !ok {
		t.Error("換裝後升級標記應保留")
	}
}

func TestApplyStagedKeepsCurrentWhenVerifyFails(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "old", 0)
	stage(t, p, "new", 0)
	fakeBin(t, p.New, "tampered", 0) // 破壞校驗和

	action, st := applyStaged(p)
	if action != Continue {
		t.Fatalf("校驗失敗時期望 Continue，得到 %v", action)
	}
	if !st.FailedStage {
		t.Error("狀態應標記為 FailedStage")
	}
	if !strings.Contains(readAll(t, p.Current), "old") {
		t.Fatal("校驗失敗時絕不能動當前版本")
	}
	if _, err := os.Stat(p.New); !os.IsNotExist(err) {
		t.Error("校驗失敗的暫存件應被清理，否則下次啟動會再試一遍")
	}
}

func TestSwapOverwritesPreviousBackup(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "v2", 0)
	fakeBin(t, p.Old, "v1", 0) // 上一輪升級留下的備份
	stage(t, p, "v3", 0)

	if err := swap(p); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if !strings.Contains(readAll(t, p.Current), "v3") {
		t.Error("應換裝到 v3")
	}
	if !strings.Contains(readAll(t, p.Old), "v2") {
		t.Error("備份應更新為剛被換下的 v2")
	}
}

func TestConfirmCountsAttemptsThenRollsBack(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "broken-new", 0)
	fakeBin(t, p.Old, "good-old", 0)
	m := marker{From: "0.3.7", To: "0.3.8"}

	// 前 maxAttempts 次啟動只累計計數，讓新版有機會自己站穩。
	for i := 1; i <= maxAttempts; i++ {
		action, st := confirmOrRollback(p, m)
		if action != Continue {
			t.Fatalf("第 %d 次嘗試期望 Continue，得到 %v", i, action)
		}
		if !st.Pending {
			t.Errorf("第 %d 次嘗試狀態應為 Pending", i)
		}
		got, ok := readMarker(p.Marker)
		if !ok || got.Attempts != i {
			t.Fatalf("第 %d 次嘗試後 attempts=%d（ok=%v），期望 %d", i, got.Attempts, ok, i)
		}
		m = got
	}

	// 再崩一次就超限，自動把舊版換回來。
	action, st := confirmOrRollback(p, m)
	if action != Restart {
		t.Fatalf("超過嘗試上限時期望 Restart，得到 %v", action)
	}
	if !st.RolledBack {
		t.Error("狀態應標記為 RolledBack")
	}
	if !strings.Contains(readAll(t, p.Current), "good-old") {
		t.Fatal("應已回滾到舊版本")
	}
	if _, err := os.Stat(p.Marker); !os.IsNotExist(err) {
		t.Error("回滾後標記應清除，否則會無限回滾")
	}
	// 起不來的那個版本留作排查，不直接刪。
	if _, err := os.Stat(p.Current + ".failed"); err != nil {
		t.Error("失敗的版本應保留為 .failed 供排查")
	}
}

func TestManualRollbackIsReversible(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "v2", 0)
	fakeBin(t, p.Old, "v1", 0)

	// Rollback() 走 ResolvePaths()，這裡直接測底層的交換語義。
	tmp := p.Current + ".swap"
	if err := os.Rename(p.Current, tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p.Old); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readAll(t, p.Current), "v1") {
		t.Error("回滾後當前版本應是 v1")
	}
	if !strings.Contains(readAll(t, p.Old), "v2") {
		t.Error("回滾後備份應變成 v2，這樣還能再滾回去")
	}
}

func TestParseSums(t *testing.T) {
	const (
		linuxSum = "1111111111111111111111111111111111111111111111111111111111111111"
		winSum   = "ABCDEF0000000000000000000000000000000000000000000000000000000000"
	)
	// sha256sum 輸出是雙空格分隔；shasum -a 256 在二進位制模式下會給檔名加 *。
	raw := linuxSum + "  artex-0.3.8-linux-amd64.zip\n" +
		winSum + " *artex-0.3.8-windows-amd64.zip\n" +
		"\n" +
		"garbage line\n" + // 恰好兩個欄位，但第一個不是摘要
		"deadbeef  artex-0.3.8-darwin-arm64.zip\n" // 摘要長度不對

	out := parseSums(raw)
	if out["artex-0.3.8-linux-amd64.zip"] != linuxSum {
		t.Errorf("linux 條目解析錯誤: %v", out)
	}
	// 摘要統一小寫，比對時才不會因大小寫誤判為不匹配。
	if got := out["artex-0.3.8-windows-amd64.zip"]; got != strings.ToLower(winSum) {
		t.Errorf("windows 條目錯誤（* 字首應剝離、摘要應轉小寫）: %q", got)
	}
	if len(out) != 2 {
		t.Errorf("應忽略空行、非摘要行和長度不對的行，得到 %v", out)
	}
}

func TestExtractBinaryFindsNestedEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("包內基名在 Windows 上是 artex.exe，此用例按 Unix 命名構造")
	}
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "release.zip")

	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	// 真實發布包的結構：artex-<版本>-<os>-<arch>/artex，外加若干干擾檔案。
	for name, body := range map[string]string{
		"artex-0.3.8-linux-amd64/README.md":           "readme",
		"artex-0.3.8-linux-amd64/skills/a.md":         "skill",
		"artex-0.3.8-linux-amd64/artex":               "#!/bin/sh\nexit 0\n",
		"artex-0.3.8-linux-amd64/config.example.json": "{}",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	dst := filepath.Join(dir, "out")
	if err := extractBinary(zipPath, dst); err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if got := readAll(t, dst); !strings.Contains(got, "exit 0") {
		t.Errorf("解壓出來的不是 artex 執行檔: %q", got)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Error("解壓出的二進位制必須帶執行位")
	}
}

func TestExtractBinaryMissingEntry(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "release.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("artex-0.3.8-linux-amd64/README.md")
	_, _ = w.Write([]byte("readme"))
	_ = zw.Close()
	f.Close()

	if err := extractBinary(zipPath, filepath.Join(dir, "out")); err == nil {
		t.Fatal("包內沒有執行檔時應報錯")
	}
}

func TestCheckURLRejectsNonGitHub(t *testing.T) {
	bad := []string{
		"http://github.com/x",           // 非 HTTPS
		"https://evil.com/artex.zip",    // 域名不在白名單
		"https://github.com.evil.com/x", // 字尾偽裝
		"https://raw.githubusercontent.com.evil.com/x",
	}
	for _, raw := range bad {
		u := mustParse(t, raw)
		if err := checkURL(u); err == nil {
			t.Errorf("checkURL(%q) 應當拒絕", raw)
		}
	}
	good := []string{
		"https://api.github.com/repos/x/releases/latest",
		"https://objects.githubusercontent.com/blah",
		"https://GitHub.com/x", // 域名大小寫不敏感
	}
	for _, raw := range good {
		u := mustParse(t, raw)
		if err := checkURL(u); err != nil {
			t.Errorf("checkURL(%q) 應當放行，卻報錯: %v", raw, err)
		}
	}
}

func TestAssetNameMatchesBuildScript(t *testing.T) {
	// build.sh 的 package_binary 用的是 artex-<版本>-<os>-<arch>.zip，且版本號
	// 去掉了 v 字首。這裡對錯一個字元，所有平臺的一鍵更新都會找不到資產。
	if got := AssetName("v0.3.8", "linux", "amd64"); got != "artex-0.3.8-linux-amd64.zip" {
		t.Errorf("AssetName = %q", got)
	}
	if got := AssetName("0.3.8", "windows", "amd64"); got != "artex-0.3.8-windows-amd64.zip" {
		t.Errorf("AssetName = %q", got)
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("解析 %q: %v", raw, err)
	}
	return u
}

func TestSettleClearsMarkerAndStopsRollback(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "new", 0)
	fakeBin(t, p.Old, "old", 0)
	if err := writeMarker(p.Marker, marker{From: "0.3.7", To: "0.3.8", Attempts: 2}); err != nil {
		t.Fatal(err)
	}

	settle(p)

	if _, err := os.Stat(p.Marker); !os.IsNotExist(err) {
		t.Fatal("確認穩定後升級標記必須清除")
	}
	// 標記沒了，後續正常重啟就不會再累計次數、也不會誤觸發回滾。
	if _, ok := readMarker(p.Marker); ok {
		t.Error("標記讀取應失敗")
	}
	// 備份要留著，使用者還能手動回滾。
	if _, err := os.Stat(p.Old); err != nil {
		t.Error("確認穩定後仍應保留上一版本備份")
	}
}

func TestSettleIsNoopWithoutMarker(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "cur", 0)
	settle(p) // 普通啟動路徑，不該 panic 也不該動任何檔案
	if _, err := os.Stat(p.Current); err != nil {
		t.Error("無標記時 settle 不應影響任何檔案")
	}
}
