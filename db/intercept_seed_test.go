package db

import (
	"regexp"
	"testing"
)

// 內建「刪除類介面路徑」規則匹配的是整個 tool_input JSON 串，因此用例直接以
// JSON 形態給出，與 Interceptor 實際拿到的 subject 一致。
func TestDeleteEndpointPathPattern(t *testing.T) {
	re := regexp.MustCompile(deleteEndpointPathPattern)

	hit := []string{
		`{"command":"curl -s 'http://t.com/api/user/delete?id=1'"}`,    // GET 打刪除介面
		`{"command":"curl -X POST http://t.com/admin/delete -d id=1"}`, // POST 打刪除介面
		`{"command":"curl 'http://t.com/api/deleteAll'"}`,
		`{"command":"curl 'http://t.com/api/delete_user?id=1'"}`,
		`{"command":"curl 'http://t.com/api/delete-user?id=1'"}`,
		`{"url":"http://t.com/api/remove?id=1"}`,
		`{"command":"curl http://t.com/files/unlink/3"}`,
		`{"command":"curl http://t.com/api/del?id=2"}`,
		`{"command":"curl -X POST http://t/v1/erase"}`,
		`{"command":"curl http://t/admin/destroyAll"}`, // v1 的路徑規則不允許字尾，這裡補上
	}
	for _, s := range hit {
		if !re.MatchString(s) {
			t.Errorf("應命中卻放行: %s", s)
		}
	}

	// 動詞後必須跟分隔符，避免 /delivery、/details 這類只讀路徑被誤攔。
	miss := []string{
		`{"command":"curl 'http://t.com/api/delivery?id=1'"}`,
		`{"command":"curl 'http://t.com/order/details'"}`,
		`{"command":"curl 'http://t.com/api/delta/sync'"}`,
		`{"command":"curl 'http://t.com/user/delegate'"}`,
		`{"command":"curl 'http://delete.example.com/'"}`, // 刪除動詞出現在域名而非路徑
		`{"command":"curl 'http://t.com/remote/status'"}`,
		`{"command":"nmap -p80 10.0.0.1"}`,
	}
	for _, s := range miss {
		if re.MatchString(s) {
			t.Errorf("誤攔: %s", s)
		}
	}
}
