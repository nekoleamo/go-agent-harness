// P5 背景块测试:annotateRowBg 标注(user 整块/工具调用与结果首行)+ 渲染背景输出 +
// 工具调用行左缘竖线前缀 + 折叠结果行背景保持。
package tui

import (
	"strings"
	"testing"
)

// mkRows 构造 lines 的展平行 + annotateRowBg(flattenLines 与 view 路径共用标注函数)。
func mkRows(lines []Line) []physRow {
	rows := flattenLines(lines, 100)
	annotateRowBg(rows, lines)
	return rows
}

func TestAnnotateRowBg(t *testing.T) {
	lines := []Line{
		{Kind: "user", Text: "第一条用户消息"},
		{Kind: "assistant", Text: "回复"},
		{Kind: "tool", Text: "⚙ read main.go {}"},
		{Kind: "tool", Text: "✓ read: ok"},
		{Kind: "error", Text: "✗ bash: boom"},
		{Kind: "error", Text: "agent error: 回合失败"},
	}
	rows := mkRows(lines)
	// user 整块:全部物理行带 user-bg
	for _, p := range rows {
		if p.lineIdx == 0 {
			if p.bg != string(TokUserBg) {
				t.Fatalf("user 行 %d 应全程背景 %s: %q", p.lineIdx, TokUserBg, p.bg)
			}
		}
	}
	// 工具三态:仅首物理行带背景(键用逻辑行原文,忽略 ▍ 前缀)
	got := map[string]string{}
	for _, p := range rows {
		if p.first {
			got[strings.TrimPrefix(p.text, "▍ ")] = p.bg
		} else if p.bg != "" {
			t.Fatalf("非首物理行不应有背景(折叠/展开标题外): %q → %q", p.text, p.bg)
		}
	}
	if got["⚙ read main.go {}"] != string(TokToolBg) {
		t.Fatalf("调用行应 tool-bg: %q", got["⚙ read main.go {}"])
	}
	if got["✓ read: ok"] != string(TokToolOKBg) {
		t.Fatalf("成功结果应 tool-ok-bg: %q", got["✓ read: ok"])
	}
	if got["✗ bash: boom"] != string(TokToolErrBg) {
		t.Fatalf("失败结果应 tool-err-bg: %q", got["✗ bash: boom"])
	}
	// agent error 行(无 ✗ 前缀)不误标
	if got["agent error: 回合失败"] != "" {
		t.Fatalf("agent error 行不应工具失败背景: %q", got["agent error: 回合失败"])
	}
	// 折行续行仅首行有背景(user 除外)
	long := []Line{{Kind: "tool", Text: "⚙ long_argument " + strings.Repeat("x", 200)}}
	rows2 := mkRows(long)
	first := 0
	for _, p := range rows2 {
		if p.first {
			first++
			if p.bg != string(TokToolBg) {
				t.Fatal("调用行首行应有背景")
			}
		} else if p.bg != "" {
			t.Fatal("调用行续行不应有背景(竖线/背景只标注标题行)")
		}
	}
	if first != 1 {
		t.Fatalf("应恰有 1 首行: %d", first)
	}
}

func TestUserRowBackgroundRendered(t *testing.T) {
	s := &State{}
	s.Lines = []Line{{Kind: "user", Text: "你好"}}
	rows := mkRows(s.Lines)
	out := renderSessionRow(rows[0], s, 0)
	// 背景 SGR(256 色 48;5;236)+ 前景保持青色 81
	if !strings.Contains(out, "48;5;236") {
		t.Fatalf("user 行应含背景 48;5;236: %q", out)
	}
	if !strings.Contains(out, "38;5;81") {
		t.Fatalf("user 行前景应保持: %q", out)
	}
	if !strings.Contains(out, "❯ 你好") {
		t.Fatalf("user 行内容字符无损: %q", stripANSI(out))
	}
}

func TestToolRowEdgeAndBgRendered(t *testing.T) {
	// 调用行:左缘竖线 ▍ 前缀(flattenLine 阶段)+ tool-bg 背景
	rows := flushToolRow("⚙ read main.go {\"path\":\"a.go\"}")
	if !strings.HasPrefix(stripANSI(rows[0].text), "▍ ") {
		t.Fatalf("调用行首物理行应前缀 ▍: %q", rows[0].text)
	}
	s := &State{}
	s.Lines = []Line{{Kind: "tool", Text: "⚙ read main.go {}"}}
	rows = mkRows(s.Lines)
	out := renderSessionRow(rows[0], s, 0)
	if !strings.Contains(out, "48;5;235") {
		t.Fatalf("调用行应含 tool-bg: %q", out)
	}
	// 成功结果行背景(折叠摘要行)
	s2 := &State{}
	s2.Lines = []Line{{Kind: "tool", Text: "✓ read: ok", Full: "ok\ncontent"}}
	rows2 := mkRows(s2.Lines)
	out2 := renderSessionRow(rows2[0], s2, 0)
	if !strings.Contains(out2, "48;5;237") {
		t.Fatalf("成功结果应 tool-ok-bg: %q", out2)
	}
	if !strings.Contains(stripANSI(out2), "✓ read: ok ▲") {
		t.Fatalf("结果行折叠标记保持: %q", stripANSI(out2))
	}
}

// flushToolRow 渲染前展平(flattenLine 阶段竖线前缀已写入)。
func flushToolRow(text string) []physRow {
	return flattenLines([]Line{{Kind: "tool", Text: text}}, 100)
}
