// P5 V6 测试:回合耗时计时与状态栏展示、Ctrl+O 折叠切换、消息跳转定位。
package tui

import (
	"strings"
	"testing"
	"time"
)

// TestTurnDurationTiming 回合耗时:running 进入计时(idle 结算);running 幂等;idle 无起点不更新。
func TestTurnDurationTiming(t *testing.T) {
	s := &State{}
	s.ApplyStatus("running")
	s.ApplyStatus("running") // 重复 running 不重置起点
	time.Sleep(5 * time.Millisecond)
	s.ApplyStatus("idle")
	if s.turnDur < 5*time.Millisecond {
		t.Fatalf("turnDur 应记录耗时: %v", s.turnDur)
	}
	// idle 后起点清零:再次 idle 不覆盖
	prev := s.turnDur
	s.ApplyStatus("idle")
	if s.turnDur != prev {
		t.Fatalf("无起点的 idle 不应更新 turnDur: %v → %v", prev, s.turnDur)
	}
	// 新回合重新计时(睡足 20ms 量化断言)
	s.ApplyStatus("running")
	time.Sleep(20 * time.Millisecond)
	s.ApplyStatus("idle")
	if s.turnDur < 20*time.Millisecond {
		t.Fatalf("新回合应更新耗时: %v", s.turnDur)
	}
}

// TestStatusLineTurnDur 状态栏空闲态显示"上一回合";运行态不显示。
func TestStatusLineTurnDur(t *testing.T) {
	s := &State{turnDur: 12*time.Second + 300*time.Millisecond}
	s.Running = true
	out := renderStatusLine(s, 80)
	if strings.Contains(stripColor(out), "上一回合") {
		t.Fatalf("运行态不应显示耗时: %q", stripColor(out))
	}
	s.Running = false
	out = renderStatusLine(s, 80)
	if !strings.Contains(stripColor(out), "上一回合 12s") {
		t.Fatalf("空闲态应显示耗时: %q", stripColor(out))
	}
	// 无耗时不显示
	s2 := &State{}
	if out := renderStatusLine(s2, 80); strings.Contains(stripColor(out), "上一回合") {
		t.Fatalf("无耗时不显示: %q", stripColor(out))
	}
}

// TestFmtDur 耗时短格式:>=10s 整数秒,<10s 一位小数。
func TestFmtDur(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{1200 * time.Millisecond, "1.2s"},
		{10*time.Second + 400*time.Millisecond, "10s"},
		{90 * time.Second, "90s"},
	}
	for _, c := range cases {
		if got := fmtDur(c.d); got != c.want {
			t.Fatalf("fmtDur(%v) = %q,want %q", c.d, got, c.want)
		}
	}
}

// TestToggleLastFold Ctrl+O:对最近可折叠结果行切换;无则 no-op;切换后回底。
func TestToggleLastFold(t *testing.T) {
	m := &Model{state: &State{}}
	m.state.Lines = []Line{
		{Kind: "user", Text: "问题"},
		{Kind: "assistant", Text: "回复"},
		{Kind: "tool", Text: "⚙ read a.go {}"},
		{Kind: "tool", Text: "✓ read: 短结果", Full: "内容..."},
		{Kind: "tool", Text: "✓ read: 长结果", Full: "长内容..."},
	}
	m.toggleLastFold()
	if !m.state.FoldOpen[4] {
		t.Fatalf("应展开最近结果行(下标 4)")
	}
	if m.state.FoldOpen[3] {
		t.Fatalf("不应展开更早结果行")
	}
	m.toggleLastFold()
	if m.state.FoldOpen[4] {
		t.Fatalf("再按应收起")
	}
	// 无可折叠行:no-op 不 panic
	m2 := &Model{state: &State{Lines: []Line{{Kind: "user", Text: "x"}}}}
	m2.toggleLastFold()
	// 切换后回底
	m.toggleLastFold() // 展开
	if m.state.ScrollOffset != 0 {
		t.Fatalf("切换后应回底: %d", m.state.ScrollOffset)
	}
}

// TestJumpToFirstUser Ctrl+↑:滚动窗口顶端对齐最早 user 行;无 user 行 no-op。
func TestJumpToFirstUser(t *testing.T) {
	m := &Model{state: &State{}, w: 80}
	m.state.Lines = []Line{
		{Kind: "meta", Text: "—— 轮次结束 ——"},
		{Kind: "user", Text: "第一问"},
		{Kind: "assistant", Text: "第一答"},
		{Kind: "user", Text: "第二问"},
	}
	m.state.ScrollOffset = 99
	m.jumpToFirstUser()
	if m.state.ScrollOffset != 1 { // 前置 1 meta 物理行:窗口顶端对齐 user 行
		t.Fatalf("首条 user 前 1 meta 行,offset 应 1: %d", m.state.ScrollOffset)
	}
	// 首条 user 前有长内容:offset = 前置物理行数(与 jumpToFirstUser 同宽计算)
	m2 := &Model{state: &State{}, w: 100}
	m2.state.Lines = []Line{
		{Kind: "assistant", Text: strings.Repeat("长回复 ", 40)}, // 多物理行
		{Kind: "user", Text: "问"},
	}
	rows := flattenLines(m2.state.Lines[:1], 97)
	m2.jumpToFirstUser()
	if m2.state.ScrollOffset != len(rows) {
		t.Fatalf("offset 应为首行前置物理行数 %d: %d", len(rows), m2.state.ScrollOffset)
	}
	// 无 user:no-op
	m3 := &Model{state: &State{Lines: []Line{{Kind: "assistant", Text: "a"}}}}
	m3.jumpToFirstUser()
	if m3.state.ScrollOffset != 0 {
		t.Fatal("无 user 行不应滚动")
	}
}
