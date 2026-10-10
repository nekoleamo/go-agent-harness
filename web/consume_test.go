// consumeStream 回归单测:重放→实时切换窗口(事件在重放快照后、实时订阅建立后
// 广播)不重不漏。旧实现「先重放后订阅」在重放期间新广播的帧既不进重放集、
// 又错过订阅 → 永久丢失;修复为「先订阅后重放 + 按 Seq 去重」。
package web

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// gapLog 可控 SessionLog:首调 Replay 阻塞直到 release —— 构造「订阅已建立但
// 重放尚未返回」的确定窗口(慢历史会话重放场景),供测试在窗口内广播新帧。
type gapLog struct {
	mu      sync.Mutex
	evs     []sdk.SessionEvent
	replayC chan struct{} // 首调 Replay 已进入(此刻 consumeStream 订阅已完成)
	release chan struct{} // 放行 Replay 返回
	blocked bool
}

func (g *gapLog) Append(ev sdk.SessionEvent) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.evs = append(g.evs, ev)
	return nil
}
func (g *gapLog) Replay() []sdk.SessionEvent {
	g.mu.Lock()
	if !g.blocked {
		g.blocked = true
		close(g.replayC)
		g.mu.Unlock()
		<-g.release
	} else {
		g.mu.Unlock()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]sdk.SessionEvent(nil), g.evs...)
}
func (g *gapLog) DeriveMessages() []sdk.LLMMessage              { return nil }
func (g *gapLog) Flush() error                                  { return nil }
func (g *gapLog) SetPath(string)                                {}
func (g *gapLog) Load(string) error                             { return nil }
func (g *gapLog) SetHistory(int)                                {}
func (g *gapLog) RegisterCompressor(int, sdk.SessionCompressor) {}

// TestConsumeStreamGapNoLossNoDup 覆盖切换窗口不变量:
//   - 重放进行中广播的新会话帧(已落盘 → 重放集含;已广播 → 实时流缓冲)→ 恰发一次;
//   - 非会话帧(ID=0)不参与去重,始终透传。
func TestConsumeStreamGapNoLossNoDup(t *testing.T) {
	hub := NewHub()
	s := New(Config{}, hub, NewConfirm(hub), slog.Default())
	log := &gapLog{replayC: make(chan struct{}), release: make(chan struct{})}
	_ = log.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Seq: 1})
	_ = log.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Seq: 2})
	s.sessions = log

	stop := make(chan struct{})
	var mu sync.Mutex
	var got []Frame
	sink := func(f Frame) error { mu.Lock(); got = append(got, f); mu.Unlock(); return nil }
	done := make(chan struct{})
	go func() { defer close(done); s.consumeStream(0, sink, nil, stop, "") }()

	<-log.replayC // 订阅已建立、重放被阻塞 = 窗口内
	// 窗口内广播新会话帧:先落盘(Append)再广播(push,真实语义)→ 重放集与实时流均含 seq3
	_ = log.Append(sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Seq: 3})
	hub.Push(Frame{ID: 3, Type: FrameSession, Payload: nil})
	// 另广播一条非会话状态帧(ID=0)
	hub.Push(Frame{Type: FrameStatus, Payload: "idle"})
	close(log.release) // 放行重放(返回 1,2,3)

	wantLen := 4
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= wantLen || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	<-done
	mu.Lock()
	defer mu.Unlock()
	// 首帧 = 基线(S-P1-2:告诉前端本次回放的窗口边界;不是会话帧,ID=0)
	if got[0].Type != FrameBaseline {
		t.Fatalf("首帧应为 %q 基线,得 %q", FrameBaseline, got[0].Type)
	}
	base, ok := got[0].Payload.(Baseline)
	if !ok {
		t.Fatalf("基线载荷类型: %T", got[0].Payload)
	}
	if base.From != 1 || base.To != 3 || base.Count != 3 || base.HasMore {
		t.Fatalf("基线应覆盖全窗口且无更早历史: %+v", base)
	}
	ids := make([]uint64, 0, len(got)-1)
	for _, f := range got[1:] {
		ids = append(ids, f.ID)
	}
	want := []uint64{1, 2, 3, 0} // 会话帧各恰一次 + 状态帧透传
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("基线之后的帧序应 %v(切换窗口不重不漏),得 %v", want, ids)
	}
}

