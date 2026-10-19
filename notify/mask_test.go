package notify

import (
	"errors"
	"strings"
	"testing"
)

func TestMaskedValueHidesBodyButKeepsTailHint(t *testing.T) {
	const secret = "https://oapi.dingtalk.com/robot/send?access_token=abcdef123456"
	got := MaskedValue(secret)
	if strings.Contains(got, "abcdef123456") {
		t.Fatalf("掩碼值洩露了完整憑據: %q", got)
	}
	if strings.Contains(got, "oapi.dingtalk.com") {
		t.Fatalf("掩碼值不應暴露地址主體: %q", got)
	}
	// 末 6 位要保留，使用者才能認出是哪個機器人。
	if !strings.HasSuffix(got, "123456") {
		t.Fatalf("應保留末 6 位作為辨識提示: %q", got)
	}
	if !IsMasked(got) {
		t.Fatalf("掩碼值必須能被 IsMasked 識別: %q", got)
	}
}

func TestMaskedValueShortSecretGivesNoHint(t *testing.T) {
	// 短憑據如果也暴露末 6 位，等於把整個憑據暴露出去。
	for _, s := range []string{"abc", "abcdef", ""} {
		got := MaskedValue(s)
		if got != MaskedPrefix {
			t.Fatalf("長度 %d 的憑據不應給出尾部提示，得到 %q", len(s), got)
		}
		if s != "" && strings.Contains(got, s) {
			t.Fatalf("掩碼值包含了原值: %q", got)
		}
	}
}

func TestMaskConfigMasksOnlySecrets(t *testing.T) {
	cfg := map[string]any{
		"webhook": "https://example.com/hook?token=SECRETVALUE",
		"secret":  "SECtest123456",
		"port":    float64(587),
		"host":    "smtp.example.com",
	}
	masked := MaskConfig(KindDingTalk, cfg)
	for _, k := range []string{"webhook", "secret"} {
		s, _ := masked[k].(string)
		if !IsMasked(s) {
			t.Errorf("%s 應被掩碼，得到 %q", k, s)
		}
	}
	// 非憑據欄位必須原樣保留，否則 UI 無法展示。
	if masked["port"] != float64(587) {
		t.Errorf("非憑據欄位 port 不應改動: %v", masked["port"])
	}
}

func TestMaskConfigUnknownKindReturnsEmpty(t *testing.T) {
	// 渠道型別無法識別時，寧可讓 UI 顯示空配置，也不要把可能含憑據的原始內容吐回去。
	got := MaskConfig("nope", map[string]any{"webhook": "https://x/y?token=LEAK"})
	if len(got) != 0 {
		t.Fatalf("未知渠道型別應返回空配置，得到 %v", got)
	}
}

func TestMaskConfigDoesNotMutateInput(t *testing.T) {
	// 掩碼是展示層行為，不能反過來把庫裡的真值改掉。
	cfg := map[string]any{"webhook": "https://example.com/hook", "secret": "SECtest123456"}
	_ = MaskConfig(KindDingTalk, cfg)
	if IsMasked(cfg["secret"].(string)) {
		t.Fatal("MaskConfig 修改了入參，會導致真實憑據被掩碼值覆蓋")
	}
}

func TestMergeConfigKeepsStoredOnMaskedIncoming(t *testing.T) {
	stored := map[string]any{"webhook": "https://real/hook", "secret": "REALSECRET", "method": "POST"}
	// 使用者只改了 method，瀏覽器提交的是掩碼值+新 method。
	incoming := map[string]any{
		"webhook": MaskedValue("https://real/hook"),
		"secret":  MaskedValue("REALSECRET"),
		"method":  "PUT",
	}
	got := MergeConfig(stored, incoming)
	if got["webhook"] != "https://real/hook" || got["secret"] != "REALSECRET" {
		t.Fatalf("掩碼欄位應保留庫中原值，得到 %v", got)
	}
	if got["method"] != "PUT" {
		t.Fatalf("被修改的欄位應生效，得到 %v", got["method"])
	}
}

func TestMergeConfigEmptyStringClears(t *testing.T) {
	stored := map[string]any{"webhook": "https://real/hook", "secret": "REALSECRET"}
	got := MergeConfig(stored, map[string]any{"secret": ""})
	if _, ok := got["secret"]; ok {
		t.Fatalf("空串應清空該欄位，得到 %v", got)
	}
	// 沒提到的欄位保留（區域性更新語義）。
	if got["webhook"] != "https://real/hook" {
		t.Fatalf("未提及的欄位應保留，得到 %v", got)
	}
}

