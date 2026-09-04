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
// 可选接口 ModelRouter 声明支持的模型前缀(host-llm 按当前模型名路由;
// 未实现则作为默认回退适配器,对齐原 order[0] 语义)。
type LLMAdapter interface {
	Name() string
	// Complete 发起流式请求:onChunk 按增量回调;返回聚合后的完整响应。
	// 实现必须尊重 ctx 取消(取消链见设计 §8)。
	Complete(ctx context.Context, req *LLMRequest, onChunk func(ev LLMStreamEvent) error) (*LLMResponse, error)
}

// ModelRouter 可选接口:声明适配器支持的模型名/前缀(如 "claude")。
// host-llm 路由:当前模型名精确或前缀命中任一适配器声明 → 优先;
// 无命中 → 首个注册适配器(默认回退)。
type ModelRouter interface {
	Models() []string
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

	// SetProvider 运行时切换端点与凭据(作用于通用适配器,零重启)。
	// claude-* 前缀路由的适配器不受影响;无支持适配器时显式报错。
	SetProvider(baseURL, apiKey string) error

	// ProviderInfo 当前通用适配器的端点与凭据(展示用;key 返回原文供打码)。
	ProviderInfo() (baseURL, apiKey string, ok bool)
}

// ProviderAdapter 可选接口:适配器支持运行时端点/凭据配置(TUI /provider)。
type ProviderAdapter interface {
	// Configure 切换端点与凭据(原子生效;baseURL 校验 http(s) 前缀)。
	Configure(baseURL, apiKey string) error
	// ProviderInfo 返回当前端点与凭据。
	ProviderInfo() (baseURL, apiKey string)
}
