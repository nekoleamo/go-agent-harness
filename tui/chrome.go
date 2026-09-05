// 界面装饰层(S2.2 组件化):输入行 / 命令提示行 / 状态栏的文本构建。
// 从 render.go 抽出的无状态(仅读 s)纯函数,便于逐段单测;Render 按序拼装。
package tui

import (
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// renderInputLine 输入区:块光标插入在光标处(前后分半);双按退出武装提示。
// 多行输入(P4-6):文本按 \n 逐行渲染,续行前缀与首行 "❯ " 对齐(等宽空格);
// 光标所在行插入块光标,其余行原样。单行行为与历史一致(逐字等价)。
func renderInputLine(s *State, _ int) string {
	line, col := cursorLineCol([]rune(s.Input), s.Cursor)
	var sb strings.Builder
	for i, ln := range strings.Split(s.Input, "\n") {
		if i > 0 {
			sb.WriteByte('\n')
		}
		if i == 0 {
			sb.WriteString(stylePrompt.Render("❯ "))
		} else {
			sb.WriteString("  ") // 续行对齐(❯ 为 1 列宽 + 空格)
		}
		r := []rune(ln)
		if i == line {
			c := col
			if c < 0 {
				c = 0
			}
			if c > len(r) {
				c = len(r)
			}
			sb.WriteString(string(r[:c]))
			sb.WriteString(styleCursor.Render("█"))
			sb.WriteString(string(r[c:]))
		} else {
			sb.WriteString(ln)
		}
	}
	// 双按退出武装提示(防误触):第一次 Ctrl+C(输入为空)后高亮提醒再按一次才彻底退出
	if s.QuitArmed {
		sb.WriteString(" " + styleBusy.Render("⚠ 再按一次 Ctrl+C 彻底退出 (2s)"))
	}
	return sb.String()
}

// renderHintLines 命令提示区(输入 / 前缀时显示;选择器激活时高亮当前项)。
// hintItems 已含高亮样式(由 Render 依 Pick 状态构建),此处截取前 hintRows 行。
func renderHintLines(_ *State, hintItems []string, hintRows int) []string {
	var hints []string
	for i := 0; i < hintRows; i++ {
		hints = append(hints, hintItems[i])
	}
	if len(hintItems) > maxHintRows {
		hints = append(hints, styleMeta.Render("…"))
	}
	return hints
}

// renderStatusLine 状态栏:gah 标识 + 回合状态(思考/执行工具)+ profile + 工作区 +
// 模型/思维 + 沙箱 + 会话 + 上下文使用率/缓存命中率。宽度填充防行尾锯齿。
func renderStatusLine(s *State, width int) string {
	// 回合运行中前置像素循环 logo(旋转帧)高亮显示“思考中/执行工具”,
	// 提交回车即置 Running → 立即可见(不依赖事件广播时序);空闲灰字。
	state := "空闲"
	runningStyle := styleStatus
	if s.Running {
		frame := spinnerFrame(s.SpinnerIdx)
		if s.LastTool != "" {
			state = frame + " 执行工具: " + s.LastTool
		} else {
			state = frame + " 思考中"
		}
		state += " (Esc 取消)"
		runningStyle = styleBusy // 运行态高亮(醒目,一眼看到当前状态)
	}
	state = runningStyle.Render(state)
	// P4-1 消息队列:有待发消息时显示计数与取回键(空闲时亦提示,队列由回合结束/取消后保留)
	if n := len(s.Queue); n > 0 {
		state += " " + styleBusy.Render(fmt.Sprintf("· 待发 %d (Alt+Up 取回)", n))
	}
	sess := ""
	if s.Session != "" {
		sess = " | 会话: " + s.Session
	}
	// 上下文使用率 / 缓存命中率(host-usage-stats 统计;无请求时显示 -)。
	// 窗口已知(>0):显示 使用量/总量 与百分比;窗口未知(0,未知/空模型):只显示使用量,
	// 不显示总量与百分比(不假精确)。缓存命中率与窗口无关,有命中即显示。
	stats := " | 上下文 -"
	if s.Stats.Requests > 0 {
		used := s.Stats.PromptTokens
		if w := s.Stats.Window; w > 0 {
			stats = fmt.Sprintf(" | 上下文 %s/%s (%d%%) ", fmtK(used), fmtK(w), used*100/w)
		} else {
			stats = fmt.Sprintf(" | 上下文 %s ", fmtK(used)) // 窗口未知:仅使用量
		}
		if s.Stats.CachedTokens > 0 {
			stats += fmt.Sprintf("缓存 %d%%", s.Stats.CachedTokens*100/s.Stats.PromptTokens)
		}
	}
	think := ""
	if s.Thinking != "" && s.Thinking != "off" {
		think = " | 思维: " + s.Thinking
	}
	model := orDefault(s.Model, "未设置")
	if s.Model != "" && s.ModelSrc != "" {
		model += "(" + s.ModelSrc + ")" // 来源标注:同名模型跨 provider 可辨
	}
	return styleStatus.Render(fmt.Sprintf(
		" gah | %s | %s | 工作区: %s | 模型: %s%s | 沙箱: %s%s%s%s",
		state, s.Profile, orDefault(s.Workspace, "?"), model, think, orDefault(s.Sandbox, string(sdk.SandboxWorkspace)), sess, stats, strings.Repeat(" ", width),
	))
}
