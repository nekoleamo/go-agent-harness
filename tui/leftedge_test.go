package tui

import (
	"strings"
	"testing"
)

// TestThinkingLeftMarker 思考行要有**左缘标记**(2026-10-20 B4)。
//
// 为什么:thinking 行此前只靠「灰 + 斜体」区分,而**多数终端不渲染斜体**,
// 于是长会话里"这段是模型的思考"与"这段是它给我的答复"分不清 ——
// 而这两者的可信度完全不同(思考是过程,答复是结论)。
// 形状标记(左缘 "⋯ ")不依赖终端能力,任何终端都看得到。
func TestThinkingLeftMarker(t *testing.T) {
	s := &State{}
	s.Lines = []Line{{Kind: "thinking", Text: "思考中"}}
	rows := flattenViewLines(s, 100)
	out := stripANSI(renderSessionRow(rows[0], s, 0))
	if !strings.HasPrefix(out, "⋯ ") {
		t.Fatalf("思考行应带左缘标记: %q", out)
	}
	// 正文行**不加**标记(标记是给"过程"的,正文是默认形态)
	s2 := &State{}
	s2.Lines = []Line{{Kind: "assistant", Text: "答复"}}
	rows2 := flattenViewLines(s2, 100)
	out2 := stripANSI(renderSessionRow(rows2[0], s2, 0))
	if strings.HasPrefix(out2, "⋯ ") {
		t.Fatalf("正文行不该带思考标记: %q", out2)
	}
}

// TestThreeContentKindsHaveDistinctEdges 三类内容各有可辨的左缘(形状区分,非只靠颜色)。
func TestThreeContentKindsHaveDistinctEdges(t *testing.T) {
	s := &State{}
	s.Lines = []Line{
		{Kind: "user", Text: "问题"},
		{Kind: "thinking", Text: "思考"},
		{Kind: "tool", Text: "⚙ shell ls"},
		{Kind: "assistant", Text: "答复"},
	}
	rows := flattenViewLines(s, 100)
	first := map[string]string{}
	for _, p := range rows {
		if p.first {
			first[p.kind] = stripANSI(renderSessionRow(p, s, 0))
		}
	}
	if !strings.HasPrefix(first["user"], "❯ ") {
		t.Fatalf("用户行左缘: %q", first["user"])
	}
	if !strings.HasPrefix(first["thinking"], "⋯ ") {
		t.Fatalf("思考行左缘: %q", first["thinking"])
	}
	if !strings.HasPrefix(first["tool"], "▍ ") {
		t.Fatalf("工具行左缘: %q", first["tool"])
	}
	if strings.HasPrefix(first["assistant"], "❯ ") ||
		strings.HasPrefix(first["assistant"], "⋯ ") ||
		strings.HasPrefix(first["assistant"], "▍ ") {
		t.Fatalf("正文行不该带其它类的左缘: %q", first["assistant"])
	}
}
