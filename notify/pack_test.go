package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 本檔案覆蓋「按整條打包」這個修復：彙總訊息超出渠道長度上限時，必須**按整條**
// 截斷並把沒裝下的條目數如實報出來，讓呼叫方只標記真正送達的那些。
//
// 之前的做法是渲染完整篇再截斷、然後整批標記已送達：訊息後半截憑空消失，
// 而投遞歷史顯示全部成功——漏洞就這麼沒了，且沒有任何地方能發現。

func TestMarkdownBodyPacksWholeItemsWithinByteLimit(t *testing.T) {
	// 200 條中文彙總，必然遠超企微 4096 位元組。
	m := batchMsg(200)
	body, kept := markdownBody(m, weComMarkdownLimit)

	if len(body) > weComMarkdownLimit {
		t.Fatalf("正文 %d 位元組超上限 %d", len(body), weComMarkdownLimit)
	}
	if !utf8.ValidString(body) {
		t.Fatal("正文不是合法 UTF-8")
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("應只裝下一部分（0 < kept < %d），得到 %d", len(m.Items), kept)
	}
	// 頭部必須如實說明本條只包含多少條、其餘有多少條——否則讀者會把頭部
	// 那個數字當成全部。
	if !strings.Contains(body, "其餘") || !strings.Contains(body, "下一條訊息繼續") {
		t.Fatalf("頭部應說明還有多少條未包含在本條裡:\n%s", body[:minInt(400, len(body))])
	}
	// 只應包含前 kept 條。
	for i := 0; i < kept; i++ {
		if !strings.Contains(body, "漏洞"+itoa(i+1)) {
			t.Fatalf("第 %d 條應在本條訊息裡:\n%s", i+1, body)
		}
	}
	if strings.Contains(body, "漏洞"+itoa(kept+1)) {
		t.Fatalf("第 %d 條不該出現（它屬於下一批）", kept+1)
	}
}

func TestMarkdownBodyKeepsEverythingWhenUnderLimit(t *testing.T) {
	m := batchMsg(3)
	body, kept := markdownBody(m, 0) // 0 = 不限制
	if kept != len(m.Items) {
		t.Fatalf("不限制長度時應全部保留，得到 kept=%d", kept)
	}
	if strings.Contains(body, "其餘") {
		t.Fatalf("沒有截斷時不該出現截斷提示:\n%s", body)
	}
}

func TestMarkdownBodyAlwaysKeepsAtLeastOneItem(t *testing.T) {
	// 預算小到連一條都裝不下時，仍要發出一條（由最終截斷兜底）。
	// 否則一條超長漏洞會把整批永久卡在原地：每次領取都裝不下、每次都不發。
	m := batchMsg(5)
	_, kept := markdownBody(m, 50)
	if kept != 1 {
		t.Fatalf("至少應保留 1 條，得到 %d", kept)
	}
}

func TestMarkdownBodySingleReturnsOne(t *testing.T) {
	_, kept := markdownBody(singleMsg(), 4096)
	if kept != 1 {
		t.Fatalf("單條訊息應報送達 1 條，得到 %d", kept)
	}
	// 空訊息沒有可送達的條目。
	if _, k := markdownBody(Message{}, 4096); k != 0 {
		t.Fatalf("空訊息應報 0 條，得到 %d", k)
	}
}

func TestTelegramPackingUsesRuneBudget(t *testing.T) {
	m := batchMsg(200)
	text, kept := telegramHTML(m)
	// Telegram 按**字元數**限長；用位元組口徑會把中文訊息壓到三分之一。
	if n := utf8.RuneCountInString(text); n > telegramTextLimit {
		t.Fatalf("正文 %d 字元超上限 %d", n, telegramTextLimit)
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("應只裝下一部分，得到 %d", kept)
	}
	if !strings.Contains(text, "下一條繼續") {
		t.Fatalf("應說明還有餘量未包含:\n%.300s", text)
	}
}

func TestFeishuPackingReportsKept(t *testing.T) {
	m := batchMsg(2000)
	_, kept := feishuCard(m)
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("卡片應只裝下一部分，得到 %d", kept)
	}
}

