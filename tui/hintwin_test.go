// 提示/选择列表滚动窗口测试:超窗选项随光标滚动,不再省略不可达。
package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestPickWindow(t *testing.T) {
	cases := []struct {
		n, cur, vis, ws, we int
	}{
		{0, 0, 6, 0, 0}, {3, 1, 6, 0, 3}, // 不足窗口:全量
		{10, 0, 6, 0, 6}, {10, 5, 6, 0, 6}, // 顶部对齐,光标贴底仍前段
		{10, 6, 6, 1, 7}, {10, 7, 6, 2, 8}, // 触底后随光标下滚
		{10, 9, 6, 4, 10},                     // 到底:尾部对齐
		{6, 5, 6, 0, 6},                       // 恰好等窗
		{10, -1, 6, 0, 6}, {10, 99, 6, 4, 10}, // 越界钳制
		{10, 3, 0, 0, 0}, // 零可见:空窗口
	}
	for _, c := range cases {
		ws, we := pickWindow(c.n, c.cur, c.vis)
		if ws != c.ws || we != c.we {
			t.Errorf("pickWindow(%d,%d,%d) = (%d,%d),want (%d,%d)", c.n, c.cur, c.vis, ws, we, c.ws, c.we)
		}
	}
}

// TestRenderPickWindowScrolls 选择器选项超窗:渲染窗口含当前光标项、高亮唯一(▸)、
// 窗口 ≤ maxHintRows;光标在尾部时窗口尾对齐(旧截断使高亮项不可见)。
func TestRenderPickWindowScrolls(t *testing.T) {
	items := make([]sdk.Option, 20)
	for i := range items {
		items[i] = sdk.Option{Value: "opt" + fmt.Sprint(i), Desc: "描述" + fmt.Sprint(i)}
	}
	s := &State{Pick: &Pick{Items: items, Cursor: 18}}
	out := Render(s, 80, 24)
	if !strings.Contains(out, "▸ /opt18") {
		t.Fatalf("高亮项 opt18 应在窗口内")
	}
	if strings.Contains(out, "▸ /opt0") {
		t.Fatalf("窗口尾对齐不应含头部高亮")
	}
	if strings.Count(out, "▸") != 1 {
		t.Fatalf("高亮项应唯一")
	}
	for _, v := range []string{"/opt13", "/opt15", "/opt17", "/opt18"} {
		if !strings.Contains(out, v) {
			t.Fatalf("尾部窗口应含 %s", v)
		}
	}
	if strings.Contains(out, "/opt19") {
		t.Fatalf("窗口 6 行不应含 opt19")
	}
	// 顶部光标:窗口顶对齐(前段可见)
	s.Pick.Cursor = 1
	out = Render(s, 80, 24)
	if !strings.Contains(out, "▸ /opt1") || !strings.Contains(out, "/opt5") {
		t.Fatalf("顶部窗口应前段可见")
	}
}

// TestRenderSuggestionsOverflow 静态命令提示(/)超窗:截前段 + 末行余量提示,不再静默丢弃。
func TestRenderSuggestionsOverflow(t *testing.T) {
	s := &State{}
	for i := 0; i < 10; i++ {
		s.Suggestions = append(s.Suggestions, "/cmd"+fmt.Sprint(i))
	}
	out := Render(s, 80, 24)
	if !strings.Contains(out, "还有 5 项") {
		t.Fatalf("应提示余量 5 项")
	}
	if !strings.Contains(out, "/cmd0") || strings.Contains(out, "/cmd9") {
		t.Fatalf("应截前段不含末项")
	}
}
