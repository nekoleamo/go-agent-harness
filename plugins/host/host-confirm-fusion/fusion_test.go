// host-confirm-fusion 单测:广播注册/竞速应答/cancel 清理/无渠道安全拒绝。
package hostconfirmfusion

import (
	"log/slog"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubPresenter 可控应答(经 answer chan 注入)。
type stubPresenter struct {
	promptCh chan string // Present 收到的 prompt(断言)
	answer   chan bool   // 注入应答(chan 有值才应答;Close 模拟不可用)
	cancels  *int
	cancelMu *sync.Mutex
}

func (sp *stubPresenter) Present(ctx context.Context, prompt string) (<-chan bool, func(), error) {
	sp.promptCh <- prompt
	return sp.answer, func() {
		if sp.cancels != nil {
			sp.cancelMu.Lock()
			*sp.cancels++
			sp.cancelMu.Unlock()
		}
	}, nil
}

// TestFusionBroadcastAndFirstWins 广播两渠道;先应答者生效,另一渠道收到 cancel。
func TestFusionBroadcastAndFirstWins(t *testing.T) {
	f := &Fusion{presenters: make(map[string]sdk.ConfirmPresenter)}
	cancels := 0
	var cmu sync.Mutex
	a1 := make(chan bool, 1)
	p1 := &stubPresenter{promptCh: make(chan string, 1), answer: a1, cancels: &cancels, cancelMu: &cmu}
	a2 := make(chan bool, 1)
	p2 := &stubPresenter{promptCh: make(chan string, 1), answer: a2, cancels: &cancels, cancelMu: &cmu}
	d1 := f.Register("web", p1)
	d2 := f.Register("qq", p2)
	defer d1()
	defer d2()

	go func() { a2 <- true }() // 渠道 2 先应答
	ok, err := f.Confirm(context.Background(), "危险操作?")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("应答应生效")
	}
	if got := <-p1.promptCh; got != "危险操作?" {
		t.Fatalf("渠道1应收到提示: %q", got)
	}
	if got := <-p2.promptCh; got != "危险操作?" {
		t.Fatalf("渠道2应收到提示: %q", got)
	}
	// 渠道 1(未应答)应收到 cancel
	deadline := time.Now().Add(2 * time.Second)
	for cmu.Lock(); *p1.cancels == 0; {
		cmu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("未应答渠道应收到 cancel")
		}
		time.Sleep(10 * time.Millisecond)
		cmu.Lock()
	}
	cmu.Unlock()
}

// TestFusionNoPresenter 无渠道:Confirm 安全失败(取消语义,非静默放行)。
func TestFusionNoPresenter(t *testing.T) {
	f := &Fusion{presenters: make(map[string]sdk.ConfirmPresenter)}
	if _, err := f.Confirm(context.Background(), "x"); err == nil {
		t.Fatal("无渠道应报错(安全拒绝)")
	}
}

// TestFusionCtxCancel ctx 取消按拒绝处理。
func TestFusionCtxCancel(t *testing.T) {
	f := &Fusion{presenters: make(map[string]sdk.ConfirmPresenter)}
	a := make(chan bool, 1)
	f.Register("web", &stubPresenter{promptCh: make(chan string, 1), answer: a})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := f.Confirm(ctx, "x"); err == nil {
		t.Fatal("ctx 取消应返回错误(拒绝)")
	}
}

// TestFusionUnregister 注销渠道后不再被广播。
func TestFusionUnregister(t *testing.T) {
	f := &Fusion{presenters: make(map[string]sdk.ConfirmPresenter)}
	a := make(chan bool, 1)
	sp := &stubPresenter{promptCh: make(chan string, 1), answer: a}
	d := f.Register("web", sp)
	d()
	if _, err := f.Confirm(context.Background(), "x"); err == nil {
		t.Fatal("注销后应无渠道可应答")
	}
}

// TestFusionChannelList Channels 反映注册集。
func TestFusionChannelList(t *testing.T) {
	f := &Fusion{presenters: make(map[string]sdk.ConfirmPresenter)}
	f.Register("web", &stubPresenter{promptCh: make(chan string, 1)})
	f.Register("im-qq", &stubPresenter{promptCh: make(chan string, 1)})
	if got := len(f.Channels()); got != 2 {
		t.Fatalf("Channels 应 2,got %d", got)
	}
}

