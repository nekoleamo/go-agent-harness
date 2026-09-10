// QuestionService 单测(P3 语义交互):提问弹层推送 → 作答回传;未知 id/取消安全。
package web

import (
	"context"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestQuestionAnswerFlow(t *testing.T) {
	hub := NewHub()
	svc := NewQuestionService(hub)
	ch, release := hub.Stream()
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type res struct {
		a   sdk.QuestionAnswer
		err error
	}
	done := make(chan res, 1)
	go func() {
		a, cancelFn, err := svc.PresentQuestion(ctx, sdk.Question{
			Prompt:   "部署到哪个环境?",
			Options:  []sdk.QuestionOption{{Value: "dev", Desc: "开发"}, {Value: "prod", Desc: "生产"}},
			Multiple: false,
			FreeText: true,
		})
		if err != nil {
			done <- res{err: err}
			return
		}
		defer cancelFn()
		select {
		case ans := <-a:
			done <- res{a: ans}
		case <-ctx.Done():
			done <- res{err: ctx.Err()}
		}
	}()

	f := <-ch
	if f.Type != FrameQuestion {
		t.Fatalf("期望 question 帧,得 %+v", f)
	}
	req, ok := f.Payload.(*QuestionRequest)
	if !ok {
		t.Fatalf("载荷应为 *QuestionRequest,得 %T", f.Payload)
	}
	if req.Prompt != "部署到哪个环境?" || len(req.Options) != 2 || !req.FreeText || req.ID == "" {
		t.Fatalf("弹层内容不符: %+v", req)
	}
	if svc.PendingCount() != 1 {
		t.Fatalf("应有 1 个未决提问,got %d", svc.PendingCount())
	}
	svc.Answer(req.ID, sdk.QuestionAnswer{Values: []string{"prod"}})
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if len(r.a.Values) != 1 || r.a.Values[0] != "prod" {
			t.Fatalf("作答回传不符: %+v", r.a)
		}
	case <-time.After(time.Second):
		t.Fatal("作答未回传")
	}
}

// TestQuestionUnknownID 未知弹层 id 幂等忽略(超时/重复作答不 panic)。
func TestQuestionUnknownID(t *testing.T) {
	svc := NewQuestionService(NewHub())
	svc.Answer("nope", sdk.QuestionAnswer{Text: "x"}) // 不 panic 即可
	if svc.PendingCount() != 0 {
		t.Fatal("未知 id 不应产生待答")
	}
}

// TestQuestionCancelCleans 取消(超时/放弃)清理未决项。
func TestQuestionCancelCleans(t *testing.T) {
	svc := NewQuestionService(NewHub())
	_, cancel, err := svc.PresentQuestion(context.Background(), sdk.Question{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if svc.PendingCount() != 1 {
		t.Fatal("应登记 1 个未决提问")
	}
	cancel()
	if svc.PendingCount() != 0 {
		t.Fatal("cancel 后应清理")
	}
}

// G-E5-4:SingleChannel 适配 sdk.QuestionService(单 web profile 曾 Provide
// QuestionPresenter → 工具侧 Inject 类型不符,ask_user_question 实际不可用)。
func TestQuestionSingleChannelAdapter(t *testing.T) {
	svc := NewQuestionService(NewHub())
	var qs sdk.QuestionService = svc.SingleChannel()
	done := make(chan sdk.QuestionAnswer, 1)
	go func() {
		a, err := qs.Ask(context.Background(), sdk.Question{ID: "q-9", Prompt: "选一个"})
		if err == nil {
			done <- a
		}
	}()
	// 弹层 id 沿用调用方给的 q.ID(与事件同 id)
	deadline := time.Now().Add(2 * time.Second)
	for svc.PendingCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	svc.Answer("q-9", sdk.QuestionAnswer{Values: []string{"a"}})
	select {
	case a := <-done:
		if len(a.Values) != 1 || a.Values[0] != "a" {
			t.Fatalf("作答异常: %+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask 未返回")
	}
	// 未给 id 时回退生成(不接受未知 id 应答)
	_, cancel, err := svc.PresentQuestion(context.Background(), sdk.Question{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if svc.PendingCount() != 1 {
		t.Fatalf("应有 1 个未决: %d", svc.PendingCount())
	}
	svc.Answer("unknown-id", sdk.QuestionAnswer{Text: "n"})
	if svc.PendingCount() != 1 {
		t.Fatal("未知 id 应答应被忽略")
	}
	// RegisterQuestioner 单渠道空实现(Disposer 幂等)
	qs.RegisterQuestioner("web", svc)()
}
