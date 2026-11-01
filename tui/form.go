// 整卷问卷的 TUI 底部面板(P3 扩展)。
//
// 为什么是"底部面板"而不是全屏模态(tui/docview.go 的 pager 那条路):答问卷时用户
// 常要回看上一轮结论(面板上方仍能看见会话流);pi 侧也是同一取舍(它的扩展方案里
// 明确否决了 overlay 浮层)。几何沿用 dock.go 的做法:面板行数由 Render 从主区高度里扣。
//
// 状态机与 Web 端同口径(单选→前进 / 多选→空格勾选+回车提交 / 手写覆盖 /
// 「聊聊这个」/ 确认页),只是键位映射到终端。
package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 面板几何:最少 6 行,选项窗口最多 8 行(超出滚动),总行数封到主区一半。
const (
	formMinRows   = 6
	formOptWindow = 8
)

// formItem 面板里可选的一行(真选项 + 两个伪选项)。
type formItem struct {
	Kind  string // option | other | chat
	Index int    // 真选项下标(伪选项为 -1)
	Value string
	Label string
	Desc  string
}

// formTabHit tab chip 的点击区(列范围;tab 行不换行才登记,换行会让列错位)。
type formTabHit struct {
	row, col0, col1, idx int
}

// formOptHit 选项行的点击区(行范围;带说明的选项占两行)。
type formOptHit struct {
	row0, row1, idx int
}

// QuestionForm 一个整卷问卷的面板状态(答案在 Draft 里,进程内即权威副本)。
type QuestionForm struct {
	ID     string                        // 整卷 id(App 按它定向回填作答通道)
	Group  string                        // = ID(与 sdk.Question.Group 同源)
	Title  string                        // tab 行上方标题
	Qs     []sdk.Question                // 题目(归一化后)
	Tab    int                           // [0..len]:len = 确认页
	Sel    int                           // 选项光标
	Draft  map[string]sdk.QuestionAnswer // 题 id → 作答
	Input  bool                          // 「自己输入」编辑态
	Text   string                        // 手写文本
	Notice string                        // 一次性提示
	Chat   string                        // 选了「聊聊这个」的题 id(非空 ⇒ 立即收口)
	Arm    bool                          // 整卷跳过武装(防误触,同 dock 的 x 二次确认)
	Open   bool                          // 面板是否展开(Esc 收起,进度保留)

	// 渲染期登记(鼠标命中用;Render 每帧重建)
	top     int
	tabHits []formTabHit
	optHits []formOptHit
}

// NewQuestionForm 由整卷构造面板(qn 应已归一化)。
func NewQuestionForm(qn sdk.Questionnaire) *QuestionForm {
	return &QuestionForm{
		ID:    qn.ID,
		Group: qn.ID,
		Title: qn.Title,
		Qs:    qn.Questions,
		Draft: make(map[string]sdk.QuestionAnswer, len(qn.Questions)),
		Open:  true,
	}
}

// formItems 当前题的可选行(无选项的题不给伪选项:输入框就是唯一作答路径)。
func formItems(q sdk.Question) []formItem {
	out := make([]formItem, 0, len(q.Options)+2)
	for i, o := range q.Options {
		label := o.Label
		if label == "" {
			label = o.Value
		}
		out = append(out, formItem{Kind: "option", Index: i, Value: o.Value, Label: label, Desc: o.Desc})
	}
	if len(out) > 0 && q.AllowOther {
		out = append(out, formItem{Kind: "other", Index: -1})
	}
	if len(out) > 0 && q.AllowChat {
		out = append(out, formItem{Kind: "chat", Index: -1})
	}
	return out
}

// formCurrent 当前题(确认页返回 nil)。
func (f *QuestionForm) formCurrent() *sdk.Question {
	if f.Tab < 0 || f.Tab >= len(f.Qs) {
		return nil
	}
	return &f.Qs[f.Tab]
}

// formAnswered 该题是否已作答。
func (f *QuestionForm) formAnswered(q sdk.Question) bool {
	a, ok := f.Draft[q.ID]
	return ok && !a.Empty()
}

// formAllAnswered 全部答完(确认页允许提交的前提)。
func (f *QuestionForm) formAllAnswered() bool {
	for _, q := range f.Qs {
		if !f.formAnswered(q) {
			return false
		}
	}
	return true
}

// formLabel 题的展示名(label 缺省 Q<n>)。
func formLabel(q sdk.Question, i int) string {
	if q.Label != "" {
		return q.Label
	}
	return fmt.Sprintf("Q%d", i+1)
}