// stubQuestioner 可控提问应答。
type stubQuestioner struct {
	got  chan sdk.Question
	ans  chan sdk.QuestionAnswer
	done chan struct{} // cancel 触发
}

func (s *stubQuestioner) PresentQuestion(_ context.Context, q sdk.Question) (<-chan sdk.QuestionAnswer, func(), error) {
	s.got <- q
	return s.ans, func() {
		select {
		case <-s.done:
		default:
			close(s.done)
		}
	}, nil
}

// TestFusionAskQuestion 提问融合:广播两渠道,首答生效,未答渠道 cancel。
func TestFusionAskQuestion(t *testing.T) {
	f := &Fusion{presenters: make(map[string]sdk.ConfirmPresenter), questioners: make(map[string]sdk.QuestionPresenter)}
	q1 := &stubQuestioner{got: make(chan sdk.Question, 1), ans: make(chan sdk.QuestionAnswer, 1), done: make(chan struct{})}
	q2 := &stubQuestioner{got: make(chan sdk.Question, 1), ans: make(chan sdk.QuestionAnswer, 1), done: make(chan struct{})}
	d1 := f.RegisterQuestioner("web", q1)
	d2 := f.RegisterQuestioner("im-qq", q2)
	defer d1()
	defer d2()

	question := sdk.Question{Prompt: "选环境", Options: []sdk.QuestionOption{{Value: "dev"}, {Value: "prod"}}}
	go func() { q2.ans <- sdk.QuestionAnswer{Values: []string{"prod"}} }()
	ans, err := f.Ask(context.Background(), question)
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Values) != 1 || ans.Values[0] != "prod" {
		t.Fatalf("应取首答渠道结果: %+v", ans)
	}
	for i, q := range []*stubQuestioner{q1, q2} {
		select {
		case got := <-q.got:
			if got.Prompt != "选环境" || len(got.Options) != 2 {
				t.Fatalf("渠道 %d 提问载荷不符: %+v", i, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("渠道 %d 应收到提问(广播)", i)
		}
	}
	// 未应答渠道应被 cancel
	select {
	case <-q1.done:
	case <-time.After(time.Second):
		t.Fatal("未应答渠道应收到 cancel")
	}
}

// TestFusionAskNoChannel 无提问渠道:显式报错(不静默假答)。
func TestFusionAskNoChannel(t *testing.T) {
	f := &Fusion{presenters: make(map[string]sdk.ConfirmPresenter), questioners: make(map[string]sdk.QuestionPresenter)}
	if _, err := f.Ask(context.Background(), sdk.Question{Prompt: "x"}); err == nil {
		t.Fatal("无渠道应报错")
	}
	// 注册后注销 → 视为无渠道
	q := &stubQuestioner{got: make(chan sdk.Question, 1), ans: make(chan sdk.QuestionAnswer, 1), done: make(chan struct{})}
	d := f.RegisterQuestioner("tui", q)
	if len(f.QuestionChannels()) != 1 {
		t.Fatal("注册后应有 1 渠道")
	}
	d()
	if _, err := f.Ask(context.Background(), sdk.Question{Prompt: "x"}); err == nil {
		t.Fatal("注销后应报错")
	}
}

// TestInteractionEvents 事件化:Confirm/Ask 广播 requested 与 resolved(载荷含结果/错误)。
func TestInteractionEvents(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	f := &Fusion{c: c, presenters: make(map[string]sdk.ConfirmPresenter), questioners: make(map[string]sdk.QuestionPresenter)}
	if !f.EmitsEvents() {
		t.Fatal("有 Ctx 时应广播事件")
	}
	var mu sync.Mutex
	var got []string
	var lastConfirm *sdk.ConfirmEvent
	var lastQuestion *sdk.QuestionEvent
	for _, name := range []string{sdk.EventConfirmRequested, sdk.EventConfirmResolved, sdk.EventQuestionRequested, sdk.EventQuestionResolved} {
		n := name
		c.Subscribe(n, func(_ context.Context, ev *sdk.Event) error {
			mu.Lock()
			got = append(got, n)
			if e, ok := ev.Payload.(*sdk.ConfirmEvent); ok {
				lastConfirm = e
			}
			if e, ok := ev.Payload.(*sdk.QuestionEvent); ok {
				lastQuestion = e
			}
			mu.Unlock()
			return nil
		})
	}
	// confirm:渠道 stub 立即批准
	ca := make(chan bool, 1)
	ca <- true
	f.Register("web", &stubPresenter{promptCh: make(chan string, 1), answer: ca})
	if ok, err := f.Confirm(context.Background(), "危险?"); err != nil || !ok {
		t.Fatalf("confirm 应批准: %v %v", ok, err)
	}
	// question:渠道 stub 作答
	qa := make(chan sdk.QuestionAnswer, 1)
	qa <- sdk.QuestionAnswer{Values: []string{"prod"}}
	f.RegisterQuestioner("web", &stubQuestioner{got: make(chan sdk.Question, 1), ans: qa, done: make(chan struct{})})
	if _, err := f.Ask(context.Background(), sdk.Question{Prompt: "选环境"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	n := len(got)
	lc, lq := lastConfirm, lastQuestion
	mu.Unlock()
	if n != 4 {
		t.Fatalf("应广播 4 个事件,got %d", n)
	}
	if lc == nil || !lc.Resolved || !lc.OK || lc.Prompt != "危险?" {
		t.Fatalf("confirm resolved 载荷不符: %+v", lc)
	}
	if lc.Channel != "web" {
		t.Fatalf("confirm 事件应标明作答渠道: %+v", lc)
	}
	if lq == nil || !lq.Resolved || len(lq.Answer.Values) != 1 || lq.Question.Prompt != "选环境" {
		t.Fatalf("question resolved 载荷不符: %+v", lq)
	}
	if lq.Channel != "web" {
		t.Fatalf("question 事件应标明作答渠道: %+v", lq)
	}
	// G-E5-4:Question.ID 由 Fusion 补齐(事件与各端弹层共用同一 id)
	if lq.Question.ID == "" {
		t.Fatalf("Fusion 应补齐 Question.ID: %+v", lq)
	}
	// 渠道显式给 id 时不覆盖(单渠道 Fusion,避开上例 web stub 的通道容量)
	f3 := &Fusion{c: c, presenters: make(map[string]sdk.ConfirmPresenter), questioners: make(map[string]sdk.QuestionPresenter)}
	qa2 := make(chan sdk.QuestionAnswer, 1)
	qa2 <- sdk.QuestionAnswer{Text: "ok"}
	f3.RegisterQuestioner("tui", &stubQuestioner{got: make(chan sdk.Question, 1), ans: qa2, done: make(chan struct{})})
	if _, err := f3.Ask(context.Background(), sdk.Question{ID: "fixed-9", Prompt: "保留 id"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	lq2 := lastQuestion
	mu.Unlock()
	if lq2 == nil || lq2.Question.ID != "fixed-9" || lq2.Channel != "tui" {
		t.Fatalf("显式 id/渠道追踪异常: %+v", lq2)
	}
	// 无渠道时 resolved 也应带错误(观察面可见失败);经 channel 传递避免回调重入锁
	f2 := &Fusion{c: c, presenters: make(map[string]sdk.ConfirmPresenter), questioners: make(map[string]sdk.QuestionPresenter)}
	errCh := make(chan string, 4)
	c.Subscribe(sdk.EventConfirmResolved, func(_ context.Context, ev *sdk.Event) error {
		if e, ok := ev.Payload.(*sdk.ConfirmEvent); ok {
			select {
			case errCh <- e.Err:
			default:
			}
		}
		return nil
	})
	if _, err := f2.Confirm(context.Background(), "x"); err == nil {
		t.Fatal("无渠道应报错")
	}
	select {
	case e := <-errCh:
		if e == "" {
			t.Fatal("resolved 事件应带错误详情")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 resolved 事件")
	}
}
