// 渲染几何不变量:每行显示宽度 ≤ width、总行数 ≤ height(帧不许比屏幕宽/高)。
//
// 由来(2026-09-22 用户实测):会话内容多了之后输入框/状态栏被挤出屏幕,上滑才逐渐露出来
// —— 帧比屏幕高的典型症状。任何一处按 rune 数算宽的行(全角字算 1 列)都会让终端多折一行,
// 折出来的行不被几何记账 ⇒ 越积越高,最后把底部两块顶出屏幕。
package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// checkFrame 断言一帧的几何:不超宽、不超高,且底部输入行/状态栏仍在屏内。
func checkFrame(t *testing.T, name, out string, w, h int) {
	t.Helper()
	lines := strings.Split(out, "\n")
	if len(lines) > h {
		t.Fatalf("%s: 帧高 %d 超屏高 %d(输入区/状态栏会被顶出屏幕)", name, len(lines), h)
	}
	for i, ln := range lines {
		if lw := lipgloss.Width(ln); lw > w {
			t.Fatalf("%s: 第 %d 行宽 %d 超屏宽 %d(终端会折行 ⇒ 帧变高): %q", name, i, lw, w, ln)
		}
	}
	if !strings.Contains(out, "❯") || !strings.Contains(out, "工作区:") {
		t.Fatalf("%s: 输入行/状态栏不在渲染结果里", name)
	}
}

// 长会话 + 全角 widget 行 + 长输入:三种"按 rune 算会漏算"的行同时在场。
func TestRenderGeometryInvariant(t *testing.T) {
	s := &State{
		WidgetOn: true,
		Widgets: []Widget{{ID: "w", Text: func() string {
			return strings.Repeat("中文 widget 状态行", 12) // 远超 widgetMaxCols 且全角
		}}},
		Thinking: "high",
	}
	for i := 0; i < 120; i++ {
		s.Lines = append(s.Lines, Line{Kind: "assistant", Text: fmt.Sprintf("第 %d 行:%s", i, strings.Repeat("全角内容", 30))})
	}
	s.Lines = append(s.Lines, Line{Kind: "user", Text: strings.Repeat("用户消息很长", 40)})
	s.Input = strings.Repeat("输入很长", 30)

	for _, tc := range []struct{ w, h int }{{80, 24}, {100, 30}, {120, 40}, {60, 12}, {80, 8}} {
		label := fmt.Sprintf("%dx%d", tc.w, tc.h)
		out := Render(s, tc.w, tc.h)
		checkFrame(t, label, out, tc.w, tc.h)
	}
}

// 无 widget/无内容/有错误横幅/开着坞面板:几何同样不得越界(横幅要按折行记账)。
func TestRenderGeometryInvariantEdges(t *testing.T) {
	base := func() *State { return &State{Thinking: "off"} }
	// 空会话
	if out := Render(base(), 80, 24); out == "" {
		t.Fatal("空会话也应有输入区/状态栏")
	} else {
		checkFrame(t, "空会话", out, 80, 24)
	}
	// 超长错误横幅(按列折行,行数必须计入主区扣除)
	s := base()
	s.Error = strings.Repeat("错误信息很长", 60)
	out := Render(s, 80, 24)
	checkFrame(t, "长横幅", out, 80, 24)
	// 坞展开面板
	s2 := base()
	s2.DockOpen = true
	out2 := Render(s2, 80, 24)
	checkFrame(t, "坞展开", out2, 80, 24)
}

// clampFrame 直测:超宽行硬截、超高帧从顶部裁(底部保留)。
func TestClampFrame(t *testing.T) {
	wide := strings.Join([]string{strings.Repeat("宽", 100), "❯ 尾行", "工作区: x"}, "\n")
	got := clampFrame(wide, 20, 10)
	for i, ln := range strings.Split(got, "\n") {
		if w := lipgloss.Width(ln); w > 20 {
			t.Fatalf("第 %d 行仍超宽 %d: %q", i, w, ln)
		}
	}
	tall := strings.Join([]string{"1", "2", "3", "4", "5", "❯ 输入", "工作区: x"}, "\n")
	got2 := strings.Split(clampFrame(tall, 40, 3), "\n")
	if len(got2) != 3 || got2[len(got2)-1] != "工作区: x" {
		t.Fatalf("超高帧应从顶部裁、底部保留: %q", got2)
	}
}
