// TUI 文档预览 pager(D1,D 组):/preview <path> 或 doc/open 事件打开的全屏浮层。
//
// 纪律(对齐 DOC_PREVIEW_PLAN §6.2):
//   - 独立组件,**不动会话流渲染路径**(tui/markdown.go 与既有测试全绿作护栏);
//   - 复用块模型:由 sdk.DocView 摊平为行(表格 ASCII + 列宽预算、图片给元信息、页/表/片标记);
//   - 能力:滚动(↑/↓/PgUp/PgDn/Home/End/g/G/滚轮)、水平滚动(←/→)、/ 搜索(n/N 跳转)、q/Esc 关闭;
//   - 状态行:格式 · 行 x/y · 页/总页 · 截断与警告提示(绝不静默)。
package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/mattn/go-runewidth"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// DocOpenMsg 请求打开文档预览(host `doc/open` 事件 / /preview 命令 / 工具行)。
type DocOpenMsg struct {
	Path  string
	Page  int
	Sheet int
}

// PagerMsg 直接送入已构造好的浮层(diff/open 等由 host 载荷一次成型的内容)。
type PagerMsg struct{ Pager *DocPager }

// DocPager 文档 pager 浮层状态。
type DocPager struct {
	Title  string   // 标题(文件名)
	Format string   // 格式标签
	Lines  []string // 摊平后的文本行
	Off    int      // 垂直偏移(行)
	HOff   int      // 水平偏移(列)
	Search string   // 当前搜索词(非空 = 高亮)
	Hits   []int    // 命中行号
	HitIdx int      // 当前命中(相对 Hits)
	Status string   // 状态行右侧附加(截断/警告摘要)
	Width  int      // 最近一次渲染宽度(水平滚动用)
	Height int      // 最近一次渲染高度

	// Diff 标记「内容是 unified diff」:行首 +/-/@@ 轻染色(与工具结果行的 diffColors 同一套词色)。
	// 非 diff 文本浮层(/diff 之外的未来使用者)置 false 即回普通弱化样式。
	Diff bool

	searching bool   // / 搜索输入中(键入进 searchBuf)
	searchBuf string // 搜索输入缓冲
}

// TextPagerSpec 纯文本 pager 构造参数(S-P1-1:`/diff` 拼好的 patch 原样送进来,TUI 侧不重算 diff)。
type TextPagerSpec struct {
	Title  string
	Format string
	Lines  []string
	Status string
	Diff   bool
}

// NewTextPager 纯文本 pager(与 NewDocPager 同一渲染路径:滚动/搜索/横移/全屏浮层复用,
// 不新增渲染栈)。空内容给显式占位行,不留空白屏。
func NewTextPager(spec TextPagerSpec) *DocPager {
	p := &DocPager{Title: spec.Title, Format: spec.Format, Status: spec.Status, Diff: spec.Diff}
	p.Lines = spec.Lines
	if len(p.Lines) == 0 {
		p.Lines = []string{"(无内容)"}
	}
	return p
}

// NewDiffPager 由 host `diff/open` 载荷构造浮层(S-P1-1)。
// 无 patch(Diff 空)= 纯清单意图 → 返回 nil,由调用方决定不弹窗(命令文本已在会话流里)。
func NewDiffPager(p sdk.DiffOpenEvent) *DocPager {
	if p.Diff == "" {
		return nil
	}
	title := p.Title
	if title == "" {
		title = p.Path
	}
	status := "来自捕获的写操作(不依赖 git)"
	if n := p.Changes; n > 1 {
		status = strconv.Itoa(n) + " 次改动 · " + status
	}
	if p.Truncated {
		status = "patch 已截断 · " + status
	}
	return NewTextPager(TextPagerSpec{
		Title: title, Format: "diff", Status: status, Diff: true,
		Lines: strings.Split(strings.TrimRight(p.Diff, "\n"), "\n"),
	})
}

