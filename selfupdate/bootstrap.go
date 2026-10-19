package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// smokeEnv 讓被冒煙測試拉起的子程序直接跳過 Bootstrap。
//
// 嚴格來說不加也不會出事：子程序的 os.Executable() 是 artex.new，推匯出來的
// 全部路徑都帶 .new 字首，碰不到真正的升級檔案。但依賴這種巧合太脆弱，
// 顯式短路一目瞭然，也省掉子程序一次無謂的磁碟探測。
const smokeEnv = "ARTEX_SELFUPDATE_SMOKE"

// Action 是 Bootstrap 給 main 的指令。
type Action int

const (
	// Continue：照常啟動 server。
	Continue Action = iota
	// Restart：立刻以 ExitRestart 退出，讓守護指令碼重新拉起。
	Restart
)

// State 描述本次啟動時的升級狀態，供 /api/update/check 如實告訴前端
// "上一次升級是成功了還是被回滾了"。
type State struct {
	Pending     bool   // 換裝後尚未確認穩定
	RolledBack  bool   // 本次啟動剛剛執行過自動回滾
	FailedStage bool   // 暫存件校驗/冒煙未通過，已丟棄
	Detail      string // 面向使用者的一句話說明
}

// Bootstrap 在 main 的最開頭執行，必須在任何監聽埠、開啟資料庫之前呼叫。
//
// 三種局面：
//
//	① 存在暫存件 artex.new  → 校驗 + 冒煙，通過則換裝並要求重啟；不通過則丟棄繼續跑舊版
//	② 只剩標記檔案          → 說明剛換裝完，累計一次嘗試；連續失敗夠多次則回滾
//	③ 什麼都沒有            → 正常啟動
func Bootstrap() (Action, State) {
	if os.Getenv(smokeEnv) != "" {
		return Continue, State{}
	}
	p, err := ResolvePaths()
	if err != nil {
		log.Printf("[update] 跳過自舉：%v", err)
		return Continue, State{}
	}

	if _, err := os.Stat(p.New); err == nil {
		return applyStaged(p)
	}

	m, ok := readMarker(p.Marker)
	if !ok {
		return Continue, State{}
	}
	return confirmOrRollback(p, m)
}

// applyStaged 處理"存在暫存件"的局面：校驗通過就換裝，失敗就丟棄。
//
// 這裡是整個升級鏈路唯一會覆蓋執行檔的地方，也是最後一道閘門——冒煙測試擋掉
// 下載損壞、架構選錯、動態連結缺失這類問題。一旦放行一個跑不起來的二進位制，
// 守護指令碼會不知疲倦地反覆拉起它，而 Go 程式碼根本沒機會執行，自動回滾也就無從談起。
func applyStaged(p Paths) (Action, State) {
	m, _ := readMarker(p.Marker)

	if err := verifyStaged(p); err != nil {
		log.Printf("[update] 暫存的新版本未通過校驗，已丟棄，繼續運行當前版本：%v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "新版本校驗失敗，已丟棄：" + err.Error()}
	}

	if err := swap(p); err != nil {
		log.Printf("[update] 換裝失敗，繼續運行當前版本：%v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "換裝失敗：" + err.Error()}
	}

	// 換裝成功。保留標記，交給下一次啟動（跑的就是新版）確認是否穩定。
	m.Attempts = 0
	if m.StagedAt == 0 {
		m.StagedAt = time.Now().Unix()
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] 寫升級標記失敗（失去自動回滾能力）：%v", err)
	}
	log.Printf("[update] 已換裝到 %s，退出以重啟（exit %d）", orUnknown(m.To), ExitRestart)
	return Restart, State{Pending: true}
}

// confirmOrRollback 處理"換裝後的啟動"：累計嘗試次數，超限則把舊版換回來。
//
// 計數只在 Go 程式碼跑起來後才遞增，所以它覆蓋的是"能執行但初始化時崩潰"
// （配置不相容、埠被佔、DB 遷移炸了）這類故障；"根本無法 exec" 由換裝前的
// 冒煙測試擋住，兩者合起來才是完整的。
func confirmOrRollback(p Paths, m marker) (Action, State) {
	m.Attempts++
	if m.Attempts > maxAttempts {
		if err := rollback(p); err != nil {
			// 回滾都失敗了就別再重啟了，否則會陷入無限重啟。清掉標記，
			// 讓程序按當前狀態起——起不來的話使用者至少能在日誌裡看到原因。
			log.Printf("[update] 新版本連續 %d 次啟動失敗，且回滾失敗：%v", maxAttempts, err)
			_ = os.Remove(p.Marker)
			return Continue, State{Detail: "新版本啟動失敗且回滾失敗：" + err.Error()}
		}
		log.Printf("[update] 新版本連續 %d 次啟動失敗，已回滾到 %s，退出以重啟（exit %d）",
			maxAttempts, orUnknown(m.From), ExitRestart)
		_ = os.Remove(p.Marker)
		return Restart, State{RolledBack: true, Detail: fmt.Sprintf("新版本啟動失敗，已回滾到 %s", orUnknown(m.From))}
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] 更新升級標記失敗：%v", err)
	}
	log.Printf("[update] 新版本啟動中（第 %d/%d 次嘗試），穩定執行後將確認升級",
		m.Attempts, maxAttempts)
	return Continue, State{Pending: true}
}