// formNextTab 切题(循环,含确认页);切题清光标/手写态/武装。
func (f *QuestionForm) formNextTab(delta int) {
	n := len(f.Qs) + 1
	f.Tab = ((f.Tab+delta)%n + n) % n
	f.Sel = 0
	f.Input = false
	f.Text = ""
	f.Notice = ""
	f.Arm = false
}

// formGoTab 直接跳到某 tab。
func (f *QuestionForm) formGoTab(tab int) {
	if tab < 0 || tab > len(f.Qs) {
		return
	}
	f.Tab = tab
	f.Sel = 0
	f.Input = false
	f.Text = ""
	f.Notice = ""
	f.Arm = false
}

// formAfterAnswer 落地一题:单题问卷原地(由 formMaybeFinalize 立即收口),多题前进。
func (f *QuestionForm) formAfterAnswer() {
	if len(f.Qs) <= 1 {
		return
	}
	f.formGoTab(min(f.Tab+1, len(f.Qs)))
}

// formPick 选中第 i 行(真选项/伪选项)。
func (f *QuestionForm) formPick(i int) {
	q := f.formCurrent()
	if q == nil {
		return
	}
	items := formItems(*q)
	if i < 0 || i >= len(items) {
		return
	}
	it := items[i]
	f.Arm = false
	switch it.Kind {
	case "other":
		f.Input = true
		f.Text = ""
		f.Notice = ""
		return
	case "chat":
		f.Chat = q.ID // 「聊聊这个」:调用方见 Chat 非空即收口(转对话,不重发整卷)
		return
	}
	if q.Multiple {
		f.formToggle(it)
		return
	}
	f.Draft[q.ID] = sdk.QuestionAnswer{Values: []string{it.Value}, Index: it.Index + 1}
	f.formAfterAnswer()
}

// formToggle 多选勾选/取消一项(不前进)。
func (f *QuestionForm) formToggle(it formItem) {
	q := f.formCurrent()
	if q == nil || it.Kind != "option" {
		return
	}
	cur := f.Draft[q.ID].Values
	out := make([]string, 0, len(cur)+1)
	found := false
	for _, v := range cur {
		if v == it.Value {
			found = true
			continue
		}
		out = append(out, v)
	}
	if !found {
		out = append(out, it.Value)
	}
	f.Draft[q.ID] = sdk.QuestionAnswer{Values: out}
	f.Notice = ""
}

// formCommitMulti 多选提交本题:空集合给一次性提示,不前进(与 pi 同口径)。
func (f *QuestionForm) formCommitMulti() {
	q := f.formCurrent()
	if q == nil {
		return
	}
	if len(f.Draft[q.ID].Values) == 0 {
		f.Notice = "先按空格勾选至少一项"
		return
	}
	f.formAfterAnswer()
}

// formSubmitInput 手写落地(空白按 (no response) 记 —— 不静默丢题)。
func (f *QuestionForm) formSubmitInput() {
	q := f.formCurrent()
	if q == nil {
		return
	}
	text := strings.TrimSpace(f.Text)
	if text == "" {
		text = "(no response)"
	}
	f.Draft[q.ID] = sdk.QuestionAnswer{Text: text, Custom: true}
	f.Input = false
	f.Text = ""
	f.formAfterAnswer()
}

// formAnswer 组装整卷作答(与题序同长,未答留零值)。
func (f *QuestionForm) formAnswer(cancelled bool) sdk.QuestionnaireAnswer {
	out := sdk.QuestionnaireAnswer{Answers: make([]sdk.QuestionAnswer, len(f.Qs)), Cancelled: cancelled, Chat: f.Chat}
	for i, q := range f.Qs {
		out.Answers[i] = f.Draft[q.ID]
	}
	return out
}

