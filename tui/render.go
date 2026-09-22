// 渲染:状态 → 终端文本(lipgloss 着色)。纯函数,可单测。
package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// 主题可变样式:全部为指针,rebuildStyles 按当前调色板**原地重建**。
// 旧实现把 fg(...) 固化在包级 var(init 期求值),ApplyTheme 只改 active map →
// /theme、data.palette、theme.yaml 对主要前景色完全无效(只有现场取值的
// markdown/语法/diff 跟随)。指针 + 原地赋值保证既有引用立即看到新色。
var (
	styleUser     = &lipgloss.Style{}
	styleAsst     = &lipgloss.Style{}
	styleTool     = &lipgloss.Style{}
	styleToolOK   = &lipgloss.Style{}
	styleMeta     = &lipgloss.Style{}
	styleThink    = &lipgloss.Style{}
	styleError    = &lipgloss.Style{}
	stylePrompt   = &lipgloss.Style{}
	styleStatus   = &lipgloss.Style{}
	styleBusy     = &lipgloss.Style{}
	stylePick     = &lipgloss.Style{}
	styleCursor   = &lipgloss.Style{}
	styleBarThumb = &lipgloss.Style{}
	styleBarTrack = &lipgloss.Style{}
	styleBarHover = &lipgloss.Style{}
	styleBarEnd   = &lipgloss.Style{}
	styleWidget   = &lipgloss.Style{}
)

// init 与 ApplyTheme/ResetTheme 都要调:包级样式是指针,初始为空样式,不建则无颜色。
func init() { rebuildStyles() }

// rebuildStyles 重建全部包级样式(palette.go 的 ApplyTheme/ResetTheme 亦调用)。
func rebuildStyles() {
	*styleUser = lipgloss.NewStyle().Foreground(fg(TokUser)).Bold(true)
	*styleAsst = lipgloss.NewStyle().Foreground(fg(TokAssistant))
	*styleTool = lipgloss.NewStyle().Foreground(fg(TokTool))
	*styleToolOK = lipgloss.NewStyle().Foreground(fg(TokToolOK))
	*styleMeta = lipgloss.NewStyle().Foreground(fg(TokMeta))
	*styleThink = lipgloss.NewStyle().Foreground(fg(TokThinking)).Italic(true)
	*styleError = lipgloss.NewStyle().Foreground(fg(TokError))
	*stylePrompt = lipgloss.NewStyle().Foreground(fg(TokPrompt)).Bold(true)
	*styleStatus = lipgloss.NewStyle().Foreground(fg(TokStatus))
	*styleBusy = lipgloss.NewStyle().Foreground(fg(TokBusy))
	*stylePick = lipgloss.NewStyle().Foreground(fg(TokPick)).Bold(true)
	*styleCursor = lipgloss.NewStyle().Foreground(fg(TokCursor)).Bold(true)
	*styleBarThumb = lipgloss.NewStyle().Foreground(fg(TokBarThumb))
	*styleBarTrack = lipgloss.NewStyle().Foreground(fg(TokBarTrack))
	*styleBarHover = lipgloss.NewStyle().Foreground(fg(TokBarHover))
	*styleBarEnd = lipgloss.NewStyle().Foreground(fg(TokBarEnd)).Bold(true)
	*styleWidget = lipgloss.NewStyle().Foreground(fg(TokWidget))
}

const maxHintRows = 6

// inputMaxRows 输入区窗口物理行上限(超长输入封顶,主区不被压没):
// ≈ 主区可用几何的三分之一,至少 3 行。由 renderInputLine 的 maxRows 变参传入渲染。
func inputMaxRows(height int) int {
	r := (height - 8) / 3
	if r < 3 {
		r = 3
	}
	return r
}

