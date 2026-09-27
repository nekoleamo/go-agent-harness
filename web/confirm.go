// Web 确认服务(ctx.confirm):审批弹层推送前端,经 REST /api/confirm 回传用户决定。
// 与 TUI 弹层同语义:Confirm 阻塞等待用户应答;ctx 取消/连接中断按拒绝处理(安全默认)。
package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
)

// ConfirmRequest 审批弹层载荷(SSE 帧 payload;前端据此渲染弹层)。
type ConfirmRequest struct {
	ID     string `json:"id"`     // 弹层唯一 id(/api/confirm 回传)
	Prompt string `json:"prompt"` // 待确认说明
}

// ConfirmService Web 版 sdk.ConfirmService 实现。必须与 EventHub 配合(推送经 hub)。
type ConfirmService struct {
	hub *EventHub

	mu      sync.Mutex
	pending map[string]pendingConfirm
}

// pendingConfirm 未决确认(prompt 留着是为了重连重放:连接断开时推出去的弹层会丢)。
type pendingConfirm struct {
	prompt string
	ch     chan bool
}

// NewConfirm 构造 Web 确认服务(prompt 经 hub 广播为 FrameConfirm 帧)。
func NewConfirm(hub *EventHub) *ConfirmService {
	return &ConfirmService{hub: hub, pending: make(map[string]pendingConfirm)}
}

// Present sdk.ConfirmPresenter:推送审批弹层并返回应答通道(/api/confirm 回传);
// cancel 清理本次 pending(幂等)。融合场景(Fusion 广播)多 UI 并存共用。
func (s *ConfirmService) Present(ctx context.Context, prompt string) (<-chan bool, func(), error) {
	id := randID()
	ch := make(chan bool, 1)
	s.mu.Lock()
	s.pending[id] = pendingConfirm{prompt: prompt, ch: ch}
	s.mu.Unlock()
	s.hub.Push(Frame{Type: FrameConfirm, Payload: &ConfirmRequest{ID: id, Prompt: prompt}})
	cancel := func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}
	return ch, cancel, nil
}

// Confirm 推送审批弹层并阻塞等待用户应答。
func (s *ConfirmService) Confirm(ctx context.Context, prompt string) (bool, error) {
	ch, cancel, err := s.Present(ctx, prompt)
	if err != nil {
		return false, err
	}
	defer cancel()
	select {
	case ok := <-ch:
		return ok, nil
	case <-ctx.Done():
		// 超时/取消(回合被停止、进程退出):安全默认拒绝,且**必须通知前端关掉弹层**。
		// 为何要自己推:单 profile 是 web 自己 Provide ctx.confirm,不经 host-confirm-fusion,
		// 没有 confirm/resolved 事件可订阅 —— 不推这一帧,弹层就永远挂在界面上(真机反馈:
		// 「超时系统默认失败,继续进行,但弹窗仍在界面上」)。融合路径不会走到这里
		// (由 Fusion 统一广播 resolved)。
		s.hub.Push(Frame{Type: FrameConfirmDone, Payload: &ConfirmDone{Prompt: prompt, Err: ctx.Err().Error()}})
		return false, ctx.Err()
	}
}

// Pending 当前未决确认(按 id 升序),供**新连接建立时补推**。
// 为何需要:confirm 帧是实时广播、不落会话账本,而审批现在默认**不限时地等** ——
// 用户刷新/重开页面期间弹层就丢了,不补推他就会永远等下去(回合一直挂)。
func (s *ConfirmService) Pending() []*ConfirmRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*ConfirmRequest, 0, len(s.pending))
	for id, p := range s.pending {
		out = append(out, &ConfirmRequest{ID: id, Prompt: p.prompt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Answer 接收前端应答(/api/confirm 处理器调用);未知弹层 id 忽略(已超时/重复应答)。
func (s *ConfirmService) Answer(id string, ok bool) {
	s.mu.Lock()
	p, found := s.pending[id]
	s.mu.Unlock()
	if !found {
		return
	}
	select {
	case p.ch <- ok:
	default:
	}
}

// PendingCount 当前未决确认数(融合断言/诊断)。
func (s *ConfirmService) PendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// randID 生成 8 字节随机十六进制 id(审批弹层标识,不可枚举猜解)。
func randID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "confirm-unknown"
	}
	return hex.EncodeToString(b)
}
