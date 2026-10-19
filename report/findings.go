package report

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// 發現頁「匯出」用的渲染:把一批 findings 錶行渲染成彙總 Markdown、單條 Markdown、
// 或 CSV。JSON 由 server 層直接用 DTO 序列化,不在此處。

// sortFindingsForExport 按嚴重等級降序、再按時間倒序排,與彙總報告的分組一致。
func sortFindingsForExport(fs []*db.DBFinding) {
	sort.SliceStable(fs, func(i, j int) bool {
		ri, rj := sevRank[fs[i].Severity], sevRank[fs[j].Severity]
		if ri != rj {
			return ri < rj // sevRank 越小越嚴重
		}
		return fs[i].CreatedAt.After(fs[j].CreatedAt)
	})
}

// findingTitle 取漏洞可讀標題:名稱 → 類別 → 「未分類」。
func findingTitle(f *db.DBFinding) string {
	return nz(f.Name, nz(f.VulnClass, "未分類"))
}

// FindingsMarkdown 把一批 findings 整合成一份彙總報告(摘要 + 按嚴重等級分組,
// 每條含類別/狀態/所屬任務/證據/詳細報告)。
func FindingsMarkdown(fs []*db.DBFinding, generatedAt time.Time) string {
	items := append([]*db.DBFinding(nil), fs...)
	sortFindingsForExport(items)

	var b strings.Builder
	b.WriteString("# 漏洞發現彙總報告\n\n")
	fmt.Fprintf(&b, "- **生成時間**：%s\n", generatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- **發現總數**：%d 個\n\n", len(items))

	// 摘要:各嚴重等級計數。
	counts := map[string]int{}
	for _, f := range items {
		counts[f.Severity]++
	}
	b.WriteString("## 摘要\n\n")
	b.WriteString("| 嚴重等級 | 數量 |\n| --- | --- |\n")
	for _, s := range []struct{ key, label string }{
		{"critical", "嚴重"}, {"high", "高危"}, {"medium", "中危"}, {"low", "低危"},
	} {
		fmt.Fprintf(&b, "| %s | %d |\n", s.label, counts[s.key])
	}
	if counts["unassessed"] > 0 {
		fmt.Fprintf(&b, "| 待評估 | %d |\n", counts["unassessed"])
	}
	b.WriteString("\n")

	if len(items) == 0 {
		b.WriteString("_無匹配的漏洞。_\n")
		return b.String()
	}

	b.WriteString("## 漏洞明細\n\n")
	for i, f := range items {
		fmt.Fprintf(&b, "### %d. [%s] %s\n\n", i+1, strings.ToUpper(nz(f.Severity, "info")), findingTitle(f))
		if f.VulnClass != "" {
			fmt.Fprintf(&b, "- **類別**：%s\n", f.VulnClass)
		}
		fmt.Fprintf(&b, "- **狀態**：%s\n", nz(f.Status, "pending"))
		if desc := strings.TrimSpace(f.TaskDescription); desc != "" {
			fmt.Fprintf(&b, "- **所屬任務**：%s\n", desc)
		}
		fmt.Fprintf(&b, "- **發現時間**：%s\n\n", f.CreatedAt.Format("2006-01-02 15:04:05"))
		if s := strings.TrimSpace(f.Summary); s != "" {
			fmt.Fprintf(&b, "%s\n\n", s)
		}
		if e := strings.TrimSpace(f.Evidence); e != "" {
			fmt.Fprintf(&b, "**證據：**\n\n```\n%s\n```\n\n", e)
		}
		if rep := strings.TrimSpace(f.Report); rep != "" {
			b.WriteString("**詳細報告：**\n\n")
			b.WriteString(rep)
			b.WriteString("\n\n")
		}
		b.WriteString(findingTrafficMarkdown(f, false))
		b.WriteString("---\n\n")
	}
	return b.String()
}

// SingleFindingMarkdown 渲染單條漏洞為一份獨立 Markdown(用於「一漏洞一檔案」打包)。
func SingleFindingMarkdown(f *db.DBFinding, generatedAt time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# [%s] %s\n\n", strings.ToUpper(nz(f.Severity, "info")), findingTitle(f))
	if f.VulnClass != "" {
		fmt.Fprintf(&b, "- **類別**：%s\n", f.VulnClass)
	}
	fmt.Fprintf(&b, "- **嚴重等級**：%s\n", nz(f.Severity, "info"))
	fmt.Fprintf(&b, "- **狀態**：%s\n", nz(f.Status, "pending"))
	if desc := strings.TrimSpace(f.TaskDescription); desc != "" {
		fmt.Fprintf(&b, "- **所屬任務**：%s\n", desc)
	}
	fmt.Fprintf(&b, "- **發現時間**：%s\n", f.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- **生成時間**：%s\n\n", generatedAt.Format("2006-01-02 15:04:05"))
	if s := strings.TrimSpace(f.Summary); s != "" {
		fmt.Fprintf(&b, "## 概述\n\n%s\n\n", s)
	}
	if e := strings.TrimSpace(f.Evidence); e != "" {
		fmt.Fprintf(&b, "## 證據\n\n```\n%s\n```\n\n", e)
	}
	if rep := strings.TrimSpace(f.Report); rep != "" {
		b.WriteString("## 詳細報告\n\n")
		b.WriteString(rep)
		b.WriteString("\n")
	}
	b.WriteString(findingTrafficMarkdown(f, true))
	return b.String()
}

var unsafeFilenameChars = regexp.MustCompile(`[^\p{Han}\p{L}\p{N}._-]+`)

// FindingFilename 為「一漏洞一檔案」生成安全的 .md 檔名,形如
// `critical_SQL注入_#123.md`。去掉路徑分隔符與控制字元,避免 zip 內非法路徑。
func FindingFilename(f *db.DBFinding) string {
	sev := nz(f.Severity, "info")
	title := findingTitle(f)
	name := fmt.Sprintf("%s_%s_#%d", sev, title, f.ID)
	if f.ExportCaseID > 0 {
		name = fmt.Sprintf("case_%d_%s_%s", f.ExportCaseID, sev, title)
	}
	name = unsafeFilenameChars.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._")
	if name == "" {
		name = fmt.Sprintf("finding_%d", f.ID)
	}
	// 防禦性:再剝一層路徑,杜絕 zip slip。
	name = path.Base(name)
	if len(name) > 120 {
		name = name[:120]
	}
	return name + ".md"
}

// FindingsCSV 把一批 findings 渲染成 CSV(帶 UTF-8 BOM,便於 Excel 正確識別中文)。
// 不含大段 report/evidence 全文,只放摘要類欄位;需要全文用 Markdown/JSON 匯出。
func FindingsCSV(fs []*db.DBFinding) []byte {
	items := append([]*db.DBFinding(nil), fs...)
	sortFindingsForExport(items)

	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF") // UTF-8 BOM
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"ID", "名稱", "類別", "嚴重等級", "狀態", "所屬任務", "發現時間", "概述", "流量證據數量", "流量證據ID"})
	for _, f := range items {
		id := fmt.Sprintf("%d", f.ID)
		if f.ExportCaseID > 0 {
			id = fmt.Sprintf("case:%d", f.ExportCaseID)
		}
		_ = w.Write([]string{
			id,
			findingTitle(f),
			f.VulnClass,
			nz(f.Severity, "info"),
			nz(f.Status, "pending"),
			f.TaskDescription,
			f.CreatedAt.Format("2006-01-02 15:04:05"),
			strings.TrimSpace(f.Summary),
			fmt.Sprint(len(f.TrafficBindings)), findingTrafficIDs(f),
		})
	}
	w.Flush()
	return buf.Bytes()
}

