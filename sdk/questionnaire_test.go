// 多题问卷契约单测:归一化 / 整卷能力探测与逐题降级 / 部分作答保留 / 摘要 / 事件面。
package sdk

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// batchStub 只实现 QuestionService 的桩(用于验证降级路径)。
type batchStub struct {
	asked   []Question
	answers []QuestionAnswer
	failAt  int // 从 1 起;0 = 不失败
}

func (s *batchStub) Ask(_ context.Context, q Question) (QuestionAnswer, error) {
	s.asked = append(s.asked, q)
	if s.failAt > 0 && len(s.asked) == s.failAt {
		return QuestionAnswer{}, errors.New("取消")
	}
	if len(s.answers) >= len(s.asked) {
		return s.answers[len(s.asked)-1], nil
	}
	return QuestionAnswer{}, nil
}
func (s *batchStub) RegisterQuestioner(string, QuestionPresenter) Disposer { return func() {} }
func (s *batchStub) AskQuestionnaire(context.Context, Questionnaire) (QuestionnaireAnswer, error) {
	return QuestionnaireAnswer{}, errors.New("不应走整卷") // 由 batchSvc 覆盖,这里只是保证接口闭包
}

// batchSvc 整卷能力桩:记录收到的整卷,回固定作答。
type batchSvc struct {
	batchStub
	got Questionnaire
	ans QuestionnaireAnswer
}

func (s *batchSvc) AskQuestionnaire(_ context.Context, qn Questionnaire) (QuestionnaireAnswer, error) {
	s.got = qn
	return s.ans, nil
}

func qnFixture() Questionnaire {
	return Questionnaire{Title: "本轮待确认", Questions: []Question{
		{ID: "scope", Label: "Scope", Prompt: "范围到哪?", Options: []QuestionOption{{Value: "a", Label: "只做 Web"}}},
		{ID: "prio", Label: "优先级", Prompt: "先做哪个?"},
	}}
}

// TestNormalizeQuestionnaireDefaults 归一化只补"能定位到题"的字段,不动已有值。
func TestNormalizeQuestionnaireDefaults(t *testing.T) {
	got := NormalizeQuestionnaire(qnFixture())
	if got.ID == "" {
		t.Fatal("整卷 id 应补齐")
	}
	if got.Questions[0].ID != "scope" {
		t.Fatalf("已有 id 不应被覆盖: %q", got.Questions[0].ID)
	}
	if got.Questions[1].Index != 2 || got.Questions[1].Group != got.ID {
		t.Fatalf("Index/Group 应补齐: %+v", got.Questions[1])
	}
	// 缺 Label 的题按位置补 Q<N>;已有的保持
	noLabel := NormalizeQuestionnaire(Questionnaire{Questions: []Question{{Prompt: "x"}, {Prompt: "y"}}})
	if noLabel.Questions[1].Label != "Q2" {
		t.Fatalf("缺 Label 应按位置补: %+v", noLabel.Questions[1])
	}
	if noLabel.Questions[0].ID == noLabel.Questions[1].ID {
		t.Fatal("每题的兜底 id 必须互不相同")
	}
	if noLabel.Questions[0].ID == "" || !strings.HasPrefix(noLabel.Questions[0].ID, noLabel.ID+"-") {
		t.Fatalf("兜底 id 应派生自整卷 id: %q / %q", noLabel.ID, noLabel.Questions[0].ID)
	}
	// 不修改入参(纯函数)
	in := qnFixture()
	_ = NormalizeQuestionnaire(in)
	if in.Questions[0].Group != "" {
		t.Fatal("归一化不应修改入参")
	}
}

// TestAskQuestionnairePrefersBatch 有整卷能力时走整卷(不退化成一题一次)。
func TestAskQuestionnairePrefersBatch(t *testing.T) {
	svc := &batchSvc{ans: QuestionnaireAnswer{Answers: []QuestionAnswer{{Values: []string{"a"}}, {}}}}
	got, err := AskQuestionnaire(context.Background(), svc, qnFixture())
	if err != nil {
		t.Fatal(err)
	}
	if len(svc.asked) != 0 {
		t.Fatalf("不应逐题 Ask: %d 次", len(svc.asked))
	}
	if svc.got.ID == "" || len(svc.got.Questions) != 2 {
		t.Fatalf("整卷应被归一化后送达: %+v", svc.got)
	}
	if len(got.Answers) != 2 || got.Answers[0].Values[0] != "a" {
		t.Fatalf("作答应原样返回: %+v", got)
	}
}

// TestAskQuestionnaireFallsBackSequential 无整卷能力 ⇒ 逐题 Ask(顺序与题序一致)。
func TestAskQuestionnaireFallsBackSequential(t *testing.T) {
	svc := &batchStub{answers: []QuestionAnswer{{Values: []string{"a"}}, {Text: "手写"}}}
	// batchStub 实现了 QuestionnaireService,这里换一个只实现 QuestionService 的包装
	only := struct{ QuestionService }{QuestionService: &plainStub{inner: svc}}
	got, err := AskQuestionnaire(context.Background(), only, qnFixture())
	if err != nil {
		t.Fatal(err)
	}
	if len(svc.asked) != 2 {
		t.Fatalf("应逐题问 2 次,实际 %d", len(svc.asked))
	}
	if svc.asked[0].Prompt != "范围到哪?" || svc.asked[1].Prompt != "先做哪个?" {
		t.Fatalf("顺序应与题序一致: %+v", svc.asked)
	}
	if got.Answers[1].Text != "手写" {
		t.Fatalf("作答应按题序回填: %+v", got.Answers)
	}
	if got.Cancelled || got.Chat != "" {
		t.Fatalf("正常完成不应带取消/chat: %+v", got)
	}
}

