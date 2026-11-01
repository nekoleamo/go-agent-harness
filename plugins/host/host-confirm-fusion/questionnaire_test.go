// 整卷问卷融合单测:整卷渠道优先 / 无整卷渠道逐题降级 / 无渠道显式失败 /
// 整卷事件面只发 questionnaire/*(不为每题补发 question/*)。
package hostconfirmfusion

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"log/slog"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubQnPresenter 单题 + 整卷都会的渠道桩(整卷收到即回固定作答)。
type stubQnPresenter struct {
	stubQuestioner
	qnCh chan sdk.Questionnaire
	ans  sdk.QuestionnaireAnswer
}

func (s *stubQnPresenter) PresentQuestionnaire(_ context.Context, qn sdk.Questionnaire) (<-chan sdk.QuestionnaireAnswer, func(), error) {
	s.qnCh <- qn
	ch := make(chan sdk.QuestionnaireAnswer, 1)
	ch <- s.ans
	return ch, func() {}, nil
}

func qnFixture() sdk.Questionnaire {
	return sdk.Questionnaire{Title: "本轮待确认", Questions: []sdk.Question{
		{ID: "scope", Label: "Scope", Prompt: "范围到哪?", Options: []sdk.QuestionOption{{Value: "a", Label: "只做 Web"}}},
		{ID: "prio", Label: "优先级", Prompt: "先做哪个?"},
	}}
}

func newFusion() *Fusion {
	return &Fusion{presenters: make(map[string]sdk.ConfirmPresenter), questioners: make(map[string]sdk.QuestionPresenter)}
}

