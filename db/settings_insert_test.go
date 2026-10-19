package db

import (
	"fmt"
	"testing"
	"time"
)

// InsertSettingIfAbsent 是 auth.password_hash 的兜底：呼叫方的 GetSetting 檢查可能
// 因為資料庫報錯而失效，也可能被併發請求插隊（bcrypt 要跑幾十毫秒），所以"僅首次
// 可設"的保證必須落在主鍵約束上，而不是上層的 if 判斷。
func TestInsertSettingIfAbsentDoesNotOverwrite(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	// 用測試專屬 key，絕不碰開發庫裡真實的 auth.password_hash。
	key := fmt.Sprintf("test.insert_if_absent.%d", time.Now().UnixNano())
	defer func() { _, _ = d.Exec(`DELETE FROM settings WHERE key=$1`, key) }()

	inserted, err := d.InsertSettingIfAbsent(key, "first")
	if err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Fatal("首次寫入應返回 inserted=true")
	}

	inserted, err = d.InsertSettingIfAbsent(key, "second")
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Fatal("鍵已存在時應返回 inserted=false")
	}

	got, ok, err := d.GetSetting(key)
	if err != nil || !ok {
		t.Fatalf("GetSetting: ok=%v err=%v", ok, err)
	}
	if got != "first" {
		t.Fatalf("值被覆蓋成 %q，應保持 %q", got, "first")
	}
}
