// Package llmanthropic 提供 llm-anthropic-compat 插件:Anthropic Messages API
// 适配器(/v1/messages + SSE,2023-06-01 协议)。零 SDK 依赖,纯 net/http。
// 模型路由:声明 Models() ["claude"],host-llm 按当前模型名前缀路由到本适配器。
// 配置(data):base_url(默认 https://api.anthropic.com/v1)、model(默认读 env
// ANTHROPIC_MODEL,再默认 claude-sonnet-4-5)、max_tokens(默认 4096);
// api key 读 env ANTHROPIC_API_KEY(凭据隔离红线:仅本适配器可读)。
package llmanthropic

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

	"github.com/nekoleamo/go-agent-harness/internal/providerfile"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 llm-anthropic-compat。
type Plugin struct{}

func (p *Plugin) Name() string { return "llm-anthropic-compat" }

// Start 注册适配器到 ctx.llm(模型前缀 claude 路由)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	a := &Adapter{client: &http.Client{Timeout: 5 * time.Minute}, maxTokens: 4096}
	explicit, err := resolveConfig(a, m)
	if err != nil {
		return nil, err
	}

	var llm sdk.LLMService
	if err := c.Inject("ctx.llm", &llm); err != nil {
		return nil, err
	}
	a.defaultBaseURL, a.defaultAPIKey, a.defaultModel = a.baseURL, a.apiKey, a.model
	d := llm.RegisterAdapter(a)
	if explicit {
		// 配置显式指向本适配器(env / 活跃 provider 的模型是 claude-* / 插件 data):
		// 全局模型也要设上,否则 anthropic-only 装配下"没有模型"直接跑不起来。
		llm.SetModel(a.model)
	}
	// 未显式指向时保持默认模型由 openai 插件设置;用户 /model claude-* 时前缀路由到本适配器。
	return d, nil
}

// firstEnv 取第一个非空环境变量。
func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// resolveConfig 配置解析链(每字段独立):env 显式 > provider.yaml(仅当活跃 provider 属于
// 本适配器路由范围)> data 样板 > 内置默认。返回 explicit = 有明确的"归属本适配器"来源。
//
// 2026-09-21 补:此前本适配器完全不看 provider.yaml(只吃 data/env)→ /provider 配好的
// anthropic 兼容端点对 claude 模型无效(静默打回默认端点)。
func resolveConfig(a *Adapter, m *sdk.Manifest) (explicit bool, err error) {
	base := firstEnv("ANTHROPIC_BASE_URL")
	key := firstEnv("ANTHROPIC_API_KEY")
	mod := firstEnv("ANTHROPIC_MODEL")
	if base != "" || key != "" || mod != "" {
		explicit = true
	}
	pv, perr := providerfile.Load()
	if perr != nil {
		return false, fmt.Errorf("llm-anthropic: 读取 provider.yaml 失败: %w", perr)
	}
	// 只在活跃 provider 属于本适配器路由范围(model 空 = 未定,或 claude-*)时采用;
	// 否则会把 openai 端点错配到本适配器上(路由到谁由 host-llm 按模型前缀决定)。
	if pv.Model == "" || strings.HasPrefix(pv.Model, "claude") {
		if pv.BaseURL != "" || pv.APIKey != "" || pv.Model != "" {
			if base == "" {
				base = strings.TrimSuffix(pv.BaseURL, "/")
			}
			if key == "" {
				key = pv.APIKey
			}
			if mod == "" {
				mod = pv.Model
			}
			if pv.Model != "" {
				explicit = true
			}
		}
	}
	if m != nil && m.Data != nil {
		if u, ok := m.Data["base_url"].(string); ok && base == "" && u != "" {
			base = strings.TrimSuffix(u, "/")
		}
		if md, ok := m.Data["model"].(string); ok && mod == "" && md != "" {
			mod = md
			explicit = true
		}
		if mt, ok := m.Data["max_tokens"].(int); ok && mt > 0 {
			a.maxTokens = mt
		}
	}
	if base == "" {
		base = "https://api.anthropic.com/v1"
	}
	if mod == "" {
		mod = "claude-sonnet-4-5"
	}
	a.baseURL, a.apiKey, a.model = base, key, mod
	return explicit, nil
}

