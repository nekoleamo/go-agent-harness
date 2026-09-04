// Package llmopenai 提供 llm-openai-compat 插件:OpenAI 兼容协议适配器(/v1/chat/completions + SSE)。
// 零 SDK 依赖,纯 net/http 实现;通吃 DeepSeek/OpenAI/Ollama/vLLM/Kimi/llama.cpp 等兼容端点。
// 配置(data):base_url(默认读 env DEEPSEEK_BASE_URL/OPENAI_BASE_URL,再默认 https://api.openai.com/v1)
//
//	model(默认读 env DEEPSEEK_MODEL/OPENAI_MODEL);api key 读 env *_API_KEY(凭据隔离红线:仅本适配器可读)。
package llmopenai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/providerfile"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 llm-openai-compat。
type Plugin struct{}

func (p *Plugin) Name() string { return "llm-openai-compat" }

// Start 注册适配器到 ctx.llm。配置优先级:provider.yaml(经 /provider set 持久化)
// > data 样板 > env;apiKey 另有 env 优先(用户 shell 显式设置最高)。
// 生效值同时存为默认快照(Unset/Reset 逐项/全量恢复用)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	a := &Adapter{client: &http.Client{Timeout: 5 * time.Minute}}
	if m != nil && m.Data != nil {
		if u, ok := m.Data["base_url"].(string); ok && u != "" {
			a.baseURL = strings.TrimSuffix(u, "/")
		}
		if mod, ok := m.Data["model"].(string); ok && mod != "" {
			a.model = mod
		}
	}
	if a.baseURL == "" {
		a.baseURL = firstEnv("DEEPSEEK_BASE_URL", "OPENAI_BASE_URL")
	}
	if a.baseURL == "" {
		a.baseURL = "https://api.openai.com/v1"
	}
	if a.model == "" {
		a.model = firstEnv("DEEPSEEK_MODEL", "OPENAI_MODEL")
	}
	if a.model == "" {
		a.model = "deepseek-chat"
	}
	// apiKey:env 显式优先;否则 provider.yaml(/provider set 持久化)
	a.apiKey = firstEnv("DEEPSEEK_API_KEY", "OPENAI_API_KEY")
	if a.apiKey == "" {
		if pv, err := providerfile.Load(); err == nil && pv.APIKey != "" {
			a.apiKey = pv.APIKey
			if pv.BaseURL != "" {
				a.baseURL = strings.TrimSuffix(pv.BaseURL, "/")
			}
			if pv.Model != "" {
				a.model = pv.Model
			}
		}
	}
	a.defaultBaseURL, a.defaultAPIKey, a.defaultModel = a.baseURL, a.apiKey, a.model

	var llm sdk.LLMService
	if err := c.Inject("ctx.llm", &llm); err != nil {
		return nil, err
	}
	d := llm.RegisterAdapter(a)
	llm.SetModel(a.model)
	return d, nil
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// Adapter 实现 sdk.LLMAdapter + sdk.ProviderAdapter(TUI /provider 运行时切换)。
type Adapter struct {
	client  *http.Client
	mu      sync.RWMutex // 保护 baseURL/apiKey(Configure 写/Complete 读)
	baseURL string
	model   string
	apiKey  string
	// 启动默认快照(Unset/Reset 恢复用;env/样板/provider.yaml 顺序的生效值)
	defaultBaseURL, defaultAPIKey, defaultModel string
	modelsCache                                 []sdk.ModelInfo // ListModels TTL 缓存
	modelsCachedAt                              time.Time       // 缓存写入时间
}

func (a *Adapter) Name() string { return "llm-openai-compat" }

// Configure 运行时切换端点与凭据(校验 http(s) 前缀;原子生效,零重启;模型缓存失效)。
func (a *Adapter) Configure(baseURL, apiKey string) error {
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return fmt.Errorf("provider: base_url 须为 http(s):// 前缀: %q", baseURL)
	}
	a.mu.Lock()
	a.baseURL = strings.TrimSuffix(baseURL, "/")
	a.apiKey = apiKey
	a.modelsCache = nil // 端点已变:缓存失效
	a.mu.Unlock()
	return nil
}

