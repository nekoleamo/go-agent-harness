// consumeStream 回归单测:重放→实时切换窗口(事件在重放快照后、实时订阅建立后
// 广播)不重不漏。旧实现「先重放后订阅」在重放期间新广播的帧既不进重放集、
// 又错过订阅 → 永久丢失;修复为「先订阅后重放 + 按 Seq 去重」。
package web

import (
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
	var got []uint64
	sink := func(f Frame) error { mu.Lock(); got = append(got, f.ID); mu.Unlock(); return nil }
	done := make(chan struct{})
	go func() { defer close(done); s.consumeStream(0, sink, stop) }()

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
	want := []uint64{1, 2, 3, 0} // 会话帧各恰一次 + 状态帧透传
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("帧序应 %v(切换窗口不重不漏),得 %v", want, got)
	}
}