// Adapter 实现 sdk.LLMAdapter + sdk.ModelRouter + sdk.ProviderAdapter(/provider 运行时切换)。
type Adapter struct {
	client    *http.Client
	mu        sync.RWMutex // 保护 baseURL/apiKey/model(Configure 写 / Complete 读)
	baseURL   string
	model     string
	apiKey    string
	maxTokens int
	// 启动默认快照(Unset/Reset 恢复用:env/样板 的生效值)
	defaultBaseURL, defaultAPIKey, defaultModel string
}

func (a *Adapter) Name() string { return "llm-anthropic-compat" }

// Models 声明支持的模型前缀(路由:模型名以 claude 开头 → 本适配器)。
func (a *Adapter) Models() []string { return []string{"claude"} }

// Configure 运行时切换端点与凭据(校验 http(s) 前缀;原子生效,零重启)。
// 2026-09-21 补齐:此前本适配器未实现 ProviderAdapter → 只有 openai 适配器吃 /provider,
// claude 模型仍打静态端点(base_url 配到了错的适配器上)。
func (a *Adapter) Configure(baseURL, apiKey string) error {
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return fmt.Errorf("provider: base_url 须为 http(s):// 前缀: %q", baseURL)
	}
	a.mu.Lock()
	a.baseURL = strings.TrimSuffix(baseURL, "/")
	a.apiKey = apiKey
	a.mu.Unlock()
	return nil
}

// ProviderInfo 当前端点与凭据(展示用)。
func (a *Adapter) ProviderInfo() (string, string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.baseURL, a.apiKey
}

// Unset 删除某一字段配置,该项恢复启动默认;其余保持。
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

// Reset 恢复全部字段为启动默认。
func (a *Adapter) Reset() error {
	a.mu.Lock()
	a.baseURL, a.apiKey, a.model = a.defaultBaseURL, a.defaultAPIKey, a.defaultModel
	a.mu.Unlock()
	return nil
}

// snapshot 读一次一致的端点/凭据/模型(Complete 期间 Configure 可能并发改)。
func (a *Adapter) snapshot() (string, string, string, int) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.baseURL, a.apiKey, a.model, a.maxTokens
}

// —— wire 结构(Messages API)——

type wireText struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type wireToolUse struct {
	Type  string         `json:"type"`
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

type wireToolResult struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

// wireImage 图片视觉块(附件一期):base64 source。
type wireImage struct {
	Type   string          `json:"type"`
	Source wireImageSource `json:"source"`
}

type wireImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type wireMsg struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type wireTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type wireReq struct {
	Model       string        `json:"model"`
	MaxTokens   int           `json:"max_tokens"`
	System      string        `json:"system,omitempty"`
	Messages    []wireMsg     `json:"messages"`
	Tools       []wireTool    `json:"tools,omitempty"`
	Stream      bool          `json:"stream"`
	Temperature *float64      `json:"temperature,omitempty"`
	Thinking    *wireThinking `json:"thinking,omitempty"` // 思考等级(off 不发送)
}

// wireThinking Anthropic 扩展思考参数(type=enabled + budget_tokens)。
type wireThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// wireEvent SSE 事件(共用字段:type、index、delta、content_block、message、usage)。
type wireEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock *struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		Name  string `json:"name"`
		Input any    `json:"input"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"` // 扩展思考块增量(thinking_delta;此前未解析 → Anthropic 思维块整段丢弃)
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *struct {
		InputTokens          int `json:"input_tokens"`
		OutputTokens         int `json:"output_tokens"`
		CacheReadInputTokens int `json:"cache_read_input_tokens"` // 缓存命中输入 token
	} `json:"usage"`
}