func findingTrafficIDs(f *db.DBFinding) string {
	ids := make([]string, 0, len(f.TrafficBindings))
	for _, b := range f.TrafficBindings {
		ids = append(ids, fmt.Sprint(b.ID))
	}
	return strings.Join(ids, ",")
}

func findingTrafficMarkdown(f *db.DBFinding, attachments bool) string {
	stale := f.Report != "" && f.EvidenceVersion != f.ReportEvidenceVersion
	if len(f.TrafficBindings) == 0 && !stale {
		return ""
	}
	var out strings.Builder
	out.WriteString("\n## 關聯流量證據\n\n")
	fmt.Fprintf(&out, "證據版本：%d；綁定數量：%d。\n\n", f.EvidenceVersion, len(f.TrafficBindings))
	if stale {
		out.WriteString("證據已變更，詳細報告待更新。\n\n")
	}
	for i, b := range f.TrafficBindings {
		fmt.Fprintf(&out, "%d. **證據 #%d · %s** — `%s %s`，狀態碼 %d\n", i+1, b.ID, b.Role, b.Snapshot.Method, strings.ReplaceAll(b.Snapshot.URL, "`", "%60"), b.Snapshot.Status)
		if b.Note != "" {
			fmt.Fprintf(&out, "   %s\n", strings.ReplaceAll(b.Note, "\n", "\n   "))
		}
		if attachments {
			fmt.Fprintf(&out, "   [請求報文](evidence/%d/%d/request.http) · [回應報文](evidence/%d/%d/response.http)\n", f.ID, b.ID, f.ID, b.ID)
		}
	}
	out.WriteString("\n")
	return out.String()
}