// ProviderInfo 当前端点与凭据(展示用)。
func (a *Adapter) ProviderInfo() (string, string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.baseURL, a.apiKey
}

// Unset 删除某一字段配置,该项恢复启动默认(env/样板);其余保持。
func (a *Adapter) Unset(field string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch field {
	case "base_url":
		a.baseURL = a.defaultBaseURL
	case "api_key":
		a.apiKey = a.defaultAPIKey
	case "model":
		a.model = a.defaultModel
	default:
		return fmt.Errorf("provider: 未知字段 %q(可选 base_url|api_key|model)", field)
	}
	return nil
}

// Reset 恢复全部字段为启动默认(env/样板)。
func (a *Adapter) Reset() error {
	a.mu.Lock()
	a.baseURL, a.apiKey, a.model = a.defaultBaseURL, a.defaultAPIKey, a.defaultModel
	a.mu.Unlock()
	return nil
}

// modelCacheTTL 模型列表缓存时长(防每次 /model 回车打端点)。
const modelCacheTTL = 10 * time.Minute

// ListModels 拉取当前端点 /models 可用模型(TTL 缓存 + 锁;切换端点后失效)。
func (a *Adapter) ListModels() ([]sdk.ModelInfo, error) {
	a.mu.RLock()
	if a.modelsCache != nil && time.Since(a.modelsCachedAt) < modelCacheTTL {
		infos := a.modelsCache
		a.mu.RUnlock()
		return infos, nil
	}
	baseURL := a.baseURL
	key := a.apiKey
	a.mu.RUnlock()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm-openai: models 列表请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("llm-openai: models HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var mr modelsResp
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, fmt.Errorf("llm-openai: models 解析失败: %w", err)
	}
	infos := make([]sdk.ModelInfo, 0, len(mr.Data))
	for _, d := range mr.Data {
		if d.ID != "" {
			infos = append(infos, sdk.ModelInfo{ID: d.ID, OwnedBy: d.OwnedBy})
		}
	}
	a.mu.Lock()
	a.modelsCache = infos
	a.modelsCachedAt = time.Now()
	a.mu.Unlock()
	return infos, nil
}

