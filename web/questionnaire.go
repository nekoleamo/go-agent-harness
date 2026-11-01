// Web 整卷问卷服务(P3 扩展):一次问完多题,tab 栏进度 + 确认页 + 部分作答。
//
// 与单题(question.go)共用同一个 QuestionService 实例、同一个 EventHub 与同一套会话归属:
//   - 单题帧 FrameQuestion(向后兼容,老前端照旧)
//   - 整卷帧 FrameQuestionnaire(老前端不认 ⇒ 静默忽略,不会弹出一个答不了的弹层)
//
// 为什么部分作答要落服务端:整卷会阻塞很久(用户在读/在想),而 question 帧是**实时广播、
// 不落账本**。只把答案存在前端内存里(pi 的 TUI 组件做法)在浏览器里意味着"刷新一下
// 已答的三题全丢";落在 pending 里,重连时 PendingQuestionnaireFrames 带上 Answered,
// tab 栏的 ■ 与已选项都能恢复。
package web

import (
	"context"
	"sort"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// QuestionnaireRequest 整卷载荷(SSE FrameQuestionnaire;前端据此渲染 tab 栏/确认页)。
type QuestionnaireRequest struct {
	ID        string                        `json:"id"`                 // 整卷 id(/api/questionnaire/* 回传)
	Title     string                        `json:"title,omitempty"`    // tab 栏上方标题
	Questions []sdk.Question                `json:"questions"`          // 每题带 label/hint/allow_other/allow_chat
	Answered  map[string]sdk.QuestionAnswer `json:"answered,omitempty"` // 补推时的已答集合(刷新后恢复 ■)
}

// QuestionnaireDone 整卷已解决(多端并存时按 id 关闭本端遗留弹层)。
type QuestionnaireDone struct {
	ID     string                  `json:"id"`
	Answer sdk.QuestionnaireAnswer `json:"answer,omitempty"`
	Err    string                  `json:"err,omitempty"`
}

// pendingQuestionnaire 一个未决整卷:作答通道 + 补推载荷 + 逐题作答累加器。
type pendingQuestionnaire struct {
	ch   chan sdk.QuestionnaireAnswer
	req  *QuestionnaireRequest
	sess string

	mu      sync.Mutex
	answers map[string]sdk.QuestionAnswer // 题 id → 已作答(服务端权威副本)
	done    bool
}

// snapshot 已答集合副本(补推载荷用;避免与并发作答共享 map)。
func (p *pendingQuestionnaire) snapshot() map[string]sdk.QuestionAnswer {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.answers) == 0 {
		return nil
	}
	out := make(map[string]sdk.QuestionAnswer, len(p.answers))
	for k, v := range p.answers {
		out[k] = v
	}
	return out
}

// PresentQuestionnaire 推送整卷弹层并返回作答通道(sdk.QuestionnairePresenter)。
// cancel 幂等清理本次 pending(其它渠道已答/超时/回合取消)。
func (s *QuestionService) PresentQuestionnaire(ctx context.Context, qn sdk.Questionnaire) (<-chan sdk.QuestionnaireAnswer, func(), error) {
	qn = sdk.NormalizeQuestionnaire(qn)
	ch := make(chan sdk.QuestionnaireAnswer, 1)
	sess := sdk.SessionFromContext(ctx)
	p := &pendingQuestionnaire{
		ch:      ch,
		sess:    sess,
		answers: make(map[string]sdk.QuestionAnswer, len(qn.Questions)),
		req:     &QuestionnaireRequest{ID: qn.ID, Title: qn.Title, Questions: qn.Questions},
	}
	s.mu.Lock()
	s.qnPending[qn.ID] = p
	s.mu.Unlock()
	s.hub.Push(Frame{Type: FrameQuestionnaire, Session: sess, Payload: p.req})
	cancel := func() {
		s.mu.Lock()
		if cur, ok := s.qnPending[qn.ID]; ok && cur == p {
			delete(s.qnPending, qn.ID)
		}
		s.mu.Unlock()
	}
	return ch, cancel, nil
}