// Complete 发起流式请求(Messages API + SSE)。
func (a *Adapter) Complete(ctx context.Context, req *sdk.LLMRequest, onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	baseURL, apiKey, defModel, maxTokens := a.snapshot()
	model := req.Model
	if model == "" {
		model = defModel
	}
	wire := wireReq{Model: model, MaxTokens: maxTokens, Stream: true, Temperature: req.Temperature}
	// 思考等级映射(low/medium/high → thinking.budget_tokens;off 不发送,兼容不支持端点)
	switch req.Thinking {
	case sdk.ThinkingLow:
		wire.Thinking = &wireThinking{Type: "enabled", BudgetTokens: 1024}
	case sdk.ThinkingMedium:
		wire.Thinking = &wireThinking{Type: "enabled", BudgetTokens: 4096}
	case sdk.ThinkingHigh:
		wire.Thinking = &wireThinking{Type: "enabled", BudgetTokens: 16384}
	}
	var system strings.Builder
	for _, msg := range req.Messages {
		switch msg.Role {
		case sdk.RoleSystem:
			system.WriteString(msg.Content)
			system.WriteString("\n")
			continue
		case sdk.RoleTool:
			// tool_result:Anthropic 要求放 user 消息的 content block
			blocks := []any{wireToolResult{Type: "tool_result", ToolUseID: msg.ToolCallID, Content: msg.Content}}
			wire.Messages = append(wire.Messages, wireMsg{Role: "user", Content: blocks})
			continue
		}
		var blocks []any
		if msg.Content != "" {
			blocks = append(blocks, wireText{Type: "text", Text: msg.Content})
		}
		// 图片视觉注入(附件一期;仅 Path 非空=当前回合上传,历史重放跳过)
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
			blocks = append(blocks, wireImage{Type: "image", Source: wireImageSource{
				Type: "base64", MediaType: mime, Data: base64.StdEncoding.EncodeToString(data),
			}})
		}
		for _, tc := range msg.ToolCalls {
			var input map[string]any
			_ = json.Unmarshal([]byte(tc.Arguments), &input)
			blocks = append(blocks, wireToolUse{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input})
		}
		wire.Messages = append(wire.Messages, wireMsg{Role: string(msg.Role), Content: blocks})
	}
	if s := system.String(); s != "" {
		wire.System = strings.TrimSpace(s)
	}
	for _, t := range req.Tools {
		wire.Tools = append(wire.Tools, wireTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}

	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")
	hreq.Header.Set("anthropic-version", "2023-06-01")
	if apiKey != "" {
		hreq.Header.Set("x-api-key", apiKey)
	}

	resp, err := a.client.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &sdk.RetryableError{Err: fmt.Errorf("llm-anthropic: request: %w", err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := fmt.Sprintf("llm-anthropic: HTTP %d", resp.StatusCode)
		if hint := sdk.HTTPStatusHint(resp.StatusCode); hint != "" {
			msg += "(" + hint + ")"
		}
		err := fmt.Errorf("%s: %s", msg, strings.TrimSpace(string(raw)))
		if resp.StatusCode >= 500 {
			return nil, &sdk.RetryableError{Err: err}
		}
		return nil, err
	}

	var content strings.Builder
	calls := []sdk.ToolCall{}
	toolIdx := map[int]int{} // SSE block index → calls 切片下标
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
		if data == "" {
			continue
		}
		var ev wireEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "content_block_start":
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				calls = append(calls, sdk.ToolCall{ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name})
				toolIdx[ev.Index] = len(calls) - 1
			}
		case "content_block_delta":
			switch {
			case ev.Delta != nil && ev.Delta.Type == "text_delta" && ev.Delta.Text != "":
				content.WriteString(ev.Delta.Text)
				if onChunk != nil {
					if err := onChunk(sdk.LLMStreamEvent{Delta: ev.Delta.Text}); err != nil {
						return nil, err
					}
				}
			case ev.Delta != nil && ev.Delta.Type == "thinking_delta" && ev.Delta.Thinking != "":
				// 扩展思考块(Anthropic thinking):与正文互斥下发,计入思维段(不进正文聚合)
				if onChunk != nil {
					if err := onChunk(sdk.LLMStreamEvent{Thinking: ev.Delta.Thinking}); err != nil {
						return nil, err
					}
				}
			case ev.Delta != nil && ev.Delta.Type == "input_json_delta" && ev.Delta.PartialJSON != "":
				if idx, ok := toolIdx[ev.Index]; ok {
					calls[idx].Arguments += ev.Delta.PartialJSON
				}
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason == "tool_use" {
				finish = sdk.FinishReasonToolCalls
			} else if ev.Delta != nil && ev.Delta.StopReason == "max_tokens" {
				finish = sdk.FinishReasonLength
			}
			if ev.Usage != nil {
				usage = sdk.Usage{PromptTokens: ev.Usage.InputTokens, CompletionTokens: ev.Usage.OutputTokens,
					CachedTokens: ev.Usage.CacheReadInputTokens}
			}
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &sdk.RetryableError{Err: fmt.Errorf("llm-anthropic: stream: %w", err)}
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