// NewDocPager 由 DocView 构造 pager(纯文本摊平;不引入新渲染路径)。
func NewDocPager(v *sdk.DocView, page, sheet int) *DocPager {
	p := &DocPager{Title: v.Name, Format: string(v.Format)}
	if v.Title != "" {
		p.Lines = append(p.Lines, v.Title)
	}
	parts := make([]string, 0, 4)
	if v.Kind != "" {
		parts = append(parts, v.Kind)
	}
	if v.Pages > 0 {
		parts = append(parts, fmt.Sprintf("%d 页", v.Pages))
	}
	for _, sh := range v.Sheets {
		parts = append(parts, fmt.Sprintf("表:%s(%d×%d)", sh.Name, sh.Rows, sh.Cols))
	}
	if len(parts) > 0 {
		p.Lines = append(p.Lines, strings.Join(parts, " · "))
	}
	if len(v.Truncated) > 0 {
		p.Status = "截断:" + strings.Join(v.Truncated, ",")
	}
	if len(v.Warnings) > 0 {
		w := v.Warnings[0]
		if len(v.Warnings) > 1 {
			w += fmt.Sprintf("(共 %d 条)", len(v.Warnings))
		}
		if p.Status != "" {
			p.Status += " · "
		}
		p.Status += w
	}
	for i := range v.Blocks {
		p.Lines = append(p.Lines, docBlockLines(&v.Blocks[i], 100)...)
	}
	if len(p.Lines) == 0 {
		p.Lines = []string{"(无可见内容)"}
	}
	// page 定位:跳到页标记附近
	if page > 0 {
		if idx := p.findPage(page); idx >= 0 {
			p.Off = idx
		}
	}
	_ = sheet
	return p
}

// docBlockLines 单个块 → 文本行(表格 ASCII + 列宽预算;图片给元信息)。
func docBlockLines(b *sdk.DocBlock, maxWidth int) []string {
	switch b.Kind {
	case sdk.DocBlockHeading:
		lvl := b.Level
		if lvl < 1 {
			lvl = 1
		}
		if lvl > 3 {
			lvl = 3
		}
		mark := strings.Repeat("#", lvl) + " "
		return []string{mark + headingText(b)}
	case sdk.DocBlockList:
		return []string{strings.Repeat("  ", b.Level) + "· " + blockLine(b)}
	case sdk.DocBlockQuote:
		return prefixLines(blockLine(b), "│ ")
	case sdk.DocBlockCode:
		out := []string{"``` " + b.Lang}
		out = append(out, strings.Split(b.Text, "\n")...)
		return append(out, "```")
	case sdk.DocBlockTable:
		return asciiTable(b, maxWidth)
	case sdk.DocBlockImage:
		name := b.Text
		if b.Asset != nil && b.Asset.Name != "" {
			name = b.Asset.Name
		}
		if b.Asset != nil && b.Asset.W > 0 {
			return []string{fmt.Sprintf("[图片] %s(%d×%d,%d 字节)", name, b.Asset.W, b.Asset.H, b.Asset.Bytes)}
		}
		return []string{"[图片] " + name}
	case sdk.DocBlockDivider:
		return []string{strings.Repeat("─", 40)}
	case sdk.DocBlockPage:
		return []string{fmt.Sprintf("── 第 %d 页 ──", b.Page)}
	case sdk.DocBlockSheet:
		return []string{"─ " + b.Text + " ─"}
	case sdk.DocBlockSlide:
		return []string{fmt.Sprintf("── 幻灯片 %d ──", b.Page)}
	case sdk.DocBlockNote:
		return prefixLines(b.Text, "› ")
	case sdk.DocBlockUnsupported:
		return []string{"[不支持] " + b.Text}
	}
	if t := blockLine(b); t != "" {
		return strings.Split(t, "\n")
	}
	return nil
}

func blockLine(b *sdk.DocBlock) string {
	if len(b.Runs) == 0 {
		return strings.ReplaceAll(b.Text, "\r\n", "\n")
	}
	var sb strings.Builder
	for _, r := range b.Runs {
		sb.WriteString(r.Text)
	}
	return sb.String()
}

func headingText(b *sdk.DocBlock) string {
	if len(b.Runs) == 0 {
		return b.Text
	}
	return blockLine(b)
}

func prefixLines(text, prefix string) []string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, prefix+l)
	}
	return out
}

