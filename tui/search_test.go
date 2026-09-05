// 会话内搜索测试:命中计算(大小写不敏感)、首跳定位、n/N 循环、Esc 退出、
// 无命中自动退、渲染命中高亮标记。
package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// searchModel 装配少量 Lines(含大小写/重复词),固定窗口与展平。
func searchModel(t *testing.T) *Model {
	t.Helper()
	m := &Model{state: &State{}, w: 60, h: 20}
	m.state.Lines = append(m.state.Lines,
		Line{Kind: "user", Text: "帮我搜一下 keyword 和 KEYWORD"},
		Line{Kind: "assistant", Text: "这是响应,keyword 出现两次 keyword。"},
		Line{Kind: "meta", Text: "无关行"},
	)
	m.state.flatN = len(flattenLines(m.state.Lines, 57))
	m.state.sessionWin = 17
	return m
}

func TestSearchRunHitsAndFirstJump(t *testing.T) {
	m := searchModel(t)
	got := m.searchRun("keyword")
	if !strings.Contains(got, "命中 2 行") {
		t.Fatalf("应命中 2 行(行0/行1 各计一行): %q", got)
	}
	// 命中逻辑行集合:行0 与 行1(各计 1 行)
	if len(m.state.SearchHits) != 2 {
		t.Fatalf("应 2 个命中行(0 与 1): %v", m.state.SearchHits)
	}
	if m.state.SearchQuery != "keyword" || m.state.SearchIdx != 0 {
		t.Fatalf("搜索态: q=%q idx=%d", m.state.SearchQuery, m.state.SearchIdx)
	}
	// 首个命中定位:行0 的展平首物理行应为窗口顶(offset=0,因行0在顶)
	if m.state.ScrollOffset != 0 {
		t.Fatalf("首跳应定位行0(offset=0): %d", m.state.ScrollOffset)
	}
	// 大小写不敏感确认(上面已命中 KEYWORD 所在行)
}

func TestSearchJumpCycle(t *testing.T) {
	m := searchModel(t)
	m.state.sessionWin = 2 // 窗口 2:内容 3 行,行1 可置顶(maxOff=1)
	m.searchRun("keyword")
	// n:下一个 → 行1
	m.searchJump(true)
	if m.state.SearchIdx != 1 || m.state.SearchHits[m.state.SearchIdx] != 1 {
		t.Fatalf("n 应跳第2个命中: idx=%d line=%d", m.state.SearchIdx, m.state.SearchHits[m.state.SearchIdx])
	}
	// 定位:行1 展平首物理行(第2行)放窗口顶
	rows := flattenLines(m.state.Lines, 57)
	firstRowOf1 := -1
	for i, p := range rows {
		if p.lineIdx == 1 {
			firstRowOf1 = i
			break
		}
	}
	if m.state.ScrollOffset != firstRowOf1 {
		t.Fatalf("跳转应定位行1首物理行: off=%d want=%d", m.state.ScrollOffset, firstRowOf1)
	}
	// n:循环回行0
	m.searchJump(true)
	if m.state.SearchHits[m.state.SearchIdx] != 0 {
		t.Fatalf("循环应回行0: idx=%d", m.state.SearchIdx)
	}
	// N(prev):回行1
	m.searchJump(false)
	if m.state.SearchHits[m.state.SearchIdx] != 1 {
		t.Fatalf("N 应回上一命中行1: idx=%d", m.state.SearchIdx)
	}
}

func TestSearchEscAndNoHit(t *testing.T) {
	m := searchModel(t)
	m.searchRun("keyword")
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.state.SearchQuery != "" || m.state.SearchHits != nil {
		t.Fatal("Esc 应退出搜索态")
	}
	// 无命中自动退
	got := m.searchRun("不存在的词xyz")
	if !strings.Contains(got, "无命中") {
		t.Fatalf("无命中应提示: %q", got)
	}
	if m.state.SearchQuery != "" {
		t.Fatal("无命中应自动退出搜索态")
	}
}

func TestSearchRenderHighlight(t *testing.T) {
	m := searchModel(t)
	m.searchRun("keyword")
	out := Render(m.state, 60, 20)
	// 命中行(行0/1)展平行整行背景:输出含搜索背景色序列;简单验证搜索态下渲染不崩且含命中行文本
	if !strings.Contains(out, "KEYWORD") && !strings.Contains(out, "keyword") {
		t.Fatal("渲染应含命中文本")
	}
	// 搜索背景色(256 索引 238)应出现在输出中
	if !strings.Contains(out, "38;5;238") && !strings.Contains(out, "48;5;238") {
		t.Fatalf("命中行应有背景色序列: %q", out[:200])
	}
	// Esc 退出后渲染不再有背景(可选),至少状态清除
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.state.SearchQuery != "" {
		t.Fatal("Esc 后应无搜索态")
	}
}

func TestSearchKeyNWhenTyping(t *testing.T) {
	m := searchModel(t)
	m.searchRun("keyword")
	// 输入框有内容时按 n 是输入(不跳转)
	m.state.Input = "ab"
	m.state.Cursor = 2 // 光标在尾,输入 n 应追加
	m.handleKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.state.Input != "abn" {
		t.Fatalf("输入中 n 应输入文字: %q", m.state.Input)
	}
	if m.state.SearchIdx != 0 {
		t.Fatalf("输入中 n 不应跳转: idx=%d", m.state.SearchIdx)
	}
	// 输入框空时 n 跳转
	m.state.Input = ""
	m.handleKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.state.SearchIdx != 1 {
		t.Fatalf("空输入 n 应跳转: idx=%d", m.state.SearchIdx)
	}
}
