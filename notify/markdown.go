package notify

import (
	"fmt"
	"strings"
)

// 本檔案是「Markdown 系」渠道（釘釘、企業微信）共用的訊息渲染。
// 飛書用卡片 JSON、Telegram 用 HTML、郵件用 HTML，各自在介面卡裡渲染。

// maxAssetsShown 是訊息裡最多列出幾個資產。一個漏洞可能錨定幾十個資產，
// 全列會擠爆訊息且沒有資訊價值——第 4 個之後的域名沒人會在 IM 裡看。
const maxAssetsShown = 3

// maxSummaryRunes 是摘要被壓縮到多少字元。IM 訊息是「提示去看詳情」，
// 不是報告本體，完整內容在平臺裡。
const maxSummaryRunes = 120

// markdownReservedBytes 預留給訊息頭部（彙總行 + 級別分佈 + 可能的截斷提示）
// 與尾部（平臺連結）。按整條打包時把這部分從預算裡扣掉，保證頭尾不會被截掉——
// 頭尾一旦被截，讀者連「這是哪一批、還有多少條沒顯示」都看不出來。
const markdownReservedBytes = 320

// markdownEscape 轉義 markdown 元字元。
//
// 為什麼必須做：漏洞標題、摘要、型別、資產展示名全都來自**不可信來源**——
// 標題與摘要出自模型輸出（模型讀的是被測目標的回應），資產的 url 則是掃描
// 得到的完整 URL（含目標可控的查詢串）。不轉義的話，一條標題為
//
//	登入口 SQL 注入\n[緊急：點此驗證帳號](http://attacker.tld)
//
// 的漏洞會在安全工程師的釘釘/飛書裡渲染成**可點選的外鏈**；而
// `![](http://attacker.tld/beacon)` 會在渲染時被客戶端拉取，等於通報了
// 「這條漏洞已經被看過」並洩露閱讀者 IP。就算是無惡意的內容，注入的粗體或
// 引用塊也能把下面的嚴重漏洞擠出摺疊線。
//
// 轉義集合覆蓋標題/連結/強調/列表/引用/刪除線這幾類會改變結構或產生可點選
// 元素的字元。`\` 必須最先處理，否則會把後面補上的反斜槓再次轉義。
func markdownEscape(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", `\*`,
		"_", `\_`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"!", `\!`,
		"#", `\#`,
		">", `\>`,
		"|", `\|`,
		"~", `\~`,
	)
	return replacer.Replace(s)
}

// markdownText 把不可信文本壓成單行並轉義，供 markdown 正文使用。
// 單行化是轉義之外的另一半：換行本身就能偽造出新的列表項或引用塊，
// 而跳脫字元擋不住它。
func markdownText(s string, maxRunes int) string {
	return markdownEscape(OneLine(s, maxRunes))
}

// markdownTitle 返回訊息標題（IM 平臺的標題欄/卡片標題），內容是**未轉義的原文**。
//
// 這裡刻意不做轉義：這個標題被四種語境的渲染器共用——markdown 正文、Telegram 的
// HTML、飛書卡片的 plain_text、以及通用 Webhook 的 JSON 與郵件主題。每個語境的
// 轉義規則都不同（markdown 轉義塞進 HTML 會留下可見的反斜槓，塞進 JSON 會汙染
// 資料），所以轉義必須由各自的輸出端負責，見 writeItem / feishuItemLines /
// telegramEscape。曾經在共享函數裡加過 markdown 轉義，結果 Telegram 訊息裡
// 出現了 `\(1\)` 這種可見的反斜槓。
func markdownTitle(m Message) string {
	if m.Batch {
		return fmt.Sprintf("漏洞彙總 · 共 %d 條", len(m.Items))
	}
	if len(m.Items) == 0 {
		return "漏洞通知"
	}
	it := m.Items[0]
	return fmt.Sprintf("[%s] %s", SeverityLabel(it.Severity), OneLine(it.Title(), 0))
}

