package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// assetInterceptCandidates 提取一條待插入資產輸入項的 域名/IP/URL 候選串，用於資產攔截匹配。
// URL 的 host 會拆出歸類，使「只帶 URL」的服務/端點資產也能被 域名/IP 規則命中。
func assetInterceptCandidates(item assetInputItem) (domains, ips, urls []string) {
	add := func(dst *[]string, s string) {
		if s = strings.TrimSpace(s); s != "" {
			*dst = append(*dst, s)
		}
	}
	add(&domains, item.Domain)
	for _, d := range item.BoundDomains {
		add(&domains, d)
	}
	add(&ips, item.IP)
	add(&ips, item.ServiceIP)
	add(&urls, item.URL)
	if item.URL != "" {
		if u, err := url.Parse(item.URL); err == nil {
			if h := u.Hostname(); h != "" {
				if net.ParseIP(h) != nil {
					add(&ips, h)
				} else {
					add(&domains, h)
				}
			}
		}
	}
	return domains, ips, urls
}

// assetInputLabel 返回一條待插入資產的簡短標識，用於攔截說明訊息。
func assetInputLabel(item assetInputItem) string {
	typ := strings.TrimSpace(item.Type)
	var target string
	switch {
	case strings.TrimSpace(item.Domain) != "":
		target = strings.TrimSpace(item.Domain)
	case strings.TrimSpace(item.URL) != "":
		target = strings.TrimSpace(item.URL)
	case strings.TrimSpace(item.IP) != "":
		target = strings.TrimSpace(item.IP)
	case strings.TrimSpace(item.ServiceIP) != "":
		target = strings.TrimSpace(item.ServiceIP)
	default:
		target = "(未知)"
	}
	if typ != "" {
		return fmt.Sprintf("[%s] %s", typ, target)
	}
	return target
}

// =====================================================================
// Unified asset insertion tools
// =====================================================================

// SetAssetStore wires the asset store and company store onto this ToolSet
// so the insert_assets, add_company_scope, and list_assets tools are active.
func (t *ToolSet) SetAssetStore(as *db.AssetStore, cs *db.CompanyStore) {
	t.as = as
	t.cs = cs
}

// assetInputItem is one element of the insert_assets "assets" array.
type assetInputItem struct {
	Type string `json:"type"` // root_domain|ip|subdomain|app|service|endpoint

	// ---- root_domain / subdomain ----
	Domain      string   `json:"domain"`
	ICP         string   `json:"icp"`
	RecordType  string   `json:"record_type"`
	RecordValue []string `json:"record_value"`

	// ---- ip ----
	IP           string           `json:"ip"`
	BoundDomains []string         `json:"bound_domains"`
	OpenPorts    []db.PortService `json:"open_ports"`

	// ---- app ----
	AppName     string `json:"app_name"`
	BundleID    string `json:"bundle_id"`
	Category    string `json:"category"`
	Description string `json:"description"`
	AppICP      string `json:"app_icp"`
	CompanyID   *int64 `json:"company_id"` // explicit company link (app only; others auto-attribute via scope)

	// ---- service (http) ----
	URL           string           `json:"url"`
	Technologies  []string         `json:"technologies"`
	StatusCode    *int             `json:"status_code"`
	ContentLength *int64           `json:"content_length"`
	PageTitle     string           `json:"page_title"`
	FaviconMMH3   string           `json:"favicon_mmh3"`
	Auth          []map[string]any `json:"auth"`
	ServiceName   string           `json:"service_name"`
	ServiceIP     string           `json:"service_ip"` // optional enrichment IP

	// ---- service (other) ----
	Port  int    `json:"port"`
	Proto string `json:"proto"`

	// ---- endpoint ----
	Method string           `json:"method"`
	Params []map[string]any `json:"params"`
}

