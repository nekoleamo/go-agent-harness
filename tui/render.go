// 渲染:状态 → 终端文本(lipgloss 着色)。纯函数,可单测。
package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

var (
	styleUser   = lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)
	styleAsst   = lipgloss.NewStyle().Foreground(lipgloss.Color("120"))
	styleTool   = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	styleMeta   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleError  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	stylePrompt = lipgloss.NewStyle().Foreground(lipgloss.Color("207")).Bold(true)
	styleStatus = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
)

// Render 渲染整屏。mainH = 会话流区域高度,input+status 占 3 行。
func Render(s *State, width, height int) string {
	if width <= 0 || height <= 0 {
		width, height = 80, 24
	}
	mainH := height - 3
	if mainH < 1 {
		mainH = 1
	}
	var lines []string
	// 错误横幅(若有)
	if s.Error != "" {
		lines = append(lines, styleError.Render("⛔ "+s.Error))
	}
	// 会话流(滚动窗口)
	for _, l := range s.visible(mainH - len(lines)) {
		lines = append(lines, renderLine(l))
	}
	main := strings.Join(lines, "\n")

	// 输入区
	input := stylePrompt.Render("❯ ") + s.Input
	if s.Cursor >= 0 {
		cursor := s.Cursor
		if cursor > len(s.Input) {
			cursor = len(s.Input)
		}
		// 光标指示:置于输入末尾之后渲染块光标的简化形式
		input += styleAsst.Render("█")
		_ = cursor
	}

	// 状态栏
	state := "空闲"
	if s.Running {
		state = "运行中…"
	}
	status := styleStatus.Render(fmt.Sprintf(
		" gah | %s | %s | 模型: %s | %s%s",
		s.Profile, state, orDefault(s.Model, "未设置"), s.LastTool, strings.Repeat(" ", width),
	))

	return lipgloss.JoinVertical(lipgloss.Left, main, input, status)
}

func renderLine(l Line) string {
	prefix := "· "
	switch l.Kind {
	case "user":
		return styleUser.Render("❯ " + l.Text)
	case "assistant":
		prefix = ""
		return styleAsst.Render(prefix + l.Text)
	case "tool":
		return styleTool.Render(l.Text)
	case "meta":
		return styleMeta.Render(l.Text)
	case "error":
		return styleError.Render(l.Text)
	default:
		return l.Text
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
