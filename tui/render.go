// 渲染:状态 → 终端文本(lipgloss 着色)。纯函数,可单测。
package tui

import (
	"fmt"
	"strings"
	"time"

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
	styleBusy   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))            // 运行中状态高亮(琥珀色,醒目)
	stylePick   = lipgloss.NewStyle().Foreground(lipgloss.Color("207")).Bold(true) // 选择器高亮行
	styleCursor = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true) // 输入块光标(琥珀)
	// 滚动条:滑块(琥珀)与轨道(灰)——会话流超过窗口时右侧显示,位置反映浏览进度
	styleBarThumb = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	styleBarTrack = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	// 滚动条增强:悬停高亮(更亮琥珀)与回底指示(▼,浏览历史时底行显示,点击回最新)
	styleBarHover = lipgloss.NewStyle().Foreground(lipgloss.Color("172"))
	styleBarEnd   = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
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

// scrollMetrics 滚动条度量:总行 total、窗口 win、上滚 offset → 滑块顶行 top 与滑块高 thumb。
// total<=win(无需滚动)或 win<=0 → 全窗口滑块;offset=0(跟随最新)时滑块沉底。
func scrollMetrics(total, win, offset int) (top, thumb int) {
	if total <= win || win <= 0 {
		return 0, win
	}
	maxOff := total - win
	thumb = win * win / total
	if thumb < 1 {
		thumb = 1
	}
	// 上滚越多滑块越靠上(offset=0 跟随最新沉底,offset=maxOff 到顶)
	top = (maxOff - offset) * (win - thumb) / maxOff
	return top, thumb
}

func renderRow(p physRow) string {
	st := styleForKind(p.kind)
	return st.Render(p.text)
}

// styleForKind 按物理行 kind 取基础样式(高亮叠加在其上做反色)。
func styleForKind(kind string) lipgloss.Style {
	switch kind {
	case "user":
		return styleUser
	case "assistant":
		return styleAsst
	case "tool":
		return styleTool
	case "meta":
		return styleMeta
	case "error":
		return styleError
	default:
		return lipgloss.NewStyle()
	}
}

// selRange 选区对全局物理行 gRow 的命中列区间(0 基 rune);未命中返回 active=false。
func (s *State) selRange(gRow int) (active bool, c0, c1 int) {
	if !s.SelActive {
		return false, 0, 0
	}
	a, b := s.SelRow0, s.SelRow1
	cA, cB := s.SelCol0, s.SelCol1
	if a > b { // 反向拖动归一
		a, b = b, a
		cA, cB = cB, cA
	}
	if gRow < a || gRow > b {
		return false, 0, 0
	}
	c0, c1 = 0, 1<<30
	if gRow == a {
		c0 = cA
	}
	if gRow == b {
		c1 = cB
	}
	return true, c0, c1
}

// renderRowSel 渲染物理行并叠加选区反色高亮(选中 rune 区间 [c0,c1),未命中走 renderRow)。
// 搜索高亮颜色:命中行暗背景、当前命中琥珀背景(醒目,与选区反色叠加。
// lipgloss.Background 用 256 色索引,与 24 位色共存)。
const (
	searchBg    = "238" // 命中行背景(暗)
	searchCurBg = "214" // 当前命中背景(琥珀,醒目)
)