// TestFusionAskQuestionnaireBatch 有整卷渠道:整卷送达(含归一化),答案原样返回。
func TestFusionAskQuestionnaireBatch(t *testing.T) {
	f := newFusion()
	p := &stubQnPresenter{
		stubQuestioner: stubQuestioner{got: make(chan sdk.Question, 1), ans: make(chan sdk.QuestionAnswer, 1), done: make(chan struct{})},
		qnCh:           make(chan sdk.Questionnaire, 1),
		ans:            sdk.QuestionnaireAnswer{Answers: []sdk.QuestionAnswer{{Values: []string{"a"}}, {Text: "先做 Web"}}},
	}
	defer f.RegisterQuestioner("web", p)()

	got, err := f.AskQuestionnaire(context.Background(), qnFixture())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Answers) != 2 || got.Answers[1].Text != "先做 Web" {
		t.Fatalf("整卷作答应原样返回: %+v", got)
	}
	select {
	case qn := <-p.qnCh:
		if qn.ID == "" || qn.Questions[1].Index != 2 || qn.Questions[1].Group == "" {
			t.Fatalf("送达前应归一化(整卷 id/题序号): %+v", qn)
		}
	case <-time.After(time.Second):
		t.Fatal("整卷渠道应收到整卷")
	}
	// 整卷路径不应再走单题呈现
	select {
	case q := <-p.got:
		t.Fatalf("整卷渠道不应收到单题: %+v", q)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestFusionAskQuestionnaireFallback 无整卷渠道(只有单题能力):逐题 Ask 降级。
func TestFusionAskQuestionnaireFallback(t *testing.T) {
	f := newFusion()
	got := make(chan sdk.Question, 4)
	ans := make(chan sdk.QuestionAnswer, 4)
	var cancels int
	var cmu sync.Mutex
	p := &chanQuestioner{got: got, ans: ans, onCancel: func() { cmu.Lock(); cancels++; cmu.Unlock() }}
	defer f.RegisterQuestioner("acp", p)()

	go func() {
		ans <- sdk.QuestionAnswer{Values: []string{"a"}}
		ans <- sdk.QuestionAnswer{Text: "手写"}
	}()
	out, err := f.AskQuestionnaire(context.Background(), qnFixture())
	if err != nil {
		t.Fatal(err)
	}
	if out.Answers[0].Values[0] != "a" || out.Answers[1].Text != "手写" {
		t.Fatalf("逐题降级应组装成整卷答案: %+v", out.Answers)
	}
	// 两题都逐题呈现过,顺序与题序一致
	for i := 0; i < 2; i++ {
		select {
		case q := <-got:
			if q.Prompt != qnFixture().Questions[i].Prompt {
				t.Fatalf("第 %d 题顺序不符: %q", i, q.Prompt)
			}
		case <-time.After(time.Second):
			t.Fatalf("第 %d 题应被逐题呈现", i)
		}
	}
}

// chanQuestioner 单题渠道桩(可选 cancel 计数)。
type chanQuestioner struct {
	got      chan sdk.Question
	ans      chan sdk.QuestionAnswer
	onCancel func()
}

func (c *chanQuestioner) PresentQuestion(_ context.Context, q sdk.Question) (<-chan sdk.QuestionAnswer, func(), error) {
	c.got <- q
	return c.ans, func() {
		if c.onCancel != nil {
			c.onCancel()
		}
	}, nil
}

// TestFusionAskQuestionnaireNoChannel 无渠道:显式错误(不静默假答)。
func TestFusionAskQuestionnaireNoChannel(t *testing.T) {
	f := newFusion()
	if _, err := f.AskQuestionnaire(context.Background(), qnFixture()); err == nil ||
		!strings.Contains(err.Error(), "无提问渠道") {
		t.Fatalf("无渠道应显式报错: %v", err)
	}
}

// TestFusionAskQuestionnaireEvents 事件面:整卷路径只有 questionnaire/requested↔resolved。
func TestFusionAskQuestionnaireEvents(t *testing.T) {
	bus := event.New(slog.New(slog.DiscardHandler))
	c := ctx.New(slog.New(slog.DiscardHandler), bus)
	f := &Fusion{c: c, presenters: make(map[string]sdk.ConfirmPresenter), questioners: make(map[string]sdk.QuestionPresenter)}
	var mu sync.Mutex
	var names []string
	var payload *sdk.QuestionnaireEvent
	for _, name := range []string{
		sdk.EventQuestionRequested, sdk.EventQuestionResolved,
		sdk.EventQuestionnaireRequested, sdk.EventQuestionnaireResolved,
	} {
		n := name
		c.Subscribe(n, func(_ context.Context, ev *sdk.Event) error {
			mu.Lock()
			names = append(names, n)
			if e, ok := ev.Payload.(*sdk.QuestionnaireEvent); ok {
				payload = e
			}
			mu.Unlock()
			return nil
		})
	}
	p := &stubQnPresenter{
		stubQuestioner: stubQuestioner{got: make(chan sdk.Question, 1), ans: make(chan sdk.QuestionAnswer, 1), done: make(chan struct{})},
		qnCh:           make(chan sdk.Questionnaire, 1),
		ans:            sdk.QuestionnaireAnswer{Answers: []sdk.QuestionAnswer{{Values: []string{"a"}}, {}}},
	}
	defer f.RegisterQuestioner("web", p)()
	if _, err := f.AskQuestionnaire(context.Background(), qnFixture()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(names) != 2 || names[0] != sdk.EventQuestionnaireRequested || names[1] != sdk.EventQuestionnaireResolved {
		t.Fatalf("整卷路径事件面应只有两条: %v", names)
	}
	if payload == nil || payload.Channel != "web" || !payload.Resolved {
		t.Fatalf("resolved 事件应带胜出渠道与结果: %+v", payload)
	}
}

// TestFusionAskQuestionnaireCapabilityDetection 只有单题能力的渠道不会被当成整卷渠道
// (能力探测是 type assertion,不是"注册了就行")。
func TestFusionAskQuestionnaireCapabilityDetection(t *testing.T) {
	f := newFusion()
	p := &stubQuestioner{got: make(chan sdk.Question, 2), ans: make(chan sdk.QuestionAnswer, 2), done: make(chan struct{})}
	defer f.RegisterQuestioner("acp", p)()
	if _, isQn := any(p).(sdk.QuestionnairePresenter); isQn {
		t.Fatal("桩不应实现整卷能力(测试前提)")
	}
	go func() {
		p.ans <- sdk.QuestionAnswer{}
		p.ans <- sdk.QuestionAnswer{}
	}()
	if _, err := f.AskQuestionnaire(context.Background(), qnFixture()); err != nil {
		t.Fatal(err)
	}
	// 走的是逐题:两题各一次
	for i := 0; i < 2; i++ {
		select {
		case <-p.got:
		case <-time.After(time.Second):
			t.Fatalf("第 %d 题应逐题呈现", i)
		}
	}
}