// renderInputFrame 输入区圆角矩形框(整宽对齐终端):
// 边框/边线颜色随思考等级(thinking 语义,对齐 pi 编辑器边框色),内容行 pad 统一宽。
// 多行内容(窗口滚动/省略指示行)整体包框;左缘不再重复竖线(语义移交边框色)。
// 输出行宽恒等于 width(顶边 ╭+─×width-2+╮;内容行 │ +pad+ │)。
func renderInputFrame(content string, width int, thinking string) string {
	if width < 4 {
		width = 4
	}
	edge := thinkEdgeColor(thinking)
	inner := width - 2 // 框内宽(不含左右边框列)
	lines := strings.Split(content, "\n")
	var sb strings.Builder
	sb.WriteString(lipgloss.NewStyle().Foreground(edge).Render("╭" + strings.Repeat("─", inner) + "╮"))
	for _, ln := range lines {
		sb.WriteByte('\n')
		pad := (inner - 2) - lipgloss.Width(ln) // 内容宽 = inner-2(左"│ "右" │"各 2 列)
		if pad < 0 {
			pad = 0
		}
		sb.WriteString(lipgloss.NewStyle().Foreground(edge).Render("│ "))
		sb.WriteString(ln)
		sb.WriteString(strings.Repeat(" ", pad))
		sb.WriteString(lipgloss.NewStyle().Foreground(edge).Render(" │"))
	}
	sb.WriteByte('\n')
	sb.WriteString(lipgloss.NewStyle().Foreground(edge).Render("╰" + strings.Repeat("─", inner) + "╯"))
	return sb.String()
}

