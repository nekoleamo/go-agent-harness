// perm.go:权限请求与结构化提问 → ACP 反向请求(session/request_permission)。
//
// 映射关系:gah 的审批确认(ctx.confirm,policy-guard 发起)与结构化提问(ctx.question,
// ask_user_question 工具发起)在编辑器里都是"agent 向用户要一次裁决",正好对应 ACP 的
// session/request_permission。选项 kind 是语义提示:allow_once/reject_once 表示本次有效。
//
// 刻意不提供的两项(登记为偏离,见 DESIGN §14.1 S-P2-3):
//  1. allow_always/reject_always:在 gah 里等价于改写审批档位(prefs 持久化),
//     不该由编辑器里一次点击隐式完成(否则"总是允许"会永久放宽危险操作)。
//  2. 自由文本/多选提问:ACP 的权限弹层是单选按钮组,无法忠实表达 → 显式报错,
//     由 TUI/Web 作答,而不是替用户挑一个选项。
package acpserver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// channelName ACP 渠道名(与 confirm/question 事件里的 Channel 字段同源)。
const channelName = "acp"

// registerPresenters 注册审批与提问的渠道呈现者。
// 缺 host-confirm-fusion 时**不静默**:此时审批无人应答(policy-guard 按拒绝处理),
// 编辑器里表现为危险操作一律失败而看不出原因,故直接让装配失败并说明怎么修。
func (s *server) registerPresenters() error {
	var fusion sdk.ConfirmFusion
	if err := s.c.Inject("ctx.confirmFusion", &fusion); err != nil || fusion == nil {
		return errors.New("acp-server: ctx.confirmFusion 未装配:ACP 的审批/提问无法送达编辑器" +
			"(profile 需包含 confirm-fusion bundle);危险操作会一律按拒绝处理,故拒绝启动")
	}
	s.presenters = append(s.presenters, fusion.Register(channelName, s))

	var qs sdk.QuestionService
	if err := s.c.Inject("ctx.question", &qs); err != nil || qs == nil {
		s.warn("ctx.question 未装配: ask_user_question 将不可用(TUI/Web 侧同样如此)")
		return nil
	}
	s.presenters = append(s.presenters, qs.RegisterQuestioner(channelName, s))
	return nil
}

// closePresenters 撤销呈现者注册(幂等)。
func (s *server) closePresenters() {
	for _, d := range s.presenters {
		if d != nil {
			d()
		}
	}
	s.presenters = nil
}

// Present 呈现一次审批确认(实现 sdk.ConfirmPresenter)。
// 立即返回应答通道:真正的等待在内部 goroutine 里做(呈现管道要求 Present 不阻塞)。
func (s *server) Present(ctx context.Context, prompt string) (<-chan bool, func(), error) {
	opts := []permissionOption{
		{OptionID: "allow_once", Name: "允许本次", Kind: "allow_once"},
		{OptionID: "reject_once", Name: "拒绝本次", Kind: "reject_once"},
	}
	sel, done, cancel, err := s.askAsync(ctx, prompt, opts, "other")
	if err != nil {
		return nil, nil, err
	}
	ch := make(chan bool, 1)
	go func() {
		var ok bool
		select {
		case id := <-sel:
			ok = id == "allow_once" // 未选中/cancelled → 拒绝(安全默认)
		case <-done:
			return
		}
		select {
		case ch <- ok:
		case <-done:
		}
	}()
	return ch, cancel, nil
}

// PresentQuestion 呈现一次结构化提问(实现 sdk.QuestionPresenter)。
// 选项经 ACP 权限选项送出(optionId = 选项下标),答复映射回 sdk.QuestionOption.Value。
func (s *server) PresentQuestion(ctx context.Context, q sdk.Question) (<-chan sdk.QuestionAnswer, func(), error) {
	if len(q.Options) == 0 || q.Multiple {
		return nil, nil, errors.New("acp: 编辑器通道不支持自由文本/多选题(需在 TUI/Web 作答)")
	}
	opts := make([]permissionOption, 0, len(q.Options))
	for i, o := range q.Options {
		name := o.Value
		if o.Desc != "" {
			name = o.Value + " (" + o.Desc + ")"
		}
		opts = append(opts, permissionOption{OptionID: strconv.Itoa(i), Name: oneLine(name, 200), Kind: "allow_once"})
	}
	sel, done, cancel, err := s.askAsync(ctx, oneLine(q.Prompt, 240), opts, "other")
	if err != nil {
		return nil, nil, err
	}
	ch := make(chan sdk.QuestionAnswer, 1)
	go func() {
		var ans sdk.QuestionAnswer
		select {
		case id := <-sel:
			if i, err := strconv.Atoi(id); err == nil && i >= 0 && i < len(q.Options) {
				ans = sdk.QuestionAnswer{Values: []string{q.Options[i].Value}}
			}
		case <-done:
			return
		}
		select {
		case ch <- ans:
		case <-done:
		}
	}()
	return ch, cancel, nil
}

// askAsync 发起反向请求并立即返回:选中项通道 / 取消信号 / 幂等 cancel。
// 选不中(客户端取消、连接断开、回合被取消)时选中通道收到空串。
func (s *server) askAsync(ctx context.Context, title string, opts []permissionOption, kind string) (<-chan string, <-chan struct{}, func(), error) {
	t := s.currentTurn()
	if t == nil {
		return nil, nil, nil, errors.New("acp: 无进行中的回合(请求无归属会话,无法送达编辑器)")
	}
	sel := make(chan string, 1)
	done := make(chan struct{})
	var once sync.Once
	cancel := func() { once.Do(func() { close(done) }) }
	id := fmt.Sprintf("perm-%d", s.permSeq.Add(1))
	go func() {
		picked, err := s.askPermission(ctx, t, id, title, kind, opts)
		if err != nil {
			// 不静默:送达失败必须让人看见(否则用户只看到"操作被拒绝")
			s.warn("权限请求未能送达编辑器(%s): %v", title, err)
		}
		select {
		case sel <- picked:
		case <-done:
		}
	}()
	return sel, done, cancel, nil
}

// askPermission 发 session/request_permission 并等裁决,返回选中的 optionId(空 = 未选中/取消)。
func (s *server) askPermission(ctx context.Context, t *turn, id, title, kind string, opts []permissionOption) (string, error) {
	if kind == "" {
		kind = "other"
	}
	var res permissionResult
	err := s.call(ctx, "session/request_permission", permissionParams{
		SessionID: t.sess.id,
		ToolCall: permissionToolCall{
			ToolCallID: id,
			Title:      title,
			Kind:       kind,
			Status:     "pending",
		},
		Options: opts,
	}, &res)
	if err != nil {
		return "", err
	}
	if res.Outcome.Outcome != "selected" {
		return "", nil // cancelled:按拒绝/空作答处理(安全默认)
	}
	return res.Outcome.OptionID, nil
}