// plainStub 只暴露 QuestionService 面(剥离可选整卷能力)。
type plainStub struct{ inner QuestionService }

func (p *plainStub) Ask(ctx context.Context, q Question) (QuestionAnswer, error) {
	return p.inner.Ask(ctx, q)
}
func (p *plainStub) RegisterQuestioner(n string, pr QuestionPresenter) Disposer {
	return p.inner.RegisterQuestioner(n, pr)
}

// TestAskQuestionnaireSequentialKeepsPartial 中途失败:已答的题保留(不整体清零)。
func TestAskQuestionnaireSequentialKeepsPartial(t *testing.T) {
	svc := &batchStub{answers: []QuestionAnswer{{Values: []string{"a"}}}, failAt: 2}
	got, err := AskQuestionnaireSequential(context.Background(), svc, NormalizeQuestionnaire(qnFixture()))
	if err == nil {
		t.Fatal("第二题失败应返回错误")
	}
	if len(got.Answers) != 2 || got.Answers[0].Values[0] != "a" {
		t.Fatalf("已答部分应保留且按题序保位: %+v", got.Answers)
	}
	if !got.Answers[1].Empty() {
		t.Fatalf("未答的题应为零值: %+v", got.Answers[1])
	}
}

// TestAskQuestionnaireNoService 未装配:显式错误(不静默假答)。
func TestAskQuestionnaireNoService(t *testing.T) {
	if _, err := AskQuestionnaire(context.Background(), nil, qnFixture()); err == nil ||
		!strings.Contains(err.Error(), "未装配") {
		t.Fatalf("未装配应显式报错: %v", err)
	}
}

// TestQuestionnaireSummary 摘要:逐题/未答/取消/chat 四种口径。
func TestQuestionnaireSummary(t *testing.T) {
	qn := NormalizeQuestionnaire(qnFixture())
	got := QuestionnaireSummary(qn, QuestionnaireAnswer{Answers: []QuestionAnswer{{Values: []string{"a"}}}})
	if !strings.Contains(got, "Scope=") || !strings.Contains(got, "优先级=(未答)") {
		t.Fatalf("摘要应含答案与未答标记: %q", got)
	}
	if s := QuestionnaireSummary(qn, QuestionnaireAnswer{Cancelled: true}); s != "(整卷取消)" {
		t.Fatalf("取消口径: %q", s)
	}
	// chat 口径:优先用题目标签(人/模型都读得懂),查不到标签才退回 id
	if s := QuestionnaireSummary(qn, QuestionnaireAnswer{Chat: "scope"}); !strings.Contains(s, "Scope") {
		t.Fatalf("chat 口径应带题目标签: %q", s)
	}
	if s := QuestionnaireSummary(qn, QuestionnaireAnswer{Chat: "ghost"}); !strings.Contains(s, "ghost") {
		t.Fatalf("查不到标签时应退回题 id: %q", s)
	}
	if s := QuestionnaireSummary(Questionnaire{}, QuestionnaireAnswer{}); s != "(空问卷)" {
		t.Fatalf("空卷口径: %q", s)
	}
}

// TestObservedQuestionnaireEmitsEvents 事件面:整卷路径只发 questionnaire/*(不重复发 question/*)。
func TestObservedQuestionnaireEmitsEvents(t *testing.T) {
	ctx := &fakeCtx{}
	svc := &batchSvc{ans: QuestionnaireAnswer{Answers: []QuestionAnswer{{Values: []string{"a"}}, {Text: "b"}}}}
	obs := ObservedQuestion(ctx, "web", svc)
	if _, err := AskQuestionnaire(context.Background(), obs, qnFixture()); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ctx.events() {
		names = append(names, e.Name)
	}
	want := []string{EventQuestionnaireRequested, EventQuestionnaireResolved}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("整卷事件面应只有两条: %v", names)
	}
	// 归一化后的整卷随事件下行(tab 标签/题 id 齐全)
	for _, e := range ctx.events() {
		ev, ok := QuestionnaireEventOf(e.Payload)
		if !ok || ev.Questionnaire.ID == "" || ev.Questionnaire.Questions[1].Label != "优先级" {
			t.Fatalf("事件载荷应带归一化整卷: %+v", e.Payload)
		}
	}
}

// TestObservedQuestionnaireFallbackEmitsPerQuestion 逐题降级:每题仍有 question/* 事件。
func TestObservedQuestionnaireFallbackEmitsPerQuestion(t *testing.T) {
	ctx := &fakeCtx{}
	inner := &plainStub{inner: &batchStub{answers: []QuestionAnswer{{Values: []string{"a"}}, {Text: "b"}}}}
	obs := ObservedQuestion(ctx, "tui", inner)
	if _, err := AskQuestionnaire(context.Background(), obs, qnFixture()); err != nil {
		t.Fatal(err)
	}
	var counts = map[string]int{}
	for _, e := range ctx.events() {
		counts[e.Name]++
	}
	if counts[EventQuestionnaireRequested] != 1 || counts[EventQuestionnaireResolved] != 1 {
		t.Fatalf("整卷事件应各一条: %+v", counts)
	}
	if counts[EventQuestionRequested] != 2 || counts[EventQuestionResolved] != 2 {
		t.Fatalf("降级路径每题应有 question 事件: %+v", counts)
	}
}
