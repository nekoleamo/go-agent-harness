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
		} else if s.Answering { // S-P0-2:作答态提示符换形(单列宽:❓ 与 ❯ 同为 2 列),输入归属一眼可辨
			sb.WriteString(styleBusy.Render("❓ "))
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

// —— S-P2-4 可配置状态栏(/statusline) ——
//
// 设计:F15.3 基线（回合态集群 + 工作区/沙箱/审批/会话 四段）不变为**默认**;
// 用户可改集合与顺序。渲染拆成「逐项取值 + 按配置拼接」：
//   - 回合态集群(state/queue/questions/dock/last)组内以 " · " 连接(同一信息块);
//   - 其余段(workspace/sandbox/approval/session)以 " | " 连接(并列分区)。
// 空值项一律不渲染(不留悬空分隔符)。

// statuslineCluster 回合态集群:组内 " · "、与分区段 " | "。
var statuslineCluster = map[string]bool{
	"state": true, "queue": true, "questions": true, "dock": true, "last": true,
}

// defaultStatusline 基线默认项顺序(F15.3;不配置时逐字符等价旧输出)。
var defaultStatusline = []string{"state", "queue", "questions", "dock", "last", "workspace", "sandbox", "approval", "session"}

// statuslineTokens 全部合法项(顺序无关;/statusline 错误提示与校验用)。
var statuslineTokens = []string{"state", "queue", "questions", "dock", "last", "workspace", "sandbox", "approval", "session"}

// statuslineTokenDesc 项说明(/statusline 无参与错误提示用)。
var statuslineTokenDesc = map[string]string{
	"state":     "回合状态(思考中/执行工具;运行中带 Esc 提示)",
	"queue":     "待发消息计数(P4-1)",
	"questions": "待答提问计数(S-P0-2)",
	"dock":      "后台任务/子代理坞(S-P0-3)",
	"last":      "上一回合耗时",
	"workspace": "工作区",
	"sandbox":   "沙箱档位",
	"approval":  "审批档位",
	"session":   "会话名",
}

// statuslineItem 渲染单项(空串 = 该项当前无内容,拼接时跳过)。
func statuslineItem(s *State, token string) string {
	switch token {
	case "state":
		if s.Running {
			frame := spinnerFrame(s.SpinnerIdx)
			if s.LastTool != "" {
				return styleBusy.Render(frame + " 执行工具: " + s.LastTool + " (Esc 取消)")
			}
			return styleBusy.Render(frame + " 思考中 (Esc 取消)")
		}
		return styleStatus.Render("空闲")
	case "queue":
		if n := len(s.Queue); n > 0 {
			return styleBusy.Render(fmt.Sprintf("待发 %d (Alt+Up 取回)", n))
		}
	case "questions":
		if n := len(s.Questions); n > 0 {
			hint := "❓ 待答"
			if n > 1 {
				hint = fmt.Sprintf("❓ 待答 %d", n)
			}
			if s.Answering {
				hint += "(Esc 退出作答)"
			} else {
				hint += "(/answer 作答)"
			}
			return styleBusy.Render(hint)
		}
	case "dock":
		if lbl := dockLabel(s.Dock); lbl != "" {
			if s.Dock.Running > 0 {
				return styleBusy.Render(lbl) // 有东西在跑 = 显眼信号
			}
			return styleStatus.Render(lbl)
		}
	case "last":
		if !s.Running && s.turnDur > 0 {
			return styleStatus.Render("上一回合 " + fmtDur(s.turnDur))
		}
	case "workspace":
		return styleStatus.Render("工作区: " + orDefault(s.Workspace, "?"))
	case "sandbox":
		return styleStatus.Render("沙箱: " + orDefault(s.Sandbox, string(sdk.SandboxWorkspace)))
	case "approval":
		if s.Approval != "" {
			return styleStatus.Render("审批: " + approvalLabel(s.Approval))
		}
	case "session":
		if s.Session != "" {
			return styleStatus.Render("会话: " + s.Session)
		}
	}
	return ""
}

// renderStatusLine 状态栏(/statusline 可配置;未配置 = F15.3 基线)。
// 宽度填充防行尾锯齿(旧版空白仍在尾部)。
func renderStatusLine(s *State, width int) string {
	items := s.Statusline
	if len(items) == 0 {
		items = defaultStatusline
	}
	out := ""
	prevCluster := false
	for _, tok := range items {
		txt := statuslineItem(s, tok)
		if txt == "" {
			continue
		}
		if out != "" {
			if statuslineCluster[tok] && prevCluster {
				out += " · "
			} else {
				out += " | "
			}
		}
		out += txt
		prevCluster = statuslineCluster[tok]
	}
	return styleStatus.Render(" " + out + strings.Repeat(" ", width))
}

// statuslineNames 合法项名列表(错误提示/无参输出用)。
func statuslineNames() []string { return append([]string(nil), statuslineTokens...) }