func TestMergeConfigKeepsUnmentionedStoredKeys(t *testing.T) {
	stored := map[string]any{"host": "smtp.example.com", "port": float64(587), "password": "pw"}
	got := MergeConfig(stored, map[string]any{"port": float64(465)})
	if got["host"] != "smtp.example.com" || got["password"] != "pw" {
		t.Fatalf("未提及的欄位應保留，得到 %v", got)
	}
	if got["port"] != float64(465) {
		t.Fatalf("已提及的欄位應更新，得到 %v", got["port"])
	}
}

// TestPrepareConfigUpdateBlocksDestinationSwap 是本包最重要的一條安全不變數：
// **改目標地址不能把舊憑據帶過去**。
//
// 這些用例用的正是攻擊形狀的輸入（只改地址、對憑據避而不談），
// 而不是「防禦邏輯的正確輸入」——只測後者的話，防禦沒生效也照樣全綠。
func TestPrepareConfigUpdateBlocksDestinationSwap(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		stored   map[string]any
		incoming map[string]any
		// wantMissing 是預期被點名的憑據鍵。
		wantMissing string
	}{
		{
			name: "通用 Webhook 改地址想沿用 Authorization 頭",
			kind: KindWebhook,
			stored: map[string]any{
				"url":     "https://legit.example.com/hook",
				"headers": map[string]any{"Authorization": "Bearer REAL-TOKEN"},
			},
			incoming:    map[string]any{"url": "https://attacker.tld/c"},
			wantMissing: "headers",
		},
		{
			name:        "Telegram 改 base_url 想把 Bot Token 發到自己的端點",
			kind:        KindTelegram,
			stored:      map[string]any{"bot_token": "123456:REAL", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming:    map[string]any{"base_url": "https://attacker.tld"},
			wantMissing: "bot_token",
		},
		{
			name:        "郵件改 SMTP 主機想交出密碼",
			kind:        KindEmail,
			stored:      map[string]any{"host": "smtp.corp.com", "port": 587, "password": "REALPW", "from": "a@b.c", "to": []any{"d@e.f"}},
			incoming:    map[string]any{"host": "smtp.attacker.tld"},
			wantMissing: "password",
		},
		{
			name:        "郵件關掉 TLS 也必須重新表態密碼",
			kind:        KindEmail,
			stored:      map[string]any{"host": "smtp.corp.com", "port": 587, "tls": false, "password": "REALPW", "from": "a@b.c", "to": []any{"d@e.f"}},
			incoming:    map[string]any{"tls": true},
			wantMissing: "password",
		},
		{
			// 掩碼值 = 「沿用舊憑據」，在地址變更的語境下同樣必須拒絕。
			name:        "回傳掩碼憑據 + 新地址",
			kind:        KindTelegram,
			stored:      map[string]any{"bot_token": "123456:REAL", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming:    map[string]any{"base_url": "https://attacker.tld", "bot_token": MaskedValue("123456:REAL")},
			wantMissing: "bot_token",
		},
		{
			name:        "釘釘改 Webhook 想沿用加簽金鑰",
			kind:        KindDingTalk,
			stored:      map[string]any{"webhook": "https://oapi.dingtalk.com/robot/send?access_token=OLD", "secret": "REALSEC"},
			incoming:    map[string]any{"webhook": "https://attacker.tld/hook"},
			wantMissing: "secret",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, err := PrepareConfigUpdate(tc.kind, tc.stored, tc.incoming)
			if err == nil {
				t.Fatalf("改地址卻未重新表態憑據，應當被拒絕；得到配置 %v", merged)
			}
			var target *ErrDestinationChangedWithoutCredentials
			if !errors.As(err, &target) {
				t.Fatalf("應返回專門的錯誤型別以便介面給出可操作提示，得到 %T: %v", err, err)
			}
			found := false
			for _, m := range target.Missing {
				if m == tc.wantMissing {
					found = true
				}
			}
			if !found {
				t.Fatalf("應點名缺失的憑據鍵 %q，得到 %v", tc.wantMissing, target.Missing)
			}
			// 錯誤資訊要能指導操作者怎麼修。
			if !strings.Contains(err.Error(), tc.wantMissing) {
				t.Errorf("錯誤資訊應提到 %q: %v", tc.wantMissing, err)
			}
		})
	}
}

