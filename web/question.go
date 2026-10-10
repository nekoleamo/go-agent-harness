// Web 结构化提问服务(P3 语义交互):问题弹层推送前端(SSE question 帧),
// 经 REST POST /api/question 回传作答。与审批确认(confirm)同管道模式。
// 融合场景经 host-confirm-fusion 注册为 web 渠道呈现者(与 TUI 首答生效)。
package web

import (
	"context"
	"sort"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// QuestionRequest 提问弹层载荷(SSE FrameQuestion 的 payload;前端据此渲染)。
type QuestionRequest struct {
	ID       string               `json:"id"`                  // 弹层唯一 id(/api/question 回传)
	Prompt   string               `json:"prompt"`              // 问题正文
	Options  []sdk.QuestionOption `json:"options,omitempty"`   // 可选项(空 = 自由文本)
	Multiple bool                 `json:"multiple,omitempty"`  // 多选
	FreeText bool                 `json:"free_text,omitempty"` // 允许自由文本作答
}

// QuestionService Web 版 sdk.QuestionPresenter 实现(经 EventHub 推送)。
//
// pending 同时存**待推帧的载荷**与归属会话:question 帧是实时广播、不落会话账本,
// 而提问会一直阻塞等待作答 —— 用户刷新/重开页面后弹层就丢了,不补推他会永远等下去
// (与 confirm 的 PendingFrames 同一件事,2026-10-10 补齐)。
type QuestionService struct {
	hub *EventHub

	mu      sync.Mutex
	pending map[string]pendingQuestion
}

// pendingQuestion 一个未决提问:作答通道 + 补推所需的载荷与会话归属。
type pendingQuestion struct {
	ch   chan sdk.QuestionAnswer
	req  *QuestionRequest
	sess string
}

// NewQuestionService 构造 Web 提问服务(prompt 经 hub 广播为 FrameQuestion 帧)。
func NewQuestionService(hub *EventHub) *QuestionService {
	return &QuestionService{hub: hub, pending: make(map[string]pendingQuestion)}
}

// PresentQuestion 推送提问弹层并返回作答通道;cancel 幂等清理本次 pending。
// G-E5-4:id 优先用调用方给定的 q.ID(与 question/requested↔resolved 事件同 id,
// 以便其它渠道作答时前端能按 id 关闭遗留弹层),未给才生成。
func (s *QuestionService) PresentQuestion(ctx context.Context, q sdk.Question) (<-chan sdk.QuestionAnswer, func(), error) {
	id := q.ID
	if id == "" {
		id = randID()
	}
	ch := make(chan sdk.QuestionAnswer, 1)
	// 帧带归属会话(回合入口注入):多会话并行时,弹层必须落在**提出问题的那一个**会话,
	// 否则会在别的会话视图里弹出来,用户答的等于替别人答。
	req := &QuestionRequest{
		ID: id, Prompt: q.Prompt, Options: q.Options, Multiple: q.Multiple, FreeText: q.FreeText,
	}
	// 归属取一次并存下:补推帧必须与实时帧同一个会话归属。
	sess := sdk.SessionFromContext(ctx)
	s.mu.Lock()
	s.pending[id] = pendingQuestion{ch: ch, req: req, sess: sess}
	s.mu.Unlock()
	s.hub.Push(Frame{Type: FrameQuestion, Session: sess, Payload: req})
	cancel := func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}
	return ch, cancel, nil
}

// Answer 接收前端作答(/api/question 处理器调用);未知弹层 id 忽略(已超时/重复)。
func (s *QuestionService) Answer(id string, ans sdk.QuestionAnswer) {
	s.mu.Lock()
	p, found := s.pending[id]
	s.mu.Unlock()
	if !found {
		return
	}
	select {
	case p.ch <- ans:
	default:
	}
}

// PendingFrames 未决提问的补推帧(含**归属会话**),按弹层 id 升序。
// 与 ConfirmService.PendingFrames 同款:新连接建立/页面刷新时补推,否则弹层丢失
// 而提问侧一直阻塞等待(回合挂死)。
func (s *QuestionService) PendingFrames() []Frame {
	s.mu.Lock()
	ids := make([]string, 0, len(s.pending))
	for id := range s.pending {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	sort.Strings(ids)
	out := make([]Frame, 0, len(ids))
	s.mu.Lock()
	for _, id := range ids {
		p, ok := s.pending[id]
		if !ok {
			continue
		}
		out = append(out, Frame{Type: FrameQuestion, Session: p.sess, Payload: p.req})
	}
	s.mu.Unlock()
	return out
}

// PendingCount 当前未决提问数(融合断言/诊断)。
func (s *QuestionService) PendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// SingleChannel sdk.QuestionService 适配(web-only profile:无 host-confirm-fusion 时
// ctx.question 提供方)。Ask 直连本服务呈现,无渠道注册(web 即唯一渠道)。
// 修正历史缺口:单 web profile 曾直接 Provide *QuestionService(仅 QuestionPresenter,
// 不实现 Ask)→ 工具侧 Inject 类型不符,ask_user_question 实际不可用。
func (s *QuestionService) SingleChannel() sdk.QuestionService { return singleQuestion{s: s} }

// singleQuestion 单渠道提问服务适配。
type singleQuestion struct{ s *QuestionService }

// Ask 呈现提问并等待作答(ctx 取消按失败返回)。
func (q singleQuestion) Ask(ctx context.Context, question sdk.Question) (sdk.QuestionAnswer, error) {
	ch, cancel, err := q.s.PresentQuestion(ctx, question)
	if err != nil {
		return sdk.QuestionAnswer{}, err
	}
	defer cancel()
	select {
	case a := <-ch:
		return a, nil
	case <-ctx.Done():
		return sdk.QuestionAnswer{}, ctx.Err()
	}
}

// RegisterQuestioner 单渠道场景无需注册(本服务即唯一渠道)。
func (q singleQuestion) RegisterQuestioner(string, sdk.QuestionPresenter) sdk.Disposer {
	return func() {}
}
