// 渲染:状态 → 终端文本(lipgloss 着色)。纯函数,可单测。
package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

var (
	styleUser   = lipgloss.NewStyle().Foreground(fg(TokUser)).Bold(true)
	styleAsst   = lipgloss.NewStyle().Foreground(fg(TokAssistant))
	styleTool   = lipgloss.NewStyle().Foreground(fg(TokTool))
	styleToolOK = lipgloss.NewStyle().Foreground(fg(TokToolOK)) // 工具结果成功行(绿,与调用琥珀区分)
	styleMeta   = lipgloss.NewStyle().Foreground(fg(TokMeta))
	styleError  = lipgloss.NewStyle().Foreground(fg(TokError))
	stylePrompt = lipgloss.NewStyle().Foreground(fg(TokPrompt)).Bold(true)
	styleStatus = lipgloss.NewStyle().Foreground(fg(TokStatus))
	styleBusy   = lipgloss.NewStyle().Foreground(fg(TokBusy))              // 运行中状态高亮(琥珀色,醒目)
	stylePick   = lipgloss.NewStyle().Foreground(fg(TokPick)).Bold(true)   // 选择器高亮行
	styleCursor = lipgloss.NewStyle().Foreground(fg(TokCursor)).Bold(true) // 输入块光标(琥珀)
	// 滚动条:滑块(琥珀)与轨道(灰)——会话流超过窗口时右侧显示,位置反映浏览进度
	styleBarThumb = lipgloss.NewStyle().Foreground(fg(TokBarThumb))
	styleBarTrack = lipgloss.NewStyle().Foreground(fg(TokBarTrack))
	// 滚动条增强:悬停高亮(更亮琥珀)与回底指示(▼,浏览历史时底行显示,点击回最新)
	styleBarHover = lipgloss.NewStyle().Foreground(fg(TokBarHover))
	styleBarEnd   = lipgloss.NewStyle().Foreground(fg(TokBarEnd)).Bold(true)
)

const maxHintRows = 6

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
	// 选择器激活时提示区 = 选项列表(高亮当前,滚动窗口);否则静态提示行。
	// 参数级过滤(Filter 非空):首行过滤状态(占 1 行,选项窗口相应减 1),无匹配时仅状态行。
	var hintItems []string
	if s.Pick != nil {
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
	mainH := height - 3 - hintRows
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
	annotateCodeFences(rows) // 代码围栏跨行标注(assistant 物理行顺序切换;滚动/折叠视图每帧重算)
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

	bottom := []string{renderInputLine(s, width)}
	bottom = append(bottom, renderHintLines(s, hintItems, hintRows)...)
	bottom = append(bottom, renderStatusLine(s, width))
	return lipgloss.JoinVertical(lipgloss.Left, main, strings.Join(bottom, "\n"))
}
