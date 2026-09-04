// 会话事件与 SessionLog 服务(对齐设计 §9:会话日志 = 追加式事件流,不变量“模型可见即已记录”)。
package sdk

import "time"

// 持久会话事件 Kind(对齐 dsh 轮次流程的事件域)。
const (
	// EventSession 广播:每次会话事件 Append 后发出(UI/遥测实时订阅;对齐 dsh session/event)。
	EventSession          = "session/event"
	EventTurnStart        = "turn/start"
	EventTurnEnd          = "turn/end"
	EventStepStart        = "step/start"
	EventStepEnd          = "step/end"
	EventUserMessage      = "user/message"
	EventAssistantChunk   = "assistant/chunk"
	EventAssistantMessage = "assistant/message"
	EventToolCall         = "tool/call"
	EventToolResult       = "tool/result"
	// EventSummary 滚动摘要事件(M6.5):载荷为累计摘要文本;原始消息事件保留在日志(留盘完整)。
	EventSummary     = "session/summary"
	EventAgentStatus = "agent/status"
	EventAgentError  = "agent/error"
)

// SessionEvent 是追加到会话日志的持久事实。
type SessionEvent struct {
	Kind    string
	Seq     uint64
	Payload any
	TS      time.Time
}

// UserMessage 用户输入(user/message 载荷)。
type UserMessage struct {
	Content string
}

// AssistantMessage 助手完整消息(assistant/message 载荷;chunk 事件只携带增量)。
type AssistantMessage struct {
	Content   string
	ToolCalls []ToolCall
}

// ToolCallEvent 工具调用记录(tool/call 载荷)。
type ToolCallEvent struct {
	ID        string
	Name      string
	Arguments string
}

// ToolResultEvent 工具结果记录(tool/result 载荷)。
type ToolResultEvent struct {
	CallID  string
	Name    string
	Content string // 序列化后的结果/错误
	Error   string
}

// SessionLog 服务(ctx.sessions):追加事件 + 投影模型历史。
// 投影不变量:derive 出的消息必须能从日志重建(即模型可见 = 已记录)。
type SessionLog interface {
	Append(ev SessionEvent) error
	// DeriveMessages 从事件日志投影模型可用的历史消息。
	DeriveMessages() []LLMMessage
	// Replay 全量回放事件(供 fork/导出/UI)。
	Replay() []SessionEvent
	// Flush 落盘(内存会话为 no-op)。
	Flush() error

	// SetPath 设置会话落盘路径(jsonl;host-cwd-sessions 按项目 key 调用)。
	SetPath(path string)

	// SetHistory 设置历史注入条数:-1 = 禁止注入;0 = 全部(unlimited);N>0 = 最近 N 条。
	// 对齐设计 §9:history injection(默认 unlimited)。
	SetHistory(n int)

	// RegisterCompressor 注册滚动摘要压缩器与其字符预算(M6.5 拆分后由 token-compress 注入)。
	// budget <= 0 关闭压缩;压缩器在投影超预算时被调用(详见 SessionCompressor)。
	RegisterCompressor(budget int, c SessionCompressor)
}

// SessionCompressor 滚动摘要引擎(M6.5 拆出 token-compress;仅消费 SessionEvent,零内部状态)。
// host-session-log 在投影超预算时回调 Fold;引擎折叠事件流最旧块为累计摘要,
// 每折一块调用 summary 回调持久化 session/summary 事件;host 据此推进水位(投影跳过已压缩块)。
type SessionCompressor interface {
	// Fold 折叠 evs 中水位后的最旧块(不得越过最后一个用户轮),
	// 直至估算投影回预算内或无可折叠;返回已被摘要覆盖的最大事件索引(水位)。
	// watermark -1 表示尚未压缩;summary 回调幂等可多次调用。
	Fold(evs []SessionEvent, watermark int, budget int, summary func(string)) int
}

// CwdSessions 服务(ctx.cwdSessions):项目级会话(host-cwd-sessions)。
type CwdSessions interface {
	// Current 当前项目会话 key(由 cwd 派生,同项目跨期共享)。
	Current() string
	// Path 当前会话落盘路径。
	Path() string
	// List 列出项目会话 key(按名称;含历史项目)。
	List() []string
}