// formKey 面板按键处理:返回非 nil 作答 = 整卷收口;handled=false 表示这键不归面板。
//
// 键位:↑↓ 移动 · 1-9 直选/跳题 · 空格勾选(多选) · Enter 确认/提交 ·
// Tab/←→ 切题 · Esc 手写态退出→面板收起 · 确认页 x 整卷跳过(二次确认)。
func (f *QuestionForm) formKey(k tea.Key) (*sdk.QuestionnaireAnswer, bool) {
	key := k.Code
	if f.Input {
		switch key {
		case tea.KeyEscape:
			f.Input = false
			f.Text = ""
			return nil, true
		case tea.KeyEnter:
			f.formSubmitInput()
			return f.formMaybeFinalize(true)
		case tea.KeyBackspace:
			if r := []rune(f.Text); len(r) > 0 {
				f.Text = string(r[:len(r)-1])
			}
			return nil, true
		}
		if k.Text != "" {
			f.Text += k.Text
		}
		return nil, true
	}
	q := f.formCurrent()
	items := []formItem(nil)
	if q != nil {
		items = formItems(*q)
	}
	switch key {
	case tea.KeyEscape:
		if f.Arm {
			f.Arm = false
			return nil, true
		}
		f.Open = false // 收起面板,进度/草稿全保留(状态栏与 /answer 可再打开)
		return nil, true
	case tea.KeyTab:
		if k.Mod&tea.ModShift != 0 {
			f.formNextTab(-1)
		} else {
			f.formNextTab(1)
		}
		return nil, true
	case tea.KeyRight:
		f.formNextTab(1)
		return nil, true
	case tea.KeyLeft:
		f.formNextTab(-1)
		return nil, true
	case tea.KeyUp:
		if q != nil && len(items) > 0 && f.Sel > 0 {
			f.Sel--
		}
		f.Arm = false
		return nil, true
	case tea.KeyDown:
		if q != nil && f.Sel < len(items)-1 {
			f.Sel++
		}
		f.Arm = false
		return nil, true
	case tea.KeyEnter:
		if f.Tab >= len(f.Qs) { // 确认页
			if !f.formAllAnswered() {
				f.Notice = "还有未答的题(数字键跳回)"
				return nil, true
			}
			ans := f.formAnswer(false)
			return &ans, true
		}
		if q == nil {
			return nil, true
		}
		if len(items) == 0 { // 纯自由文本题:Enter 直接进手写
			f.Input = true
			f.Text = ""
			return nil, true
		}
		if q.Multiple && items[f.Sel].Kind == "option" {
			f.formCommitMulti()
			return f.formMaybeFinalize(true)
		}
		f.formPick(f.Sel)
		return f.formMaybeFinalize(false)
	case ' ':
		if q != nil && q.Multiple && len(items) > 0 && items[f.Sel].Kind == "option" {
			f.formToggle(items[f.Sel])
		}
		return nil, true
	case 'x', 'X':
		if f.Tab >= len(f.Qs) { // 确认页:整卷跳过需二次确认
			if !f.Arm {
				f.Arm = true
				f.Notice = "再按 x/Enter 确认整卷跳过(Esc 取消)"
				return nil, true
			}
			ans := f.formAnswer(true)
			return &ans, true
		}
		return nil, false
	}
	if len(k.Text) == 1 && k.Text[0] >= '1' && k.Text[0] <= '9' {
		n := int(k.Text[0] - '1')
		if f.Tab >= len(f.Qs) { // 确认页:数字键跳回该题修改
			if n < len(f.Qs) {
				f.formGoTab(n)
			}
			return nil, true
		}
		if n < len(items) {
			f.Sel = n
			f.formPick(n)
			return f.formMaybeFinalize(false)
		}
		return nil, true
	}
	return nil, false
}

// formMaybeFinalize 单题问卷或选了「聊聊这个」:立即收口(与 Web/pi 同语义)。
//
// committed = 本次动作是否"本题已完成"(多选回车提交 / 手写落地)。**多选必须传 true**:
// 勾选某一项只是候选,单题多选若在第一次勾选时就收口,用户永远只能选到一项(第一版真踩过)。
func (f *QuestionForm) formMaybeFinalize(committed bool) (*sdk.QuestionnaireAnswer, bool) {
	single := len(f.Qs) <= 1
	if f.Chat == "" && !single {
		return nil, true
	}
	if f.Chat == "" && single {
		if q := f.formCurrent(); q != nil && q.Multiple && !committed {
			return nil, true // 单题多选:攒够了再按 Enter
		}
		if !f.formAllAnswered() {
			return nil, true // 单题还没答出来(空提交等):不急着收口
		}
	}
	ans := f.formAnswer(false)
	return &ans, true
}

// formClick 鼠标点击(relRow = 相对面板首行,col 为终端列):tab 切题 / 选项选中。
// 点真选项与键盘同一语义(单选即收口/多选只勾选);返回非 nil 作答 = 整卷收口。
func (f *QuestionForm) formClick(relRow, col int) (*sdk.QuestionnaireAnswer, bool) {
	if f.Input {
		return nil, false // 手写态不用鼠标,避免误点丢草稿
	}
	for _, h := range f.tabHits {
		if h.row == relRow && col >= h.col0 && col <= h.col1 {
			f.formGoTab(h.idx)
			return nil, true
		}
	}
	for _, h := range f.optHits {
		if relRow >= h.row0 && relRow <= h.row1 {
			f.Sel = h.idx
			f.formPick(h.idx)
			return f.formMaybeFinalize(false)
		}
	}
	return nil, false
}

