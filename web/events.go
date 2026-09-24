// Package web 提供 Web UI 运行时(对称 tui/ 先例):HTTP/SSE 服务 + 事件订阅 + Web 确认服务。
// 能力全在宿主,浏览器只订阅事件流(SSE 下行)+ REST 上行;消息 JSON 结构冻结(v1)。
//
// 事件通道设计:订阅 session/event(每次会话 Append 后广播,载荷 *sdk.SessionEvent 自带 Seq)
// 与 agent/status、agent/error。SSE 帧 id = 会话事件 Seq,天然支持 Last-Event-ID 断线重放:
// 连接建立时按 ?after=<seq> 从会话历史(Replay 全量)重投 seq 之后的事件,再续实时流。
package web

import (
	"context"
	"fmt"
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
	// 前端据此更新连接卡,取代 2s 轮询)。
	// FrameDoc 文档预览意图(D 组 D5:模型 doc_open 工具 / `/preview` 命令发出 doc/open;
	// 载荷 sdk.DocOpenEvent —— 前端打开文档面板并定位文件)。
	FrameDoc = "doc"
	// FrameQuestionDone 提问已解决(G-E5-4:question/resolved 事件订阅面;
	// 多端并存时关闭本端遗留弹层 —— 已由其它渠道作答/超时)。载荷 *QuestionDone。
	FrameQuestionDone = "questiondone"
	// FrameConfirmDone 审批已裁决(G-E5-4:confirm/resolved 事件订阅面;
	// 多端并存时关闭本端遗留弹层)。载荷 *ConfirmDone。
	FrameConfirmDone = "confirmdone"
	// FrameSchedule 定时计划运行终态(schedule/run;NOND-W4 无人值守任务):
	// 前端据此刷新计划列表(上次运行时间/状态/下次触发已变),无需轮询。
	// 载荷 sdk.ScheduleRunEvent。
	FrameSchedule = "schedule"
	// FrameDiff 变更审查意图(S-P1-1:`/diff` 命令发 diff/open;载荷 sdk.DiffOpenEvent)。
	// 前端切到变更视图;Path/Diff 非空时定位到该文件(内容本就可从事件账本重建,不依赖 git)。
	FrameDiff = "diff"
	// FrameBaseline 首帧基线(S-P1-2):**首连的第一帧**,描述本次回放的事件窗口
	// (载荷 Baseline)。前端据此知道「更早历史还没加载」,从而显示上滚入口并按需分页。
	// 只在全新连接(after==0)发送:断线续传是差集补齐,不描述窗口(前端保留自己的窗口状态)。
	FrameBaseline = "baseline"
	// FrameNotice 用户提示(NOND-N1:`ctx.notices` 发出 `sdk.EventNotice`;载荷 *sdk.Notice)。
	// 面向「需要人回来的时刻」(后台任务终态/定时计划失败或跳过/回合报错):前端弹离散 toast
	// (不占用会话流)。提示不进会话记录 → 刷新/重连后靠 `GET /api/notices?since=<id>` 回填。
	FrameNotice = "notice"
	// FrameSteerDropped 回合结束时未注入的转向消息(宿主 agent/steer-dropped;载荷 []string)。
	// 用户在本端插了话而回合却结束了(取消/失败):前端把内容还回输入框并提示,
	// 不静默丢 —— 与 TUI 侧「转为待发」同语义。
	FrameSteerDropped = "steer_dropped"
)

// QuestionDone 提问解决载荷(多端同步观察:按 id 关闭本端遗留弹层)。
type QuestionDone struct {
	ID     string             `json:"id"`               // 与弹层同一 id(Fusion 补齐 / ObservedQuestion 生成)
	Answer sdk.QuestionAnswer `json:"answer,omitempty"` // 最终作答(空 = 取消/超时)
	Err    string             `json:"err,omitempty"`
}

// ConfirmDone 审批裁决载荷(按 prompt 关闭本端遗留弹层;Confirm 无端侧 id)。
type ConfirmDone struct {
	Prompt string `json:"prompt"`
	OK     bool   `json:"ok,omitempty"`
	Err    string `json:"err,omitempty"`
}