// asciiTable 表格 → 定宽 ASCII 表(列宽按内容计算但受预算封顶,超宽截断)。
func asciiTable(b *sdk.DocBlock, maxWidth int) []string {
	cols := len(b.Head)
	for _, r := range b.Rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	if cols == 0 {
		return nil
	}
	widths := make([]int, cols)
	set := func(i int, s string) {
		if w := runewidth.StringWidth(s); w > widths[i] {
			widths[i] = w
		}
	}
	for i, h := range b.Head {
		set(i, h)
	}
	rows := make([][]string, 0, len(b.Rows))
	for _, r := range b.Rows {
		cells := make([]string, cols)
		for i := range cells {
			if i < len(r) {
				cells[i] = strings.ReplaceAll(r[i].Text, "\n", " ")
			}
			set(i, cells[i])
		}
		rows = append(rows, cells)
	}
	// 列宽预算:总宽超过 maxWidth 时逐列收缩(最小 4)
	total := 0
	for _, w := range widths {
		total += w + 3
	}
	for total > maxWidth {
		widest := 0
		for i, w := range widths {
			if w > widths[widest] {
				_ = i
				widest = i
			}
		}
		if widths[widest] <= 4 {
			break
		}
		widths[widest]--
		total--
	}
	line := func(cells []string) string {
		var sb strings.Builder
		sb.WriteString("|")
		for i := 0; i < cols; i++ {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			sb.WriteString(" " + padRight(c, widths[i]) + " |")
		}
		return strings.TrimRight(sb.String(), " ")
	}
	var out []string
	if len(b.Head) > 0 {
		out = append(out, line(b.Head))
		sep := make([]string, cols)
		for i := range sep {
			sep[i] = strings.Repeat("-", widths[i])
		}
		out = append(out, line(sep))
	}
	for _, r := range rows {
		out = append(out, line(r))
	}
	return out
}

// padRight 按显示宽度右侧补空格(超宽截断并加省略号)。
func padRight(s string, w int) string {
	cur := runewidth.StringWidth(s)
	if cur == w {
		return s
	}
	if cur > w {
		var sb strings.Builder
		used := 0
		for _, r := range s {
			rw := runewidth.RuneWidth(r)
			if used+rw > w-1 {
				break
			}
			sb.WriteRune(r)
			used += rw
		}
		return sb.String() + "…"
	}
	return s + strings.Repeat(" ", w-cur)
}

// findPage 找页标记行(第 n 页 / 幻灯片 n)。
func (p *DocPager) findPage(n int) int {
	needles := []string{fmt.Sprintf("第 %d 页", n), fmt.Sprintf("幻灯片 %d", n)}
	for i, l := range p.Lines {
		for _, nd := range needles {
			if strings.Contains(l, nd) {
				return i
			}
		}
	}
	return -1
}

// HandleKey 处理 pager 内按键;返回 true = 关闭浮层。
func (p *DocPager) HandleKey(k tea.Key, w, h int) bool {
	page := 0
	if h > 4 {
		page = h - 4
	}
	// / 搜索输入模式:键入进缓冲,Enter 应用,Esc 取消
	if p.searching {
		switch k.Code {
		case tea.KeyEscape:
			p.searching = false
			p.searchBuf = ""
		case tea.KeyEnter:
			p.searching = false
			p.SetSearch(p.searchBuf)
		case tea.KeyBackspace:
			r := []rune(p.searchBuf)
			if len(r) > 0 {
				p.searchBuf = string(r[:len(r)-1])
			}
		default:
			if k.Text != "" {
				p.searchBuf += k.Text
			}
		}
		p.clamp()
		return false
	}
	switch k.Code {
	case tea.KeyEscape, 'q':
		return true
	case tea.KeyDown:
		p.scrollBy(1)
	case tea.KeyUp:
		p.scrollBy(-1)
	case tea.KeyPgDown:
		p.scrollBy(page)
	case tea.KeyPgUp:
		p.scrollBy(-page)
	case tea.KeyHome:
		p.Off = 0
	case tea.KeyEnd:
		p.Off = max0(len(p.Lines) - 1)
	case tea.KeyRight:
		p.HOff += 4
	case tea.KeyLeft:
		if p.HOff > 0 {
			p.HOff -= 4
		}
	case 'g':
		p.Off = 0
	case 'G':
		p.Off = max0(len(p.Lines) - 1)
	case 'n':
		p.nextHit(1)
	case 'N':
		p.nextHit(-1)
	case '/':
		p.searching = true
		p.searchBuf = ""
	}
	_ = w
	p.clamp()
	return false
}

// ScrollWheel 滚轮滚动(正 = 向上)。
func (p *DocPager) ScrollWheel(up bool) {
	if up {
		p.scrollBy(-3)
	} else {
		p.scrollBy(3)
	}
	p.clamp()
}

func (p *DocPager) scrollBy(n int) {
	p.Off += n
}

func (p *DocPager) clamp() {
	if p.Off < 0 {
		p.Off = 0
	}
	if p.Off > len(p.Lines)-1 {
		p.Off = max0(len(p.Lines) - 1)
	}
}