// TestPrepareConfigUpdateAllowsLegitimateEdits 反向用例：正常的編輯不能被誤攔，
// 否則這個防護會因為「太煩」而被繞過或刪掉。
func TestPrepareConfigUpdateAllowsLegitimateEdits(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		stored   map[string]any
		incoming map[string]any
	}{
		{
			name:     "只改名字（配置原樣回傳）",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://legit.example.com/hook", "headers": map[string]any{"Authorization": "Bearer REAL"}},
			incoming: map[string]any{"url": MaskedValue("https://legit.example.com/hook")},
		},
		{
			name:     "只改請求方法，地址與憑據都不動",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://legit.example.com/hook", "method": "POST"},
			incoming: map[string]any{"method": "PUT"},
		},
		{
			name:     "換地址並**同時**給新憑據",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://old.example.com/hook", "headers": map[string]any{"Authorization": "Bearer OLD"}},
			incoming: map[string]any{"url": "https://new.example.com/hook", "headers": map[string]any{"Authorization": "Bearer NEW"}},
		},
		{
			name:     "換地址並顯式宣告不再需要憑據",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://old.example.com/hook", "headers": map[string]any{"Authorization": "Bearer OLD"}},
			incoming: map[string]any{"url": "https://new.example.com/hook", "headers": ""},
		},
		{
			name:     "Telegram 改 chat_id（不是目的地）",
			kind:     KindTelegram,
			stored:   map[string]any{"bot_token": "t", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming: map[string]any{"chat_id": "-100200"},
		},
		{
			name:     "郵件改收件人（不是目的地）",
			kind:     KindEmail,
			stored:   map[string]any{"host": "smtp.corp.com", "port": 587, "password": "PW", "from": "a@b.c", "to": []any{"x@y.z"}},
			incoming: map[string]any{"to": []any{"new@y.z"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, err := PrepareConfigUpdate(tc.kind, tc.stored, tc.incoming)
			if err != nil {
				t.Fatalf("合法編輯被誤攔: %v", err)
			}
			if merged == nil {
				t.Fatal("應返回合併結果")
			}
		})
	}
}

// TestPrepareConfigUpdatePortTypeTolerance 覆蓋一個容易誤判的細節：
// 前端提交的埠是 JSON number（float64），庫裡讀回來也是 float64，
// 但兩個值的型別可能不同（如 int vs float64）。用 == 比較會把「沒改」判成「改了」，
// 從而對只改了名字的使用者彈出「請重新填寫密碼」——假警報會讓人不再信任這個防護。
func TestPrepareConfigUpdatePortTypeTolerance(t *testing.T) {
	stored := map[string]any{"host": "smtp.corp.com", "port": float64(587), "password": "PW"}
	// 同一個埠，以 int 形式提交。
	if _, err := PrepareConfigUpdate(KindEmail, stored, map[string]any{"port": 587}); err != nil {
		t.Fatalf("埠值相同（僅型別不同）不應被判為地址變更: %v", err)
	}
	// 真的換了埠則必須攔。
	if _, err := PrepareConfigUpdate(KindEmail, stored, map[string]any{"port": 25}); err == nil {
		t.Fatal("埠變更應被攔下")
	}
}

// TestPrepareConfigUpdateSurvivesRepeatedSaveWithBlankDestination 覆蓋「可留空的
// 目的地欄位」這條路徑：Telegram 的 base_url 留空表示用官方 API 地址。
//
// 曾經這裡會讓渠道從第二次儲存起永久儲存失敗：
//
//	新建時庫裡存下 base_url:""（建立路徑直接存前端提交的 config，不走 MergeConfig）
//	→ 第一次儲存，MergeConfig 把空串當顯式清空、delete 掉該鍵
//	→ 第二次儲存，incoming 仍是 ""、而 stored 裡已經沒這個鍵，被判成「地址變了」
//	→ bot_token 是掩碼回顯值 → 400「目標地址已變更，請同時重新填寫憑據欄位」
//
// 使用者什麼都沒改，卻從此再也存不上，除非重新貼上一遍 Bot Token。
func TestPrepareConfigUpdateSurvivesRepeatedSaveWithBlankDestination(t *testing.T) {
	stored := map[string]any{"bot_token": "123:ABC", "chat_id": "-100", "base_url": ""}

	// 前端 buildConfig() 對該渠道的每個欄位定義都提交一個值：憑據回填掩碼，
	// 空文本框提交空串。這裡完整重現它的輸出，而不是隻提交「改動的鍵」。
	submit := func() map[string]any {
		return map[string]any{
			"bot_token": MaskedValue("123:ABC"),
			"chat_id":   "-100",
			"base_url":  "",
		}
	}

	// 第一次儲存：只改了渠道名字，config 原樣回傳。
	merged, err := PrepareConfigUpdate(KindTelegram, stored, submit())
	if err != nil {
		t.Fatalf("第一次儲存被誤攔: %v", err)
	}
	if _, ok := merged["base_url"]; ok {
		t.Fatal("前提已變：空串應被 MergeConfig 刪除——本用例要覆蓋的正是「鍵消失之後」那一步")
	}

	// 第二次儲存：提交內容與上次完全一致，使用者什麼都沒改。
	merged2, err := PrepareConfigUpdate(KindTelegram, merged, submit())
	if err != nil {
		t.Fatalf("第二次儲存被誤攔（使用者什麼都沒改）: %v", err)
	}
	// 第三次，確認不是「只錯一次」而是穩定可儲存。
	if _, err := PrepareConfigUpdate(KindTelegram, merged2, submit()); err != nil {
		t.Fatalf("第三次儲存被誤攔: %v", err)
	}
	// 憑據必須一路保留下來，沒有被空串邏輯連帶清掉。
	if got := merged2["bot_token"]; got != "123:ABC" {
		t.Fatalf("Bot Token 應沿用原值，得到 %v", got)
	}
}

// TestPrepareConfigUpdateStillGuardsBlankDestinationChanges 是上一條用例的配對
// 斷言：把空串與「鍵不存在」視為等價，**不能**連帶放過真正的地址變更。
// 這兩個方向都是真實的憑據外發路徑——Telegram 的 Bot Token 走在 URL 路徑裡，
// 換了 base_url 就等於把 Token 送給新地址。
func TestPrepareConfigUpdateStillGuardsBlankDestinationChanges(t *testing.T) {
	// 方向一：從「空」（官方地址）換到自建地址。
	official := map[string]any{"bot_token": "123:ABC", "chat_id": "-100"}
	if _, err := PrepareConfigUpdate(KindTelegram, official, map[string]any{
		"bot_token": MaskedValue("123:ABC"),
		"base_url":  "https://tg-proxy.attacker.tld",
	}); err == nil {
		t.Fatal("從官方地址換到自建地址必須要求重新填寫 Token")
	}

	// 方向二：把自建地址清空（= 換回官方 API）同樣是地址變更。
	proxied := map[string]any{"bot_token": "123:ABC", "base_url": "https://proxy.internal/bot"}
	if _, err := PrepareConfigUpdate(KindTelegram, proxied, map[string]any{
		"bot_token": MaskedValue("123:ABC"),
		"base_url":  "",
	}); err == nil {
		t.Fatal("清空自建地址（換回官方 API）同樣是地址變更，必須要求重新填寫 Token")
	}
}

func TestDestinationKeysDeclaredForEveryKind(t *testing.T) {
	// 與 SecretKeys 同理：渠道若忘記宣告目的地鍵，PrepareConfigUpdate 就保護不到它。
	for kind, ch := range registry {
		if len(ch.DestinationKeys()) == 0 {
			t.Errorf("渠道 %s 未宣告目的地鍵，改地址帶出憑據的防護對它無效", kind)
		}
		if len(ch.SecretKeys()) == 0 {
			t.Errorf("渠道 %s 未宣告憑據鍵", kind)
		}
	}
}

func TestSecretKeysDeclaredForEveryKind(t *testing.T) {
	// 編譯器已經強制每個渠道實現 SecretKeys，這裡再確認一遍「沒有渠道在掩碼上
	// 交白卷」——返回空切片的渠道意味著它的憑據會明文回顯到瀏覽器。
	expect := map[string]bool{
		KindDingTalk: true, KindFeishu: true, KindWeCom: true,
		KindWebhook: true, KindTelegram: true, KindEmail: true,
	}
	for kind, ch := range registry {
		if !expect[kind] {
			t.Errorf("渠道 %s 未在測試中登記掩碼預期", kind)
			continue
		}
		if len(ch.SecretKeys()) == 0 {
			t.Errorf("渠道 %s 未宣告任何憑據欄位，其配置會明文回顯", kind)
		}
	}
}

// TestPrepareConfigUpdateRejectsMaskedInContainer 覆蓋審計指出的一處口子：
// 把掩碼哨兵塞進**非字串**結構（如 webhook.headers 是個物件）時，
// MergeConfig 只認「字串且帶字首」為掩碼，於是字面量 "__masked__" 會被當成
// 真實頭值存進庫——後續鑑權靜默失效，且沒有任何報錯。
func TestPrepareConfigUpdateRejectsMaskedInContainer(t *testing.T) {
	stored := map[string]any{
		"url":     "https://legit.example.com/hook",
		"headers": map[string]any{"Authorization": "Bearer REAL"},
	}
	// 物件內部夾帶掩碼哨兵。
	incoming := map[string]any{
		"headers": map[string]any{"Authorization": MaskedPrefix},
	}
	if _, err := PrepareConfigUpdate(KindWebhook, stored, incoming); err == nil {
		t.Fatal("結構體內部夾帶掩碼哨兵應被拒絕（否則會把字面量存進庫）")
	}
	// 整體提交物件（真實新值）照常接受。
	ok := map[string]any{"headers": map[string]any{"Authorization": "Bearer NEW"}}
	if _, err := PrepareConfigUpdate(KindWebhook, stored, ok); err != nil {
		t.Fatalf("正常提交新請求頭不應被攔: %v", err)
	}
}