// formPanelLines 渲染面板(行数即主区要扣的高度),同时登记鼠标命中区。
//
// 行结构:头行 → tab 行 → 题干 → hint → 选项窗口 → 提示 → 帮助。
// 输入态与确认页各走一支,三支都以 maxRows 为上界(几何不忽高忽低)。
func formPanelLines(s *State, width, height int) []string {
	f := s.Form
	if f == nil || !f.Open {
		return nil
	}
	f.tabHits = f.tabHits[:0]
	f.optHits = f.optHits[:0]
	w := width - 2
	if w < 12 {
		w = 12
	}
	maxRows := height / 2
	if maxRows < formMinRows {
		maxRows = formMinRows
	}
	var lines []string
	title := f.Title
	if title == "" {
		title = "问卷"
	}
	head := fmt.Sprintf("❓ %s(%d 题)", title, len(f.Qs))
	lines = append(lines, styleMeta.Render(" "+truncateVisible(head, w)))

	// tab 行:■ 已答 / □ 未答 / ✓ 提交(超宽整体截断,不换行 —— 换行会让列命中区错位)
	var chips []string
	col := 1
	tabRow := len(lines)
	for i, q := range f.Qs {
		box := "□"
		style := styleMeta
		if f.formAnswered(q) {
			box = "■"
			style = styleToolOK
		}
		if i == f.Tab {
			style = stylePick
		}
		text := fmt.Sprintf(" %s %s ", box, formLabel(q, i))
		chips = append(chips, style.Render(text))
		n := len([]rune(text))
		f.tabHits = append(f.tabHits, formTabHit{row: tabRow, col0: col, col1: col + n - 1, idx: i})
		col += n
	}
	submit := " ✓ 提交 "
	switch {
	case f.Tab >= len(f.Qs):
		chips = append(chips, stylePick.Render(submit))
	case f.formAllAnswered():
		chips = append(chips, styleToolOK.Render(submit))
	default:
		chips = append(chips, styleMeta.Render(submit))
	}
	// 提交位始终可点(答完前点它只是跳到确认页看缺哪题,不会提交)
	f.tabHits = append(f.tabHits, formTabHit{row: tabRow, col0: col, col1: col + len([]rune(submit)) - 1, idx: len(f.Qs)})
	lines = append(lines, truncateVisible(" "+strings.Join(chips, ""), width))

	q := f.formCurrent()
	if q == nil { // 确认页
		lines = append(lines, stylePick.Render(" 确认提交"))
		room := maxRows - len(lines) - 1
		if room < 1 {
			room = 1
		}
		shown := 0
		for i, qq := range f.Qs {
			if shown >= room {
				lines = append(lines, styleMeta.Render(fmt.Sprintf("   … 还有 %d 题", len(f.Qs)-i)))
				break
			}
			a := f.Draft[qq.ID]
			text := "(未答)"
			style := styleError
			if !a.Empty() {
				style = styleAsst
				if a.Custom || strings.TrimSpace(a.Text) != "" {
					text = "(手写) " + a.Text
				} else {
					var labels []string
					for _, v := range a.Values {
						lab := v
						for _, o := range qq.Options {
							if o.Value == v {
								if o.Label != "" {
									lab = o.Label
								} else if o.Desc != "" {
									lab = o.Desc
								}
								break
							}
						}
						labels = append(labels, lab)
					}
					text = strings.Join(labels, "、")
				}
			}
			lines = append(lines, truncateVisible(fmt.Sprintf("  %d. %s: ", i+1, formLabel(qq, i))+style.Render(text), width))
			shown++
		}
		if f.formAllAnswered() {
			lines = append(lines, styleToolOK.Render(" Enter 提交 · 数字键跳回修改"))
		} else {
			lines = append(lines, styleError.Render(" 还有未答的题:数字键跳回修改"))
		}
		if f.Notice != "" {
			lines = append(lines, styleError.Render(" "+truncateVisible(f.Notice, w)))
		} else {
			lines = append(lines, styleMeta.Render(" x 整卷跳过(二次确认) · Esc 收起面板(进度保留)"))
		}
		return lines
	}

	// 题干 + hint(限行:模型可能把整段需求塞进 prompt,不封顶会把主区挤没)
	lines = append(lines, capWrapped(wrapToLines(q.Prompt, w), 4)...)
	if q.Hint != "" {
		lines = append(lines, capWrappedStyled(wrapToLines(q.Hint, w), 2)...)
	}

	if f.Input {
		lines = append(lines, "")
		cursor := "▌"
		lines = append(lines, truncateVisible(" 你的回答: "+f.Text+cursor, width))
		lines = append(lines, styleMeta.Render(" Enter 提交 · Esc 返回选项"))
		return lines
	}

	// 选项窗口(带 desc 的选项占两行;超出可用行数就滚动,不再追加)
	items := formItems(*q)
	room := maxRows - len(lines) - 2 // 留提示/帮助两行
	if room < 1 {
		room = 1
	}
	start := 0
	if f.Sel >= start+formOptWindow {
		start = f.Sel - formOptWindow + 1
	}
	if f.Sel < start {
		start = f.Sel
	}
	if maxScroll := len(items) - formOptWindow; start > maxScroll && maxScroll > 0 {
		start = maxScroll
	}
	if start > 0 {
		lines = append(lines, styleMeta.Render(fmt.Sprintf("  ↑ 还有 %d 项", start)))
	}
	used := 0
	for i := start; i < len(items) && used < room; i++ {
		it := items[i]
		mark := "  "
		style := styleAsst
		if i == f.Sel {
			mark = "▸ "
			style = stylePick
		}
		box := ""
		if q.Multiple && it.Kind == "option" {
			if formHasValue(f.Draft[q.ID].Values, it.Value) {
				box = "[x] "
			} else {
				box = "[ ] "
			}
		}
		num := fmt.Sprintf("%d.", i+1)
		var text string
		switch it.Kind {
		case "other":
			text = "✎ 自己输入…"
		case "chat":
			text = "💬 聊聊这个"
		default:
			text = it.Label
			if !q.Multiple && formHasValue(f.Draft[q.ID].Values, it.Value) {
				text += " ●"
			}
		}
		row0 := len(lines)
		lines = append(lines, truncateVisible(" "+mark+box+num+" "+style.Render(text), width))
		used++
		if it.Kind == "option" && it.Desc != "" && used < room {
			for _, ln := range wrapToLines(it.Desc, w-5) {
				lines = append(lines, styleMeta.Render("      "+ln))
				used++
			}
		}
		f.optHits = append(f.optHits, formOptHit{row0: row0, row1: len(lines) - 1, idx: i})
	}
	if rest := len(items) - (start + used); rest > 0 && used > 0 {
		lines = append(lines, styleMeta.Render(fmt.Sprintf("  ↓ 还有 %d 项(↑↓ 移动)", rest)))
	}

	if f.Notice != "" {
		lines = append(lines, styleError.Render(" "+truncateVisible(f.Notice, w)))
	} else if q.Multiple {
		lines = append(lines, styleMeta.Render(" 空格勾选 · Enter 提交本题 · 数字键直选 · Tab 切题 · Esc 收起"))
	} else {
		lines = append(lines, styleMeta.Render(" 数字键/Enter 选择 · ↑↓ 移动 · Tab 切题 · Esc 收起(进度保留)"))
	}
	return capPanel(lines, maxRows)
}

