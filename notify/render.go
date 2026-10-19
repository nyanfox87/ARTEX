package notify

import (
	"strings"
	"unicode/utf8"
)

const ellipsis = "…"

// TruncateBytes 把 s 截斷到不超過 max 位元組，保證結果是合法 UTF-8 且不切斷字元。
//
// 為什麼必須按字元邊界切：企微群機器人的 markdown 有 4096 **位元組**硬上限（不是
// 字元數），而中文一個字 3 位元組。直接按位元組切片會把一個漢字切成兩半，產出非法
// UTF-8——平臺側要麼整條拒收，要麼顯示成亂碼方塊。這裡的做法是先從預算位置
// 往前回退到最近的 rune 起始位元組（utf8.RuneStart 判定續位元組 0b10xxxxxx）。
//
// max<=0 表示不限制。截斷後追加省略號，除非 max 小到裝不下省略號。
func TruncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	budget := max - len(ellipsis)
	suffix := ellipsis
	if budget < 0 {
		// max 比省略號還短：放棄省略號，純截斷，避免結果反而超出 max。
		budget = max
		suffix = ""
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// OneLine 把多行文本壓成單行：摺疊所有空白，再按字元數截斷。
// 用於 IM 訊息的標題行——摘要裡常有換行，直接塞進表格/標題會撐壞排版。
// max<=0 表示不限制長度。
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	return TruncateRunes(s, max)
}

// TruncateRunes 把 s 截斷到不超過 max 個字元（而非位元組），超出時追加省略號。
// max<=0 表示不限制。
//
// 與 TruncateBytes 的區別在於平臺口徑：企微按位元組限長，Telegram 按字元數限長。
// 用錯口徑不會報錯，只會讓訊息被切得遠比預期短（中文 1 字 = 3 位元組，
// 按位元組切 4096 只剩約 1365 字），所以兩個函式都必須保留、按渠道選用。
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + ellipsis
}

// TruncateHTML 按字元數截斷 HTML 片段，並保證不產生半截標籤。
//
// 直接對 HTML 做字元截斷會切出 `<a href="htt` 這種殘缺標籤，平臺解析器要麼
// 報錯拒收整條、要麼把後續正文當成屬性值吞掉。這裡的做法是：先按字元截斷，
// 再檢查尾部是否有未閉合的 `<`，有就退到它之前。
//
// 不做標籤配平（補全 </b> 之類）：Telegram 的 HTML 解析器會自動閉合未閉合標籤，
// 而自己實現配平要處理屬性裡的引號、註釋、自閉合標籤，複雜度與收益不成比例。
func TruncateHTML(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	cut := TruncateRunes(s, max)
	// 尾部若是 `<` 開頭的殘片（最後出現 `<` 之後沒有 `>`），退回 `<` 之前。
	if lt := strings.LastIndex(cut, "<"); lt >= 0 && !strings.Contains(cut[lt:], ">") {
		cut = cut[:lt]
	}
	// 尾部若是被切斷的 HTML 實體（如 `&amp;` 被切成 `&amp`），同樣要退回去。
	// 實體殘片在一個只認實體的解析器裡可能讓**整條訊息**被拒收——一條超過
	// 長度上限的彙總訊息本來就常見，不值得為此丟掉整條通知。
	if amp := strings.LastIndex(cut, "&"); amp >= 0 && !strings.Contains(cut[amp:], ";") {
		cut = cut[:amp]
	}
	return cut
}

// packItemCount 計算在預算內能**完整**放下多少條，供彙總訊息按整條打包。
//
// 為什麼要按整條而不是渲染完整篇再截斷：截斷會讓後半截條目憑空消失，
// 而它們的投遞記錄仍會被標記為已送達——訊息裡看不出來、投遞歷史裡也看不出來，
// 漏洞就這麼沒了。按整條打包後，裝不下的條目留在庫裡成為下一批，
// 呼叫方拿到的 kept 就是本條訊息真正送達的條數。
//
// 引數：maxSize<=0 表示不限制；reserve 是給訊息頭部/尾部預留的量；
// size 負責計量（各平臺口徑不同：企微/釘釘按位元組，Telegram 按字元數——
// 用錯口徑不會報錯，只會讓中文訊息被壓到遠小於上限）；
// render 把第 idx 條渲染成它的實際文本——長度因內容而異，不能靠估算。
//
// 至少返回 1（只要還有條目）。單條極端超長時也要發出這一條、由呼叫方的
// 最終截斷兜底，否則一條超長漏洞會把整批永久卡在原地。
func packItemCount(items []Item, maxSize, reserve int, footer string, size func(string) int, render func(Item, int) string) int {
	if maxSize <= 0 {
		return len(items)
	}
	budget := maxSize - reserve - size(footer)
	if budget < 0 {
		budget = 0
	}
	used := 0
	for i, it := range items {
		used += size(render(it, i))
		if used > budget && i > 0 {
			return i
		}
	}
	return len(items)
}

// byteSize / runeSize 是 packItemCount 的兩種計量口徑，命名出來避免呼叫處
// 出現裸的 func(s string) int 閉包，否則很難一眼看出用的是哪種口徑。
func byteSize(s string) int { return len(s) }
func runeSize(s string) int { return utf8.RuneCountInString(s) }

// assetLine 把資產列表渲染成一行展示文本，超過 limit 個時省略其餘並標註總數。
// 一個漏洞可能錨定幾十個資產，全列出來會擠爆訊息。
func assetLine(assets []string, limit int) string {
	if len(assets) == 0 {
		return ""
	}
	if limit <= 0 || len(assets) <= limit {
		return strings.Join(assets, "、")
	}
	return strings.Join(assets[:limit], "、") + " 等 " + itoa(len(assets)) + " 個"
}

// itoa 是 strconv.Itoa 的短別名，僅用於拼接展示文本，避免到處 import strconv。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
