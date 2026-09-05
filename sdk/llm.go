// LLM 统一域模型与适配器 seam(对齐设计 §5.2:模型层不感知提供商)。
// 适配器实现本包接口,提供商差异在适配器内映射;宿主仅消费域模型。
package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

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
	Thinking    ThinkingLevel // 思考等级(默认 Off=不发送;host-llm 注入会话级)
}

// ThinkingLevel 思考等级(推理预算)。Off=关闭(默认,不发送推理字段——对不支持端点安全);
// Low/Medium/High 由适配器映射各 API 参数(OpenAI reasoning_effort / Anthropic budget)。
type ThinkingLevel int

const (
	ThinkingOff     ThinkingLevel = iota
	ThinkingLow
	ThinkingMedium
	ThinkingHigh
)

// Names 全部等级(循环切换/枚举显示用,顺序即切换顺序)。
func (ThinkingLevel) Names() []string { return []string{"off", "low", "medium", "high"} }

// Parse 解析等级名(未知 = Off)。
func ParseThinking(s string) ThinkingLevel {
	switch s {
	case "low":
		return ThinkingLow
	case "medium":
		return ThinkingMedium
	case "high":
		return ThinkingHigh
	default:
		return ThinkingOff
	}
}

// String 等级展示名。
func (t ThinkingLevel) String() string {
	return t.Names()[int(t)%len(t.Names())]
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
	CachedTokens     int // 缓存命中输入 token(openai prompt_tokens_details.cached_tokens / anthropic cache_read_input_tokens)
}

// UsageStats 会话级 token 消耗统计(ctx.usageStats,host-usage-stats 累计)。
type UsageStats struct {
	PromptTokens     int // 累计输入 token
	CompletionTokens int // 累计输出 token
	CachedTokens     int // 累计缓存命中输入 token
	Requests         int // 累计请求数
	Window           int // 模型上下文窗口(token;context_window 配置,默认 65536)
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

// LLMError LLM 请求失败包装:携带请求模型名(错误文本可能含上下文窗口信息,
// host-usage-stats 订阅 agent/error 解析学习——新模型窗口自动获取的通道)。
// 保持 Unwrap,重试/取消判定不受影响。
type LLMError struct {
	Model string
	Err   error
}

func (e *LLMError) Error() string { return e.Err.Error() }
func (e *LLMError) Unwrap() error { return e.Err }

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

// ModelInfo 一个可选模型(经 ListModels 从端点 /models 拉取)。
type ModelInfo struct {
	ID      string // 模型名(如 deepseek-ai/DeepSeek-V3)
	OwnedBy string // 模型归属(如 deepseek-ai;可空)
}

// ModelLister 可选接口:适配器支持列举端点可用模型(TUI /model 动态枚举;失败回退手动)。
type ModelLister interface {
	ListModels() ([]ModelInfo, error)
}

// UsageStatsService 服务(ctx.usageStats):会话级 token 消耗统计(host-usage-stats 提供)。
// TUI 状态栏显示上下文使用率/缓存命中率;切换会话时经 Reset 归零。
type UsageStatsService interface {
	// Stats 当前会话累计统计快照。
	Stats() UsageStats
	// Reset 归零统计(切换会话时调用;新会话从零累计)。
	Reset()
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

	// UnsetProvider 逐项删除配置(base_url|api_key|model),该项恢复为启动默认(env/样板)。
	UnsetProvider(field string) error

	// ResetProvider 恢复全部字段为启动默认(env/样板)。
	ResetProvider() error

	// ProviderInfo 当前通用适配器的端点与凭据(展示用;key 返回原文供打码)。
	ProviderInfo() (baseURL, apiKey string, ok bool)

	// ListModels 当前通用适配器端点可用模型列表(TUI /model 动态枚举;失败回退手动)。
	ListModels() ([]ModelInfo, error)

	// SetThinking 设置会话级思考等级(TUI Tab/Shift+Tab 循环;Complete 注入未显式设置的请求)。
	SetThinking(t ThinkingLevel)
	// Thinking 当前会话级思考等级。
	Thinking() ThinkingLevel
}

// ProviderAdapter 可选接口:适配器支持运行时端点/凭据配置(TUI /provider)。
type ProviderAdapter interface {
	// Configure 切换端点与凭据(原子生效;baseURL 校验 http(s) 前缀)。
	Configure(baseURL, apiKey string) error
	// Unset 删除某一字段(base_url|api_key|model),恢复为启动默认(env/样板)。
	Unset(field string) error
	// Reset 恢复全部字段为启动默认(env/样板)。
	Reset() error
	// ProviderInfo 返回当前端点与凭据。
	ProviderInfo() (baseURL, apiKey string)
}

// ProviderProfile 一个 provider 的运行时视图(展示/切换用;host-llm 提供)。
type ProviderProfile struct {
	Name    string // 标识/切换名(域短名,持久化 provider.yaml 的 name)
	BaseURL string
	APIKey  string
	Model   string
	Active  bool // 当前活跃(adapter 端点与模型均指向它)
}

// ProviderModelList 一个 provider 端点的模型列表(/model 聚合各 provider 的结果)。
type ProviderModelList struct {
	Name    string
	BaseURL string
	Models  []ModelInfo
	Err     error // 单条拉取失败记入该条(不整体失败);空 = 成功
}

// MultiProviderService 多 provider 并存扩展(host-llm 实现;类型断言发现,LLMService 接口不变)。
type MultiProviderService interface {
	// Providers 全部 provider 运行时视图(含活跃标记)。
	Providers() []ProviderProfile
	// AddProvider 新增/更新一个 provider(同名 upsert;首个自动激活;同名更新时激活并立即生效)。
	AddProvider(name, baseURL, apiKey, model string) error
	// SetActiveProvider 切换活跃 provider(校验存在;立即 Configure 适配器并 SetModel)。
	SetActiveProvider(name string) error
	// ListAllModels 聚合所有 provider 端点 /models 列表(TTL 缓存;单条失败记 Err 不整体失败)。
	ListAllModels() []ProviderModelList
}

// modelsFetchClient 模型列表直拉客户端(多 provider 聚合/非缓存路径共用;30s 超时)。
var modelsFetchClient = &http.Client{Timeout: 30 * time.Second}

// OpenAIFetchModels 直拉 openai 兼容端点 /models(不经过适配器/缓存——供 host-llm 聚合
// 非活跃端点;与适配器 ListModels 的端点语义同构)。baseURL 空 → 显式错误。
func OpenAIFetchModels(baseURL, apiKey string) ([]ModelInfo, error) {
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("openai-models: 未配置端点")
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := modelsFetchClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai-models: 模型列表请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("openai-models: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var mr struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, fmt.Errorf("openai-models: 解析失败: %w", err)
	}
	out := make([]ModelInfo, 0, len(mr.Data))
	for _, d := range mr.Data {
		if d.ID != "" {
			out = append(out, ModelInfo{ID: d.ID, OwnedBy: d.OwnedBy})
		}
	}
	return out, nil
}
