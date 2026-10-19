package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateBytesKeepsValidUTF8(t *testing.T) {
	// 這是本包最要緊的一條不變數。企微按**位元組**限長，中文 3 位元組/字，
	// 任何按位元組硬切的實現都會把漢字切成半個，產出非法 UTF-8 而被平臺拒收。
	// 用長度互質的多種中英混排輸入去撞每一個可能的切點。
	inputs := []string{
		"中文測試內容",
		"混合 mixed 內容 content",
		"a中b文c測d試e",
		"🔴🟠🟡🔵", // 4 位元組 emoji，切錯更明顯
		strings.Repeat("漏洞", 100),
	}
	for _, in := range inputs {
		for max := 1; max <= len(in)+2; max++ {
			got := TruncateBytes(in, max)
			if !utf8.ValidString(got) {
				t.Fatalf("輸入 %q max=%d: 產出非法 UTF-8 %q", in, max, got)
			}
			if len(got) > max {
				t.Fatalf("輸入 %q max=%d: 結果 %d 位元組超出上限", in, max, len(got))
			}
			// 未被截斷時不得改動內容。
			if len(in) <= max && got != in {
				t.Fatalf("輸入 %q max=%d: 未超限卻改動了內容 -> %q", in, max, got)
			}
		}
	}
}

func TestTruncateBytesZeroMeansUnlimited(t *testing.T) {
	long := strings.Repeat("x", 10000)
	if got := TruncateBytes(long, 0); got != long {
		t.Fatal("max=0 應表示不限制")
	}
	if got := TruncateBytes(long, -5); got != long {
		t.Fatal("max<0 應表示不限制")
	}
}

func TestTruncateBytesEllipsisBudget(t *testing.T) {
	// max 小於省略號本身時，不能因為追加省略號而反過來超限。
	got := TruncateBytes("abcdefgh", 1)
	if len(got) > 1 {
		t.Fatalf("max=1 時結果 %q 長度 %d 超限", got, len(got))
	}
	// 正常情況應帶省略號。
	if got := TruncateBytes("abcdefgh", 5); !strings.HasSuffix(got, ellipsis) {
		t.Fatalf("期望帶省略號，得到 %q", got)
	}
}

func TestTruncateRunesCountsCharactersNotBytes(t *testing.T) {
	// 與 TruncateBytes 的口徑差異必須保留：Telegram 按字元限長，
	// 用位元組口徑會把中文訊息切到只剩三分之一。
	s := "一二三四五六七八九十"
	got := TruncateRunes(s, 5)
	if n := utf8.RuneCountInString(got); n != 5 {
		t.Fatalf("期望 5 個字元，得到 %d 個 (%q)", n, got)
	}
	// 同樣的字串按位元組口徑應明顯更短。
	if utf8.RuneCountInString(TruncateBytes(s, 5)) >= 5 {
		t.Fatal("位元組口徑不應產出與字元口徑相同的字元數")
	}
}

func TestOneLineCollapsesWhitespace(t *testing.T) {
	got := OneLine("第一行\n\n第二行\t帶製表   多空格", 0)
	if strings.ContainsAny(got, "\n\t") {
		t.Fatalf("應摺疊所有空白，得到 %q", got)
	}
	if strings.Contains(got, "  ") {
		t.Fatalf("不應保留連續空格，得到 %q", got)
	}
	// 截斷後仍須可讀且合法。
	got = OneLine("一二三四五六七八九十", 4)
	if n := utf8.RuneCountInString(got); n != 4 {
		t.Fatalf("期望 4 字元，得到 %d (%q)", n, got)
	}
}

func TestTruncateHTMLNeverCutsTagInHalf(t *testing.T) {
	// 直接截斷 HTML 會切出 `<a href="htt` 這種殘片，平臺會拒收整條訊息。
	s := `<b>標題</b>正文正文正文<a href="https://example.com/very/long/path">檢視詳情</a>`
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := TruncateHTML(s, max)
		if n := utf8.RuneCountInString(got); max > 0 && n > max {
			t.Fatalf("max=%d: 結果 %d 字元超限", max, n)
		}
		// 尾部不能有未閉合的 `<`（即最後一段裡出現 `<` 卻無 `>`）。
		if lt := strings.LastIndex(got, "<"); lt >= 0 && !strings.Contains(got[lt:], ">") {
			t.Fatalf("max=%d: 尾部標籤被切斷 -> %q", max, got)
		}
	}
}

func TestAssetLineOmitsExcess(t *testing.T) {
	if got := assetLine(nil, 3); got != "" {
		t.Fatalf("無資產應返回空串，得到 %q", got)
	}
	if got := assetLine([]string{"a", "b"}, 3); got != "a、b" {
		t.Fatalf("未超限應全列，得到 %q", got)
	}
	// 超出上限時必須標註總數，否則讀者不知道還有多少資產沒列出來。
	got := assetLine([]string{"a", "b", "c", "d", "e"}, 2)
	if !strings.Contains(got, "等 5 個") {
		t.Fatalf("應標註總數 5，得到 %q", got)
	}
}

func TestSeverityAndStatusLabels(t *testing.T) {
	if AtLeast("", "low") {
		t.Fatal("空級別序數為 0，應被任何門檻擋住")
	}
	if !AtLeast("critical", "") {
		t.Fatal("空門檻應放行")
	}
	if got := StatusLabel("fixed"); got != "已修復" {
		t.Fatalf("未知狀態對映，得到 %q", got)
	}
	// 未知狀態原樣回顯，不臆造標籤。
	if got := StatusLabel("weird_status"); got != "weird_status" {
		t.Fatalf("未知狀態應原樣回顯，得到 %q", got)
	}
}

// TestTruncateHTMLNeverCutsEntity 覆蓋審計指出的一處遺漏：截斷不只要避開
// 半截標籤，還要避開被切斷的 HTML 實體。
//
// `&amp;` 被切成 `&amp` 之後，一個只認實體的解析器可能拒收**整條**訊息——
// 而超長彙總訊息本來就常見，代價太大。
func TestTruncateHTMLNeverCutsEntity(t *testing.T) {
	s := "aaaa&amp;bbbb&lt;cccc&quot;dddd"
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := TruncateHTML(s, max)
		// 尾部不得出現「有 & 但沒有對應 ;」的實體殘片。
		if amp := strings.LastIndex(got, "&"); amp >= 0 && !strings.Contains(got[amp:], ";") {
			t.Fatalf("max=%d: 尾部留下實體殘片 %q", max, got[amp:])
		}
		if strings.Contains(got, "&amp\x00") {
			t.Fatalf("max=%d: 出現畸形實體", max)
		}
	}
}
