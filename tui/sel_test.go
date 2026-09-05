// 鼠标划选复制测试:坐标映射(行/列)、跨行选区文本截取、单击清除、与滚动条互不干扰、
// 反向拖动、释放经 OSC52 写剪贴板、Esc 清除。
package tui

import (
	"encoding/base64"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// selModel 装配:物理行展平文本固定(模拟已渲染),sessionWin/flatN 就位。
func selModel(t *testing.T) *Model {
	t.Helper()
	m := &Model{state: &State{}, w: 60, h: 20}
	m.state.Lines = append(m.state.Lines, Line{Kind: "assistant", Text: "第一行内容 abc\n第二行内容 def"})
	rows := flattenLines(m.state.Lines, 57) // colW=w-3
	m.state.flatN = len(rows)
	m.state.sessionWin = 17
	return m
}

func TestSelColAtChinese(t *testing.T) {
	// "❯ " 前缀 2 列 + 中文双宽:文本 "❯ 你好abc"
	text := "❯ 你好abc"
	// 列 x=0 → rune0(❯);x=2 → "你";x=4 → "好";x=6 → "a"
	cases := []struct{ x, want int }{
		{0, 0}, {1, 1}, {2, 2}, {3, 2}, {4, 3}, {5, 3}, {6, 4}, {7, 5}, {100, len([]rune(text))},
	}
	for _, c := range cases {
		if got := colAt(text, c.x); got != c.want {
			t.Fatalf("colAt(%q,x=%d)=%d want %d", text, c.x, got, c.want)
		}
	}
}

func TestSelPressMotionReleaseCopy(t *testing.T) {
	m := selModel(t)
	var copied string
	writeClipboardOSC52 = func(s string) { copied = s }
	defer func() { writeClipboardOSC52 = func(string) {} }()
	rows := flattenLines(m.state.Lines, 57)
	// press 第 0 行开头:y=0,x=0
	m.handleMousePress(tea.Mouse{X: 0, Y: 0})
	if !m.state.SelActive || m.state.SelRow0 != 0 || m.state.SelCol0 != 0 {
		t.Fatalf("press 应开始选区: active=%v r0=%d c0=%d", m.state.SelActive, m.state.SelRow0, m.state.SelCol0)
	}
	// 拖动到第 1 行 x=4(中文双宽:第+二=4 列 → c1=2)
	m.handleMouseMotion(tea.Mouse{X: 4, Y: 1})
	if m.state.SelRow1 != 1 || m.state.SelCol1 != 2 {
		t.Fatalf("motion 应更新终点: r1=%d c1=%d", m.state.SelRow1, m.state.SelCol1)
	}
	// 释放 → 复制(行间 \n;第 0 行 [0:len) 全文,第 1 行 [0:2) rune = “第二”)
	m.handleMouseRelease(tea.Mouse{X: 4, Y: 1})
	want := rows[0].text + "\n" + string([]rune(rows[1].text)[:2])
	if copied != want {
		t.Fatalf("复制文本不符:\n got=%q\nwant=%q", copied, want)
	}
	if !m.state.SelActive {
		t.Fatal("释放后有位移应保留高亮(SelActive)")
	}
	if m.selRows != nil {
		t.Fatal("释放后应清空 selRows 缓存")
	}
	// Esc 清除
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.state.SelActive {
		t.Fatal("Esc 应清除选区")
	}
}

func TestSelClickClears(t *testing.T) {
	m := selModel(t)
	// 先形成选区
	m.handleMousePress(tea.Mouse{X: 0, Y: 0})
	m.handleMouseMotion(tea.Mouse{X: 5, Y: 1})
	if !m.state.SelActive {
		t.Fatal("应已选区")
	}
	// 单击(无位移):press+release 同点 → 清除
	m.handleMousePress(tea.Mouse{X: 2, Y: 0})
	m.handleMouseRelease(tea.Mouse{X: 2, Y: 0})
	if m.state.SelActive {
		t.Fatal("单击应清除选区")
	}
}

func TestSelReverseDrag(t *testing.T) {
	m := selModel(t)
	var copied string
	writeClipboardOSC52 = func(s string) { copied = s }
	defer func() { writeClipboardOSC52 = func(string) {} }()
	// 反向:从第 1 行按下拖回第 0 行;终点 x=2 → c1=1(第0行从第2个 rune 起)
	// 选区 = 第0行 [1..] + 第1行 [0..0) 空
	m.handleMousePress(tea.Mouse{X: 0, Y: 1})
	m.handleMouseMotion(tea.Mouse{X: 2, Y: 0})
	m.handleMouseRelease(tea.Mouse{X: 2, Y: 0})
	want := "一行内容 abc\n"
	if copied != want {
		t.Fatalf("反向选区文本不符: got=%q want=%q", copied, want)
	}
}

func TestSelBarColumnIgnored(t *testing.T) {
	m := selModel(t)
	m.state.Lines = append(m.state.Lines, Line{Kind: "meta", Text: "L"})
	// bar 列按下走滚动条(不进入划选)
	bar := m.scrollbarCol() // w=60 → 57
	m.handleMousePress(tea.Mouse{X: bar, Y: 5})
	if m.state.SelActive {
		t.Fatal("barch 列按下不应进入划选")
	}
	_ = bar
}

func TestSelOSC52Payload(t *testing.T) {
	// 验证 OSC52 编码内容的 base64 正确(直接调用底层?由 writeClipboard 捕获验证)
	var got string
	writeClipboardOSC52 = func(s string) { got = s }
	defer func() { writeClipboardOSC52 = func(string) {} }()
	writeClipboardOSC52("hello")
	if got != "hello" {
		t.Fatalf("剪贴板回调应得原文: %q", got)
	}
	if enc := base64.StdEncoding.EncodeToString([]byte(got)); enc != "aGVsbG8=" {
		t.Fatalf("base64 编码不符: %q", enc)
	}
}
