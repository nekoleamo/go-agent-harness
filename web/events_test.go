// EventHub 单测:会话事件→帧(带 Seq)、非会话帧广播、历史重放(ReplayAfter/断线续传)。
package web

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// memLog 内存 SessionLog stub(测试;仅 Replay/Append 有实际行为)。
type memLog struct {
	mu   sync.Mutex
	evs  []sdk.SessionEvent
	next uint64
}

func (m *memLog) Append(ev sdk.SessionEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ev.Seq == 0 {
		m.next++
		ev.Seq = m.next
	}
	m.evs = append(m.evs, ev)
	return nil
}
func (m *memLog) Replay() []sdk.SessionEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sdk.SessionEvent(nil), m.evs...)
}
func (m *memLog) DeriveMessages() []sdk.LLMMessage              { return nil }
func (m *memLog) Flush() error                                  { return nil }
func (m *memLog) SetPath(string)                                {}
func (m *memLog) Load(string) error                             { return nil }
func (m *memLog) SetHistory(int)                                {}
func (m *memLog) RegisterCompressor(int, sdk.SessionCompressor) {}

// testCtx 最小 sdk.Ctx:只实现 Subscribe/Emit(供 hub 订阅测试)。
type testCtx struct {
	subs map[string][]sdk.AnyListener
	mu   sync.Mutex
}

func newTestCtx() *testCtx                   { return &testCtx{subs: map[string][]sdk.AnyListener{}} }
func (c *testCtx) Provide(string, any) error { return nil }
func (c *testCtx) Inject(string, any) error  { return nil }
func (c *testCtx) Logger() *slog.Logger      { return slog.Default() }
func (c *testCtx) Emit(_ context.Context, name string, payload any, _ sdk.DispatchMode) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, fn := range c.subs[name] {
		_ = fn(context.Background(), &sdk.Event{Name: name, Payload: payload})
	}
	return nil, nil
}
func (c *testCtx) Subscribe(name string, fn sdk.AnyListener) sdk.Disposer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subs[name] = append(c.subs[name], fn)
	return func() {}
}
func (c *testCtx) fire(name string, payload any) {
	c.mu.Lock()
	list := append([]sdk.AnyListener(nil), c.subs[name]...)
	c.mu.Unlock()
	for _, fn := range list {
		_ = fn(context.Background(), &sdk.Event{Name: name, Payload: payload})
	}
}

func TestHubBroadcastAndSeq(t *testing.T) {
	ctx := newTestCtx()
	log := &memLog{}
	hub := NewHub()
	unsub, err := hub.Subscribe(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()

	ch, release := hub.Stream()
	defer release()
	// 宿主总线广播会话事件(生产路径:sessionlog Append 后发 session/event)
	ctx.fire(sdk.EventSession, &sdk.SessionEvent{Kind: sdk.EventUserMessage, Seq: 1, Payload: &sdk.UserMessage{Content: "hi"}})
	ctx.fire(sdk.EventAgentStatus, "running")
	f1 := <-ch
	if f1.Type != FrameSession || f1.ID != 1 {
		t.Fatalf("期望会话帧 id=1,得 %+v", f1)
	}
	f2 := <-ch
	if f2.Type != FrameStatus || f2.Payload != "running" {
		t.Fatalf("期望状态帧 running,得 %+v", f2)
	}
	if hub.LastSeq() != 1 {
		t.Fatalf("LastSeq=1,得 %d", hub.LastSeq())
	}
}

func TestReplayAfter(t *testing.T) {
	ctx := newTestCtx()
	log := &memLog{}
	for i := 0; i < 5; i++ {
		_ = log.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: &sdk.UserMessage{Content: "m"}})
	}
	hub := NewHub()
	_ = log.Append(sdk.SessionEvent{Kind: sdk.EventAssistantMessage}) // seq 6
	_, _ = hub.Subscribe(ctx, log)

	all := hub.ReplayAfter(log, 0)
	if len(all) != 6 {
		t.Fatalf("全量重放应 6 帧,得 %d", len(all))
	}
	for i, f := range all {
		if !f.Replay || f.ID != uint64(i+1) {
			t.Fatalf("帧 %d: replay 标记或 id 不符 %+v", i, f)
		}
	}
	after3 := hub.ReplayAfter(log, 3)
	if len(after3) != 3 || after3[0].ID != 4 {
		t.Fatalf("after=3 应续传 4..6,得 %+v", after3)
	}
	_ = ctx
}

func TestHubDisposerIdempotent(t *testing.T) {
	ctx := newTestCtx()
	log := &memLog{}
	hub := NewHub()
	unsub, err := hub.Subscribe(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	unsub()
	unsub() // 幂等
}
