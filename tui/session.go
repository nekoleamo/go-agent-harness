// 会话流引擎层(S2.2 组件化):逻辑行 → 物理行的展平/折行、样式、行渲染、选区与滚动度量。
// 自 render.go 拆出;render.go 负责整屏装配(Render),chrome.go 负责装饰层(输入/提示/状态栏)。
package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

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

// styleForKind 按物理行 kind 取基础样式(高亮叠加在其上做反色)。
func styleForKind(kind string) lipgloss.Style {
	switch kind {
	case "user":
		return *styleUser
	case "assistant":
		return *styleAsst
	case "tool":
		return *styleTool
	case "meta":
		return *styleMeta
	case "thinking":
		return *styleThink // 思维块灰斜体(弱化,不抢正文)
	case "error":
		return *styleError
	default:
		return lipgloss.NewStyle()
	}
}

// rowBaseStyle 逻辑行渲染基础样式:工具结果成功行(文本 ✓ 开头,kind tool)用成功绿,
// 与调用行(琥珀)/失败行(error 红)区分——语义色差(P4-7)。其余按 kind。
func rowBaseStyle(p physRow) lipgloss.Style {
	if p.kind == "tool" && strings.HasPrefix(p.text, "✓") {
		return *styleToolOK
	}
	return styleForKind(p.kind)
}

