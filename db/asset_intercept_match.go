package db

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// 資產攔截規則的匹配/執行層。asset_intercept.go 只負責規則儲存，這裡負責把
// 「目標資產」的域名/IP/URL 與啟用中的規則做匹配。供 agent 工具（add_intent、
// insert_assets）在下發意圖 / 插入資產前呼叫，命中則拒絕。

// AssetInterceptKindLabel 返回 kind 的中文標籤，用於給 agent 的說明訊息。
func AssetInterceptKindLabel(kind string) string {
	switch kind {
	case "exact_domain":
		return "域名(全等)"
	case "exact_ip":
		return "IP(全等)"
	case "exact_url":
		return "URL(全等)"
	case "fuzzy_domain":
		return "域名(模糊)"
	case "fuzzy_ip":
		return "IP(模糊)"
	case "fuzzy_url":
		return "URL(模糊)"
	case "cidr":
		return "CIDR 網段"
	}
	return kind
}

// Reason 返回一條可讀的命中原因，形如：命中資產攔截規則 [域名(模糊): .gov.cn]（備註）。
func (r AssetInterceptRule) Reason() string {
	s := fmt.Sprintf("命中資產攔截規則 [%s: %s]", AssetInterceptKindLabel(r.Kind), r.Pattern)
	if note := strings.TrimSpace(r.Note); note != "" {
		s += "（" + note + "）"
	}
	return s
}

// matchOne 判斷單條啟用規則是否命中給定的域名/IP/URL 候選串，返回命中的具體值。
func matchOne(r AssetInterceptRule, domains, ips, urls []string) (string, bool) {
	p := strings.TrimSpace(r.Pattern)
	if p == "" {
		return "", false
	}
	switch r.Kind {
	case "exact_domain":
		for _, d := range domains {
			if strings.EqualFold(strings.TrimSpace(d), p) {
				return d, true
			}
		}
	case "exact_ip":
		for _, ip := range ips {
			if strings.TrimSpace(ip) == p {
				return ip, true
			}
		}
	case "exact_url":
		for _, u := range urls {
			if strings.TrimSpace(u) == p {
				return u, true
			}
		}
	case "fuzzy_domain":
		lp := strings.ToLower(p)
		for _, d := range domains {
			if d != "" && strings.Contains(strings.ToLower(d), lp) {
				return d, true
			}
		}
	case "fuzzy_ip":
		for _, ip := range ips {
			if ip != "" && strings.Contains(ip, p) {
				return ip, true
			}
		}
	case "fuzzy_url":
		lp := strings.ToLower(p)
		for _, u := range urls {
			if u != "" && strings.Contains(strings.ToLower(u), lp) {
				return u, true
			}
		}
	case "cidr":
		_, ipnet, err := net.ParseCIDR(p)
		if err != nil {
			return "", false
		}
		for _, ip := range ips {
			if pip := net.ParseIP(strings.TrimSpace(ip)); pip != nil && ipnet.Contains(pip) {
				return ip, true
			}
		}
	}
	return "", false
}

// MatchAssetInterceptRules 返回第一條命中給定 域名/IP/URL 候選串的啟用規則，及命中的具體值。
// 供 insert_assets 用原始輸入（尚未落庫的 assetInputItem）匹配。
func MatchAssetInterceptRules(rules []AssetInterceptRule, domains, ips, urls []string) (AssetInterceptRule, string, bool) {
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if v, ok := matchOne(r, domains, ips, urls); ok {
			return r, v, true
		}
	}
	return AssetInterceptRule{}, "", false
}

