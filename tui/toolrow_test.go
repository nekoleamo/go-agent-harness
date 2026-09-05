// P4-7 工具行视觉增强测试:成功/失败/调用行语义色差、diff 轻染色、搜索/选区回落。
package tui

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestDiffToolRow diff 轻染色:+/ -/头 三色,字符无损;非 diff 行不染色。
func TestDiffToolRow(t *testing.T) {
	cases := []struct{ in, want string }{
		{"+新增行", "38;5;114"},
		{"-删除行", "38;5;167"},
		{"+++ b/main.go", "38;5;245"},
		{"--- a/main.go", "38;5;245"},
		{"@@ -1,2 +1,2 @@", "38;5;245"},
		{"   + 有前导空格", "38;5;114"},
	}
	for _, c := range cases {
		out := diffToolRow(c.in)
		if out == "" || !strings.Contains(out, c.want) {
			t.Fatalf("diff 行应染色 %q: %q", c.in, out)
		}
		if stripANSI(out) != c.in {
			t.Fatalf("diff 行应字符无损: %q → %q", c.in, stripANSI(out))
		}
	}
	if out := diffToolRow("普通上下文行"); out != "" {
		t.Fatalf("非 diff 行不应染色: %q", out)
	}
}

// TestToolRowSemanticColor 工具三态语义色:调用琥珀、成功绿、失败红;diff 行绿/红族区分。
func TestToolRowSemanticColor(t *testing.T) {
	apply := func(ev sdk.SessionEvent) []string {
		s := &State{}
		s.ApplyReplay(&ev)
		var out []string
		for _, l := range s.Lines {
			rows := flattenLines([]Line{l}, 100)
			out = append(out, renderSessionRow(rows[0], s, 0))
		}
		return out
	}
	call := apply(sdk.SessionEvent{Kind: sdk.EventToolCall, Payload: sdk.ToolCallEvent{ID: "1", Name: "web_fetch", Arguments: "{}"}})
	if !strings.Contains(call[0], "38;5;220") {
		t.Fatalf("调用行应琥珀: %q", call[0])
	}
	ok := apply(sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "1", Name: "read", Content: "ok"}})
	if !strings.Contains(ok[0], "38;5;114") {
		t.Fatalf("成功结果行应绿: %q", ok[0])
	}
	fail := apply(sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "1", Name: "bash", Content: "", Error: "boom"}})
	if !strings.Contains(fail[0], "38;5;203") {
		t.Fatalf("失败行应红: %q", fail[0])
	}
}

// TestDiffRowSearchFallback diff 行在搜索命中/选区激活时回落基础样式(不带 diff 色,底色叠加几何正确)。
func TestDiffRowSearchFallback(t *testing.T) {
	line := Line{Kind: "tool", Text: "+ echo hi"}
	s := &State{}
	s.Lines = []Line{line}
	rows := flattenLines(s.Lines, 100)
	// 无命中:走 diff 染色
	out := renderSessionRow(rows[0], s, 0)
	if !strings.Contains(out, "38;5;114") {
		t.Fatalf("无命中应 diff 染色: %q", out)
	}
	// 选区激活:回落基础样式(琥珀调用色;整行不 diff)
	s.SelActive = true
	out = renderSessionRow(rows[0], s, 0)
	if strings.Contains(out, "38;5;114") {
		t.Fatalf("选区激活不应 diff 染色: %q", out)
	}
	s.SelActive = false
	// 搜索命中该行:回落基础样式 + 背景(不 diff)
	s.SearchQuery = "echo"
	s.SearchHits = []int{0}
	s.SearchIdx = 0
	out = renderSessionRow(rows[0], s, 0)
	if strings.Contains(out, "38;5;114") {
		t.Fatalf("搜索命中不应 diff 染色: %q", out)
	}
	if !strings.Contains(out, "48;5;214") && !strings.Contains(out, "48;5;238") {
		t.Fatalf("搜索命中应有命中背景: %q", out)
	}
}
