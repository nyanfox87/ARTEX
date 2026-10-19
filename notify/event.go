package notify

// Snapshot 是 notification_events.snapshot 這一 JSONB 列的契約。寫方是 db 層的
// 漏洞落庫事務，讀方是 server 層的投遞引擎與過濾匹配。定義放在本包是因為它是
// 「通知領域」的載荷：db 只負責序列化，不理解欄位含義。
//
// 為什麼冗餘存漏洞欄位而不在渲染時回查：漏洞事後會被改名、改級別、改狀態，
// 而推送內容應當反映**事發當時**的結論——回查會得到「事後被改成 low」的
// 危險誤導。另外 fan-out 與渲染因此不必 JOIN findings/tasks/assets 三張表。
type Snapshot struct {
	// 事件型別：finding_created / finding_status_changed
	Kind      string  `json:"kind"`
	FindingID int64   `json:"finding_id"`
	TaskID    int64   `json:"task_id"`
	VulnClass string  `json:"vulnclass"`
	Name      string  `json:"name"`
	Severity  string  `json:"severity"`
	Summary   string  `json:"summary"`
	AssetIDs  []int64 `json:"asset_ids"`
	// 僅 kind=finding_status_changed 時非空。
	FromStatus string `json:"from_status,omitempty"`
	ToStatus   string `json:"to_status,omitempty"`
}

// Item 是一條待推送的漏洞，供渠道渲染。
type Item struct {
	FindingID int64
	Name      string
	VulnClass string
	Severity  string
	Summary   string
	// Assets 是解析後的資產展示名（如域名/IP）。由 server 層填充——
	// 本包不碰資料庫，拿不到名字。
	Assets []string
	// DetailURL 是漏洞詳情回鏈；為空表示未配 public_base_url，渲染時省略。
	DetailURL string
	// 狀態變更事件專用；兩項均非空時渲染成「待處理 → 已修復」。
	FromStatus string
	ToStatus   string
}

// IsStatusChange 報告該條目是否為狀態變更事件。
func (i Item) IsStatusChange() bool { return i.FromStatus != "" || i.ToStatus != "" }

// Title 返回條目的展示標題：優先人工命名的 name，回退漏洞型別 vulnclass，
// 兩者都空時用一個佔位符——絕不輸出空標題。
func (i Item) Title() string {
	if i.Name != "" {
		return i.Name
	}
	if i.VulnClass != "" {
		return i.VulnClass
	}
	return "(未命名漏洞)"
}

// Message 是一次渠道傳送的完整內容。
type Message struct {
	// 單條推送時長度為 1；彙總推送（digest）時為一整批。
	// 空切片是非法的，呼叫方須保證至少一條。
	Items []Item
	// Batch=true 時按彙總訊息渲染（換標題、帶上時間窗與條數）。
	Batch bool
	// WindowMinutes 是彙總週期（分鐘），僅 Batch=true 時用於文案「近 N 分鐘」。
	// 刻意由配置顯式傳入而不是渲染時算 time.Since：渲染保持確定性，才好測。
	WindowMinutes int
	// HomeURL 是平臺面板地址（全域 public_base_url）；空則不帶面板入口。
	HomeURL string
}