// Baseline 首帧基线载荷(S-P1-2:长会话的首帧窗口描述)。
// From/To 是本窗口的事件 Seq 边界(实时帧从 To 之后续接);HasMore = 窗口前还有更早事件。
type Baseline struct {
	From    uint64 `json:"from"`     // 窗口最老事件 Seq(空会话 = 0)
	To      uint64 `json:"to"`       // 窗口最新事件 Seq
	Count   int    `json:"count"`    // 窗口内事件条数
	HasMore bool   `json:"has_more"` // 更早历史存在(前端显示「上滚加载」)
	Window  int    `json:"window"`   // 服务端窗口口径(条数;供前端解释为何看不到更早内容)
}

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
	add(sdk.EventDocOpen, func(_ context.Context, ev *sdk.Event) error {
		// 文档预览意图(doc/open):广播给浏览器 → 前端打开文档面板并定位该文件
		switch p := ev.Payload.(type) {
		case sdk.DocOpenEvent:
			h.Push(Frame{Type: FrameDoc, Payload: p})
		case *sdk.DocOpenEvent:
			h.Push(Frame{Type: FrameDoc, Payload: p})
		}
		return nil
	})
	add(sdk.EventDiffOpen, func(_ context.Context, ev *sdk.Event) error {
		// 变更审查意图(diff/open):广播给浏览器 → 前端切到变更视图(可选定位单文件)
		switch p := ev.Payload.(type) {
		case sdk.DiffOpenEvent:
			h.Push(Frame{Type: FrameDiff, Payload: p})
		case *sdk.DiffOpenEvent:
			h.Push(Frame{Type: FrameDiff, Payload: p})
		}
		return nil
	})
	add(sdk.EventScheduleRun, func(_ context.Context, ev *sdk.Event) error {
		// 定时计划跑到终态(ok/failed/skipped):广播给浏览器 → 前端刷新计划列表。
		// 无人值守任务没有人在场,状态变化必须主动推到 UI,否则用户明天才看得到失败。
		switch p := ev.Payload.(type) {
		case sdk.ScheduleRunEvent:
			h.Push(Frame{Type: FrameSchedule, Payload: p})
		case *sdk.ScheduleRunEvent:
			h.Push(Frame{Type: FrameSchedule, Payload: p})
		}
		return nil
	})
	add(sdk.EventNotice, func(_ context.Context, ev *sdk.Event) error {
		// 事件与回填端点同源(ctx.notices.List),两者给的是同一份载荷。
		switch p := ev.Payload.(type) {
		case *sdk.Notice:
			h.Push(Frame{Type: FrameNotice, Payload: p})
		case sdk.Notice:
			h.Push(Frame{Type: FrameNotice, Payload: p})
		}
		return nil
	})
	add(sdk.EventQuestionResolved, func(_ context.Context, ev *sdk.Event) error {
		// G-E5-4:提问已解决(requested↔resolved 事件订阅面)。多端并存时本端弹层可能
		// 仍开着(已由 web 之外的渠道作答)—— 推送 done 帧让前端按 id 关闭遗留弹层。
		qe, ok := sdk.QuestionEventOf(ev.Payload)
		if !ok {
			return nil
		}
		h.Push(Frame{Type: FrameQuestionDone, Payload: &QuestionDone{
			ID: qe.Question.ID, Answer: qe.Answer, Err: qe.Err,
		}})
		return nil
	})
	add(sdk.EventConfirmResolved, func(_ context.Context, ev *sdk.Event) error {
		// G-E5-4:审批已裁决(同上;Confirm 无端侧 id,按 prompt 关联)。
		ce, ok := sdk.ConfirmEventOf(ev.Payload)
		if !ok {
			return nil
		}
		h.Push(Frame{Type: FrameConfirmDone, Payload: &ConfirmDone{
			Prompt: ce.Prompt, OK: ce.OK, Err: ce.Err,
		}})
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
		// 载荷归一为**文本**再下发:同一事件在 TUI 内是 error 值(可直接 Error()),
		// 但 error 经 HTTP/JSON 序列化为 `{}` → 前端 String(payload) 只能显示
		// "[object Object]"(2026-09-19 阶段 7 真机逮到:断网回合"错误可读"不达标)。
		f := Frame{Type: FrameError, Payload: errorTextOf(ev.Payload)}
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

// broadcast 向全部活跃流投递帧(非阻塞;调用方持 h.mu)。
// 会话帧丢弃不能静默:游标 replay 只在连接重建时发生,而"只是慢"的连接不会断——
// 被丢的帧会永久缺失(前端消息与后端会话日志漂移,直到手动切会话)。故丢会话帧即
// 摘除并关闭该流(在锁内摘,不会再有人写),读侧见通道关闭 → 断开 → 客户端按
// after 游标重连重放补齐。非会话帧(status 等)丢弃无害:下一帧即最新。
func (h *EventHub) broadcast(f Frame) {
	if f.Type != FrameSession {
		for _, ch := range h.subs {
			select {
			case ch <- f:
			default:
			}
		}
		return
	}
	keep := h.subs[:0]
	for _, ch := range h.subs {
		select {
		case ch <- f:
			keep = append(keep, ch)
		default:
			close(ch) // 摘除:读侧收到关闭 → 断流重连重放
		}
	}
	for i := len(keep); i < len(h.subs); i++ {
		h.subs[i] = nil // 释放引用,防订阅者泄漏
	}
	h.subs = keep
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
				// 已因丢帧被摘除时不会命中(语义等价:流已关闭)
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

// ReplayAfter 取会话全量事件中 seq 严格大于 after 的帧(**断线续传差集**;
// 首连历史重放不走这里 —— 见 consumeStream 的尾部窗口 + Baseline 基线)。帧标记 Replay=true。
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

// ReplayTail 取会话事件**尾部窗口**的帧 + 窗口基线(首连重放;S-P1-2)。
// 窗口回合对齐(S-P1-2 见 web/paging.go 头部);更早历史由前端上滚经 /api/session/events 分页拉取。
func (h *EventHub) ReplayTail(sessions sdk.SessionLog) (frames []Frame, base Baseline) {
	evs := sessions.Replay()
	page, hasMore := pageEvents(evs, 0, SessionTailEvents)
	frames = make([]Frame, 0, len(page))
	for i := range page {
		ev := page[i]
		frames = append(frames, Frame{ID: ev.Seq, Type: FrameSession, TS: ev.TS.UnixMilli(), Payload: &ev, Replay: true})
	}
	base = Baseline{Count: len(page), HasMore: hasMore, Window: SessionTailEvents}
	if len(page) > 0 {
		base.From = page[0].Seq
		base.To = page[len(page)-1].Seq
	}
	return frames, base
}

// Push 进程内主动投递一帧(confirm/command 结果;广播全部活跃流)。
func (h *EventHub) Push(f Frame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.broadcast(f)
}

// errorTextOf 把回合错误载荷归一为可读文本。
// 生产路径上 err 是 error 值(TUI 订阅方直接类型断言 Error()),而 web 帧要过 JSON ——
// error 无导出字段 → `{}` → 前端 String(payload) 得到 "[object Object]"。
// 故在 web 面统一转文本(字符串/其它形态同样兜住,不因载荷形态变化再退化)。
func errorTextOf(p any) string {
	switch v := p.(type) {
	case nil:
		return ""
	case error:
		return v.Error()
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}
