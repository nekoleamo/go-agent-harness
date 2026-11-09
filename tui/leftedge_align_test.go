package tui

// 左缘标记的**机械判据**(2026-11-09):把原来挂在人眼清单上的「▍ 竖线不间断 /
// ▲ 与正文错位」这类观感项,收敛成可断言的两条性质 ——
//
//	① 标记**只在首物理行**(续行重复标记会让长消息看起来像多条消息);
//	② 整条行的**预留宽度统一**(续行 2 格缩进):视觉左边界一条线,且任何物理行不超列宽。
//
// 为什么之前没人发现口径漂移:VERIFY 写的是「▍ 竖线不间断」,而实现只给首行挂标记、
// 续行直接贴 0 列 —— 两种说法都不等于当时的行为,而这条判据从来没被自动化跑过。
// 2026-11-09 拍板口径:标记不重复、续行缩进对齐(见 docs/VERIFY.md #42)。

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestLeftEdgeAlignsOnContinuationRows 长内容折行后:首行标记唯一 + 续行 2 格对齐 + 不超列宽。
func TestLeftEdgeAlignsOnContinuationRows(t *testing.T) {
	const colW = 40
	cases := []struct {
		name   string
		kind   string
		prefix string
		state  func(s *State)
		text   string
	}{
		{"user", "user", "❯ ", nil, strings.Repeat("长消息内容", 20)},
		{"thinking", "thinking", "⋯ ", func(s *State) { s.ThinkingFull = true }, strings.Repeat("思考片段", 20)},
		{"tool", "tool", "▍ ", nil, "⚙ shell " + strings.Repeat("x", 120)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &State{}
			s.Lines = []Line{{Kind: c.kind, Text: c.text}}
			if c.state != nil {
				c.state(s)
			}
			rows := flattenViewLines(s, colW)
			if len(rows) < 2 {
				t.Fatalf("colW=%d 下应折成多行(否则这条用例没测到折行), rows=%d", colW, len(rows))
			}
			for i, p := range rows {
				plain := stripANSI(renderSessionRow(p, s, 0))
				if w := lipgloss.Width(plain); w > colW {
					t.Fatalf("第 %d 物理行宽 %d > 列宽 %d(溢出会换行错位): %q", i, w, colW, plain)
				}
				if i == 0 {
					if !strings.HasPrefix(plain, c.prefix) {
						t.Fatalf("首行应带左缘标记 %q: %q", c.prefix, plain)
					}
					continue
				}
				if strings.HasPrefix(plain, c.prefix) {
					t.Fatalf("续行不该重复左缘标记(看起来像另一条消息): %q", plain)
				}
				if !strings.HasPrefix(plain, "  ") {
					t.Fatalf("续行应以 2 格缩进对齐正文左边界: %q", plain)
				}
			}
		})
	}
}

// TestLeftEdgeSingleMarkerPerLine 一条逻辑行只能出现一次标记(含多段文本 si>0 的情况)。
func TestLeftEdgeSingleMarkerPerLine(t *testing.T) {
	s := &State{ThinkingFull: true}
	s.Lines = []Line{{Kind: "thinking", Text: "第一段思考\n第二段思考"}}
	rows := flattenViewLines(s, 60)
	markers := 0
	for _, p := range rows {
		if strings.HasPrefix(stripANSI(p.text), "⋯ ") {
			markers++
		}
	}
	if markers != 1 {
		t.Fatalf("同一条逻辑行只该有一个标记,实际 %d 个", markers)
	}
}
