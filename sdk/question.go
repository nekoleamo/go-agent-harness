// 结构化提问服务(ctx.question;P3 语义交互 seam)。
// 与审批确认(ctx.confirm)同源但独立:confirm 是 bool 裁决,question 是单选/多选/
// 自由文本作答(dsh ask_user_question 语义)。由 host-confirm-fusion 同一实例提供
// (广播各 UI 渠道呈现者,首答生效);模型经 ask_user_question 工具消费。
// 未装配(单 UI profile 无 fusion)时工具侧显式报错(不静默假答)。
package sdk

import "context"

// QuestionOption 一个可选项。
type QuestionOption struct {
	Value string `json:"value"`          // 选中后返回的值(比 Desc 更短、机器可读)
	Desc  string `json:"desc,omitempty"` // 展示说明
}

// Question 一次结构化提问。
type Question struct {
	ID       string           `json:"id,omitempty"`       // 提问标识(呈现/诊断用;空由服务生成)
	Prompt   string           `json:"prompt"`             // 问题正文
	Options  []QuestionOption `json:"options,omitempty"`  // 可选项(空 = 纯自由文本提问)
	Multiple bool             `json:"multiple,omitempty"` // 多选(用户可选多个 Value)
	FreeText bool             `json:"free_text,omitempty"`// 允许自由文本作答(即使有选项)
}

// QuestionAnswer 用户作答(Values 与 Text 至少一项非空)。
type QuestionAnswer struct {
	Values []string `json:"values,omitempty"` // 选中的 Value(单选 ≤1,多选可多个)
	Text   string   `json:"text,omitempty"`   // 自由文本作答
}

// Empty 作答是否为空(认定用户跳过/无法解析时调用方决定语义)。
func (a QuestionAnswer) Empty() bool { return len(a.Values) == 0 && a.Text == "" }

// QuestionPresenter 渠道提问呈现者(与 ConfirmPresenter 对称):把问题推给自己的 UI,
// 返回作答通道;cancel 负责清理该次呈现(超时/放弃/已答均幂等安全)。
type QuestionPresenter interface {
	PresentQuestion(ctx context.Context, q Question) (answer <-chan QuestionAnswer, cancel func(), err error)
}

// QuestionService 结构化提问服务(ctx.question,host-confirm-fusion 提供):
// Ask 供工具/流程消费;Register 供 UI 渠道注册呈现者(随 Disposer 撤销)。
type QuestionService interface {
	// Ask 广播提问给全部已注册渠道,首答生效;无渠道/ctx 取消显式报错。
	Ask(ctx context.Context, q Question) (QuestionAnswer, error)
	// Register 注册渠道呈现者(同名覆盖;返回 Disposer 幂等撤销)。
	RegisterQuestioner(channel string, p QuestionPresenter) Disposer
}