// diffToolRow 工具结果展开行的 diff 轻染色(单行整色,字符无损;无 diff 特征返回空串):
// 文件/块头(+++ / --- / @@)灰,新增行(+)淡绿,删除行(-)暗红。
// 只识别行首(允许前导空白)的 +/- 前缀,不解析 diff 内部结构(轻渲染取舍)。
func diffToolRow(text string) string {
	i := 0
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	s := text[i:]
	switch {
	case strings.HasPrefix(s, "+++") || strings.HasPrefix(s, "---") || strings.HasPrefix(s, "@@"):
		return lipgloss.NewStyle().Foreground(fg(TokDiffHdr)).Render(text)
	case strings.HasPrefix(s, "+"):
		return lipgloss.NewStyle().Foreground(fg(TokDiffAdd)).Render(text)
	case strings.HasPrefix(s, "-"):
		return lipgloss.NewStyle().Foreground(fg(TokDiffDel)).Render(text)
	}
	return ""
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

// 搜索高亮颜色经 colorVal 取当前生效值(主题覆盖即时生效,见 palette.go)。

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
		// 代码围栏(P4-3):围栏行/块内行统一代码色 + 极简语法着色(render 层先行标注)
		if p.inCode || p.codeFence {
			if styled := mdCodeBlockRow(p.text, p.lang); styled != "" {
				return styled
			}
		}
		if styled := mdAnnotateRow(p.text, styleAsst.GetForeground()); styled != "" {
			return styled
		}
	}
	// P4-7 工具结果 diff 轻染色:展开内容行的 +/- 行(搜索命中/选区回落基础样式,叠加几何不冲突)
	if p.kind == "tool" && !s.searchHitLine(p.lineIdx) && !s.SelActive {
		if d := diffToolRow(p.text); d != "" {
			return d
		}
	}
	st := rowBaseStyle(p)
	// P5 背景块:先应用语义背景(user 整块/工具调用与结果首行),搜索命中背景后置覆盖(命中优先)。
	if p.bg != "" {
		st = st.Background(lipgloss.Color(colorVal(Token(p.bg))))
	}
	if s.searchHitLine(p.lineIdx) {
		if p.lineIdx == s.searchCurLine() {
			st = st.Background(lipgloss.Color(colorVal(TokSearchCurBg)))
		} else {
			st = st.Background(lipgloss.Color(colorVal(TokSearchBg)))
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

// annotateRowBg 逐逻辑行标注背景块(P5 视觉升级,对齐 pi userMessageBg/tool*Bg 语义):
// user 行全部物理行深灰底(整块消息背景);工具调用行(⚙ 前缀)与工具结果行
// (✓ 成功/✗ 失败 前缀)仅首物理行带背景——折叠摘要即"框感"标题底,展开全文行
// 走 diff/md 染色(内嵌 ANSI reset 会清外层背景,不加背景防冲突)。
// 叠加顺序:renderSessionRow 先应用 bg,搜索命中背景后置覆盖(命中优先),选区反色叠加其上。
func annotateRowBg(rows []physRow, lines []Line) {
	for li, ln := range lines {
		var tok Token
		switch {
		case ln.Kind == "user":
			tok = TokUserBg // 用户消息:整块背景
		case ln.Kind == "tool" && strings.HasPrefix(ln.Text, "⚙ "):
			tok = TokToolBg // 工具调用行(pending 态)
		case ln.Kind == "tool" && strings.HasPrefix(ln.Text, "✓"):
			tok = TokToolOKBg // 工具成功结果行
		case ln.Kind == "error" && strings.HasPrefix(ln.Text, "✗"):
			tok = TokToolErrBg // 工具失败结果行(agent error 行不含 ✗ 前缀,不误标)
		}
		if tok == "" {
			continue
		}
		for i := range rows {
			p := &rows[i]
			if p.lineIdx != li {
				continue
			}
			if tok == TokUserBg || p.first {
				p.bg = string(tok)
			}
		}
	}
}

// annotateCodeFences 逐行扫描标注代码围栏(assistant 行参与切换;其它 kind 不翻转——
// 工具结果/元信息里出现 ``` 不干扰正文围栏配对;同逻辑行跨物理行按序连续判定)。
// 围栏行标记 codeFence + 开围栏解析 lang;其后续 assistant 内容行置 inCode 并沿用 lang。
func annotateCodeFences(rows []physRow) {
	in := false
	lang := ""
	for i := range rows {
		p := &rows[i]
		if p.kind != "assistant" {
			continue
		}
		if l, ok := mdFenceInfo(strings.TrimSpace(p.text)); ok {
			p.codeFence = true
			if !in {
				lang = l // 开围栏:记录语言(如无 info 则空)
			}
			in = !in
			if !in {
				lang = "" // 闭围栏:清除
			}
			continue
		}
		if in {
			p.inCode = true
			p.lang = lang
		}
	}
}

// thinkingFoldLen 思维块折叠态首段 rune 数:宽字符 2 列,40 rune ≈ 80 列,+展开提示 ~13 列 < 100 列单行。
// 截断在 flattenViewLines 展平层(按逻辑行),展开态(Ctrl+T)全文参与展平。
const thinkingFoldLen = 40

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
	st := rowBaseStyle(p)
	// P5 背景块:折叠结果行同样带语义背景(与 renderSessionRow 主路径一致)
	if p.bg != "" {
		st = st.Background(lipgloss.Color(colorVal(Token(p.bg))))
	}
	// 在首物理行文本后追加可点击标记(颜色弱化),方便识别可切换行
	_ = gRow
	return st.Render(p.text + mark)
}

// physRow 会话流物理显示行:kind 决定着色;text 已含首行前缀(❯)且宽度 ≤ 内容列宽。
// inCode/codeFence/lang 为代码围栏标注(annotateCodeFences 逐行扫描后写;仅 assistant 参与)。
type physRow struct {
	kind      string
	text      string
	lineIdx   int    // 归属逻辑行(Lines 索引;搜索命中/高亮定位用)
	first     bool   // 该逻辑行首物理行(折叠标记/搜索定位只在首行)
	inCode    bool   // 位于代码围栏内(内容行)
	codeFence bool   // 围栏行本身(``` / ~~~ 开或闭)
	lang      string // 围栏语言(开围栏行解析;块内内容行沿用)
	bg        string // 背景块语义色 token 名(annotateRowBg 后写;user 全行/工具调用与结果首行);空=无背景
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
		// B1 思维块折叠(Ctrl+T):折叠态截断到阈值一字物理行+展开提示(宽字符 2 列,48 rune ≈ 96 列)
		if ln.Kind == "thinking" && !s.ThinkingFull {
			if r := []rune(ln.Text); len(r) > thinkingFoldLen {
				text = string(r[:thinkingFoldLen]) + " …(Ctrl+T 展开)"
			}
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
		switch {
		case si == 0 && ln.Kind == "user":
			limit = colW - 2 // 首行挂 "❯ " 前缀,可用宽减 2
		case si == 0 && ln.Kind == "tool" && strings.HasPrefix(ln.Text, "⚙ "):
			limit = colW - 2 // 工具调用行:左缘竖线 "▍ " 前缀(P5 框感)
		}
		if limit < 1 {
			limit = 1
		}
		parts := wrapSegment(seg, limit)
		for pi, p := range parts {
			t := p
			if si == 0 && pi == 0 {
				switch {
				case ln.Kind == "user":
					t = "❯ " + p
				case ln.Kind == "tool" && strings.HasPrefix(ln.Text, "⚙ "):
					t = "▍ " + p
				}
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

// tabStop 制表符展开宽度(8 列 tab stop,与终端/tabwriter 惯例一致)。
const tabStop = 8

// wrapSegment 按终端列宽折一段无换行文本:双宽字符不跨行拆分;不可见控制字符丢弃;
// 制表符按 8 列 tab stop 展开为空格(不可丢弃——代码/补丁缩进全靠它,且整行仅由
// \t 构成时丢弃会让物理行数变少、鼠标划选/滚动锚点错位)。
func wrapSegment(seg string, w int) []string {
	if w < 1 {
		w = 1
	}
	var out []string
	var b strings.Builder
	cw := 0
	for _, r := range seg {
		if r == '\t' {
			n := tabStop - cw%tabStop
			if cw > 0 && cw+n > w {
				out = append(out, b.String()) // 本行放不下:折行后从新行 0 列重新对齐
				b.Reset()
				cw = 0
				n = tabStop
			}
			if n > w {
				n = w
			}
			b.WriteString(strings.Repeat(" ", n))
			cw += n
			continue
		}
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

// spinnerFrames 思考动画帧(F15.3 修正:shade 光条滚动)。
// 用 ░▒▓█(ANSI/Mac 字体普遍支持)做定宽 5 的光条左右滚动——比 braille 旋转醒目,
// 比 ▁▂▃▄▅ 细分块稳(后者在 Menlo 等字体缺失,显示空白被用戶反馈“无波浪”)；
// 回合运行中 tick 推进。
var spinnerFrames = []string{
	"█▓▒░░", "░█▓▒░", "░░█▓▒", "░█▓▒░", "█▓▒░░",
}

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
