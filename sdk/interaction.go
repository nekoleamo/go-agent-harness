// 交互事件(P3 事件化):审批确认与结构化提问的请求/完成广播。
// 事件语义对齐 dsh Session 事件模型(requested ↔ resolved),供第三方 UI 插件/遥测订阅;
// 呈现管道(confirm-fusion presenter)仍是主路径,事件为只读观察面。
//
// 广播面覆盖(L1 补齐):
//   - host-confirm-fusion 装配(多端并存):Fusion 自身广播(Question.ID 由 Fusion 补齐);
//   - 单 UI profile(无 Fusion):ctx.question provider 经 ObservedQuestion 包装后广播。
//
// 事件 Channel 字段标明“作答/请求来自哪个渠道”(如 web/tui),各端据此跳过自己。
package sdk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// 交互事件名(host-confirm-fusion 在 Confirm/Ask 前后广播;sdk.Emit 广播模式)。
const (
	EventConfirmRequested  = "confirm/requested"
	EventConfirmResolved   = "confirm/resolved"
	EventQuestionRequested = "question/requested"
	EventQuestionResolved  = "question/resolved"
)

// ConfirmEvent 审批事件载荷(requested 时 OK/Resolved 无意义)。
type ConfirmEvent struct {
	Prompt   string `json:"prompt"`
	Channel  string `json:"channel,omitempty"` // 请求/作答渠道(多端并存时判定是否自己)
	OK       bool   `json:"ok,omitempty"`
	Resolved bool   `json:"resolved,omitempty"`
	Err      string `json:"err,omitempty"`
}

// QuestionEvent 提问事件载荷(requested 时 Answer/Resolved 无意义)。
type QuestionEvent struct {
	Question Question       `json:"question"`
	Answer   QuestionAnswer `json:"answer,omitempty"`
	Resolved bool           `json:"resolved,omitempty"`
	Channel  string         `json:"channel,omitempty"` // 请求/作答渠道(多端并存时判定是否自己)
	Err      string         `json:"err,omitempty"`
}

// InteractionObserver 可选能力:交互事件化是否可用(实现方=host-confirm-fusion)。
// 订阅方按需 Emit 观察;不实现时事件不广播(不影响呈现管道)。
type InteractionObserver interface {
	// EmitsEvents 是否广播 confirm/question 事件。
	EmitsEvents() bool
}

// NewQuestionID 生成提问标识(事件与各端弹层共用同一 id:审计/关联/去重)。
func NewQuestionID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil { // 极端不可用时的确定性兜底(仍进程内唯一)
		return fmt.Sprintf("q-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// ObservedQuestion 包装单渠道提问服务:Ask 前后广播 question/requested ↔ question/resolved,
// 使无 host-confirm-fusion 的 profile 也具备事件观察面(审计/多端订阅)。
// channel 为事件里的来源渠道名;c 或 inner 为 nil 时原样返回 inner(嵌入/单测无事件面)。
// 注意:fusion profile 由 Fusion 自身广播,**勿再包装**(否则重复事件)。
func ObservedQuestion(c Ctx, channel string, inner QuestionService) QuestionService {
	if c == nil || inner == nil {
		return inner
	}
	return observedQuestion{c: c, channel: channel, inner: inner}
}

// observedQuestion 事件装饰器(转发 RegisterQuestioner,仅拦截 Ask)。
type observedQuestion struct {
	c       Ctx
	channel string
	inner   QuestionService
}

func (o observedQuestion) Ask(ctx context.Context, q Question) (QuestionAnswer, error) {
	if q.ID == "" {
		q.ID = NewQuestionID()
	}
	o.emit(EventQuestionRequested, &QuestionEvent{Question: q, Channel: o.channel})
	ans, err := o.inner.Ask(ctx, q)
	o.emit(EventQuestionResolved, &QuestionEvent{
		Question: q, Answer: ans, Resolved: true, Channel: o.channel, Err: errorText(err),
	})
	return ans, err
}

func (o observedQuestion) RegisterQuestioner(channel string, p QuestionPresenter) Disposer {
	return o.inner.RegisterQuestioner(channel, p)
}

// emit best-effort 广播(失败/无 Ctx 不影响呈现管道)。
func (o observedQuestion) emit(name string, payload any) {
	if o.c == nil {
		return
	}
	_, _ = o.c.Emit(context.Background(), name, payload, Emit)
}

// errorText 错误转字符串(空错误 = "")。
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// QuestionEventOf 从事件载荷取 QuestionEvent(值/指针兼容;取不到返回 false)。
func QuestionEventOf(payload any) (QuestionEvent, bool) {
	switch v := payload.(type) {
	case QuestionEvent:
		return v, true
	case *QuestionEvent:
		if v == nil {
			return QuestionEvent{}, false
		}
		return *v, true
	}
	return QuestionEvent{}, false
}

// ConfirmEventOf 从事件载荷取 ConfirmEvent(值/指针兼容;取不到返回 false)。
func ConfirmEventOf(payload any) (ConfirmEvent, bool) {
	switch v := payload.(type) {
	case ConfirmEvent:
		return v, true
	case *ConfirmEvent:
		if v == nil {
			return ConfirmEvent{}, false
		}
		return *v, true
	}
	return ConfirmEvent{}, false
}

// AnswerSummary 作答摘要(事件展示/审计用;单选/多选与自由文本合并)。
func AnswerSummary(a QuestionAnswer) string {
	parts := append([]string(nil), a.Values...)
	if t := strings.TrimSpace(a.Text); t != "" {
		parts = append(parts, t)
	}
	if len(parts) == 0 {
		return "(空作答)"
	}
	return strings.Join(parts, " , ")
}