// AskQuestionnaire 整卷提问的同步语义(单 web profile 的 sdk.QuestionnaireService 实现,
// 与单题 Ask 同口径:ctx 取消按失败返回)。
func (s *QuestionService) AskQuestionnaire(ctx context.Context, qn sdk.Questionnaire) (sdk.QuestionnaireAnswer, error) {
	ch, cancel, err := s.PresentQuestionnaire(ctx, qn)
	if err != nil {
		return sdk.QuestionnaireAnswer{}, err
	}
	defer cancel()
	select {
	case a := <-ch:
		return a, nil
	case <-ctx.Done():
		return sdk.QuestionnaireAnswer{}, ctx.Err()
	}
}

// AnswerQuestion 记录**单题**作答(/api/questionnaire/answer);整卷 id 未知则忽略
// (已超时/重复/已收口),返回是否确被记录。同一题重复作答以最后一次为准(幂等覆盖)。
func (s *QuestionService) AnswerQuestion(id, questionID string, ans sdk.QuestionAnswer) bool {
	s.mu.Lock()
	p, found := s.qnPending[id]
	s.mu.Unlock()
	if !found || questionID == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return false
	}
	if _, ok := questionIndex(p.req, questionID); !ok {
		return false // 不在本卷内:不静默接受(前端拼错/旧帧不污染)
	}
	p.answers[questionID] = ans
	return true
}

// SubmitQuestionnaire 收口整卷(/api/questionnaire/submit):按题序组装答案(未答留零值),
// 支持整卷取消与「聊聊这个」;重复提交忽略(先到的生效)。
func (s *QuestionService) SubmitQuestionnaire(id string, cancelled bool, chat string) bool {
	s.mu.Lock()
	p, found := s.qnPending[id]
	if found {
		delete(s.qnPending, id)
	}
	s.mu.Unlock()
	if !found {
		return false
	}
	p.mu.Lock()
	if p.done {
		p.mu.Unlock()
		return false
	}
	p.done = true
	out := sdk.QuestionnaireAnswer{
		Answers:   make([]sdk.QuestionAnswer, len(p.req.Questions)),
		Cancelled: cancelled,
		Chat:      chat,
	}
	for i, q := range p.req.Questions {
		if a, ok := p.answers[q.ID]; ok {
			out.Answers[i] = a
		}
	}
	p.mu.Unlock()
	select {
	case p.ch <- out:
	default: // 通道缓冲 1:重复提交/已投递不阻塞
	}
	return true
}

// PendingQuestionnaireFrames 未决整卷的补推帧(含会话归属与已答集合),按整卷 id 升序。
func (s *QuestionService) PendingQuestionnaireFrames() []Frame {
	s.mu.Lock()
	ids := make([]string, 0, len(s.qnPending))
	for id := range s.qnPending {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	sort.Strings(ids)
	out := make([]Frame, 0, len(ids))
	for _, id := range ids {
		s.mu.Lock()
		p, ok := s.qnPending[id]
		s.mu.Unlock()
		if !ok {
			continue
		}
		req := &QuestionnaireRequest{
			ID:        p.req.ID,
			Title:     p.req.Title,
			Questions: p.req.Questions,
			Answered:  p.snapshot(),
		}
		out = append(out, Frame{Type: FrameQuestionnaire, Session: p.sess, Payload: req})
	}
	return out
}

// QuestionnaireCount 当前未决整卷数(诊断/融合断言)。
func (s *QuestionService) QuestionnaireCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.qnPending)
}

// questionIndex 按题 id 定位卷内序号(不存在返回 false)。
func questionIndex(req *QuestionnaireRequest, questionID string) (int, bool) {
	for i := range req.Questions {
		if req.Questions[i].ID == questionID {
			return i, true
		}
	}
	return 0, false
}
