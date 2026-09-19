// EventHub 单测:会话事件→帧(带 Seq)、非会话帧广播、历史重放(ReplayAfter/断线续传)。
package web

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

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

// G-E5-4:question/confirm resolved 事件 → SSE done 帧(多端并存时前端关闭遗留弹层)。
func TestInteractionResolvedFrames(t *testing.T) {
	hub := NewHub()
	c := newTestCtx()
	dis, err := hub.Subscribe(c, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	defer dis()
	ch, release := hub.Stream()
	defer release()

	c.fire(sdk.EventQuestionResolved, &sdk.QuestionEvent{
		Question: sdk.Question{ID: "q-1"},
		Answer:   sdk.QuestionAnswer{Values: []string{"prod"}},
		Resolved: true, Channel: "web",
	})
	select {
	case f := <-ch:
		if f.Type != FrameQuestionDone {
			t.Fatalf("帧类型应为 %q,得 %q", FrameQuestionDone, f.Type)
		}
		done, ok := f.Payload.(*QuestionDone)
		if !ok || done.ID != "q-1" || len(done.Answer.Values) != 1 {
			t.Fatalf("questiondone 载荷异常: %#v", f.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 FrameQuestionDone")
	}

	c.fire(sdk.EventConfirmResolved, sdk.ConfirmEvent{Prompt: "危险?", OK: true, Resolved: true, Channel: "tui"})
	select {
	case f := <-ch:
		if f.Type != FrameConfirmDone {
			t.Fatalf("帧类型应为 %q,得 %q", FrameConfirmDone, f.Type)
		}
		done, ok := f.Payload.(*ConfirmDone)
		if !ok || done.Prompt != "危险?" || !done.OK {
			t.Fatalf("confirmdone 载荷异常: %#v", f.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 FrameConfirmDone")
	}
	// 无关载荷不产帧(类型不符 → 静默)
	c.fire(sdk.EventQuestionResolved, "无关注载荷")
	select {
	case f := <-ch:
		t.Fatalf("无关载荷不应产帧: %#v", f)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestSlowConsumerSessionDropClosesStream 慢消费者丢会话帧必须摘流(通道关闭),
// 让读侧断开连接、客户端按 after 游标重连重放——静默丢弃会让前端永久少消息。
func TestSlowConsumerSessionDropClosesStream(t *testing.T) {
	h := NewHub()
	ch, release := h.Stream()
	defer release()
	n := 0
	for i := 0; i < 300; i++ {
		h.Push(Frame{Type: FrameSession, ID: uint64(i + 1), Payload: &sdk.SessionEvent{Kind: sdk.EventUserMessage, Seq: uint64(i + 1)}})
		n++
	}
	// 排空后可读到的帧数 < 推送总数(有丢弃),且通道最终关闭
	drained := 0
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				if drained >= 300 {
					t.Fatalf("通道关闭前不应丢帧: drained=%d", drained)
				}
				return
			}
			drained++
		case <-time.After(2 * time.Second):
			t.Fatal("丢帧后流应被摘除并关闭(不得静默丢弃)")
		}
	}
}

// 定时计划运行终态必须主动推给浏览器(NOND-W4):无人值守任务没有人在场,
// 失败/跳过若只能靠轮询发现,用户第二天才知道。
func TestHubScheduleFrame(t *testing.T) {
	ctx := newTestCtx()
	hub := NewHub()
	unsub, err := hub.Subscribe(ctx, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	ch, release := hub.Stream()
	defer release()

	ev := sdk.ScheduleRunEvent{ID: "sched-1", State: sdk.ScheduleRunFailed, Error: "模型调用失败"}
	ctx.fire(sdk.EventScheduleRun, &ev)
	f := <-ch
	if f.Type != FrameSchedule {
		t.Fatalf("期望计划帧,得 %+v", f)
	}
	got, ok := f.Payload.(*sdk.ScheduleRunEvent)
	if !ok || got.ID != "sched-1" || got.State != sdk.ScheduleRunFailed {
		t.Fatalf("计划帧载荷不符 %+v", f.Payload)
	}
	// 值载荷(非指针)同样应转发(事件派发两种姿势都合法)
	ctx.fire(sdk.EventScheduleRun, ev)
	f2 := <-ch
	if f2.Type != FrameSchedule {
		t.Fatalf("值载荷未转发: %+v", f2)
	}
}

// TestErrorFramePayloadIsReadable 回合错误帧必须携带**可读文本**。
// 回归护栏:载荷在同进程内是 error 值(TUI 直接 Error()),但经 JSON 序列化会变成 {}
// → 前端 String(payload) 只显示 "[object Object]"(2026-09-19 阶段 7 真机逮到)。
func TestErrorFramePayloadIsReadable(t *testing.T) {
	ctx := newTestCtx()
	hub := NewHub()
	unsub, err := hub.Subscribe(ctx, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	ch, release := hub.Stream()
	defer release()

	ctx.fire(sdk.EventAgentError, errors.New("dial tcp 127.0.0.1:9: connect: connection refused"))
	f := <-ch
	if f.Type != FrameError {
		t.Fatalf("期望错误帧,得 %+v", f)
	}
	s, ok := f.Payload.(string)
	if !ok || !strings.Contains(s, "connection refused") {
		t.Fatalf("错误帧载荷应为可读文本,得 %#v", f.Payload)
	}
	// 下发形态(JSON)同样必须可读 —— 前端拿到的就是它
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"payload":{}`) {
		t.Fatalf("JSON 载荷不可读: %s", b)
	}
	// 字符串载荷与其它类型同样兜住
	if got := errorTextOf("直接文本"); got != "直接文本" {
		t.Fatalf("字符串载荷应原样: %q", got)
	}
	if got := errorTextOf(nil); got != "" {
		t.Fatalf("nil 载荷应为空串: %q", got)
	}
}