// insertAssets is the unified insert_assets agent tool.
func (t *ToolSet) insertAssets() actool.CoreTool {
	return writeTool(
		"insert_assets",
		"批次登記新發現的資產，一次可混合多種型別（type 見列舉）。\n"+
			"各型別必填欄位：root_domain→domain；ip→ip（須為 IPv4/IPv6，非主機名）；subdomain→domain；app→app_name；service(HTTP)→url；service(非HTTP)→service_name+port（ip/domain 至少填一個）；endpoint→url+method。其餘欄位含義見各自說明。\n"+
			"auth/technologies/params 為追加合併(append)，不覆蓋原值。\n"+
			"返回：{results:[{index,id,type}], errors:[{index,error}]}",
		obj(map[string]any{
			// task_id 不暴露給模型：worker 歸屬哪個 task 由程式經 SetTaskID 權威賦值(見 handler)。
			"assets": map[string]any{
				"type":        "array",
				"description": "資產陣列，每個元素對應一條資產記錄",
				"items": obj(map[string]any{
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"},
						"description": "資產型別",
					},
					// root_domain / subdomain
					"domain":      str("根域名或子域名（root_domain/subdomain 必填）"),
					"icp":         str("ICP 備案號（可選）"),
					"record_type": str("DNS 解析型別：A/AAAA/CNAME/MX 等（subdomain 可選）"),
					"record_value": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "DNS 解析值列表（subdomain 可選，如 [\"1.2.3.4\",\"2.3.4.5\"]）",
					},
					// ip
					"ip": str("IP 地址，必須是 IPv4/IPv6 地址，不能填主機名（主機名請用 type=subdomain 的 domain 欄位）；ip 型別必填；service/endpoint 型別可填，用於關聯 IP"),
					"bound_domains": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "該 IP 綁定的域名列表（ip 型別可選）",
					},
					"open_ports": map[string]any{
						"type":        "array",
						"description": "開放埠列表（ip 型別可選）",
						"items": obj(map[string]any{
							"port":    intp("埠號"),
							"service": str("服務名稱，如 http/ssh/mysql 等（可選）"),
						}, "port"),
					},
					// app
					"app_name":    str("應用名稱（app 型別必填）"),
					"bundle_id":   str("Bundle ID（app 型別可選）"),
					"category":    str("應用分類（可選）"),
					"description": str("應用描述（可選）"),
					"app_icp":     str("應用 ICP 備案（可選）"),
					"company_id":  intp("歸屬企業 id（app 型別可選；app 無法靠 scope 自動歸因，需顯式指定。id 由 add_company_scope 返回）"),
					// service (http)
					"url":         str("完整 URL，含協議和埠（HTTP 服務必填；service_type 自動設為 http）"),
					"status_code": intp("HTTP 回應狀態碼，如 200/301/403/404（可選）"),
					"content_length": map[string]any{
						"type":        "integer",
						"description": "HTTP 回應體位元組數（可選）",
					},
					"page_title":   str("頁面 <title> 內容（可選）"),
					"favicon_mmh3": str("favicon MMH3 雜湊（可選）"),
					"technologies": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "指紋/技術棧列表，如 [\"Nginx\",\"Vue\",\"Bootstrap\"]（可選）",
					},
					"auth": map[string]any{
						"type":        "array",
						"description": "發現的認證資訊列表，每條含 type/username/password 等欄位（可選，追加不覆蓋）",
						"items":       map[string]any{"type": "object"},
					},
					// service (other，非 HTTP)
					"service_name": str("服務名稱，如 ssh/mysql/redis（service 非 HTTP 時必填）"),
					"port":         intp("埠號（service 非 HTTP 時必填）"),
					// endpoint
					"method": str("HTTP 方法：GET/POST/PUT/PATCH/DELETE 等（endpoint 必填）"),
					"params": map[string]any{
						"type":        "array",
						"description": "請求引數列表，每條含 location(query/body/header/path)/name/value/type（可選，追加不覆蓋）",
						"items":       map[string]any{"type": "object"},
					},
				}, "type"),
			},
		}, "assets"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("insert_assets 未啟用: AssetStore 未初始化"), nil
			}
			var a struct {
				Assets []assetInputItem `json:"assets"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid input: " + err.Error()), nil
			}
			// task_id 由程式權威賦值(worker: SetTaskID)，不接受模型傳入——避免模型漏傳/錯傳
			// 導致資產未歸任務或歸錯任務。無任務上下文的呼叫方(auto/pentest/chat)其 t.taskID=0。
			taskID := t.taskID

			type result struct {
				Index int    `json:"index"`
				ID    int64  `json:"id"`
				Type  string `json:"type"`
			}
			type errEntry struct {
				Index int    `json:"index"`
				Error string `json:"error"`
			}

			var results []result
			var errs []errEntry

			// 資產閘門規則一次性載入；讀取失敗則跳過判定（不阻斷插入）。
			// 攔截規則 = 全域 ∪ 任務級 block；允許規則 = 任務級 allow。
			blockRules, _ := t.as.ListAssetInterceptRules()
			var allowRules []db.AssetInterceptRule
			if t.taskID > 0 {
				if tb, ta, err := t.as.TaskInterceptRulesSplit(t.taskID); err == nil {
					blockRules = append(blockRules, tb...)
					allowRules = ta
				}
			}

			for i, item := range a.Assets {
				// 資產閘門：先攔截後允許，被拒的資產禁止插入（跳過 Upsert 及後續副作用）。
				domains, ips, urls := assetInterceptCandidates(item)
				if d := db.EvaluateAssetGate(blockRules, allowRules, domains, ips, urls); !d.Allowed {
					errs = append(errs, errEntry{
						Index: i,
						Error: fmt.Sprintf("資產 %s %s，已禁止插入", assetInputLabel(item), d.Reason),
					})
					continue
				}

				typ := strings.TrimSpace(item.Type)
				var id int64
				var err error

				switch typ {
				case "root_domain":
					id, err = t.as.UpsertRootDomain(db.UpsertRootDomainReq{
						Domain: item.Domain,
						ICP:    item.ICP,
						TaskID: taskID,
					})

				case "ip":
					id, err = t.as.UpsertIP(db.UpsertIPReq{
						IP:           item.IP,
						BoundDomains: item.BoundDomains,
						OpenPorts:    item.OpenPorts,
						TaskID:       taskID,
					})

				case "subdomain":
					id, err = t.as.UpsertSubdomain(db.UpsertSubdomainReq{
						Domain:      item.Domain,
						RecordType:  item.RecordType,
						RecordValue: item.RecordValue,
						ICP:         item.ICP,
						TaskID:      taskID,
					})

				case "app":
					id, err = t.as.UpsertApp(db.UpsertAppReq{
						Name:        item.AppName,
						BundleID:    item.BundleID,
						Category:    item.Category,
						Description: item.Description,
						ICP:         item.AppICP,
						CompanyID:   item.CompanyID,
						TaskID:      taskID,
					})

				case "service":
					// distinguish HTTP vs other by presence of url
					if item.URL != "" {
						// agent may send "ip" or "service_ip" for the enrichment IP; accept both
						svcIP := item.ServiceIP
						if svcIP == "" {
							svcIP = item.IP
						}
						id, err = t.as.UpsertHTTPService(db.UpsertHTTPServiceReq{
							URL:           item.URL,
							Technologies:  item.Technologies,
							StatusCode:    item.StatusCode,
							ContentLength: item.ContentLength,
							PageTitle:     item.PageTitle,
							FaviconMMH3:   item.FaviconMMH3,
							Auth:          item.Auth,
							IP:            svcIP,
							TaskID:        taskID,
						})
					} else {
						id, err = t.as.UpsertOtherService(db.UpsertOtherServiceReq{
							Domain:      item.Domain,
							IP:          item.IP,
							Port:        item.Port,
							ServiceName: item.ServiceName,
							Auth:        item.Auth,
							TaskID:      taskID,
						})
					}

				case "endpoint":
					id, err = t.as.UpsertEndpoint(db.UpsertEndpointReq{
						URL:    item.URL,
						Method: item.Method,
						Params: item.Params,
						IP:     item.ServiceIP,
						TaskID: taskID,
					})

				default:
					errs = append(errs, errEntry{Index: i, Error: "unknown type: " + typ})
					continue
				}

				if err != nil {
					errs = append(errs, errEntry{Index: i, Error: err.Error()})
					continue
				}
				results = append(results, result{Index: i, ID: id, Type: typ})
				t.writes.Assets++
				t.anchorOwner(id)
				if taskID > 0 {
					var sourceNodeID *int64
					if t.ownerNode > 0 {
						nodeID := t.ownerNode
						sourceNodeID = &nodeID
					}
					summary := "Agent 通過 insert_assets 登記"
					if t.ownerNode > 0 {
						summary = fmt.Sprintf("Worker 意圖 #%d 通過 insert_assets 登記", t.ownerNode)
					}
					_ = t.as.SetTaskAssetSource(taskID, id, "agent", summary, sourceNodeID)
				}
				// 自動入測試範圍(source='auto')：只對 worker 頂層顯式插入的這一項，按其
				// 型別加保守範圍；side-effect 派生的資產不經此處，故範圍不盲目擴大。taskID=0 時無操作。
				// 與覆蓋度開關無關：task_scope 是任務的範圍邊界(list/查詢的過濾基準)，
				// 覆蓋度開關只決定要不要把它當分母去算指標，不決定要不要累積範圍本身。
				{
					svcIP := item.ServiceIP
					if svcIP == "" {
						svcIP = item.IP
					}
					_ = t.as.AddAutoScope(taskID, typ, item.Domain, item.URL, svcIP)
				}
			}

			return jsonResult(map[string]any{
				"results": results,
				"errors":  errs,
			})
		},
	)
}

// addCompanyScope writes to company_scope table and triggers asset attribution.
func (t *ToolSet) addCompanyScope() actool.CoreTool {
	return writeTool(
		"add_company_scope",
		"把域名/IP/CIDR/ICP備案/企業關鍵詞加入某公司的【資產範圍】——域名、網路和ICP會自動認領命中的資產，關鍵詞只提供給Agent作為範圍提示。\n"+
			"公司名唯一：company 不存在則新建，已存在則複用(只把範圍並進去)。\n"+
			"scope 一行一條，系統自動識別：根域名 / URL / 單個 IP / CIDR 網段 / ICP備案 / 企業關鍵詞。\n"+
			"務必給 reason 說明歸屬依據(whois/證書/ASN 等)。\n"+
			"護欄：拒絕裸 TLD 與過寬網段(IPv4字首需為/16-/32、IPv6字首需為/32-/128)，非法行會被跳過並在 errors 返回。",
		obj(map[string]any{
			"company": str("公司名(不存在則新建、存在則複用；名稱唯一)"),
			"scope":   str("資產範圍，一行一條：域名 / URL / IP / CIDR / ICP備案 / 企業關鍵詞"),
			"reason":  str("歸屬依據(證據/來源)，務必填寫"),
			"logo":    str("公司圖示 URL(可選；僅新建公司時生效)"),
		}, "company", "scope"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("add_company_scope 未啟用: CompanyStore 未初始化"), nil
			}
			var a struct {
				Company string `json:"company"`
				Scope   string `json:"scope"`
				Reason  string `json:"reason"`
				Logo    string `json:"logo"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if strings.TrimSpace(a.Company) == "" {
				return actool.Errorf("company 不能為空"), nil
			}
			companyID, _, err := t.cs.UpsertCompany(a.Company, a.Logo)
			if err != nil {
				return actool.Errorf("建立/獲取公司失敗: " + err.Error()), nil
			}
			lines := splitLines(a.Scope)
			added, skipped, invalid, errMsgs := t.cs.AddScope(companyID, lines, a.Reason)
			out := map[string]any{
				"company_id": companyID,
				"added":      added,
				"skipped":    skipped,
				"invalid":    invalid,
			}
			if len(errMsgs) > 0 {
				out["errors"] = errMsgs
			}
			return jsonResult(out)
		},
	)
}

