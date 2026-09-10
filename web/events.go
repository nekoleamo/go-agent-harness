// Package web 提供 Web UI 运行时(对称 tui/ 先例):HTTP/SSE 服务 + 事件订阅 + Web 确认服务。
// 能力全在宿主,浏览器只订阅事件流(SSE 下行)+ REST 上行;消息 JSON 结构冻结(v1)。
//
// 事件通道设计:订阅 session/event(每次会话 Append 后广播,载荷 *sdk.SessionEvent 自带 Seq)
// 与 agent/status、agent/error。SSE 帧 id = 会话事件 Seq,天然支持 Last-Event-ID 断线重放:
// 连接建立时按 ?after=<seq> 从会话历史(Replay 全量)重投 seq 之后的事件,再续实时流。
package web

import (
	"context"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// FrameType SSE 帧类别(前端分发用)。
const (
	// FrameSession 会话事件(载荷 *sdk.SessionEvent,JSON 序列化后为 {kind,seq,ts,payload})。
	FrameSession = "session"
	// FrameStatus 代理运行状态(载荷 string:"running"/"idle")。
	FrameStatus = "status"
	// FrameError 回合错误(agent/error 载荷)。
	FrameError = "error"
	// FrameConfirm 审批弹层推送(载荷 *ConfirmRequest)。
	FrameConfirm = "confirm"
	// FrameQuestion 结构化提问弹层推送(载荷 *QuestionRequest;P3 语义交互)。
	FrameQuestion = "question"
	// FrameCommand 命令执行结果(载荷 *CommandResult)。
	FrameCommand = "command"
)

// Frame 一条 SSE 帧(JSON 序列化后发往浏览器)。
type Frame struct {
	ID      uint64 `json:"id"`               // 会话事件 Seq;非会话帧为 0
	Type    string `json:"type"`             // FrameType
	TS      int64  `json:"ts,omitempty"`     // 会话事件时间戳(unix ms;非会话帧可省略)
	Payload any    `json:"payload"`          // 事件载荷
	Replay  bool   `json:"replay,omitempty"` // true = 历史重放帧(连接建立/断线续传)
}

// EventHub 订阅宿主事件并转换为 SSE 帧;支持按会话 Seq 断线续传。
// 只读历史经 sdk.SessionLog.Replay()(全量事件,坏行容忍由宿主保证)。
type EventHub struct {
	mu      sync.Mutex
	subs    []chan Frame // 活跃 SSE 流(广播;慢消费者由 server 层 per-stream goroutine 隔离)
	lastSeq uint64       // 最近会话 Seq(Replay 上限/快照起点)
	subsD   []sdk.Disposer
}

// NewHub 构造事件通道(惰性:Subscribe 时才订阅宿主事件)。
func NewHub() *EventHub {
	return &EventHub{}
}

// Subscribe 订阅宿主事件(对齐 TUI 侧订阅集:会话事件 + 状态 + 错误)。
func (h *EventHub) Subscribe(c sdk.Ctx, sessions sdk.SessionLog) (disposer sdk.Disposer, err error) {
	var ds []sdk.Disposer
	add := func(name string, fn sdk.AnyListener) {
		d := c.Subscribe(name, fn)
		ds = append(ds, d)
	}
	add(sdk.EventSession, func(_ context.Context, ev *sdk.Event) error {
		se, ok := ev.Payload.(*sdk.SessionEvent)
		if !ok {
			return nil
		}
		h.push(se)
		return nil
	})
	add(sdk.EventAgentStatus, func(_ context.Context, ev *sdk.Event) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		f := Frame{Type: FrameStatus, Payload: ev.Payload}
		h.broadcast(f)
		return nil
	})
	add(sdk.EventAgentError, func(_ context.Context, ev *sdk.Event) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		f := Frame{Type: FrameError, Payload: ev.Payload}
		h.broadcast(f)
		return nil
	})
	h.mu.Lock()
	h.subsD = ds
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, d := range h.subsD {
			d()
		}
		h.subsD = nil
	}, nil
}

// push 会话事件 → 帧并广播(维持 lastSeq)。
func (h *EventHub) push(se *sdk.SessionEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if se.Seq > h.lastSeq {
		h.lastSeq = se.Seq
	}
	f := Frame{ID: se.Seq, Type: FrameSession, TS: se.TS.UnixMilli(), Payload: se}
	h.broadcast(f)
}

// broadcast 向全部活跃流投递帧(非阻塞;流缓冲由 server 层负责 drain)。
func (h *EventHub) broadcast(f Frame) {
	for _, ch := range h.subs {
		select {
		case ch <- f:
		default:
			// 慢消费者丢弃该帧(流层经会话 Replay 兜底,不阻塞事件总线)
		}
	}
}

// Stream 返回一个只接收新帧的流(调用方负责 Unsubscribe)。
func (h *EventHub) Stream() (<-chan Frame, func()) {
	ch := make(chan Frame, 256)
	h.mu.Lock()
	h.subs = append(h.subs, ch)
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		for i, c := range h.subs {
			if c == ch {
				h.subs = append(h.subs[:i], h.subs[i+1:]...)
				break
			}
		}
	}
}

// LastSeq 最近会话事件 Seq(断线续传基准;无事件 = 0)。
func (h *EventHub) LastSeq() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastSeq
}

// ReplayAfter 取会话全量事件中 seq 严格大于 after 的帧(连接建立/断线续传;
// after=0 = 全量历史重放)。帧标记 Replay=true。
func (h *EventHub) ReplayAfter(sessions sdk.SessionLog, after uint64) []Frame {
	evs := sessions.Replay()
	out := make([]Frame, 0, len(evs))
	for _, ev := range evs {
		if ev.Seq <= after {
			continue
		}
		out = append(out, Frame{ID: ev.Seq, Type: FrameSession, TS: ev.TS.UnixMilli(), Payload: &ev, Replay: true})
	}
	return out
}

// Push 进程内主动投递一帧(confirm/command 结果;广播全部活跃流)。
func (h *EventHub) Push(f Frame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.broadcast(f)
}
