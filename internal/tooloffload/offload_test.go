package tooloffload

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSplitUnderBudgetIsNoop 不超预算 ⇒ 原样、不分流。
//
// 钉它的理由:分流只解决"太大"。小结果上动一行代码(多包一层信封/多个字段)都是
// 白送的风险 —— 而它每天都在被调用。
func TestSplitUnderBudgetIsNoop(t *testing.T) {
	content := `{"output":"hello\n"}`
	got := Split(content, 20000)
	if got.Trimmed || got.Data != "" {
		t.Fatalf("不超预算不该分流: %+v", got)
	}
	if got.Visible != content {
		t.Fatalf("不超预算内容必须逐字相同, got %q", got.Visible)
	}
	if got.OrigChars != len([]rune(content)) {
		t.Fatalf("OrigChars = %d, want %d", got.OrigChars, len([]rune(content)))
	}
}

// TestSplitJSONTrimsBigStringField 大字符串字段按头+尾裁,结论字段(count/status)必须还在。
//
// 钉它的理由:模型靠 `count`/`exit`/`truncated` 这类字段判断"还剩多少、成没成功"。
// 一味把整段 JSON 腰斩成半截文本,模型会拿不到这些结论 —— 那才是"给你看开销不是结论"。
func TestSplitJSONTrimsBigStringField(t *testing.T) {
	big := strings.Repeat("行内容\n", 4000) // ~20000 字符
	content := `{"output":"` + escape(big) + `","exit":0,"count":4000}`
	const limit = 2000
	got := Split(content, limit)
	if !got.Trimmed {
		t.Fatal("应分流")
	}
	if got.Data != content {
		t.Fatal("Data 必须是完整原文(一个字都不能改)")
	}
	if got.VisibleChars > limit {
		t.Fatalf("瘦身后 %d 字符,超预算 %d", got.VisibleChars, limit)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(got.Visible), &obj); err != nil {
		t.Fatalf("瘦身后必须是合法 JSON(模型会解析它): %v\n%s", err, got.Visible)
	}
	for _, k := range []string{"exit", "count"} {
		if _, ok := obj[k]; !ok {
			t.Fatalf("结论字段 %q 被裁掉了: %s", k, got.Visible)
		}
	}
	out, _ := obj["output"].(string)
	if !strings.Contains(out, "省略") {
		t.Fatalf("省略必须**可见**(模型要知道这里少了东西): %q", tail(out, 80))
	}
	if !strings.HasPrefix(out, "行内容") {
		t.Fatalf("头部丢了: %q", out[:20])
	}
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "行内容") {
		t.Fatalf("尾部丢了(尾部常带错误与退出状态): %q", tail(out, 40))
	}
	if _, ok := obj["_offloaded"]; !ok {
		t.Fatalf("缺 _offloaded 说明: %s", got.Visible)
	}
}

// TestSplitJSONTrimsArray 大数组按条裁,且**不塞伪元素**(伪元素会被按 schema 消费的
// 工具/插件当成真数据);丢弃条数记在 _offloaded.omitted 里。
func TestSplitJSONTrimsArray(t *testing.T) {
	items := make([]string, 0, 400)
	for i := 0; i < 400; i++ {
		items = append(items, `{"path":"/a/b.go","line":`+itoa(i)+`,"text":"`+strings.Repeat("x", 40)+`"}`)
	}
	content := `{"matches":[` + strings.Join(items, ",") + `],"count":400}`
	got := Split(content, 3000)
	if !got.Trimmed {
		t.Fatal("应分流")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(got.Visible), &obj); err != nil {
		t.Fatalf("瘦身后必须合法: %v", err)
	}
	matches, _ := obj["matches"].([]any)
	if len(matches) == 0 {
		t.Fatal("不能把数组裁空(空数组会被读成「没结果」)")
	}
	if len(matches) >= 400 {
		t.Fatalf("数组没裁: %d 条", len(matches))
	}
	for _, m := range matches {
		if _, ok := m.(map[string]any)["path"]; !ok {
			t.Fatalf("数组里出现非原始元素(伪标记会污染 schema 消费方): %v", m)
		}
	}
	off, _ := obj["_offloaded"].(map[string]any)
	om, _ := off["omitted"].(map[string]any)
	if n, ok := om["matches"].(float64); !ok || int(n) != 400-len(matches) {
		t.Fatalf("omitted.matches = %v, want %d", om["matches"], 400-len(matches))
	}
}

// TestSplitNonJSONFallsBackToEnvelope 非 JSON 大文本退化成信封:仍是**合法 JSON**,
// 不是半截文本(半截 JSON 会被模型当成完整结果去猜)。
func TestSplitNonJSONFallsBackToEnvelope(t *testing.T) {
	content := strings.Repeat("plain log line\n", 5000)
	const limit = 1500
	got := Split(content, limit)
	if !got.Trimmed {
		t.Fatal("应分流")
	}
	if got.VisibleChars > limit {
		t.Fatalf("瘦身后 %d 字符,超预算 %d", got.VisibleChars, limit)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(got.Visible), &env); err != nil {
		t.Fatalf("兜底必须是合法 JSON: %v\n%s", err, head(got.Visible, 200))
	}
	if env["_offloaded"] != true {
		t.Fatalf("信封缺 _offloaded: %v", env["_offloaded"])
	}
	if int(env["orig_chars"].(float64)) != len([]rune(content)) {
		t.Fatalf("orig_chars = %v, want %d", env["orig_chars"], len([]rune(content)))
	}
	if h, _ := env["head"].(string); !strings.HasPrefix(h, "plain log line") {
		t.Fatal("head 不是原文开头")
	}
	if hint, _ := env["hint"].(string); !strings.Contains(hint, "缩小") {
		t.Fatal("hint 必须告诉模型怎么拿到被省略的内容")
	}
}

// TestSplitTinyLimitDoesNotPanic 极小预算(配置写错/极端值)不许 panic,不许返回空串。
func TestSplitTinyLimitDoesNotPanic(t *testing.T) {
	content := strings.Repeat("x", 10000)
	for _, limit := range []int{1, 2, 50, 100, 400} {
		got := Split(content, limit)
		if !got.Trimmed {
			t.Fatalf("limit=%d 下应分流", limit)
		}
		if got.Visible == "" {
			t.Fatalf("limit=%d 返回了空串(模型会以为工具没输出)", limit)
		}
	}
	// limit=0 是"关闭"语义:原样。
	if got := Split(content, 0); got.Trimmed {
		t.Fatal("limit=0 表示关闭分流")
	}
}

// TestSplitSaveRatio 省下的比例口径(展示/日志用):未分流 = 0。
func TestSplitSaveRatio(t *testing.T) {
	if r := (Result{OrigChars: 100, VisibleChars: 100}); r.SaveRatio() != 0 {
		t.Fatal("未分流应为 0")
	}
	r := Result{Trimmed: true, OrigChars: 1000, VisibleChars: 200}
	if got := r.SaveRatio(); got < 0.79 || got > 0.81 {
		t.Fatalf("SaveRatio = %v, want ~0.8", got)
	}
}

func escape(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

func head(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
