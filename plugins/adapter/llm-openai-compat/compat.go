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
	"encoding/base64"
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

// Start 注册适配器到 ctx.llm。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	a := &Adapter{client: &http.Client{Timeout: 5 * time.Minute}}
	base, key, mod, err := resolveConfig(m)
	if err != nil {
		return nil, err
	}
	a.baseURL, a.apiKey, a.model = base, key, mod
	// 生效值快照(Unset/Reset 逐项/全量恢复用)
	a.defaultBaseURL, a.defaultAPIKey, a.defaultModel = a.baseURL, a.apiKey, a.model

	var llm sdk.LLMService
	if err := c.Inject("ctx.llm", &llm); err != nil {
		return nil, err
	}
	d := llm.RegisterAdapter(a)
	llm.SetModel(a.model)
	return d, nil
}

// resolveConfig 适配器配置解析链(每字段独立,纯逻辑可测)。修复:模型/base_url 恢复
// 不再被 apiKey 有无 gate——env 提供 key 或 provider.yaml 仅存 model 时同样生效:
//
//	env 显式 > provider.yaml(/provider set、/model 持久化)> data 样板 > 内置默认。
//
// apiKey env 显式最高(凭据隔离);坏 provider.yaml 显式报错(不静默降级到样板)。
func resolveConfig(m *sdk.Manifest) (baseURL, apiKey, model string, err error) {
	// 1) env 显式(最高)
	base := firstEnv("DEEPSEEK_BASE_URL", "OPENAI_BASE_URL")
	key := firstEnv("DEEPSEEK_API_KEY", "OPENAI_API_KEY")
	mod := firstEnv("DEEPSEEK_MODEL", "OPENAI_MODEL")
	// 2) provider.yaml:持久化运行时配置(/provider set、/model);env 未显式字段用之
	pv, perr := providerfile.Load()
	if perr != nil {
		return "", "", "", fmt.Errorf("llm-openai: 读取 provider.yaml 失败: %w", perr)
	}
	if base == "" && pv.BaseURL != "" {
		base = strings.TrimSuffix(pv.BaseURL, "/")
	}
	if key == "" {
		key = pv.APIKey
	}
	if mod == "" && pv.Model != "" {
		mod = pv.Model // 模型独立恢复:与 apiKey/baseURL 是否持久化无关
	}
	// 3) data 样板
	if m != nil && m.Data != nil {
		if u, ok := m.Data["base_url"].(string); ok && base == "" && u != "" {
			base = strings.TrimSuffix(u, "/")
		}
		if md, ok := m.Data["model"].(string); ok && mod == "" && md != "" {
			mod = md
		}
	}
	// 4) 内置默认
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if mod == "" {
		mod = "deepseek-chat"
	}
	return base, key, mod, nil
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
	if baseURL == "" {
		// 空端点会让 http.NewRequest 报 "missing protocol scheme"(对用户毫无信息量)
		return nil, fmt.Errorf("llm-openai: 还没有配置模型端点:请在设置里 Provider 处粘贴 API Key")
	}

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
// Content 为 string(纯文本,兼容)或 []any(多模态:image_url 视觉注入,附件一期)。
type wireMsg struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Index    int    `json:"index"` // 并行 tool_calls 的槽位下标(此前未解析 → 多调用只能靠 id/lastCallID 猜,参数会串)
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
			Content          string         `json:"content"`
			ReasoningContent string         `json:"reasoning_content"` // 思维增量(deepseek 推理模型)
			ToolCalls        []wireToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"` // 缓存命中(deepseek 等)
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

// Complete 发起流式请求(SSE)。
func (a *Adapter) Complete(ctx context.Context, req *sdk.LLMRequest, onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	model := req.Model
	if model == "" {
		model = a.model
	}
	if base, _ := a.ProviderInfo(); base == "" {
		// 空端点会让 http.NewRequest 报 "missing protocol scheme"(对用户毫无信息量)
		return nil, fmt.Errorf("llm-openai: 还没有配置模型端点:请在设置里 Provider 处粘贴 API Key(或用本地 Ollama)")
	}
	wire := wireReq{Model: model, Stream: true, MaxTokens: req.MaxTokens, Temperature: req.Temperature}
	for _, msg := range req.Messages {
		wm := wireMsg{Role: string(msg.Role)}
		if msg.ToolCallID != "" {
			wm.ToolCallID = msg.ToolCallID
		}
		if msg.Content != "" || len(msg.Attachments) > 0 {
			wm.Content = wireContent(msg)
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
		msg := fmt.Sprintf("llm-openai: HTTP %d", resp.StatusCode)
		if hint := sdk.HTTPStatusHint(resp.StatusCode); hint != "" {
			msg += "(" + hint + ")" // 人话提示 + 原始响应体一并给出
		}
		err := fmt.Errorf("%s: %s", msg, strings.TrimSpace(string(raw)))
		if resp.StatusCode >= 500 {
			return nil, &sdk.RetryableError{Err: err} // 5xx 瞬态:可重试
		}
		return nil, err // 4xx(auth/quota):不可重试(§11)
	}

	var content strings.Builder
	var calls []sdk.ToolCall
	var lastCallID string        // 兼容后续 chunk 不带 id 的流(常见推理模型只带 index/tool 类型):沿用首个非空 id
	toolCallIdx := map[int]int{} // 流内 index → calls 下标(并行 tool_calls 必须按 index 记账,不能只看 id)
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
			if ck.Usage.PromptTokensDetails != nil {
				usage.CachedTokens = ck.Usage.PromptTokensDetails.CachedTokens
			}
		}
		for _, ch := range ck.Choices {
			ev := sdk.LLMStreamEvent{Delta: ch.Delta.Content, Thinking: ch.Delta.ReasoningContent}
			if len(ch.Delta.ToolCalls) > 0 {
				// 可能一次给多个调用(并行 tool_calls):逐个按 **index** 记账。
				// 只用 id + lastCallID 会把第二个调用的参数增量并进第一个(工具收到坏参数)。
				for i, tc := range ch.Delta.ToolCalls {
					pos, seen := toolCallIdx[tc.Index]
					if seen && tc.ID != "" && calls[pos].ID != "" && calls[pos].ID != tc.ID {
						// 端点复用了 index(或压根不给 index)但 id 变了:是新调用 → 另起一条
						pos = findCall(&calls, tc.ID)
						toolCallIdx[tc.Index] = pos
					} else if !seen {
						pos = findCall(&calls, tc.ID)
						toolCallIdx[tc.Index] = pos
					}
					if calls[pos].ID == "" {
						calls[pos].ID = tc.ID
					}
					if tc.ID != "" {
						lastCallID = tc.ID
					}
					calls[pos].Name += tc.Function.Name
					calls[pos].Arguments += tc.Function.Arguments
					if i == 0 { // 流事件是单调用形态:报本条 chunk 首个条目归属的调用
						ev.ToolCallID = calls[pos].ID
						ev.ToolCallName = tc.Function.Name
						ev.ToolCallArgs = tc.Function.Arguments
					}
				}
				if ev.ToolCallID == "" {
					ev.ToolCallID = lastCallID
				}
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

// wireContent 构造消息 content:含可视觉注入的图片附件时输出结构化数组
// (text + image_url[data URI]);否则纯文本 string(既有兼容)。
func wireContent(msg sdk.LLMMessage) any {
	hasVis := false
	for _, att := range msg.Attachments {
		if att.Kind == sdk.AttachmentImage && att.Path != "" {
			hasVis = true
			break
		}
	}
	if !hasVis {
		return msg.Content
	}
	var parts []any
	if msg.Content != "" {
		parts = append(parts, map[string]any{"type": "text", "text": msg.Content})
	}
	for _, att := range msg.Attachments {
		if att.Kind != sdk.AttachmentImage || att.Path == "" {
			continue
		}
		data, err := os.ReadFile(att.Path)
		if err != nil {
			continue
		}
		mime := att.MimeType
		if mime == "" {
			mime = "image/png"
		}
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)},
		})
	}
	if len(parts) == 0 {
		return msg.Content
	}
	return parts
}
