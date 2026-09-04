// 渲染:状态 → 终端文本(lipgloss 着色)。纯函数,可单测。
package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

var (
	styleUser   = lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)
	styleAsst   = lipgloss.NewStyle().Foreground(lipgloss.Color("120"))
	styleTool   = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	styleMeta   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleError  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	stylePrompt = lipgloss.NewStyle().Foreground(lipgloss.Color("207")).Bold(true)
	styleStatus = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	stylePick   = lipgloss.NewStyle().Foreground(lipgloss.Color("207")).Bold(true) // 选择器高亮行
)

// Render 渲染整屏。mainH = 会话流区域高度;底部含输入行 + 命令提示区(动态) + 状态栏。
// 提示区最多 maxHintRows 行(超限截断),避免挤压会话流。
const maxHintRows = 6

func Render(s *State, width, height int) string {
	if width <= 0 || height <= 0 {
		width, height = 80, 24
	}
	// 选择器激活时提示区 = 选项列表(高亮当前);否则静态提示行
	var hintItems []string
	if s.Pick != nil {
		for i, it := range s.Pick.Items {
			line := " /" + it.Value + " " + it.Desc
			if i == s.Pick.Cursor {
				hintItems = append(hintItems, stylePick.Render("▸"+line))
			} else {
				hintItems = append(hintItems, styleMeta.Render(line))
			}
		}
	} else {
		hintItems = append(hintItems, s.Suggestions...)
	}
	hintRows := len(hintItems)
	if hintRows > maxHintRows {
		hintRows = maxHintRows
	}
	mainH := height - 3 - hintRows
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

	// 命令提示区(输入 / 前缀时显示;选择器激活时高亮当前项)
	var hints []string
	for i := 0; i < hintRows; i++ {
		hints = append(hints, hintItems[i])
	}
	if len(hintItems) > maxHintRows {
		hints = append(hints, styleMeta.Render("…"))
	}

	// 状态栏
	state := "空闲"
	if s.Running {
		state = "运行中…"
	}
	status := styleStatus.Render(fmt.Sprintf(
		" gah | %s | %s | 模型: %s | 沙箱: %s%s",
		s.Profile, state, orDefault(s.Model, "未设置"), orDefault(s.Sandbox, string(sdk.SandboxWorkspace)), strings.Repeat(" ", width),
	))

	bottom := []string{input}
	bottom = append(bottom, hints...)
	bottom = append(bottom, status)
	return lipgloss.JoinVertical(lipgloss.Left, main, strings.Join(bottom, "\n"))
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
