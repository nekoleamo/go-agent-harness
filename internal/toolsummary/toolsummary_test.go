package toolsummary

import (
	"strings"
	"testing"
)

// TestSummarizePerTool 每个工具的摘要要说出**结论**,不是 JSON 的开头。
//
// 钉它的理由:旧行为是把 JSON 前 160/400 字符直接显示 —— file_read 显示成
// `{"content":"package main\n\nimport (` —— 一个字也没告诉人"它读了多少行"。
func TestSummarizePerTool(t *testing.T) {
	cases := []struct {
		tool    string
		content string
		want    string
	}{
		{"file_read", `{"path":"/a/b.go","content":"package main\nfunc main(){}\n","lines":2,"start_line":1,"end_line":2}`, "读 2 行"},
		{"file_read", `{"path":"/a/b.go","content":"x","lines":1204,"truncated":true}`, "读 1,204 行"},
		{"file_write", `{"path":"/a/b.go","wrote":2048}`, "写入 2.0 KB"},
		{"file_edit", `{"path":"/a/b.go","replaced":1}`, "替换 1 处"},
		{"file_list", `{"path":"/a","count":17,"entries":[]}`, "列出 17 项"},
		{"file_glob", `{"root":"/a","pattern":"*.go","count":8,"matches":[]}`, "匹配 8 个"},
		{"file_grep", `{"root":"/a","pattern":"x","count":12,"matches":[{"path":"/a/1.go"},{"path":"/a/2.go"},{"path":"/a/1.go"}]}`, "12 处命中"},
		{"file_delete", `{"count":3,"permanent":false,"results":[]}`, "移入回收站 3 项"},
		{"file_delete", `{"count":2,"permanent":true,"results":[]}`, "永久删除 2 项"},
		{"shell", `{"output":"line1\nline2\nline3\n"}`, "输出 3 行"},
		{"web_fetch", `{"status":200,"url":"https://x","content_type":"text/html","content":"hello"}`, "HTTP 200"},
		{"web_search", `{"query":"q","results":[{"title":"a"},{"title":"b"}]}`, "2 条结果"},
		{"todo", `{"id":"t3","status":"pending"}`, "任务 t3"},
		{"image_ops", `{"src":"/a.png","dst":"/b.jpg","src_bytes":2048,"dst_bytes":1024}`, "2.0 KB → 1.0 KB"},
	}
	for _, tc := range cases {
		got := Summarize(tc.tool, tc.content, "")
		if !strings.Contains(got.Text, tc.want) {
			t.Errorf("%s 摘要应含 %q,实际 %q", tc.tool, tc.want, got.Text)
		}
		if got.Err {
			t.Errorf("%s 摘要不该标成错误: %q", tc.tool, got.Text)
		}
	}
}

// TestSummarizeErrorIsError 工具把失败写在**结果内容**里时,摘要必须标明是错误。
//
// 为什么重要:旧行为给一次失败打「✓」—— 比"没信息"更误导。
func TestSummarizeErrorIsError(t *testing.T) {
	got := Summarize("file_read", `{"error":"读 /x: permission denied"}`, "")
	if !got.Err {
		t.Fatalf("内容级错误应标 Err: %+v", got)
	}
	if !strings.Contains(got.Text, "permission denied") {
		t.Fatalf("摘要应含错误原文: %q", got.Text)
	}
	// 错误通道(errText)优先
	got2 := Summarize("file_read", `{"content":"x"}`, "读 /x: 不存在")
	if !got2.Err || !strings.Contains(got2.Text, "不存在") {
		t.Fatalf("错误通道应优先: %+v", got2)
	}
}

// TestSummarizeUnknownFallsBack 不认识就不猜(返回空串 ⇒ 展示层回退到原截断行为)。
func TestSummarizeUnknownFallsBack(t *testing.T) {
	// 未知工具 + 无可数字段 ⇒ 空
	if got := Summarize("some_unknown_tool", `{"foo":"bar"}`, ""); got.Text != "" {
		t.Fatalf("不认识就不该编摘要: %+v", got)
	}
	// 非 JSON ⇒ 空
	if got := Summarize("file_read", "not json at all", ""); got.Text != "" {
		t.Fatalf("非 JSON 不该编摘要: %+v", got)
	}
	// 未知工具但有 count ⇒ 仍能给一句(只说确实有的量)
	got := Summarize("some_unknown_tool", `{"count":5}`, "")
	if !strings.Contains(got.Text, "5") {
		t.Fatalf("有 count 时应给通用摘要: %+v", got)
	}
}

// TestSummarizeOneLine 摘要必须是**一行**(状态栏/结果行是单行渲染)。
func TestSummarizeOneLine(t *testing.T) {
	got := Summarize("shell", `{"output":"a\nb\nc"}`, "")
	if strings.ContainsAny(got.Text, "\n\r") {
		t.Fatalf("摘要不应含换行: %q", got.Text)
	}
	// 错误原文含换行也要压平
	got2 := Summarize("file_read", `{"error":"第一行\n第二行"}`, "")
	if strings.ContainsAny(got2.Text, "\n\r") {
		t.Fatalf("错误摘要不应含换行: %q", got2.Text)
	}
}