func TestWebhookAndEmailReportAllItems(t *testing.T) {
	// 這兩個渠道不截斷正文，整批都算送達。
	m := batchMsg(7)
	if n := len(m.Items); n != 7 {
		t.Fatal("前置條件不成立")
	}
	// 通過渲染器的返回值間接確認：markdownBody(0) 不限制時全部保留。
	if _, k := markdownBody(m, 0); k != len(m.Items) {
		t.Fatalf("不限制長度時應用全部，得到 %d", k)
	}
}

// TestMarkdownEscapesUntrustedContent 是「不可信內容不得改變訊息結構」的迴歸測試。
// 標題與摘要來自模型輸出（模型讀的是被測目標回應），資產名來自被測目標的 URL。
func TestMarkdownEscapesUntrustedContent(t *testing.T) {
	cases := []struct {
		name  string
		item  Item
		must  []string // 結果裡必須出現（轉義形態）
		wrong []string // 結果裡不得出現（未轉義形態）
	}{
		{
			name: "標題裡的換行 + 外鏈",
			item: Item{
				Severity: "high",
				Name:     "登入口 SQL 注入\n[緊急：點此驗證帳號](http://attacker.tld)",
			},
			// 換行必須被摺疊（否則能偽造出新的列表項/引用塊）；
			// 方括號與圓括號必須被轉義（否則是可點選的外鏈）。
			must:  []string{`\[緊急：點此驗證帳號\]`, `\(http://attacker.tld\)`},
			wrong: []string{"\n[緊急", "\n\n[緊急"},
		},
		{
			name: "標題裡的圖片信標",
			item: Item{
				Severity: "high",
				Name:     "漏洞 ![](http://attacker.tld/beacon)",
			},
			must:  []string{`\!`, `\(http://attacker.tld/beacon\)`},
			wrong: []string{"![]("},
		},
		{
			name: "資產名裡的強調與引用",
			item: Item{
				Severity: "high",
				Name:     "普通標題",
				Assets:   []string{"a.com/*注入*>引用"},
			},
			must:  []string{`\*注入\*`, `\>`},
			wrong: []string{"*注入*"},
		},
		{
			name: "摘要裡的反引號與豎線",
			item: Item{
				Severity: "high",
				Name:     "標題",
				Summary:  "`code` | 表格",
			},
			must:  []string{"\\`code\\`", `\|`},
			wrong: []string{"`code`"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Message{Items: []Item{tc.item}}
			// 單條模式的寫Item 是三個 markdown 渠道共用的渲染路徑。
			var b strings.Builder
			writeItem(&b, tc.item, "", true)
			got := b.String()
			for _, want := range tc.must {
				if !strings.Contains(got, want) {
					t.Errorf("缺少轉義形態 %q:\n%s", want, got)
				}
			}
			for _, bad := range tc.wrong {
				if strings.Contains(got, bad) {
					t.Errorf("出現了未轉義形態 %q（可被用來注入結構或外鏈）:\n%s", bad, got)
				}
			}
			_ = m
		})
	}
}

// TestMarkdownEscapeBackslashFirst 鎖住轉義順序：反斜槓必須最先處理，
// 否則會給後面補上的反斜槓再套一層，輸出裡出現雙反斜槓。
func TestMarkdownEscapeBackslashFirst(t *testing.T) {
	if got := markdownEscape(`a\b*c`); got != `a\\b\*c` {
		t.Fatalf("轉義順序有誤，得到 %q", got)
	}
}

// TestTelegramTitleHasNoMarkdownEscapes 鎖住一個具體的迴歸：
// markdown 轉義不能洩漏到 Telegram 的 HTML 輸出裡（曾經在共享的標題函數裡
// 加過轉義，結果 Telegram 訊息裡出現 `\(1\)` 這種可見反斜槓）。
func TestTelegramTitleHasNoMarkdownEscapes(t *testing.T) {
	m := Message{Items: []Item{{Severity: "high", Name: "alert(1) *重點*"}}}
	text, _ := telegramHTML(m)
	if strings.Contains(text, `\(`) || strings.Contains(text, `\*`) {
		t.Fatalf("Telegram 正文裡出現了 markdown 的反斜槓轉義:\n%s", text)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