type modelsResp struct {
	Object string `json:"object"`
	Data   []struct {
		ID      string `json:"id"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

// endpoint 当前聊天端点(锁保护读取)。
func (a *Adapter) endpoint() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.baseURL + "/chat/completions"
}

// credentials 当前凭据。
func (a *Adapter) credentials() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.apiKey
}

// wire 服务端 API 消息结构。
type wireMsg struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireTool struct {
	Type     string      `json:"type"`
	Function wireToolDef `json:"function"`
}

type wireToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type wireReq struct {
	Model           string     `json:"model"`
	Messages        []wireMsg  `json:"messages"`
	Tools           []wireTool `json:"tools,omitempty"`
	Stream          bool       `json:"stream"`
	MaxTokens       *int       `json:"max_tokens,omitempty"`
	Temperature     *float64   `json:"temperature,omitempty"`
	ReasoningEffort string     `json:"reasoning_effort,omitempty"` // 思考等级(off 不发送)
}

type wireChunk struct {
	Choices []struct {
		Delta struct {
			Content   string         `json:"content"`
			ToolCalls []wireToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Complete 发起流式请求(SSE)。
func (a *Adapter) Complete(ctx context.Context, req *sdk.LLMRequest, onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	model := req.Model
	if model == "" {
		model = a.model
	}
	wire := wireReq{Model: model, Stream: true, MaxTokens: req.MaxTokens, Temperature: req.Temperature}
	for _, msg := range req.Messages {
		wm := wireMsg{Role: string(msg.Role)}
		if msg.ToolCallID != "" {
			wm.ToolCallID = msg.ToolCallID
		}
		if msg.Content != "" {
			wm.Content = strPtr(msg.Content)
		}
		for _, tc := range msg.ToolCalls {
			wtc := wireToolCall{ID: tc.ID, Type: "function"}
			wtc.Function.Name = tc.Name
			wtc.Function.Arguments = tc.Arguments
			wm.ToolCalls = append(wm.ToolCalls, wtc)
		}
		wire.Messages = append(wire.Messages, wm)
	}
	for _, t := range req.Tools {
		wire.Tools = append(wire.Tools, wireTool{Type: "function", Function: wireToolDef{
			Name: t.Name, Description: t.Description, Parameters: t.InputSchema}})
	}

	// 思考等级映射(low/medium/high → reasoning_effort;off 不发送(omitempty),兼容不支持端点)
	switch req.Thinking {
	case sdk.ThinkingLow:
		wire.ReasoningEffort = "low"
	case sdk.ThinkingMedium:
		wire.ReasoningEffort = "medium"
	case sdk.ThinkingHigh:
		wire.ReasoningEffort = "high"
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")
	if key := a.credentials(); key != "" {
		hreq.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := a.client.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err() // 取消:不包装,不重试
		}
		return nil, &sdk.RetryableError{Err: fmt.Errorf("llm-openai: request: %w", err)} // 网络故障可重试
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		err := fmt.Errorf("llm-openai: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		if resp.StatusCode >= 500 {
			return nil, &sdk.RetryableError{Err: err} // 5xx 瞬态:可重试
		}
		return nil, err // 4xx(auth/quota):不可重试(§11)
	}

	var content strings.Builder
	var calls []sdk.ToolCall
	finish := sdk.FinishReasonStop
	usage := sdk.Usage{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var ck wireChunk
		if err := json.Unmarshal([]byte(data), &ck); err != nil {
			continue // 忽略坏行(脆弱兼容)
		}
		if ck.Usage != nil {
			usage = sdk.Usage{PromptTokens: ck.Usage.PromptTokens, CompletionTokens: ck.Usage.CompletionTokens}
		}
		for _, ch := range ck.Choices {
			ev := sdk.LLMStreamEvent{Delta: ch.Delta.Content}
			if len(ch.Delta.ToolCalls) > 0 {
				tc := ch.Delta.ToolCalls[0]
				ev.ToolCallID = tc.ID
				ev.ToolCallName = tc.Function.Name
				ev.ToolCallArgs = tc.Function.Arguments
				idx := findCall(&calls, tc.ID)
				calls[idx].Name += tc.Function.Name
				calls[idx].Arguments += tc.Function.Arguments
			}
			if ch.FinishReason != nil {
				switch *ch.FinishReason {
				case "tool_calls":
					finish = sdk.FinishReasonToolCalls
				case "length":
					finish = sdk.FinishReasonLength
				default:
					finish = sdk.FinishReasonStop
				}
			}
			if ev.Delta != "" {
				content.WriteString(ev.Delta)
			}
			if onChunk != nil {
				if err := onChunk(ev); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &sdk.RetryableError{Err: fmt.Errorf("llm-openai: stream: %w", err)} // 断流可重试
	}
	done := sdk.LLMStreamEvent{Done: true, FinishReason: finish, Usage: usage}
	done.Message = sdk.LLMMessage{Role: sdk.RoleAssistant, Content: content.String(), ToolCalls: calls}
	if onChunk != nil {
		if err := onChunk(done); err != nil {
			return nil, err
		}
	}
	return &sdk.LLMResponse{Message: done.Message, FinishReason: finish, Usage: usage}, nil
}

func findCall(calls *[]sdk.ToolCall, id string) int {
	for i := range *calls {
		if (*calls)[i].ID == id {
			return i
		}
	}
	*calls = append(*calls, sdk.ToolCall{ID: id})
	return len(*calls) - 1
}

func strPtr(s string) *string { return &s }