// renderSessionRow 渲染会话流物理行:kind 基础样式 + 搜索命中整行背景(当前命中更亮)
// + 鼠标选区反色段(命中/选区可同时存在)。
// S1.4 Markdown 轻渲染:assistant 行无搜索命中/无选区时走 mdAnnotateRow(token 分段着色);
// 命中/选区叠加时回落纯文本渲染(色文本不含在反色/背景几何内,轻渲染以可交互优先)。
// S2.2 折叠:结果行首物理行带折叠提示尾缀(未展开 ▲ 可点展开 / 已展开 ▼ 可点收起)。
func renderSessionRow(p physRow, s *State, gRow int) string {
	// 折叠/展开提示尾缀(结果行 Full 非空):展开态或折叠态均标记可点击切换。
	if mark := foldMarkFor(s, p); mark != "" {
		if !s.searchHitLine(p.lineIdx) && !s.SelActive {
			return stRenderText(s, p, gRow, mark)
		}
	}
	if p.kind == "assistant" && !s.searchHitLine(p.lineIdx) && !s.SelActive {
		if styled := mdAnnotateRow(p.text, styleAsst.GetForeground()); styled != "" {
			return styled
		}
	}
	st := styleForKind(p.kind)
	if s.searchHitLine(p.lineIdx) {
		if p.lineIdx == s.searchCurLine() {
			st = st.Background(lipgloss.Color(searchCurBg))
		} else {
			st = st.Background(lipgloss.Color(searchBg))
		}
	}
	act, c0, c1 := s.selRange(gRow)
	if !act {
		return st.Render(p.text)
	}
	rs := []rune(p.text)
	n := len(rs)
	if n == 0 {
		return st.Render(p.text)
	}
	a, b := c0, c1
	if a < 0 {
		a = 0
	}
	if a > n {
		a = n
	}
	if b < a {
		b = a
	}
	if b > n {
		b = n
	}
	if a == b {
		return st.Render(p.text)
	}
	sel := st.Reverse(true) // 反色高亮选中段(保留行基础/搜索背景样式)
	var sb strings.Builder
	sb.WriteString(st.Render(string(rs[:a])))
	sb.WriteString(sel.Render(string(rs[a:b])))
	sb.WriteString(st.Render(string(rs[b:])))
	return sb.String()
}

// foldMarkFor 折叠行(结果行 Full 非空)的物理行尾缀提示:仅逻辑行首物理行附加标记。
// 空 = 非折叠行/非首物理行(其余物理行不重复标注)。
func foldMarkFor(s *State, p physRow) string {
	if !p.first {
		return ""
	}
	if p.lineIdx < 0 || p.lineIdx >= len(s.Lines) {
		return ""
	}
	ln := s.Lines[p.lineIdx]
	if ln.Full == "" {
		return "" // 非结果行(无可展开全文)
	}
	if s.foldOpenOf(p.lineIdx) {
		return " ▼" // 已展开:可点收起
	}
	return " ▲" // 折叠:可点展开
}

// stRenderText 渲染带折叠标记的普通文本行(不叠加 md/搜索/选区——折叠行交互优先)。
func stRenderText(s *State, p physRow, gRow int, mark string) string {
	st := styleForKind(p.kind)
	// 在首物理行文本后追加可点击标记(颜色弱化),方便识别可切换行
	_ = gRow
	return st.Render(p.text + mark)
}

// physRow 会话流物理显示行:kind 决定着色;text 已含首行前缀(❯)且宽度 ≤ 内容列宽。
type physRow struct {
	kind    string
	text    string
	lineIdx int  // 归属逻辑行(Lines 索引;搜索命中/高亮定位用)
	first   bool // 该逻辑行首物理行(折叠标记/搜索定位只在首行)
}

// padRow 把行文本对齐到内容列宽(colW):不足补空格,超限截断(折行已保证不超)。
func padRow(text string, colW int) string {
	return lipgloss.NewStyle().MaxWidth(colW).Width(colW).Render(text)
}

// flattenLines 把会话流逻辑行展平为物理显示行:文本按 \n 分段、每段按终端列宽折行;
// 用户消息首物理行保留 "❯ " 前缀(前缀宽度计入折行)。空段保留为空行。
func flattenLines(lines []Line, colW int) []physRow {
	var rows []physRow
	for li, ln := range lines {
		rows = append(rows, flattenLine(li, ln, colW)...)
	}
	return rows
}

// flattenViewLines 折叠感知的会话流展平:已展开的结果行(lineIdx ∈ FoldOpen)以 Full 全文
// 参与展平(多物理行),其余行用摘要 Text;折叠行的摘要尾缀提示可展开。
func flattenViewLines(s *State, colW int) []physRow {
	var rows []physRow
	for li, ln := range s.Lines {
		text := ln.Text
		if ln.Full != "" && s.foldOpenOf(li) {
			text = ln.Full // 展开:全文(可能多段多行)
		} else if ln.Full != "" && !strings.Contains(ln.Text, "…") {
			// 折叠提示:内容确被截断(摘要无省略号说明其实很短——无需展开提示)
		}
		rows = append(rows, flattenLine(li, Line{Kind: ln.Kind, Text: text}, colW)...)
	}
	return rows
}

