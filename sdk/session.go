// 会话事件与 SessionLog 服务(对齐设计 §9:会话日志 = 追加式事件流,不变量“模型可见即已记录”)。
package sdk

import "time"

// 持久会话事件 Kind(对齐 dsh 轮次流程的事件域)。
const (
	EventTurnStart    = "turn/start"
	EventTurnEnd      = "turn/end"
	EventStepStart    = "step/start"
	EventStepEnd      = "step/end"
	EventUserMessage  = "user/message"
	EventAssistantChunk = "assistant/chunk"
	EventAssistantMessage = "assistant/message"
	EventToolCall     = "tool/call"
	EventToolResult   = "tool/result"
	EventAgentStatus  = "agent/status"
	EventAgentError   = "agent/error"
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
}