// TestConsumeStreamResendsPendingPopups 新连接(刷新页面)时补推未决审批/提问:
//   - FrameBaseline 必须是首帧(前端与 frame_sync/consume 的契约);
//   - 未决 confirm 与 question 都要补推(否则弹层丢失而审批/提问不限时地等 = 回合挂死)。
//
// 回归(2026-10-10):补推曾排在 baseline 之前(首帧变 confirm),且 question 根本没有补推。
func TestConsumeStreamResendsPendingPopups(t *testing.T) {
	hub := NewHub()
	s := New(Config{}, hub, NewConfirm(hub), slog.Default())
	s.question = NewQuestionService(hub)
	s.sessions = &memLog{}

	cctx := sdk.WithSessionContext(context.Background(), "sess-A")
	if _, cancel, err := s.confirm.Present(cctx, "删库?"); err != nil {
		t.Fatal(err)
	} else {
		defer cancel()
	}
	if _, qcancel, err := s.question.PresentQuestion(cctx, sdk.Question{ID: "q1", Prompt: "选哪个?"}); err != nil {
		t.Fatal(err)
	} else {
		defer qcancel()
	}

	stop := make(chan struct{})
	var mu sync.Mutex
	var got []Frame
	sink := func(f Frame) error { mu.Lock(); got = append(got, f); mu.Unlock(); return nil }
	go func() { s.consumeStream(0, sink, nil, stop, "") }()
	defer close(stop)

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	mu.Lock()
	frames := append([]Frame(nil), got...)
	mu.Unlock()
	if len(frames) == 0 || frames[0].Type != FrameBaseline {
		t.Fatalf("首帧应为 baseline(补推不得排在它之前): %+v", frames)
	}
	var haveConfirm, haveQuestion bool
	for _, f := range frames {
		switch f.Type {
		case FrameConfirm:
			haveConfirm = true
		case FrameQuestion:
			haveQuestion = true
			if f.Session != "sess-A" {
				t.Fatalf("提问补推帧应带归属会话: %+v", f)
			}
		}
	}
	if !haveConfirm || !haveQuestion {
		t.Fatalf("未决确认与提问都应补推(confirm=%v question=%v): %+v", haveConfirm, haveQuestion, frames)
	}
}

// TestConsumeStreamHeartbeat 无数据帧时也要有心跳(W2):
//   - 心跳被周期调用(否则 TCP 半开 + 久无广播的连接要滞留到下次广播才回收);
//   - 心跳写失败 = 客户端已断 ⇒ 消费立即结束(不留 goroutine/订阅)。
func TestConsumeStreamHeartbeat(t *testing.T) {
	old := streamHeartbeat
	streamHeartbeat = 20 * time.Millisecond
	defer func() { streamHeartbeat = old }()

	hub := NewHub()
	s := New(Config{}, hub, NewConfirm(hub), slog.Default())
	s.sessions = &memLog{}

	stop := make(chan struct{})
	defer close(stop)
	beat := make(chan struct{}, 8)
	go s.consumeStream(0, func(Frame) error { return nil }, func() error {
		select {
		case beat <- struct{}{}:
		default:
		}
		return nil
	}, stop, "")
	select {
	case <-beat:
	case <-time.After(2 * time.Second):
		t.Fatal("无数据帧时也应收到心跳")
	}

	// 心跳失败 ⇒ 结束
	stop2 := make(chan struct{})
	done := make(chan struct{})
	go func() {
		s.consumeStream(0, func(Frame) error { return nil }, func() error {
			return errors.New("broken pipe")
		}, stop2, "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("心跳写失败应结束消费(及时回收连接)")
	}
}