// SetSearch 设置搜索词并重算命中。
func (p *DocPager) SetSearch(q string) {
	p.Search = q
	p.Hits = nil
	p.HitIdx = 0
	if q == "" {
		return
	}
	for i, l := range p.Lines {
		if strings.Contains(l, q) {
			p.Hits = append(p.Hits, i)
		}
	}
	if len(p.Hits) > 0 {
		p.Off = p.Hits[0]
		p.clamp()
	}
}

func (p *DocPager) nextHit(dir int) {
	if len(p.Hits) == 0 {
		return
	}
	p.HitIdx = (p.HitIdx + dir + len(p.Hits)) % len(p.Hits)
	p.Off = p.Hits[p.HitIdx]
}

// Render 全屏渲染(标题行 + 内容窗口 + 状态行)。
func (p *DocPager) Render(w, h int) string {
	if w <= 0 || h <= 0 {
		w, h = 80, 24
	}
	p.Width, p.Height = w, h
	// 布局:标题行 1 + 内容 body + 状态行 1 = h(内容区不与输入框抢位,pager 为全屏模态)
	body := h - 2
	if body < 1 {
		body = 1
	}
	p.clamp()
	var b strings.Builder
	head := p.Title
	if p.Format != "" {
		head += "  [" + p.Format + "]"
	}
	b.WriteString(stylePick.Render(truncWidth(head, w)))
	b.WriteString("\n")
	end := p.Off + body
	if end > len(p.Lines) {
		end = len(p.Lines)
	}
	for i := p.Off; i < end; i++ {
		b.WriteString(p.renderLine(i, w))
		b.WriteString("\n")
	}
	// 填充剩余行(保持布局稳定)
	for i := end - p.Off; i < body; i++ {
		b.WriteString("\n")
	}
	hint := fmt.Sprintf("%d/%d 行 · q/Esc 关闭 · ↑↓/PgUp/PgDn 滚动 · ←→ 横移 · / 搜索", p.Off+1, len(p.Lines))
	if p.searching {
		hint = "搜索: " + p.searchBuf + " ⏎ 确认 / Esc 取消"
	} else if p.Search != "" {
		hint = fmt.Sprintf("搜索:%s(%d 命中,n/N 跳转) · %s", p.Search, len(p.Hits), hint)
	}
	line := " " + hint
	// 截断/警告提示优先让位操作提示(信息不丢:p.Status 也可经 --json/Web 面板查看)
	if p.Status != "" {
		avail := w - runewidth.StringWidth(line) - 4
		if avail > 8 {
			line = " " + truncWidth(p.Status, avail) + " · " + hint
		}
	}
	b.WriteString(styleMeta.Render(truncWidth(line, w)))
	return b.String()
}

// renderLine 单行(水平偏移 + 搜索高亮;diff 内容额外轻染色)。
func (p *DocPager) renderLine(i, w int) string {
	line := p.Lines[i]
	if p.HOff > 0 {
		line = sliceWidth(line, p.HOff)
	}
	line = truncWidth(line, w)
	// 搜索命中优先(用户显式意图),其次 diff 染色(与工具结果行一致:命中回落到普通样式)
	if p.Search != "" && strings.Contains(p.Lines[i], p.Search) {
		return highlightAll(line, p.Search)
	}
	if p.Diff {
		if styled := diffToolRow(line); styled != "" {
			return styled
		}
	}
	return styleMeta.Render(line)
}

// sliceWidth 按显示宽度截去前 n 列。
func sliceWidth(s string, n int) string {
	if n <= 0 {
		return s
	}
	used := 0
	for idx, r := range s {
		if used >= n {
			return s[idx:]
		}
		used += runewidth.RuneWidth(r)
	}
	return ""
}

// truncWidth 按显示宽度截断并加省略号。
func truncWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= w {
		return s
	}
	target := w - 1
	var sb strings.Builder
	used := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if used+rw > target {
			break
		}
		sb.WriteRune(r)
		used += rw
	}
	return sb.String() + "…"
}

// highlightAll 命中片段高亮(逐段渲染,行内 rune 不增删)。
func highlightAll(line, needle string) string {
	if needle == "" || !strings.Contains(line, needle) {
		return styleMeta.Render(line)
	}
	var sb strings.Builder
	rest := line
	for {
		idx := strings.Index(rest, needle)
		if idx < 0 {
			sb.WriteString(styleMeta.Render(rest))
			break
		}
		if idx > 0 {
			sb.WriteString(styleMeta.Render(rest[:idx]))
		}
		sb.WriteString(stylePick.Render(rest[idx : idx+len(needle)]))
		rest = rest[idx+len(needle):]
	}
	return sb.String()
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
