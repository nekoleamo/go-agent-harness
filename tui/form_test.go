// 整卷问卷 TUI 面板单测(P3 扩展):键位状态机 / 渲染与命中区 / 面板几何 / Model 接线。
package tui

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stripANSITest 去掉 ANSI 转义(断言文案用;tui 包内已有 stripANSI 但签名不同)。
func stripANSITest(s string) string {
	re := regexp.MustCompile("\x1b\\[[0-9;]*m")
	return re.ReplaceAllString(s, "")
}

func hasVal(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

func formFixture() sdk.Questionnaire {
	return sdk.NormalizeQuestionnaire(sdk.Questionnaire{Title: "本轮待确认", Questions: []sdk.Question{
		{
			ID: "scope", Label: "Scope", Prompt: "范围到哪?", Hint: "决定切片",
			Options:    []sdk.QuestionOption{{Value: "web", Label: "只做 Web", Desc: "最快"}, {Value: "all", Label: "三端"}},
			AllowOther: true, AllowChat: true,
		},
		{
			ID: "prio", Label: "优先级", Prompt: "做哪些?",
			Options: []sdk.QuestionOption{{Value: "a", Label: "A"}, {Value: "b", Label: "B"}}, Multiple: true,
			AllowOther: true, AllowChat: true,
		},
		{ID: "free", Label: "补充", Prompt: "还有什么?", FreeText: true},
	}})
}

func formKeyOf(code rune) tea.Key {
	k := tea.Key{Code: code}
	if code >= 32 && code < 127 {
		k.Text = string(code) // 真实输入里可打印键同时带 Text(bubbletea 语义)
	}
	return k
}

// TestFormSinglePickAdvances 单选落地 → 自动前进到下一题,记 Values+Index。
func TestFormSinglePickAdvances(t *testing.T) {
	f := NewQuestionForm(formFixture())
	if _, handled := f.formKey(formKeyOf('1')); !handled {
		t.Fatal("数字键应被面板接管")
	}
	got := f.Draft["scope"]
	if len(got.Values) != 1 || got.Values[0] != "web" || got.Index != 1 {
		t.Fatalf("单选应记 values+index: %+v", got)
	}
	if f.Tab != 1 {
		t.Fatalf("单选后应前进到第 2 题, got tab=%d", f.Tab)
	}
}

// TestFormMultiRequiresSelection 多选:空格勾选,空集合回车只提示不前进。
func TestFormMultiRequiresSelection(t *testing.T) {
	f := NewQuestionForm(formFixture())
	f.formGoTab(1)
	if _, handled := f.formKey(formKeyOf(tea.KeyEnter)); !handled || f.Tab != 1 {
		t.Fatalf("空集合回车应停在本题: tab=%d handled=%v", f.Tab, handled)
	}
	if !strings.Contains(f.Notice, "勾选") {
		t.Fatalf("空集合应给提示: %q", f.Notice)
	}
	f.formKey(formKeyOf(' ')) // 勾选 A
	if len(f.Draft["prio"].Values) != 1 {
		t.Fatalf("空格应勾选一项: %+v", f.Draft["prio"])
	}
	f.formKey(formKeyOf(tea.KeyEnter)) // 提交本题
	if f.Tab != 2 {
		t.Fatalf("提交后应前进: tab=%d", f.Tab)
	}
}

// TestFormInputModeOverride 手写:进输入态 → 逐字上屏 → Enter 落地覆盖该题。
func TestFormInputModeOverride(t *testing.T) {
	f := NewQuestionForm(formFixture())
	// 选项行:0/1 真选项,2 ✎ 自己输入,3 💬 聊聊这个
	f.Sel = 2
	f.formPick(2)
	if !f.Input {
		t.Fatal("选「自己输入」应进输入态")
	}
	for _, s := range []string{"只", "做", "W"} {
		f.formKey(tea.Key{Code: []rune(s)[0], Text: s})
	}
	if f.Text != "只做W" {
		t.Fatalf("手写文本应累积: %q", f.Text)
	}
	ans, handled := f.formKey(formKeyOf(tea.KeyEnter))
	if !handled || ans != nil {
		t.Fatalf("多题问卷的手写提交不应立即收口: %+v %v", ans, handled)
	}
	if got := f.Draft["scope"]; got.Text != "只做W" || !got.Custom || len(got.Values) != 0 {
		t.Fatalf("手写应覆盖该题且 custom=true: %+v", got)
	}
	if f.Tab != 1 {
		t.Fatalf("手写后应前进: tab=%d", f.Tab)
	}
}

// TestFormChatEscapeHatch 「聊聊这个」:立刻收口,并带 chat=题 id。
func TestFormChatEscapeHatch(t *testing.T) {
	f := NewQuestionForm(formFixture())
	f.Sel = 3 // 💬 聊聊这个
	ans, handled := func() (*sdk.QuestionnaireAnswer, bool) {
		f.formPick(3)
		return f.formMaybeFinalize(true)
	}()
	if !handled || ans == nil {
		t.Fatal("「聊聊这个」应立即收口")
	}
	if ans.Chat != "scope" || ans.Cancelled {
		t.Fatalf("chat 口径不符: %+v", ans)
	}
}

// TestFormConfirmSubmitAndSkip 确认页:未答完不给提交;答完 Enter 提交;x 需二次确认跳过。
func TestFormConfirmSubmitAndSkip(t *testing.T) {
	f := NewQuestionForm(formFixture())
	f.formGoTab(len(f.Qs))
	if ans, _ := f.formKey(formKeyOf(tea.KeyEnter)); ans != nil {
		t.Fatal("未答完不应提交")
	}
	if f.Notice == "" {
		t.Fatal("未答完应提示")
	}
	// 数字键跳回 → 逐题答完
	for i := 0; i < len(f.Qs); i++ {
		f.formGoTab(i)
		if len(f.Qs[i].Options) > 0 {
			f.formPick(0)
		} else {
			f.Sel = 0
			f.formPick(0) // 无选项题:formPick 无动作 → 用 Enter 进手写
			f.formKey(formKeyOf(tea.KeyEnter))
			f.formKey(tea.Key{Code: '好', Text: "好"})
			f.formKey(formKeyOf(tea.KeyEnter))
		}
	}
	if !f.formAllAnswered() {
		t.Fatalf("应全部答完: %+v", f.Draft)
	}
	f.formGoTab(len(f.Qs))
	ans, _ := f.formKey(formKeyOf('x'))
	if ans != nil || !f.Arm {
		t.Fatal("第一次 x 只武装,不收口")
	}
	ans, _ = f.formKey(formKeyOf('x'))
	if ans == nil || !ans.Cancelled {
		t.Fatalf("第二次 x 应收口为整卷跳过: %+v", ans)
	}
	// 重新来一遍走 Enter 提交(非取消)
	g := NewQuestionForm(formFixture())
	for i := 0; i < len(g.Qs); i++ {
		g.formGoTab(i)
		if len(g.Qs[i].Options) > 0 {
			g.formPick(0)
		} else {
			g.formKey(formKeyOf(tea.KeyEnter))
			g.formKey(tea.Key{Code: '好', Text: "好"})
			g.formKey(formKeyOf(tea.KeyEnter))
		}
	}
	g.formGoTab(len(g.Qs))
	ok, _ := g.formKey(formKeyOf(tea.KeyEnter))
	if ok == nil || ok.Cancelled {
		t.Fatalf("全部答完 Enter 应正常提交: %+v", ok)
	}
	if len(ok.Answers) != 3 {
		t.Fatalf("作答应与题序等长: %+v", ok.Answers)
	}
}

// TestFormEscapeCollapsesKeepsProgress Esc = 收起面板(进度保留),再次渲染仍能看到 ■。
func TestFormEscapeCollapsesKeepsProgress(t *testing.T) {
	f := NewQuestionForm(formFixture())
	f.formPick(0)
	if _, handled := f.formKey(formKeyOf(tea.KeyEscape)); !handled {
		t.Fatal("Esc 应被面板接管")
	}
	if f.Open {
		t.Fatal("Esc 应收起面板")
	}
	if !f.formAnswered(f.Qs[0]) {
		t.Fatal("收起不应丢进度")
	}
	s := &State{Form: f}
	if lines := formPanelLines(s, 60, 20); lines != nil {
		t.Fatalf("收起态不应渲染面板: %v", lines)
	}
	f.Open = true
	if lines := formPanelLines(s, 60, 20); len(lines) == 0 {
		t.Fatal("展开态应渲染面板")
	}
}

// TestFormPanelRenderAndHits 渲染:tab ■/□、题干/hint、选项编号;命中区可切题/选中。
func TestFormPanelRenderAndHits(t *testing.T) {
	f := NewQuestionForm(formFixture())
	f.formPick(0) // 第 1 题已答 → tab 显示 ■
	f.formGoTab(1)
	s := &State{Form: f}
	lines := formPanelLines(s, 70, 24)
	body := strings.Join(lines, "\n")
	if !strings.Contains(body, "■ Scope") || !strings.Contains(body, "□ 优先级") {
		t.Fatalf("tab 栏应标已答/未答: %s", body)
	}
	if !strings.Contains(body, "做哪些?") {
		t.Fatalf("应渲染题干: %s", body)
	}
	if len(s.Form.tabHits) != len(f.Qs)+1 {
		t.Fatalf("tab 命中区应覆盖每题 + 提交位: %d", len(s.Form.tabHits))
	}
	if len(s.Form.optHits) == 0 {
		t.Fatal("选项应有命中区")
	}
	// 点第 3 个 tab chip → 跳到第 3 题
	hit := s.Form.tabHits[2]
	if _, ok := f.formClick(hit.row, hit.col0); !ok {
		t.Fatal("点 tab chip 应命中")
	}
	if f.Tab != 2 {
		t.Fatalf("点 chip 应切到第 3 题: tab=%d", f.Tab)
	}
	// 点选项行 → 选中(第 3 题为纯自由文本,先回到第 2 题)
	f.formGoTab(1)
	lines = formPanelLines(s, 70, 24)
	_ = lines
	opt := s.Form.optHits[1]
	if _, ok := f.formClick(opt.row0, 3); !ok {
		t.Fatal("点选项行应命中")
	}
	if !hasVal(f.Draft["prio"].Values, "b") {
		t.Fatalf("点选项应勾选 b: %+v", f.Draft["prio"])
	}
}

// TestFormPanelRowsBounded 面板行数有上界(24 行终端不超过一半 + 最小行数),不压没会话流。
func TestFormPanelRowsBounded(t *testing.T) {
	qn := formFixture()
	q := qn.Questions[0]
	for i := 0; i < 20; i++ {
		q.Options = append(q.Options, sdk.QuestionOption{Value: "v", Label: "选项", Desc: "说明"})
	}
	qn.Questions[0] = q
	f := NewQuestionForm(qn)
	s := &State{Form: f}
	for _, h := range []int{16, 24, 40} {
		lines := formPanelLines(s, 80, h)
		limit := h/2 + 2
		if h/2 < formMinRows {
			limit = formMinRows + 2
		}
		if len(lines) > limit {
			t.Fatalf("%d 行终端下面板 %d 行,超上界 %d", h, len(lines), limit)
		}
	}
}

// TestRenderShrinksMainAndPlacesPanel 面板行数从主区扣除,且面板在输入区上方(绝对行号可算)。
func TestRenderShrinksMainAndPlacesPanel(t *testing.T) {
	s := &State{}
	s.Lines = append(s.Lines, Line{Kind: "user", Text: "问题"})
	base := Render(s, 80, 30)
	s.Form = NewQuestionForm(formFixture())
	withPanel := Render(s, 80, 30)
	if len(strings.Split(withPanel, "\n")) <= len(strings.Split(base, "\n")) {
		t.Fatal("面板应让整帧变高(主区扣减 + 面板行)")
	}
	if s.Form.top <= 0 {
		t.Fatalf("面板首行绝对行号应写入(鼠标命中用), got %d", s.Form.top)
	}
	lines := strings.Split(withPanel, "\n")
	if s.Form.top >= len(lines) {
		t.Fatalf("面板行号越界: top=%d 行数=%d", s.Form.top, len(lines))
	}
	block := strings.Join(lines[s.Form.top:], "\n")
	if !strings.Contains(block, "Scope") || !strings.Contains(block, "范围到哪?") {
		t.Fatalf("面板应从 tab 行开始(含标签与题干), got %q", block[:min(len(block), 120)])
	}
}

// TestModelQuestionnaireLifecycle 接线:消息建面板 → 作答收口回调 → gone 关面板。
func TestModelQuestionnaireLifecycle(t *testing.T) {
	a := commandTestApp()
	m := a.model
	m.w, m.h = 80, 30
	var gotID string
	var gotAns sdk.QuestionnaireAnswer
	m.onQuestionnaire = func(id string, ans sdk.QuestionnaireAnswer) {
		gotID, gotAns = id, ans
	}
	qn := formFixture()
	m.Update(questionnaireMsg{qn: qn})
	if m.state.Form == nil || m.state.Form.ID != qn.ID {
		t.Fatal("整卷消息应开面板")
	}
	// 重复补推不重建(保留进度)
	m.state.Form.formPick(0)
	m.Update(questionnaireMsg{qn: qn})
	if !m.state.Form.formAnswered(m.state.Form.Qs[0]) {
		t.Fatal("重连补推不应清空进度")
	}
	// 数字键作答 → 多题不进确认页;直接走确认页提交
	f := m.state.Form
	f.formGoTab(len(f.Qs))
	m.state.Form.Draft[f.Qs[0].ID] = sdk.QuestionAnswer{Values: []string{"web"}}
	m.state.Form.Draft[f.Qs[1].ID] = sdk.QuestionAnswer{Values: []string{"a"}}
	m.state.Form.Draft[f.Qs[2].ID] = sdk.QuestionAnswer{Text: "好", Custom: true}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if gotID != qn.ID || len(gotAns.Answers) != 3 {
		t.Fatalf("收口应按 id 回填整卷作答: id=%q ans=%+v", gotID, gotAns)
	}
	if m.state.Form != nil {
		t.Fatal("收口后面板应关闭")
	}
	// gone 消息:其它渠道已答 → 关面板
	m.Update(questionnaireMsg{qn: qn})
	m.Update(questionnaireGoneMsg{id: qn.ID})
	if m.state.Form != nil {
		t.Fatal("gone 消息应关面板")
	}
}

// TestModelMouseClicksPanel 鼠标:面板区域的绝对行号点击落到对应选项。
func TestModelMouseClicksPanel(t *testing.T) {
	a := commandTestApp()
	m := a.model
	m.w, m.h = 80, 30
	qn := formFixture()
	m.Update(questionnaireMsg{qn: qn})
	Render(m.state, 80, 30) // 渲染一次:写面板 top 与命中区
	f := m.state.Form
	if f == nil || len(f.optHits) == 0 {
		t.Fatal("渲染后应有选项命中区")
	}
	hit := f.optHits[1]
	m.handleMousePress(tea.Mouse{Y: f.top + hit.row0, X: 4})
	if !hasVal(f.Draft["scope"].Values, "all") {
		t.Fatalf("点第二项应选中 all: %+v", f.Draft["scope"])
	}
}

// TestAppPresentQuestionnaireRoundTrip App 侧的整卷通道:PresentQuestionnaire 返回的通道
// 必须被"面板收口"这一个动作定向回填(fusion 靠它拿答案;sdk 侧再拼给模型)。
func TestAppPresentQuestionnaireRoundTrip(t *testing.T) {
	a := commandTestApp()
	qn := formFixture()
	ch, cancel, err := a.PresentQuestionnaire(context.Background(), qn)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	// 未启动 TUI 时 PresentQuestionnaire 只登记通道(生产里由 program.Send 投递同样的消息),
	// 这里手工投递,等价于运行期那一跳。
	a.model.Update(questionnaireMsg{qn: sdk.NormalizeQuestionnaire(qn)})
	if a.model.state.Form == nil {
		t.Fatal("整卷消息应建好面板")
	}
	// 面板作答 → 收口(与真实按键同一条路径)
	a.model.state.Form.formPick(0)
	a.model.state.Form.formGoTab(len(qn.Questions))
	for i, q := range qn.Questions {
		if !a.model.state.Form.formAnswered(q) {
			a.model.state.Form.Draft[q.ID] = sdk.QuestionAnswer{Text: "好", Custom: true}
		}
		_ = i
	}
	a.model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	select {
	case ans := <-ch:
		if len(ans.Answers) != len(qn.Questions) {
			t.Fatalf("作答应与题序等长: %+v", ans)
		}
		if len(ans.Answers[0].Values) == 0 {
			t.Fatalf("第 1 题应带选中值: %+v", ans.Answers[0])
		}
	case <-time.After(time.Second):
		t.Fatal("面板收口后应向 Present 通道回填作答")
	}
	// cancel 幂等(其它渠道已答/超时):再调一次不 panic、不阻塞
	cancel()
	cancel()
}

// TestFormSingleMultiSelectNeedsEnter 单题多选:勾选不等于提交(必须先按 Enter 收口)。
// 这条是"单题自动收口"与"多选要攒几项"的交界 —— 第一版就在这里把多选题按第一项交上去了。
func TestFormSingleMultiSelectNeedsEnter(t *testing.T) {
	qn := sdk.NormalizeQuestionnaire(sdk.Questionnaire{Questions: []sdk.Question{
		{ID: "pick", Label: "选哪些", Prompt: "多选:", Multiple: true,
			Options: []sdk.QuestionOption{{Value: "a", Label: "A"}, {Value: "b", Label: "B"}}},
	}})
	f := NewQuestionForm(qn)
	// 空格勾第一项:不应收口
	if ans, handled := f.formKey(formKeyOf(' ')); !handled || ans != nil {
		t.Fatalf("勾选不应立即收口: ans=%+v handled=%v", ans, handled)
	}
	// 数字键勾第二项:同样不收口
	if ans, _ := f.formKey(formKeyOf('2')); ans != nil {
		t.Fatalf("数字键勾选不应立即收口: %+v", ans)
	}
	if got := f.Draft["pick"].Values; len(got) != 2 {
		t.Fatalf("应勾中两项: %+v", got)
	}
	// Enter 提交本题 → 收口
	ans, handled := f.formKey(formKeyOf(tea.KeyEnter))
	if !handled || ans == nil {
		t.Fatal("Enter 应提交本题并收口")
	}
	if len(ans.Answers) != 1 || len(ans.Answers[0].Values) != 2 {
		t.Fatalf("收口答案应含勾中的两项: %+v", ans.Answers)
	}
}

// TestFormLongPromptCapped 超长题干/说明不能把主区挤没:面板总行数有硬上界,且最后一行仍是帮助行。
func TestFormLongPromptCapped(t *testing.T) {
	long := strings.Repeat("这一句用来把题干撑到很多行,", 200)
	qn := sdk.NormalizeQuestionnaire(sdk.Questionnaire{Questions: []sdk.Question{
		{ID: "q", Label: "长", Prompt: long, Hint: long,
			Options: []sdk.QuestionOption{{Value: "a", Label: "A", Desc: long}, {Value: "b", Label: "B"}}},
	}})
	f := NewQuestionForm(qn)
	s := &State{Form: f}
	for _, h := range []int{16, 24, 40} {
		lines := formPanelLines(s, 80, h)
		maxRows := h / 2
		if maxRows < formMinRows {
			maxRows = formMinRows
		}
		if len(lines) > maxRows {
			t.Fatalf("%d 行终端下面板 %d 行,超上界 %d", h, len(lines), maxRows)
		}
		if last := stripANSITest(lines[len(lines)-1]); !strings.Contains(last, "Esc") {
			t.Fatalf("%d 行终端下最后一行应是帮助行, got %q", h, last)
		}
	}
}