// Settle 確認新版本已穩定執行，清除升級標記。
//
// 由 main 在 HTTP 監聽起來之後延遲呼叫：活過這段時間才算數，否則標記留在原地，
// 下次啟動繼續累計嘗試次數，直到觸發回滾。
func Settle() {
	p, err := ResolvePaths()
	if err != nil {
		return
	}
	settle(p)
}

func settle(p Paths) {
	if _, ok := readMarker(p.Marker); !ok {
		return // 不是升級後的啟動，無事可做
	}
	if err := os.Remove(p.Marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("[update] 清除升級標記失敗：%v", err)
		return
	}
	log.Printf("[update] 新版本執行穩定，升級完成（上一版本保留為 %s）", p.Old)
}

// SettleDelay 是判定"新版本活下來了"所需的執行時長。
const SettleDelay = 30 * time.Second

// verifyStaged 校驗暫存件：先比對 SHA256，再真正把它拉起來跑一次。
func verifyStaged(p Paths) error {
	want, err := os.ReadFile(p.Sum)
	if err != nil {
		return fmt.Errorf("讀取校驗和: %w", err)
	}
	got, err := fileSHA256(p.New)
	if err != nil {
		return fmt.Errorf("計算校驗和: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(string(want)), got) {
		return errors.New("SHA256 不匹配（下載損壞或被篡改）")
	}
	return smokeTest(p.New)
}

// smokeTest 用 -h 拉起新二進位制，確認它在當前系統上真的能執行。
// 這能擋掉下載截斷、架構選錯（exec format error）、缺依賴等一大類問題。
func smokeTest(bin string) error {
	if err := os.Chmod(bin, 0o755); err != nil {
		return fmt.Errorf("賦予執行權限: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "-h")
	cmd.Env = append(os.Environ(), smokeEnv+"=1")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("冒煙測試超時（新二進位制無回應）")
	}
	if err != nil {
		snippet := strings.TrimSpace(string(out))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		return fmt.Errorf("冒煙測試失敗: %v: %s", err, snippet)
	}
	return nil
}

// swap 把當前二進位制換成暫存的新版本。
//
// Unix 和 Windows 都允許 rename 一個正在執行的執行檔（Windows 禁止的是刪除和
// 覆蓋，rename 不在其列），所以這裡不需要分平臺，也不需要先停掉自己。
func swap(p Paths) error {
	// Windows 的 rename 不會覆蓋已存在的目標，上一輪升級留下的 .old 必須先清掉。
	if err := os.Remove(p.Old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("清理舊備份 %s: %w", p.Old, err)
	}
	if err := os.Rename(p.Current, p.Old); err != nil {
		return fmt.Errorf("備份當前版本: %w", err)
	}
	if err := os.Rename(p.New, p.Current); err != nil {
		// 換裝失敗但當前版本已經被挪走了，必須原樣放回去，否則下次啟動沒有執行檔。
		if rerr := os.Rename(p.Old, p.Current); rerr != nil {
			return fmt.Errorf("裝入新版本失敗(%v)，且恢復當前版本失敗: %w", err, rerr)
		}
		return fmt.Errorf("裝入新版本: %w", err)
	}
	_ = os.Remove(p.Sum)
	return nil
}

// rollback 把 swap 備份的舊版本換回來。
func rollback(p Paths) error {
	if _, err := os.Stat(p.Old); err != nil {
		return fmt.Errorf("沒有可回滾的備份 %s: %w", p.Old, err)
	}
	// 把起不來的新版挪到 .failed 留作排查，而不是直接刪掉。
	failed := p.Current + ".failed"
	_ = os.Remove(failed)
	if err := os.Rename(p.Current, failed); err != nil {
		return fmt.Errorf("移走失敗的版本: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		return fmt.Errorf("恢復舊版本: %w", err)
	}
	return nil
}

// Rollback 是 /api/update/rollback 的實現：主動退回上一版本。
// 只做換裝，重啟同樣交給守護指令碼（呼叫方隨後以 ExitRestart 退出）。
func Rollback() error {
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	if _, err := os.Stat(p.Old); err != nil {
		return errors.New("沒有可回滾的上一版本（" + p.Old + " 不存在）")
	}
	cleanStaged(p)
	if err := smokeTest(p.Old); err != nil {
		return fmt.Errorf("上一版本無法執行，拒絕回滾: %w", err)
	}
	// 交換當前與備份：回滾之後還能再滾回來。
	tmp := p.Current + ".swap"
	_ = os.Remove(tmp)
	if err := os.Rename(p.Current, tmp); err != nil {
		return fmt.Errorf("移走當前版本: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		_ = os.Rename(tmp, p.Current)
		return fmt.Errorf("裝入上一版本: %w", err)
	}
	if err := os.Rename(tmp, p.Old); err != nil {
		log.Printf("[update] 回滾後整理備份失敗（不影響執行）：%v", err)
	}
	_ = os.Remove(p.Marker)
	return nil
}

// HasBackup 報告是否存在可回滾的上一版本，供前端決定要不要顯示回滾按鈕。
func HasBackup() bool {
	p, err := ResolvePaths()
	if err != nil {
		return false
	}
	_, err = os.Stat(p.Old)
	return err == nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "未知版本"
	}
	return s
}
