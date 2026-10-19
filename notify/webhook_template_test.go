package notify

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// 本檔案鎖住通用 Webhook 模板的**能力邊界**。
//
// 這是全包唯一「使用者提供的字串會被當代碼求值」的地方，所以要明確它能做什麼、
// 不能做什麼，並用測試把這些性質固定下來——否則將來有人順手給模板上下文加個
// 方法、或在 FuncMap 裡加個 readFile，能力面就靜默擴大了，而 diff 看起來
// 只是一個無害的小函式。

// TestTemplateContextHasNoMethods 是最重要的一條。
//
// text/template 會呼叫匯出方法（{{.Foo}} 既能取欄位也能調方法）。所以模板上下文
// 只要能接觸到**任何**帶匯出方法的型別，就等於把那些方法暴露給了模板作者。
// 本功能的上下文刻意全是純資料（只有匯出欄位、零方法）。
//
// 若這條失敗：說明有人給 webhookTemplateData / webhookItem 加了方法。
// 在決定放行之前，先想清楚那個方法能不能被模板用來讀取不希望暴露的東西。
func TestTemplateContextHasNoMethods(t *testing.T) {
	for _, v := range []any{webhookTemplateData{}, webhookItem{}} {
		typ := reflect.TypeOf(v)
		if n := typ.NumMethod(); n != 0 {
			var names []string
			for i := 0; i < n; i++ {
				names = append(names, typ.Method(i).Name)
			}
			t.Fatalf("%s 暴露了 %d 個方法（%s）：text/template 可以呼叫它們，"+
				"等於把這些方法的能力開放給了模板作者", typ.Name(), n, strings.Join(names, ", "))
		}
	}
}

// TestTemplateFuncsAreMinimal 鎖住暴露給模板的函式集合。
//
// FuncMap 裡每多一個函式就多一項能力。當前只有 json / jsons，作用是把值序列化
// 成 JSON 片段——不能讀檔案、不能發請求、不能執行命令。
func TestTemplateFuncsAreMinimal(t *testing.T) {
	var got []string
	for name := range webhookTemplateFuncs {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{"json", "jsons"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("模板函式集合變了：得到 %v，期望 %v。新增函式前請確認它不會擴大能力面"+
			"（不能讀寫檔案、不能發起網路請求、不能執行命令）", got, want)
	}
}

// TestTemplateCannotReachUnknownData 覆蓋模板裡的越界訪問：
// 訪問不存在的東西必須失敗，而不是回顯點什麼；且失敗資訊不得帶出內部資料。
func TestTemplateCannotReachUnknownData(t *testing.T) {
	_, err := renderWebhookBody(`{"x": {{.Environment}}, "y": {{.Env}}}`, singleMsg())
	if err == nil {
		t.Fatal("訪問不存在的欄位應報錯")
	}
	// 錯誤裡不能出現模板上下文裡的真實內容（漏洞標題/摘要）。
	for _, leak := range []string{"SQL注入", "引數 id"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("模板錯誤洩露了訊息內容 %q: %v", leak, err)
		}
	}
}

// TestTemplateRenderFailsPermanently 模板寫錯屬於配置錯誤，重試不會自愈。
// 若被判成可重試，一條壞模板會讓每次投遞都白跑三輪退避。
func TestTemplateRenderFailsPermanently(t *testing.T) {
	cfg := map[string]any{
		"url":           "https://example.com/hook",
		"body_template": `{{.Items.`,
	}
	if err := (webhookChannel{}).Validate(cfg); err == nil {
		t.Fatal("模板語法錯誤應在儲存時就被攔下")
	}
	// 即便繞過校驗直接投遞，也必須判永久失敗而不是反覆重試。
	_, err := (webhookChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("壞模板應判永久失敗，得到 %v", err)
	}
}

// TestTemplateCanOnlyProduceJSON 覆蓋「模板渲染結果必須是合法 JSON」這條約束。
// 它順帶擋住了「用模板生成純文本去觸發別的協議」這類用法。
func TestTemplateCanOnlyProduceJSON(t *testing.T) {
	// 合法模板能過。
	ok := map[string]any{"url": "https://example.com/hook", "body_template": `{"t":{{json .Title}}}`}
	if err := (webhookChannel{}).Validate(ok); err != nil {
		t.Fatalf("合法模板應通過校驗: %v", err)
	}
	// 渲染出非 JSON 時必須拒絕（而不是原樣發出去）。
	bad := map[string]any{"url": "http://127.0.0.1:1/hook", "body_template": `not json {{.Count}}`}
	_, err := (webhookChannel{}).Send(context.Background(), bad, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("渲染出非 JSON 應判永久失敗，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "合法 JSON") {
		t.Errorf("錯誤資訊應說明是 JSON 問題，得到 %v", err)
	}
}
