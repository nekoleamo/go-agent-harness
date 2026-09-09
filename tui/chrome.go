// 界面装饰层(S2.2 组件化):输入行 / 命令提示行 / 状态栏的文本构建。
// 从 render.go 抽出的无状态(仅读 s)纯函数,便于逐段单测;Render 按序拼装。
package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// renderInputRight 输入行右侧挂载信息(M15 pi 式):模型(含来源)· 思维 · 上下文/缓存。
// 运行指标从状态栏移到输入行右侧(pi 布局:底部右侧挂模型与用量),输入留白时可见。
func renderInputRight(s *State) string {
	var parts []string
	if s.Model != "" {
		m := s.Model
		if s.ModelSrc != "" {
			m += "(" + s.ModelSrc + ")"
		}
		parts = append(parts, "模型: "+m)
	}
	if s.Thinking != "" && s.Thinking != "off" {
		parts = append(parts, "思维: "+s.Thinking)
	}
	stats := "上下文 -"
	if s.Stats.Requests > 0 {
		used := s.Stats.PromptTokens
		if w := s.Stats.Window; w > 0 {
			stats = fmt.Sprintf("上下文 %s/%s (%d%%)", fmtK(used), fmtK(w), used*100/w)
		} else {
			stats = fmt.Sprintf("上下文 %s", fmtK(used)) // 窗口未知:仅使用量(不假精确)
		}
		if s.Stats.CachedTokens > 0 {
			stats += fmt.Sprintf(" 缓存 %d%%", s.Stats.CachedTokens*100/s.Stats.PromptTokens)
		}
	}
	parts = append(parts, stats)
	return strings.Join(parts, " · ")
}

// fmtDur 耗时短格式:>=10s 整数秒,<10s 一位小数(状态栏展示)。
func fmtDur(d time.Duration) string {
	secs := d.Seconds()
	if secs >= 10 {
		return fmt.Sprintf("%.0fs", secs)
	}
	return fmt.Sprintf("%.1fs", secs)
}

// thinkEdgeColor 输入左缘竖线色:按思考等级映射(P5;对齐 pi thinkingOff/low/medium/high 边框色)。
func thinkEdgeColor(level string) color.Color {
	switch level {
	case "low":
		return fg(TokThinkLow)
	case "medium":
		return fg(TokThinkMed)
	case "high":
		return fg(TokThinkHigh)
	default:
		return fg(TokThinkOff)
	}
}

// renderMetricLine 指标行(M15 修正:F15.1):模型/思维/上下文独立一行置于输入区上方,
// 固定显示不随输入移动(输入多长都不挤压,信息完整不省略)。
func renderMetricLine(s *State) string {
	// 前导空格与状态栏左缘对齐(F15.4:倒数两行左对齐,模型前不顶格)
	return styleStatus.Render(" " + renderInputRight(s))
}

// renderInputLine 输入区**框内内容**(圆角矩形框由 renderInputFrame 外包):
// 提示符 + 块光标插入在光标处(前后分半);双按退出武装提示。
// 首行提示符 "❯ " 与续行缩进统一 2 列对齐;思考等级语义移至框边框色(renderInputFrame)。
// 超长输入(P4-6 多行 + 长行):长行按列宽折行(复用 wrapSegment,双宽字符不跨行,
// 不再横向截断);物理行数超过 maxRows(>0,由 render.go 按终端高度给)时以光标为锚
// 滚动窗口(复用 pickWindow 语义,光标触底/触顶滚),窗口上方省略指示行。
// 短行/无 maxRows 时输出与历史逐行等价(前缀对齐、光标行插块)。
// 光标块始终在光标物理行可见(跟随滚动),↑/↓/Home/End 移动即可查看并删改全文。
// 模型/上下文指标不在本行(M15 曾挂首行右缘,输入长时挤压——F15.1 拆独立指标行)。
func renderInputLine(s *State, width int, maxRows ...int) string {
	maxR := 0
	if len(maxRows) > 0 {
		maxR = maxRows[0]
	}
	// 文本列宽:框内内容区(左右边框 4 列)再减前缀 2 列(❯ / 续行缩进)
	w := width - 4 - 2
	if w < 1 {
		w = 1
	}
	phys, curPhys, curCol := inputPhys(s.Input, s.Cursor, w)
	ws, we := 0, len(phys)
	if maxR > 0 && len(phys) > maxR {
		ws, we = pickWindow(len(phys), curPhys, maxR)
	}
	var sb strings.Builder
	if ws > 0 { // 窗口上方仍有内容:省略指示行(前缀对齐)
		sb.WriteString("  " + styleMeta.Render(fmt.Sprintf("…↑ %d 行在窗口上方(↑/↓ 滚动)", ws)))
	}
	for i := ws; i < we; i++ {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		if i > 0 { // 续行缩进(与提示符 ❯ 同 2 列宽;窗口滚动后窗口首行非逻辑首行,同样续行对齐)
			sb.WriteString("  ")
		} else {
			sb.WriteString(stylePrompt.Render("❯ "))
		}
		r := []rune(phys[i])
		if i == curPhys {
			c := curCol
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
			sb.WriteString(phys[i])
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

// approvalLabel 审批档位中文标签(M17 三档,对齐 Web 设置面板;未知值回退智能档)。
func approvalLabel(mode string) string {
	switch mode {
	case "open":
		return "开放"
	case "strict":
		return "严格"
	default:
		return "智能"
	}
}

// renderStatusLine 状态栏(M15 pi 式精简 + F15.3 去 gah 标识):回合状态(思考/执行工具,
// 前置滚动动画帧)+ 工作区 + 沙箱 + 审批 + 会话;模型/思维/上下文在末行指标行。
// 宽度填充防行尾锯齿。
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
	// P5 回合耗时:空闲态展示上次回合用时(运行态不显示,状态位已表达)
	if !s.Running && s.turnDur > 0 {
		state += " " + styleStatus.Render("· 上一回合 "+fmtDur(s.turnDur))
	}
	sess := ""
	if s.Session != "" {
		sess = " | 会话: " + s.Session
	}
	// 审批档位(M17):开放/智能/严格,空值省略段(与会话段一致)。
	apv := ""
	if s.Approval != "" {
		apv = " | 审批: " + approvalLabel(s.Approval)
	}
	// 模型/思维/上下文指标在输入行右侧(renderInputRight),状态栏不再重复。
	return styleStatus.Render(fmt.Sprintf(
		" %s | 工作区: %s | 沙箱: %s%s%s%s",
		state, orDefault(s.Workspace, "?"), orDefault(s.Sandbox, string(sdk.SandboxWorkspace)), apv, sess, strings.Repeat(" ", width),
	))
}