// capWrapped 普通文本折行并限行(超出的尾部用省略行收口)。
func capWrapped(all []string, max int) []string {
	out := make([]string, 0, max+1)
	for i, ln := range all {
		if i >= max {
			out = append(out, " …") // 多余行整体省略(宽度由渲染层的 pad/截断兜底)
			break
		}
		out = append(out, " "+ln)
	}
	return out
}

// capWrappedStyled 灰字(hint)折行并限行(样式由调用方决定,这里统一用 styleMeta)。
func capWrappedStyled(all []string, max int) []string {
	out := make([]string, 0, max+1)
	for i, ln := range all {
		if i >= max {
			out = append(out, styleMeta.Render(" …"))
			break
		}
		out = append(out, styleMeta.Render(" "+ln))
	}
	return out
}

// capPanel 面板总高度兜底:超过 maxRows 时截断中间内容,**保留最后一行**(帮助/提示),
// 否则用户看到半屏选项却不知道能按什么键。
func capPanel(lines []string, maxRows int) []string {
	if len(lines) <= maxRows {
		return lines
	}
	help := lines[len(lines)-1]
	out := append([]string(nil), lines[:maxRows-2]...)
	out = append(out, styleMeta.Render(" …(终端偏小,已省略部分内容)"), help)
	return out
}

// formHasValue 值集合是否包含 v。
func formHasValue(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}
