package notify

import "testing"

func TestParseFilterMalformedFallsBackToMatchAll(t *testing.T) {
	// 畸形 JSON、空輸入、型別不對的欄位——全部必須退化為零值 Filter，
	// 即「不過濾」。這條不變數是「寧可多推不可漏推」的落點：
	// 一旦這裡改成報錯或半解析，使用者配錯一個字元就會靜默丟掉所有高危通知。
	cases := []struct {
		name string
		raw  string
	}{
		{"空輸入", ""},
		{"非法 JSON", `{not json`},
		{"截斷的 JSON", `{"min_severity":`},
		{"型別不匹配", `{"min_severity": 123, "task_ids": "abc"}`},
		{"頂層是陣列", `[1,2,3]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ParseFilter([]byte(tc.raw))
			if f.MinSeverity != "" || len(f.TaskIDs) != 0 || len(f.AssetIDs) != 0 {
				t.Fatalf("畸形配置應退化為零值 Filter，得到 %+v", f)
			}
			// 零值 Filter 必須命中任意事件。
			ev := Snapshot{Kind: EventFindingCreated, Severity: "low", VulnClass: "XSS"}
			if !Match(f, ev) {
				t.Fatal("零值 Filter 應命中所有事件")
			}
		})
	}
}

func TestMatchSeverityThreshold(t *testing.T) {
	ev := func(sev string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: sev}
	}
	cases := []struct {
		min    string
		sev    string
		expect bool
	}{
		{"", "low", true},
		{"", "critical", true},
		{"high", "critical", true},
		{"high", "high", true},
		{"high", "medium", false},
		{"high", "low", false},
		{"critical", "high", false},
		{"critical", "critical", true},
		// 未知級別序數為 0，應被任何非空門檻擋住（存疑時不推）。
		{"low", "", false},
		{"low", "unknown", false},
		{"", "", true},
	}
	for _, tc := range cases {
		got := Match(Filter{MinSeverity: tc.min}, ev(tc.sev))
		if got != tc.expect {
			t.Errorf("min=%q sev=%q: 期望 %v 得到 %v", tc.min, tc.sev, tc.expect, got)
		}
	}
}

func TestMatchScopeRestrictions(t *testing.T) {
	ev := Snapshot{
		Kind:      EventFindingCreated,
		Severity:  "high",
		TaskID:    7,
		AssetIDs:  []int64{10, 20},
		VulnClass: "SQL注入",
	}
	cases := []struct {
		name   string
		filter Filter
		expect bool
	}{
		{"空範圍=不限", Filter{}, true},
		{"任務命中", Filter{TaskIDs: []int64{7}}, true},
		{"任務未命中", Filter{TaskIDs: []int64{8}}, false},
		{"任務多選含命中", Filter{TaskIDs: []int64{8, 7}}, true},
		{"資產有交集", Filter{AssetIDs: []int64{20, 99}}, true},
		{"資產無交集", Filter{AssetIDs: []int64{99}}, false},
		{"任務與資產同時命中", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{10}}, true},
		{"任務命中但資產未命中", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{99}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev); got != tc.expect {
				t.Errorf("期望 %v 得到 %v", tc.expect, got)
			}
		})
	}
}

func TestMatchVulnClassKeywords(t *testing.T) {
	ev := func(class string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: "high", VulnClass: class}
	}
	cases := []struct {
		name   string
		filter Filter
		class  string
		expect bool
	}{
		{"include 為空=全收", Filter{}, "任意型別", true},
		{"include 命中", Filter{VulnClassInclude: []string{"SQL"}}, "SQL注入", true},
		{"include 未命中", Filter{VulnClassInclude: []string{"命令執行"}}, "SQL注入", false},
		{"include 多詞任一命中", Filter{VulnClassInclude: []string{"命令執行", "SQL"}}, "SQL注入", true},
		{"大小寫不敏感", Filter{VulnClassInclude: []string{"sql"}}, "SQL注入", true},
		{"exclude 命中即排除", Filter{VulnClassExclude: []string{"資訊洩露"}}, "資訊洩露", false},
		{"exclude 未命中則放行", Filter{VulnClassExclude: []string{"資訊洩露"}}, "SQL注入", true},
		// 排除優先於包含：同時命中時應當出局。
		{"排除優先於包含", Filter{
			VulnClassInclude: []string{"SQL"},
			VulnClassExclude: []string{"注入"},
		}, "SQL注入", false},
		// 純空白關鍵詞應被忽略，否則會退化成「匹配所有含空格的字串」。
		{"空白關鍵詞被忽略", Filter{VulnClassInclude: []string{"", "  "}}, "SQL注入", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev(tc.class)); got != tc.expect {
				t.Errorf("期望 %v 得到 %v", tc.expect, got)
			}
		})
	}
}

func TestMatchStatusChangeRequiresOptIn(t *testing.T) {
	ev := Snapshot{Kind: EventFindingStatusChanged, Severity: "critical", FromStatus: "pending", ToStatus: "fixed"}
	// 預設關：絕大多數人說的「推送漏洞」指發現新漏洞，不是狀態流水賬。
	if Match(Filter{MinSeverity: "low"}, ev) {
		t.Fatal("狀態變更事件在未開啟時應被跳過")
	}
	if !Match(Filter{OnStatusChange: true}, ev) {
		t.Fatal("開啟 on_status_change 後狀態變更事件應命中")
	}
	// 建立事件不受 on_status_change 影響。
	created := Snapshot{Kind: EventFindingCreated, Severity: "critical"}
	if !Match(Filter{MinSeverity: "low"}, created) {
		t.Fatal("建立事件不應依賴 on_status_change")
	}
}
