// LLM 统一域模型与适配器 seam(对齐设计 §5.2:模型层不感知提供商)。
// 适配器实现本包接口,提供商差异在适配器内映射;宿主仅消费域模型。
package sdk

import "context"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall 模型请求调用的工具。Arguments 是 JSON 字符串。
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// LLMMessage 模型对话消息。
type LLMMessage struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string // RoleTool 时关联的工具调用 id
}

// LLMRequest 一次模型请求。Tools 为模型可见的工具 schema 列表。
type LLMRequest struct {
	Model       string
	Messages    []LLMMessage
	Tools       []ToolDefinition
	MaxTokens   *int
	Temperature *float64
}

// FinishReason 结束原因。
type FinishReason string

const (
	FinishReasonStop          FinishReason = "stop"
	FinishReasonToolCalls     FinishReason = "tool_calls"
	FinishReasonLength        FinishReason = "length"
	FinishReasonContentFilter FinishReason = "content_filter"
)

type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// LLMStreamEvent 流式增量(文本增量 + 工具调用增量;Done 时携带最终消息)。
type LLMStreamEvent struct {
	Delta        string // 文本增量
	ToolCallID   string // 工具调用开始/延续时提供
	ToolCallName string // 工具名 delta
	ToolCallArgs string // 参数增量
	Done         bool
	Message      LLMMessage // Done=true 时的完整消息
	FinishReason FinishReason
	Usage        Usage
}

// LLMResponse 完整响应(适配器聚合流式增量后返回)。
type LLMResponse struct {
	Message      LLMMessage
	FinishReason FinishReason
	Usage        Usage
}

// LLMAdapter 模型提供商适配器。Name 为稳定标识(llm-openai-compat 等)。
type LLMAdapter interface {
	Name() string
	// Complete 发起流式请求:onChunk 按增量回调;返回聚合后的完整响应。
	// 实现必须尊重 ctx 取消(取消链见设计 §8)。
	Complete(ctx context.Context, req *LLMRequest, onChunk func(ev LLMStreamEvent) error) (*LLMResponse, error)
}

// LLMService 服务(ctx.llm):注册适配器 + 以当前默认适配器请求。
type LLMService interface {
	// RegisterAdapter 注册适配器,返回 Disposer(卸载即撤销)。
	RegisterAdapter(a LLMAdapter) Disposer
	// SetModel 设置当前模型名(如 deepseek-chat)。
	SetModel(model string)

	// Complete 以当前默认适配器发起请求。
	// 未设置模型时返回显式错误(不静默降级)。
	Complete(ctx context.Context, req *LLMRequest, onChunk func(ev LLMStreamEvent) error) (*LLMResponse, error)

	// Model 返回当前模型名。
	Model() string

	// List 列出已注册适配器(调试/UI)。
	List() []string
}