// flattenLine 单逻辑行 → 物理行序列。
func flattenLine(li int, ln Line, colW int) []physRow {
	var rows []physRow
	segs := strings.Split(ln.Text, "\n")
	for si, seg := range segs {
		limit := colW
		if si == 0 && ln.Kind == "user" {
			limit = colW - 2 // 首行挂 "❯ " 前缀,可用宽减 2
		}
		if limit < 1 {
			limit = 1
		}
		parts := wrapSegment(seg, limit)
		for pi, p := range parts {
			t := p
			if si == 0 && pi == 0 && ln.Kind == "user" {
				t = "❯ " + p
			}
			rows = append(rows, physRow{kind: ln.Kind, text: t, lineIdx: li, first: si == 0 && pi == 0})
		}
	}
	return rows
}

// wrapToLines 拆分 \n 并按列宽折行(错误横幅等短文本多行化用)。
func wrapToLines(text string, w int) []string {
	if w < 1 {
		w = 1
	}
	var out []string
	for _, seg := range strings.Split(text, "\n") {
		out = append(out, wrapSegment(seg, w)...)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// wrapSegment 按终端列宽折一段无换行文本:双宽字符不跨行拆分;不可见控制字符丢弃。
func wrapSegment(seg string, w int) []string {
	if w < 1 {
		w = 1
	}
	var out []string
	var b strings.Builder
	cw := 0
	for _, r := range seg {
		rw := runeCols(r)
		if rw == 0 {
			continue
		}
		if cw > 0 && cw+rw > w {
			out = append(out, b.String())
			b.Reset()
			cw = 0
		}
		b.WriteRune(r)
		cw += rw
	}
	if b.Len() > 0 || len(out) == 0 {
		out = append(out, b.String())
	}
	return out
}

// runeCols 单字符终端列宽:0 = 不可见控制;2 = 双宽(东亚全角/假名/emoji);1 = 其余。
func runeCols(r rune) int {
	if r < 0x20 {
		return 0
	}
	if isWideRune(r) {
		return 2
	}
	return 1
}

// isWideRune 常见终端双宽字符范围(近似 Unicode East Asian Width W/F;不引入额外依赖)。
func isWideRune(r rune) bool {
	if r >= 0x1100 && r <= 0x115F { // Hangul Jamo
		return true
	}
	if r == 0x2329 || r == 0x232A { // 〈 〉
		return true
	}
	if r >= 0x2E80 && r <= 0xA4CF && r != 0x303F { // CJK 部首/标点/假名/谚文等
		return true
	}
	if r >= 0xAC00 && r <= 0xD7A3 { // Hangul Syllables
		return true
	}
	if r >= 0xF900 && r <= 0xFAFF { // CJK 兼容表意
		return true
	}
	if r >= 0xFE10 && r <= 0xFE6F { // 竖排变体 + CJK 兼容形式
		return true
	}
	if r >= 0xFF00 && r <= 0xFF60 { // 全角 ASCII 与标点
		return true
	}
	if r >= 0xFFE0 && r <= 0xFFE6 { // 全角符号(¢ £ ¬ ¯)
		return true
	}
	if r >= 0x1F300 && r <= 0x1FAFF { // emoji/符号
		return true
	}
	if r >= 0x20000 && r <= 0x3FFFD { // CJK Ext B+
		return true
	}
	return false
}

// spinnerFrames 思考动画帧(braille 旋转,回合运行中 tick 推进)。
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinnerFrame(i int) string {
	return spinnerFrames[i%len(spinnerFrames)]
}

// fmtK 数字 → 千为单位短格式(12345 → 12.3K;小于 1024 原样)。
func fmtK(n int) string {
	if n < 1024 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%.1fK", float64(n)/1024)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
