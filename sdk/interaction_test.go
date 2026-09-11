// 交互事件化单测(G-E5-4):ObservedQuestion 广播 / id 补齐 / Channel / 载荷访问器 / 作答摘要。
package sdk

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// fakeCtx 记录 Emit 的最小 Ctx 替身(sdk 模块不依赖 core)。
type fakeCtx struct {
	mu   sync.Mutex
	got  []Event
	emit error
}

func (f *fakeCtx) Provide(string, any) error { return nil }
func (f *fakeCtx) Inject(string, any) error  { return errors.New("未装配") }
func (f *fakeCtx) Subscribe(string, AnyListener) Disposer {
	return func() {}
}
func (f *fakeCtx) Emit(_ context.Context, name string, payload any, _ DispatchMode) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, Event{Name: name, Payload: payload})
	return nil, f.emit
}
func (f *fakeCtx) Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func (f *fakeCtx) events() []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Event(nil), f.got...)
}

// stubQuestion 固定作答的提问服务。
type stubQuestion struct {
	ans      QuestionAnswer
	err      error
	gotID    string
	asked    int
	register func(string, QuestionPresenter)
}

func (s *stubQuestion) Ask(_ context.Context, q Question) (QuestionAnswer, error) {
	s.asked++
	s.gotID = q.ID
	return s.ans, s.err
}
func (s *stubQuestion) RegisterQuestioner(name string, p QuestionPresenter) Disposer {
	if s.register != nil {
		s.register(name, p)
	}
	return func() {}
}

func TestObservedQuestionBroadcastsRequestedResolved(t *testing.T) {
	c := &fakeCtx{}
	inner := &stubQuestion{ans: QuestionAnswer{Values: []string{"dev"}}}
	svc := ObservedQuestion(c, "tui", inner)
	if svc == nil {
		t.Fatal("包装后不应为 nil")
	}
	var _ QuestionService = svc // 编译期保证包装结果实现 QuestionService(ObservedQuestion 返回类型已是该接口)
	ans, err := svc.Ask(context.Background(), Question{Prompt: "部署到哪?"})
	if err != nil || len(ans.Values) != 1 {
		t.Fatalf("转发异常: %+v %v", ans, err)
	}
	evs := c.events()
	if len(evs) != 2 || evs[0].Name != EventQuestionRequested || evs[1].Name != EventQuestionResolved {
		t.Fatalf("事件序列异常: %+v", evs)
	}
	req, ok := QuestionEventOf(evs[0].Payload)
	if !ok || req.Channel != "tui" || req.Question.Prompt != "部署到哪?" {
		t.Fatalf("requested 载荷异常: %+v", req)
	}
	// id 由装饰器补齐,且与下发给 inner 的一致(事件↔弹层可关联)
	if req.Question.ID == "" || inner.gotID != req.Question.ID {
		t.Fatalf("id 应补齐且一致: event=%q inner=%q", req.Question.ID, inner.gotID)
	}
	res, ok := QuestionEventOf(evs[1].Payload)
	if !ok || !res.Resolved || res.Channel != "tui" || res.Answer.Values[0] != "dev" {
		t.Fatalf("resolved 载荷异常: %+v", res)
	}
	// 调用方已给 id 时不覆盖
	_, _ = svc.Ask(context.Background(), Question{ID: "fixed-1", Prompt: "x"})
	if inner.gotID != "fixed-1" {
		t.Fatalf("显式 id 不应被覆盖: %q", inner.gotID)
	}
}

func TestObservedQuestionErrorAndPassthrough(t *testing.T) {
	c := &fakeCtx{}
	inner := &stubQuestion{err: errors.New("无渠道")}
	svc := ObservedQuestion(c, "web", inner)
	if _, err := svc.Ask(context.Background(), Question{Prompt: "q"}); err == nil {
		t.Fatal("应透传错误")
	}
	res, _ := QuestionEventOf(c.events()[1].Payload)
	if res.Err != "无渠道" || !res.Resolved {
		t.Fatalf("失败事件异常: %+v", res)
	}
	// nil Ctx / nil inner:原样返回 inner(无事件面,不 panic)
	if got := ObservedQuestion(nil, "web", inner); got != QuestionService(inner) {
		t.Fatal("nil Ctx 应原样返回 inner")
	}
	if got := ObservedQuestion(c, "web", nil); got != nil {
		t.Fatal("nil inner 应返回 nil")
	}
	// RegisterQuestioner 转发
	called := false
	svc2 := ObservedQuestion(c, "web", &stubQuestion{register: func(string, QuestionPresenter) { called = true }})
	svc2.RegisterQuestioner("web", nil)
	if !called {
		t.Fatal("RegisterQuestioner 应转发")
	}
}

func TestInteractionEventAccessorsAndSummary(t *testing.T) {
	// 值/指针兼容;nil 指针与无关载荷返回 false
	qe := QuestionEvent{Question: Question{ID: "a"}, Channel: "cli"}
	for _, in := range []any{qe, &qe} {
		if got, ok := QuestionEventOf(in); !ok || got.Channel != "cli" {
			t.Fatalf("QuestionEventOf(%T) 异常", in)
		}
	}
	if _, ok := QuestionEventOf((*QuestionEvent)(nil)); ok {
		t.Fatal("nil 指针应返回 false")
	}
	if _, ok := QuestionEventOf("x"); ok {
		t.Fatal("无关载荷应返回 false")
	}
	ce := ConfirmEvent{Prompt: "p", Channel: "web", OK: true}
	for _, in := range []any{ce, &ce} {
		if got, ok := ConfirmEventOf(in); !ok || got.Channel != "web" {
			t.Fatalf("ConfirmEventOf(%T) 异常", in)
		}
	}
	if _, ok := ConfirmEventOf((*ConfirmEvent)(nil)); ok {
		t.Fatal("nil 指针应返回 false")
	}
	if got := AnswerSummary(QuestionAnswer{Values: []string{"a", "b"}, Text: " 自由 "}); got != "a , b , 自由" {
		t.Fatalf("AnswerSummary 异常: %q", got)
	}
	if got := AnswerSummary(QuestionAnswer{}); got != "(空作答)" {
		t.Fatalf("空作答异常: %q", got)
	}
	// NewQuestionID:非空、唯一、十六进制
	a, b := NewQuestionID(), NewQuestionID()
	if a == "" || a == b || len(a) != 16 || strings.Trim(a, "0123456789abcdef") != "" {
		t.Fatalf("NewQuestionID 异常: %q %q", a, b)
	}
}