// interceptCandidates 提取一個已落庫資產用於攔截匹配的 域名/IP/URL 候選串。
// URL 的 host 會被拆出並歸類，使「只帶 URL」的服務類資產也能被 域名/IP 規則命中。
func (a *Asset) interceptCandidates() (domains, ips, urls []string) {
	add := func(dst *[]string, s string) {
		if s = strings.TrimSpace(s); s != "" {
			*dst = append(*dst, s)
		}
	}
	add(&domains, a.Domain)
	add(&domains, a.RootDomain)
	for _, d := range a.BoundDomains {
		add(&domains, d)
	}
	add(&ips, a.IP)
	add(&urls, a.URL)
	if a.URL != "" {
		if u, err := url.Parse(a.URL); err == nil {
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

// InterceptLabel 返回資產的簡短標識，用於給 agent 的說明訊息。
func (a *Asset) InterceptLabel() string {
	var target string
	switch {
	case a.Domain != "":
		target = a.Domain
	case a.URL != "":
		target = a.URL
	case a.IP != "":
		target = a.IP
	default:
		target = fmt.Sprintf("#%d", a.ID)
	}
	return fmt.Sprintf("資產#%d[%s] %s", a.ID, a.Type, target)
}

// hasEnabledRule 判斷規則集裡是否存在任一啟用規則。
func hasEnabledRule(rules []AssetInterceptRule) bool {
	for _, r := range rules {
		if r.Enabled {
			return true
		}
	}
	return false
}

// AssetGateDecision 是「先攔截後允許」閘門對一組候選串的判定結果。
type AssetGateDecision struct {
	Allowed bool
	Reason  string // 被拒原因（不含資產標識）；Allowed=true 時為空
}

// EvaluateAssetGate 執行任務級閘門判定：
//  1. 命中任一啟用的 blockRules → 拒絕（攔截原因）。
//  2. 否則若 allowRules 存在啟用項且都不命中 → 拒絕（不在允許範圍）。
//  3. 否則放行。
//
// allowRules 為空/無啟用項時，允許閘門不生效（即不啟用白名單，全部放行），
// 避免「未配置允許規則」把所有資產擋掉。
func EvaluateAssetGate(blockRules, allowRules []AssetInterceptRule, domains, ips, urls []string) AssetGateDecision {
	if rule, _, ok := MatchAssetInterceptRules(blockRules, domains, ips, urls); ok {
		return AssetGateDecision{Allowed: false, Reason: rule.Reason()}
	}
	if hasEnabledRule(allowRules) {
		if _, _, ok := MatchAssetInterceptRules(allowRules, domains, ips, urls); !ok {
			return AssetGateDecision{Allowed: false, Reason: "不在任務允許(白名單)範圍內，不允許測試"}
		}
	}
	return AssetGateDecision{Allowed: true}
}

// AssetInterceptHit 描述一個被閘門拒絕的資產（攔截命中 或 不在允許範圍）。
type AssetInterceptHit struct {
	Asset  *Asset
	Reason string // 可讀原因
}

// Describe 返回一條可讀的說明：資產資訊 + 原因。
func (h AssetInterceptHit) Describe() string {
	return fmt.Sprintf("%s → %s", h.Asset.InterceptLabel(), h.Reason)
}

// ListAssetInterceptRules 是 *DB 同名方法的透傳，讓只持有 AssetStore 的呼叫方
// （如 agent 工具）也能讀取規則。
func (s *AssetStore) ListAssetInterceptRules() ([]AssetInterceptRule, error) {
	return s.db.ListAssetInterceptRules()
}

// CheckAssetsIntercept 按 id 載入資產，逐個執行「先攔截後允許」閘門判定，返回所有
// 被拒的資產。攔截規則 = 全域 ∪ 任務級 block；允許規則 = 任務級 allow（僅本任務）。
// 無 id 時快速返回。用全域 GetByIDs（不受任務範圍過濾）以保證攔截不被 scope 削弱。
func (s *AssetStore) CheckAssetsIntercept(taskID int64, ids []int64) ([]AssetInterceptHit, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	blockRules, err := s.db.ListAssetInterceptRules()
	if err != nil {
		return nil, err
	}
	var allowRules []AssetInterceptRule
	if taskID > 0 {
		tb, ta, err := s.TaskInterceptRulesSplit(taskID)
		if err != nil {
			return nil, err
		}
		blockRules = append(blockRules, tb...)
		allowRules = ta
	}
	// 既無攔截規則、也無啟用的允許規則 → 無需判定，全部放行。
	if len(blockRules) == 0 && !hasEnabledRule(allowRules) {
		return nil, nil
	}
	assets, err := s.GetByIDs(ids)
	if err != nil {
		return nil, err
	}
	var hits []AssetInterceptHit
	for _, a := range assets {
		domains, ips, urls := a.interceptCandidates()
		if d := EvaluateAssetGate(blockRules, allowRules, domains, ips, urls); !d.Allowed {
			hits = append(hits, AssetInterceptHit{Asset: a, Reason: d.Reason})
		}
	}
	return hits, nil
}