// addTaskScope lets the plan agent add test scope to THE CURRENT TASK — the coverage
// denominator and the task's authorization edge. Worker discoveries are auto-scoped
// (precise host) by insertAssets; this tool is for DELIBERATELY WIDENING: pull a whole
// root domain or whole company into scope, or add a specific subdomain / ip.
func (t *ToolSet) addTaskScope() actool.CoreTool {
	return writeTool(
		"add_task_scope",
		"把測試範圍加入【本任務】——這是本任務的授權邊界，也是資產測試覆蓋度的分母。\n"+
			"kind 支援：company(整個公司名下資產) / root_domain(整個根域，含所有子域) / subdomain(單個精確子域) / ip / cidr / icp / keyword。\n"+
			"說明：worker 逐個碰到的主機會被系統【自動】加進範圍(精確子域)；本工具用於【主動擴大】——把整個根域/整個公司納入，或補充指定某子域/IP。\n"+
			"value：company 傳公司名或 id(公司須已存在)；root_domain/subdomain 傳域名；ip/cidr 傳 IP 或網段；icp/keyword 傳備案號或企業關鍵詞。\n"+
			"務必給 reason 說明依據(可審計)。多條用 entries 陣列。",
		obj(map[string]any{
			"entries": map[string]any{"type": "array", "description": "批次：[{kind, value}]。kind∈company/root_domain/subdomain/ip/cidr/icp/keyword。", "items": map[string]any{"type": "object"}},
			"kind":    str("[單條] company / root_domain / subdomain / ip / cidr / icp / keyword"),
			"value":   str("[單條] 公司名或id / 域名 / IP / CIDR / ICP / 關鍵詞"),
			"reason":  str("加入依據(用於審計)，務必填寫"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("add_task_scope 未啟用: AssetStore 未初始化"), nil
			}
			if t.taskID <= 0 {
				return actool.Errorf("add_task_scope 需要任務上下文(當前無 task)"), nil
			}
			type scopeEntry struct {
				Kind  string `json:"kind"`
				Value string `json:"value"`
			}
			var a struct {
				Entries    []scopeEntry `json:"entries"`
				scopeEntry              // 單條模式
				Reason     string       `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			items := a.Entries
			if len(items) == 0 {
				items = []scopeEntry{a.scopeEntry}
			}
			var added []map[string]any
			errs := map[string]string{}
			for i, e := range items {
				ts, err := t.as.AddAgentScope(t.taskID, strings.TrimSpace(e.Kind), e.Value, a.Reason, "agent")
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				added = append(added, map[string]any{"kind": ts.Kind, "domain": ts.Domain, "net": ts.Net, "value": ts.Value, "company_id": ts.CompanyID})
			}
			out := map[string]any{"added": added}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		},
	)
}

// listUntestedAssets lets the plan agent pull the current + directly inherited
// scope's not-yet-tested assets on demand (filter by type, paginated).
func (t *ToolSet) listUntestedAssets() actool.CoreTool {
	return readTool(
		"list_untested_assets",
		"查詢【本任務及直接關聯任務】範圍內、還沒被事實錨點覆蓋的資產（關聯範圍只讀，供你自己判斷要不要補測，不代替你決策）。\n"+
			"可選按資產型別過濾：root_domain/subdomain/service/app/endpoint/ip。\n"+
			"分頁：page 從 1 起、page_size 預設 10。返回 {assets:[{id,type,label}], total, page, page_size}。僅任務上下文可用。",
		obj(map[string]any{
			"type":      str("資產型別過濾（可選）：root_domain/subdomain/service/app/endpoint/ip"),
			"page":      intp("頁碼，從 1 起（預設 1）"),
			"page_size": intp("每頁數量（預設 10）"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_untested_assets 未啟用: AssetStore 未初始化"), nil
			}
			if t.taskID <= 0 || t.ts == nil {
				return actool.Errorf("list_untested_assets 需要任務上下文"), nil
			}
			var a struct {
				Type     string `json:"type"`
				Page     int    `json:"page"`
				PageSize int    `json:"page_size"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Page <= 0 {
				a.Page = 1
			}
			if a.PageSize <= 0 {
				a.PageSize = 10
			}
			offset := (a.Page - 1) * a.PageSize
			assets, total, err := t.as.ListUntestedAssetsWithSources(t.taskID, strings.TrimSpace(a.Type), a.PageSize, offset)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{
				"assets": assets, "total": total, "page": a.Page, "page_size": a.PageSize,
			})
		},
	)
}

// listAssets lets an agent query the asset table.
func (t *ToolSet) listAssets() actool.CoreTool {
	return readTool(
		"list_assets",
		"查詢資產庫：DSL 表示式搜尋，或按 id/ids 直取；支援分頁。只返回【本任務及直接關聯任務】測試範圍內的資產。\n"+
			"DSL：field=value 模糊(ILIKE) | field==value 精確 | field!=value 排除 | 數字欄位支援 > >= < <= | 裸詞=全文模糊；AND/OR 組合(AND 優先順序高)，可用括號分組。資產型別用獨立 type 引數，不寫進 DSL。\n"+
			"未傳 id/ids 時 dsl 必須非空（不允許無條件全量查詢）。\n"+
			"可用欄位：domain(根/子/服務域名)、root_domain、ip、url、page_title、icp、service_name、app_name、method(如 GET/POST)、service_type(http|other)、record_type(如 A/CNAME)、technology(陣列，=模糊 ==精確)、port/status_code/company_id(整數)。\n"+
			"示例：status_code>=400 AND technology=shiro ；(port==80 OR port==443) AND technology=nginx",
		obj(map[string]any{
			"dsl":    str(`DSL 查詢表示式（語法/欄位見工具描述）。未傳 id/ids 時必須非空。`),
			"type":   str("資產型別過濾：root_domain|ip|subdomain|app|service|endpoint（獨立欄位，可與 dsl 疊加；單獨 type 不足以查詢，仍需 dsl）"),
			"id":     intp("直接按單個資產 id 取（可選，與 dsl/type 互斥）"),
			"ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "直接按多個資產 id 取（可選，與 dsl/type 互斥）"},
			"limit":  intp("返回上限，預設 10（可選）"),
			"offset": intp("分頁偏移，預設 0（可選）"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_assets 未啟用: AssetStore 未初始化"), nil
			}
			var a struct {
				DSL    string  `json:"dsl"`
				Type   string  `json:"type"`
				ID     int64   `json:"id"`
				IDs    []int64 `json:"ids"`
				Limit  int     `json:"limit"`
				Offset int     `json:"offset"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Limit <= 0 {
				a.Limit = 10
			}

			var assets []*db.Asset
			var err error
			switch {
			case a.ID > 0:
				assets, err = t.as.GetByIDsInScope(t.taskID, []int64{a.ID})
			case len(a.IDs) > 0:
				assets, err = t.as.GetByIDsInScope(t.taskID, a.IDs)
			case a.DSL != "":
				assets, err = t.as.QueryDSLInScope(a.DSL, a.Type, t.taskID, a.Limit, a.Offset)
			default:
				return actool.Errorf("未傳 id/ids 時 dsl 不能為空：不允許無條件查詢全部資產，請提供查詢條件"), nil
			}
			if err != nil {
				return actool.Errorf("DSL 錯誤: " + err.Error()), nil
			}
			return jsonResult(map[string]any{
				"count":  len(assets),
				"assets": assets,
			})
		},
	)
}

// listCompanies lets an agent enumerate companies (企業) with their scope + asset count.
func (t *ToolSet) listCompanies() actool.CoreTool {
	return readTool(
		"list_companies",
		"列出資產庫中的【企業/公司】及其資產範圍(scope)與已歸屬資產數。用於檢視有哪些公司、"+
			"拿到 company_id（insert_assets 關聯 app、list_assets 按 company_id 過濾時用）。"+
			"可選 search 按公司名模糊過濾(不區分大小寫)，留空返回全部。",
		obj(map[string]any{
			"search": str("按公司名模糊過濾(可選，不區分大小寫)；留空返回全部"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("list_companies 未啟用: CompanyStore 未初始化"), nil
			}
			var a struct {
				Search string `json:"search"`
			}
			_ = json.Unmarshal(in, &a)
			cos, err := t.cs.ListCompanies()
			if err != nil {
				return actool.Errorf("查詢公司失敗: " + err.Error()), nil
			}
			q := strings.ToLower(strings.TrimSpace(a.Search))
			type companyOut struct {
				ID         int64    `json:"id"`
				Name       string   `json:"name"`
				AssetCount int      `json:"asset_count"`
				Scope      []string `json:"scope"`
			}
			out := make([]companyOut, 0, len(cos))
			for _, c := range cos {
				if q != "" && !strings.Contains(strings.ToLower(c.Name), q) {
					continue
				}
				scope := make([]string, 0, len(c.Scope))
				for _, r := range c.Scope {
					scope = append(scope, r.Raw)
				}
				out = append(out, companyOut{ID: c.ID, Name: c.Name, AssetCount: c.AssetCount, Scope: scope})
			}
			return jsonResult(map[string]any{"count": len(out), "companies": out})
		},
	)
}

// splitLines splits a multi-line string into non-empty trimmed lines.
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// WorkerTools returns the tool set for a work agent.
func (t *ToolSet) WorkerTools() []actool.CoreTool {
	return []actool.CoreTool{
		// list_findings 保留：報漏洞前先查本任務已確認漏洞，避免重複上報同一漏洞。
		t.listFindings(),
		t.addFinding(), t.recordFact(),
		// asset management (handlers guard nil store internally)。
		// add_company_scope 不給 worker：定義企業資產範圍屬規劃/主控/Auto 的職責，worker 只執行探索。
		t.insertAssets(), t.listAssets(),
		// 跨 work 回看：worker 也可複用其他 work 的觀察，避免重複勞動。
		// search_all_worker_traces：不必先知道 intent_id，按關鍵字全域撈命中步驟；
		// get_worker_trace：鎖定某條 work 後列步驟/就地搜/取完整內容。
		t.searchAllWorkerTraces(), t.getWorkerTrace(),
		// node_detail：worker 拿到 intent_id/節點 id 後可查該節點完整詳情（配合上面的回看）。
		t.nodeDetail(),
		// 以下工具仍【不給】worker，只留給 planner/main（讀上下文、跨 work 復盤是規劃職責，
		// worker 只做單條意圖的執行與寫回）：list_facts / list_companies / list_worker_traces。
	}
}

// MainAgentTools returns the human-interface tool set.
func (t *ToolSet) MainAgentTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		t.expandDigest(), // cold-digest §6.1
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.addHint(), t.addIntent(),
		// steer_work：人可對某條正在執行的意圖(work)即時注入糾偏指令（不打斷、不丟進展）。
		t.steerWorkTool(),
		// set_goals：人可在執行時給本任務補一個新的最終目標（規劃者據此重判是否達成）。
		t.setGoals(),
		// set_constraints：人可在執行時給本任務補/改操作約束（allow/deny），約束 planner/worker 的探索邊界。
		t.setConstraints(),
		// asset management (handlers guard nil store internally)
		t.insertAssets(), t.addCompanyScope(), t.listAssets(),
		t.addFinding(), t.recordFact(),
		t.addTaskScope(),
		// list_untested_assets：按需查本任務範圍內未測資產(型別+分頁)，自行決定補測。
		t.listUntestedAssets(),
	}
}

// AllDomainTools returns the union of all domain tools across all agent types,
// deduped by name (mainagent order wins). Used by the server to build a registry
// for injecting domain tools into agents (Auto, custom) that don't own a per-task
// ToolSet. The caller provides real stores; tools are callable at taskID=0 scope.
func (t *ToolSet) AllDomainTools() []actool.CoreTool {
	seen := map[string]bool{}
	var out []actool.CoreTool
	all := append(append(t.MainAgentTools(), t.PlannerTools()...), t.WorkerTools()...)
	for _, tool := range all {
		if !seen[tool.Name()] {
			seen[tool.Name()] = true
			out = append(out, tool)
		}
	}
	return out
}
