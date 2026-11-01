// 整卷问卷服务单测:帧推送 / 逐题作答落库 / 收口组装 / 补推带已答 / 幂等与越界。
package web

import (
	"context"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func qnFixture() sdk.Questionnaire {
	return sdk.Questionnaire{Title: "本轮待确认", Questions: []sdk.Question{
		{ID: "scope", Label: "Scope", Prompt: "范围到哪?", Options: []sdk.QuestionOption{{Value: "web", Label: "只做 Web"}}},
		{ID: "prio", Label: "优先级", Prompt: "先做哪个?", Options: []sdk.QuestionOption{{Value: "a", Label: "A"}}},
	}}
}

// present 起一个整卷呈现,返回帧与作答通道。
func present(t *testing.T, svc *QuestionService, hub *EventHub) (*QuestionnaireRequest, <-chan sdk.QuestionnaireAnswer, func()) {
	t.Helper()
	ch, release := hub.Stream("")
	t.Cleanup(release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	ansCh, cancelFn, err := svc.PresentQuestionnaire(ctx, qnFixture())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cancelFn)
	select {
	case f := <-ch:
		if f.Type != FrameQuestionnaire {
			t.Fatalf("期望 questionnaire 帧,得 %q", f.Type)
		}
		req, ok := f.Payload.(*QuestionnaireRequest)
		if !ok {
			t.Fatalf("载荷应为 *QuestionnaireRequest,得 %T", f.Payload)
		}
		return req, ansCh, cancelFn
	case <-time.After(time.Second):
		t.Fatal("未收到整卷帧")
	}
	return nil, nil, nil
}

// TestQuestionnairePartialThenSubmit 逐题作答落库 → 收口按题序组装(未答留零值)。
func TestQuestionnairePartialThenSubmit(t *testing.T) {
	hub := NewHub()
	svc := NewQuestionService(hub)
	req, ansCh, _ := present(t, svc, hub)

	if req.Title != "本轮待确认" || len(req.Questions) != 2 || req.Questions[1].Label != "优先级" {
		t.Fatalf("整卷载荷应归一化后下发: %+v", req)
	}
	if svc.QuestionnaireCount() != 1 {
		t.Fatalf("应有 1 个未决整卷, got %d", svc.QuestionnaireCount())
	}
	// 只答第 1 题(用户在第 2 题时还没选)
	if ok := svc.AnswerQuestion(req.ID, "scope", sdk.QuestionAnswer{Values: []string{"web"}, Index: 1}); !ok {
		t.Fatal("第 1 题作答应被接受")
	}
	// 越界题 id:拒绝(不静默接受,避免污染整卷)
	if ok := svc.AnswerQuestion(req.ID, "nope", sdk.QuestionAnswer{Text: "x"}); ok {
		t.Fatal("卷外题 id 不应被接受")
	}
	if ok := svc.SubmitQuestionnaire(req.ID, false, ""); !ok {
		t.Fatal("收口应被接受")
	}
	select {
	case ans := <-ansCh:
		if len(ans.Answers) != 2 {
			t.Fatalf("作答应与题序等长: %+v", ans.Answers)
		}
		if ans.Answers[0].Values[0] != "web" || !ans.Answers[1].Empty() {
			t.Fatalf("按题序组装不符: %+v", ans.Answers)
		}
		if ans.Cancelled || ans.Chat != "" {
			t.Fatalf("正常提交不应带取消/chat: %+v", ans)
		}
	case <-time.After(time.Second):
		t.Fatal("收口后应回传作答")
	}
	if svc.QuestionnaireCount() != 0 {
		t.Fatalf("收口后应清空未决: %d", svc.QuestionnaireCount())
	}
	// 重复收口/收口后作答:幂等忽略
	if ok := svc.SubmitQuestionnaire(req.ID, false, ""); ok {
		t.Fatal("重复收口应返回 false")
	}
	if ok := svc.AnswerQuestion(req.ID, "scope", sdk.QuestionAnswer{Text: "x"}); ok {
		t.Fatal("收口后作答应被忽略")
	}
}

// TestQuestionnairePendingFramesCarryAnswers 补推帧带已答集合(刷新后 tab 栏 ■ 恢复)。
func TestQuestionnairePendingFramesCarryAnswers(t *testing.T) {
	hub := NewHub()
	svc := NewQuestionService(hub)
	req, _, _ := present(t, svc, hub)
	svc.AnswerQuestion(req.ID, "prio", sdk.QuestionAnswer{Values: []string{"a"}})

	frames := svc.PendingQuestionnaireFrames()
	if len(frames) != 1 || frames[0].Type != FrameQuestionnaire {
		t.Fatalf("应有 1 条补推帧: %+v", frames)
	}
	got, ok := frames[0].Payload.(*QuestionnaireRequest)
	if !ok || got.ID != req.ID {
		t.Fatalf("补推载荷不符: %+v", frames[0].Payload)
	}
	if len(got.Answered) != 1 || got.Answered["prio"].Values[0] != "a" {
		t.Fatalf("补推应带已答集合(键=题 id): %+v", got.Answered)
	}
	// 补推载荷是快照:之后作答不影响已发出的那一份
	svc.AnswerQuestion(req.ID, "scope", sdk.QuestionAnswer{Values: []string{"web"}})
	if len(got.Answered) != 1 {
		t.Fatalf("补推载荷应是快照(不与并发作答共享 map): %+v", got.Answered)
	}
}

// TestQuestionnaireCancelAndChat 取消与「聊聊这个」两种收口。
func TestQuestionnaireCancelAndChat(t *testing.T) {
	hub := NewHub()
	svc := NewQuestionService(hub)
	req, ansCh, _ := present(t, svc, hub)
	svc.AnswerQuestion(req.ID, "scope", sdk.QuestionAnswer{Values: []string{"web"}})
	svc.SubmitQuestionnaire(req.ID, true, "")
	ans := <-ansCh
	if !ans.Cancelled {
		t.Fatalf("应标整卷取消: %+v", ans)
	}
	if ans.Answers[0].Values[0] != "web" {
		t.Fatalf("取消应保留已答部分: %+v", ans.Answers)
	}

	req2, ansCh2, _ := present(t, svc, hub)
	svc.SubmitQuestionnaire(req2.ID, false, "prio")
	if got := <-ansCh2; got.Chat != "prio" {
		t.Fatalf("应回传 chat 题 id: %+v", got)
	}
}

// TestQuestionnaireUnknownID 未知整卷/题 id:不 panic、不阻塞、幂等忽略。
func TestQuestionnaireUnknownID(t *testing.T) {
	hub := NewHub()
	svc := NewQuestionService(hub)
	if svc.AnswerQuestion("nope", "x", sdk.QuestionAnswer{}) {
		t.Fatal("未知整卷不应被接受")
	}
	if svc.SubmitQuestionnaire("nope", false, "") {
		t.Fatal("未知整卷收口不应被接受")
	}
	if len(svc.PendingQuestionnaireFrames()) != 0 {
		t.Fatal("无未决时不应有补推帧")
	}
}

// TestQuestionnaireSingleChannelAsk 单 web profile:AskQuestionnaire 直连本服务并等待收口。
func TestQuestionnaireSingleChannelAsk(t *testing.T) {
	hub := NewHub()
	svc := NewQuestionService(hub)
	ch, release := hub.Stream("")
	defer release()
	qsvc, ok := svc.SingleChannel().(sdk.QuestionnaireService)
	if !ok {
		t.Fatal("单 web profile 的 ctx.question 适配器应实现整卷能力")
	}
	done := make(chan sdk.QuestionnaireAnswer, 1)
	go func() {
		ans, err := qsvc.AskQuestionnaire(context.Background(), qnFixture())
		if err == nil {
			done <- ans
		}
	}()
	f := <-ch
	req := f.Payload.(*QuestionnaireRequest)
	svc.AnswerQuestion(req.ID, "scope", sdk.QuestionAnswer{Values: []string{"web"}})
	svc.SubmitQuestionnaire(req.ID, false, "")
	select {
	case ans := <-done:
		if ans.Answers[0].Values[0] != "web" {
			t.Fatalf("单渠道整卷作答不符: %+v", ans)
		}
	case <-time.After(time.Second):
		t.Fatal("单渠道 AskQuestionnaire 未返回")
	}
}