// markdownBody 渲染訊息正文，返回正文與**實際寫入的條目數**。
//
// 返回值 kept 是這次投遞真正送達的條目數，呼叫方據此只把前 kept 條標記為
// 已送達——被渠道長度上限擋在外面的條目必須留待下一批，而不是跟著一起被
// 標記成功。這正是「靜默丟失」的來源：訊息被截斷了，但投遞記錄顯示全部送達，
// 沒有任何地方能看出後半截從未發出。
//
// maxBytes<=0 表示不限制。
func markdownBody(m Message, maxBytes int) (string, int) {
	if !m.Batch {
		if len(m.Items) == 0 {
			return "", 0
		}
		var b strings.Builder
		writeItem(&b, m.Items[0], "", true)
		// 單條訊息即使超長也照發（由最終截斷兜底）：一條漏洞的部分資訊
		// 也好過一條都不發。
		return TruncateBytes(b.String(), maxBytes), 1
	}

	footer := ""
	if m.HomeURL != "" {
		footer = fmt.Sprintf("\n[在平臺中檢視全部](%s)\n", m.HomeURL)
	}
	kept := packItemCount(m.Items, maxBytes, markdownReservedBytes, footer, byteSize, func(it Item, idx int) string {
		var b strings.Builder
		writeItem(&b, it, fmt.Sprintf("%d. ", idx+1), false)
		return b.String()
	})

	items := m.Items[:kept]
	var b strings.Builder
	b.WriteString(markdownBatchIntro(m, items, len(m.Items)))
	for i, it := range items {
		writeItem(&b, it, fmt.Sprintf("%d. ", i+1), false)
	}
	b.WriteString(footer)
	return TruncateBytes(b.String(), maxBytes), kept
}

// markdownBatchIntro 渲染彙總訊息的開頭：時間窗、條數與級別分佈。
// 有了這些，收到彙總的人不用點進平臺就能判斷這批需不需要立刻處理。
//
// items 是**實際裝下**的條目，total 是本批應有的總數。兩者不同時必須明說
// 「還有多少條在下一條訊息裡」——否則讀者會以為訊息頭寫的那個數字就是全部，
// 而後面那些從未發出的條目在介面上完全不存在。
func markdownBatchIntro(m Message, items []Item, total int) string {
	var b strings.Builder
	if m.WindowMinutes > 0 {
		fmt.Fprintf(&b, "**近 %d 分鐘新增 %d 個漏洞**", m.WindowMinutes, total)
	} else {
		fmt.Fprintf(&b, "**新增 %d 個漏洞**", total)
	}
	if extra := total - len(items); extra > 0 {
		fmt.Fprintf(&b, "（本條顯示前 %d 條，其餘 %d 條將在下一條訊息繼續）", len(items), extra)
	}
	// 按級別給出分佈，讓讀者一眼看到有沒有嚴重項。只統計**本條實際包含**的
	// 條目，保證「嚴重 3」和下面能數出來的條目一致。
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Severity]++
	}
	var parts []string
	for _, sev := range []string{"critical", "high", "medium", "low"} {
		if n := counts[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", SeverityLabel(sev), n))
		}
	}
	if len(parts) > 0 {
		b.WriteString("\n" + strings.Join(parts, " · "))
	}
	b.WriteString("\n\n")
	return b.String()
}

// writeItem 渲染單個漏洞條目。
//
// prefix 用於彙總列表的序號；single=true 時渲染完整版（含摘要與回鏈），
// 彙總列表裡只渲染一行摘要——否則 50 條彙總會變成一篇長文件。
//
// 所有來自外部的內容（標題/型別/資產/摘要）都過 markdownText：
// 單行化 + 轉義。回鏈是管理員配置的 public_base_url 拼出來的，不是不可信內容，
// 且必須是可點的連結，所以原樣輸出。
func writeItem(b *strings.Builder, it Item, prefix string, single bool) {
	line := fmt.Sprintf("%s**%s · %s**", prefix, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if !single {
		// 彙總模式：單行呈現，資產與摘要壓縮後跟在後面。
		var extras []string
		if a := assetLine(it.Assets, maxAssetsShown); a != "" {
			extras = append(extras, markdownText(a, 0))
		}
		if it.Summary != "" {
			extras = append(extras, markdownText(it.Summary, 60))
		}
		if len(extras) > 0 {
			line += " — " + strings.Join(extras, " · ")
		}
		b.WriteString(line + "\n")
		return
	}
	b.WriteString(line + "\n")
	if it.IsStatusChange() {
		fmt.Fprintf(b, "**狀態變更**：%s → %s\n",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		fmt.Fprintf(b, "**型別**：%s\n", markdownText(it.VulnClass, 0))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		fmt.Fprintf(b, "**資產**：%s\n", markdownText(a, 0))
	}
	if it.Summary != "" {
		if s := markdownText(it.Summary, maxSummaryRunes); s != "" {
			fmt.Fprintf(b, "**摘要**：%s\n", s)
		}
	}
	if it.DetailURL != "" {
		fmt.Fprintf(b, "[檢視詳情](%s)\n", it.DetailURL)
	}
}
