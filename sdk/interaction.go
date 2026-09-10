// 交互事件(P3 事件化):审批确认与结构化提问的请求/完成广播。
// 事件语义对齐 dsh Session 事件模型(requested ↔ resolved),供第三方 UI 插件/遥测订阅;
// 呈现管道(confirm-fusion presenter)仍是主路径,事件为只读观察面。
package sdk

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
	OK       bool   `json:"ok,omitempty"`
	Resolved bool   `json:"resolved,omitempty"`
	Err      string `json:"err,omitempty"`
}

// QuestionEvent 提问事件载荷(requested 时 Answer/Resolved 无意义)。
type QuestionEvent struct {
	Question Question       `json:"question"`
	Answer   QuestionAnswer `json:"answer,omitempty"`
	Resolved bool           `json:"resolved,omitempty"`
	Err      string         `json:"err,omitempty"`
}

// InteractionObserver 可选能力:交互事件化是否可用(实现方=host-confirm-fusion)。
// 订阅方按需 Emit 观察;不实现时事件不广播(不影响呈现管道)。
type InteractionObserver interface {
	// EmitsEvents 是否广播 confirm/question 事件。
	EmitsEvents() bool
}