// pickWindow 提示/选项列表窗口(超限滚动):以高亮 cursor 为锚取 visible 个可见项。
// cursor 下移触底后窗口随之下滚一行、上移触顶后随之上滚;n ≤ visible 时全量显示。
// 返回窗口 [start, end)(下标;n == 0 或 visible ≤ 0 时返回空窗口)。
func pickWindow(n, cursor, visible int) (start, end int) {
	if n <= 0 || visible <= 0 {
		return 0, 0
	}
	if n <= visible {
		return 0, n
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= n {
		cursor = n - 1
	}
	start = cursor - (visible - 1)
	if start < 0 {
		start = 0
	}
	if start+visible > n {
		start = n - visible
	}
	return start, start + visible
}

// Render 渲染整屏。mainH = 会话流区域高度;底部含输入行 + 命令提示区(动态) + 状态栏。
// 提示区最多 maxHintRows 行:选择器超限按 cursor 滚动窗口;静态提示超限截前段并提示余量。
func Render(s *State, width, height int) string {
	if width <= 0 || height <= 0 {
		width, height = 80, 24
	}
	// 文档预览 pager(D1):全屏模态浮层,独立渲染路径(不动会话流渲染)
	if s.Doc != nil {
		return s.Doc.Render(width, height)
	}
	// 选择器激活时提示区 = 选项列表(高亮当前,滚动窗口);否则静态提示行。
	// 参数级过滤(Filter 非空):首行过滤状态(占 1 行,选项窗口相应减 1),无匹配时仅状态行。
	var hintItems []string
	if s.Mention != nil {
		// @ 文件引用候选窗口(↑/↓ 移动、Tab/Enter 应用;与命令选择器互斥)
		items := s.Mention.Items
		if len(items) == 0 {
			hintItems = append(hintItems, styleMeta.Render("无匹配文件(继续输入/退格恢复)"))
		} else {
			start, end := pickWindow(len(items), s.Mention.Cursor, maxHintRows)
			for i := start; i < end; i++ {
				line := " @" + items[i].Value + " " + items[i].Desc
				if i == s.Mention.Cursor {
					hintItems = append(hintItems, stylePick.Render("▸"+line))
				} else {
					hintItems = append(hintItems, styleMeta.Render(line))
				}
			}
		}
	} else if s.Pick != nil {
		items := s.Pick.Items
		vis := maxHintRows
		if s.Pick.Filter != "" {
			vis = maxHintRows - 1
			total := len(s.Pick.All)
			if total == 0 {
				total = len(items)
			}
			if len(items) == 0 {
				hintItems = append(hintItems, styleMeta.Render(
					fmt.Sprintf("过滤: %s → 无匹配(退格清除)", s.Pick.Filter)))
			} else {
				hintItems = append(hintItems, styleMeta.Render(
					fmt.Sprintf("过滤: %s → %d/%d 项(退格清除/Esc 恢复)", s.Pick.Filter, len(items), total)))
			}
		}
		start, end := pickWindow(len(items), s.Pick.Cursor, vis)
		for i := start; i < end; i++ {
			it := items[i]
			line := " /" + it.Value + " " + it.Desc
			if i == s.Pick.Cursor {
				hintItems = append(hintItems, stylePick.Render("▸"+line))
			} else {
				hintItems = append(hintItems, styleMeta.Render(line))
			}
		}
	} else if len(s.Suggestions) > maxHintRows {
		// 静态提示超窗(如 / 全部命令):截前段 + 末行余量提示(输入继续前缀过滤)
		hintItems = append(hintItems, s.Suggestions[:maxHintRows-1]...)
		hintItems = append(hintItems,
			styleMeta.Render(fmt.Sprintf("… 还有 %d 项(继续输入过滤)", len(s.Suggestions)-(maxHintRows-1))))
	} else {
		hintItems = append(hintItems, s.Suggestions...)
	}
	hintRows := len(hintItems)
	if hintRows > maxHintRows {
		hintRows = maxHintRows
	}
	// 输入区可能多行(P4-6 Shift+Enter 换行,超长输入经 inputMaxRows 封顶滚动窗口):
	// 主区高度扣输入物理行数,其余(提示区/状态栏)各占其位;长输入压不没主区。
	inputContent := renderInputLine(s, width, inputMaxRows(height))
	inputStr := renderInputFrame(inputContent, width, s.Thinking) // 圆角矩形框化(边框色随思考)
	// 输入区多行:在既有单行几何(height-8-hintRows,8 = 指标行 1 + 状态栏 1 +
	// 空隙行 1 + 输入内容 1 + 顶/底边框 2 + 分隔线 1 + 预留 1)上按输入多出的物理行数再扣主区(单行时差值 0)。
	// inputExtra 按**内容行**(不含顶/底边框两行)计,省略指示行(窗口滚动时)计入,几何与渲染一致。
	inputExtra := strings.Count(inputContent, "\n")
	// P4-12 widget 槽位:输入行上方动态信息行(开关关=0),同样扣主区
	widgetRows := widgetLines(s)
	// S-P0-3 坞展开(F6):面板行同样扣主区(收起 = nil,零几何变化)
	dockPanel := dockPanelLines(s, width)
	mainH := height - 8 - hintRows - inputExtra - len(widgetRows) - len(dockPanel)
	if mainH < 1 {
		mainH = 1
	}
	colW := width - 3 // 内容列宽(bar 前留 1 空格,最右列是 bar)
	if colW < 8 {
		colW = 8
	}
	var body []string
	// 错误横幅(若有):文本折行(可能多行),行数计入会话流窗口扣除。
	if s.Error != "" {
		bannerRows := wrapToLines(s.Error, colW-2)
		for i, seg := range bannerRows {
			t := seg
			if i == 0 {
				t = "⛔ " + seg
			}
			body = append(body, styleError.Render(t))
		}
	}
	// 会话流窗口高度 = 主区减去横幅已占行数
	win := mainH - len(body)
	if win < 1 {
		win = 1
	}
	s.sessionWin = win // 渲染实际会话窗口高(滚动条命中/拖动同几何,见 state.sessionWin)
	// 会话流:逻辑行展平为物理显示行(按 \n 分段 + 终端列宽折行)。
	// 一条 Line 的文本可能含换行(多段回复/长新闻),直接当单行渲染会撑爆窗口——
	// 这里拆成与终端物理行一一对应的行,滚动窗口按物理行计算。
	// S2.2 折叠视图:已展开的结果行(lineIdx FoldOpen)用 Full 参与展平(全文多行);
	// 未展开保持 Text 摘要单行。搜索/鼠标命中仍以 Lines 摘要为基准(见 searchHitLine)。
	rows := flattenViewLines(s, colW)
	annotateCodeFences(rows)     // 代码围栏跨行标注(assistant 物理行顺序切换;滚动/折叠视图每帧重算)
	annotateRowBg(rows, s.Lines) // P5 背景块标注(user 整块/工具调用与结果首行;同每帧重算)
	total := len(rows)
	if total > 0 {
		s.flatN = total // 刷新展平行数(ScrollBy 上限钳制用)
	}
	switch {
	case total == 0:
		// 空会话:仅横幅(若有)
	case total <= win:
		// 内容不足窗口:全部显示,不渲染滚动条;offset 归零(无历史可滚)。
		if s.ScrollOffset != 0 {
			s.ScrollOffset = 0
		}
		for i, p := range rows {
			body = append(body, padRow(renderSessionRow(p, s, i), colW))
		}
	default:
		// 内容超窗口:按 offset 取窗口(物理行),渲染滚动条(贴右缘;行文本 pad 统一列宽,防长短不齐错位成锯齿)。
		off := s.ScrollOffset
		if off > total-win {
			off = total - win
		}
		if off < 0 {
			off = 0
		}
		if off != s.ScrollOffset {
			s.ScrollOffset = off // 钳制写回(渲染与状态一致)
		}
		top, thumb := scrollMetrics(total, win, off)
		base := total - win - off // 窗口第一行全局物理行号
		// 滚动条 auto-hide:交互后静止超时且非悬停 → 隐藏(初始未交互始终显示;hover 保持)
		hide := !s.HoverBar && !s.BarShownAt.IsZero() && time.Since(s.BarShownAt) > barHideDelay
		if !hide {
			for i, p := range rows[total-win-off : total-off] {
				var bar string
				if s.ScrollOffset > 0 && i == win-1 {
					bar = styleBarEnd.Render("▼") // 回底指示(点击回最新)
				} else if s.HoverBar {
					bar = styleBarHover.Render("░")
					if i >= top && i < top+thumb {
						bar = styleBarHover.Render("█")
					}
				} else {
					bar = styleBarTrack.Render("░")
					if i >= top && i < top+thumb {
						bar = styleBarThumb.Render("█")
					}
				}
				body = append(body, padRow(renderSessionRow(p, s, base+i), colW)+" "+bar)
			}
		} else {
			for i, p := range rows[total-win-off : total-off] {
				body = append(body, padRow(renderSessionRow(p, s, base+i), colW))
			}
		}
	}
	main := strings.Join(body, "\n")

	// 底部区(M15 pi 式 + F15.1/F15.2 修正):整宽分隔线 → widgets → 输入 → 提示 →
	// 状态栏 → 指标行(模型/思维/上下文在最后一行下面,固定不随输入移动)。
	rule := styleMeta.Render(strings.Repeat("─", width))
	bottom := []string{rule}
	bottom = append(bottom, widgetRows...)
	for i := 1; i < len(bottom); i++ {
		bottom[i] = styleWidget.Render("◇ " + widgetRows[i-1]) // 输入区上方动态信息(前缀区分)
	}
	// S-P0-3 坞展开面板:F6 展开时占位(在分隔线/widget 下、输入区上方;收起无行)
	bottom = append(bottom, dockPanel...)
	bottom = append(bottom, inputStr)
	bottom = append(bottom, renderHintLines(s, hintItems, hintRows)...)
	bottom = append(bottom, "") // F15.5:状态栏/指标行与输入/提示区留空隙行
	bottom = append(bottom, renderStatusLine(s, width))
	bottom = append(bottom, renderMetricLine(s)) // 最底行:模型/思维/上下文(最后一行下面)
	return clampFrame(lipgloss.JoinVertical(lipgloss.Left, main, strings.Join(bottom, "\n")), width, height)
}

// clampFrame 几何夹紧(渲染的**最后一道保险**):每行截到终端列宽、总行数封到终端高度。
//
// 为什么需要兜底:上面的几何是“按行数算账”(mainH = height - 8 - 各行占用),任何一处
// 渲染出的物理行比假设宽(未过截断的插件行、CJK/emoji 按显示列算差一倍、异常长的单行),
// 终端就会把它**折成两行** ⇒ 帧比屏幕高、光标定位错位 ⇒ 输入区/状态栏被顶出屏幕,
// 滚一下又逐渐露出来(2026-09-22 用户实测反馈)。这里宁可截掉一列也不再让终端折行。
// 行数超限时从**顶部**(会话流)裁:底部的输入区/状态栏必须在屏内。
func clampFrame(view string, width, height int) string {
	if width <= 0 || height <= 0 {
		return view
	}
	lines := strings.Split(view, "\n")
	for i, ln := range lines {
		if lipgloss.Width(ln) <= width {
			continue // 恰好满宽的行(输入框边框等)原样保留,别把末字符换成省略号
		}
		cut := truncateVisible(ln, width) // 超宽才动:结果 ≤ width
		if strings.Contains(ln, "\x1b") {
			cut += "\x1b[0m" // 本函数已不在 lipgloss 样式内,截在着色行中间要自己收尾(防颜色溢到后面)
		}
		lines[i] = cut
	}
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	return strings.Join(lines, "\n")
}
