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
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 llm-anthropic-compat。
type Plugin struct{}

func (p *Plugin) Name() string { return "llm-anthropic-compat" }

// Start 注册适配器到 ctx.llm(模型前缀 claude 路由)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	a := &Adapter{client: &http.Client{Timeout: 5 * time.Minute}, maxTokens: 4096}
	if m != nil && m.Data != nil {
		if u, ok := m.Data["base_url"].(string); ok && u != "" {
			a.baseURL = strings.TrimSuffix(u, "/")
		}
		if mod, ok := m.Data["model"].(string); ok && mod != "" {
			a.model = mod
		}
		if mt, ok := m.Data["max_tokens"].(int); ok && mt > 0 {
			a.maxTokens = mt
		}
	}
	if a.baseURL == "" {
		a.baseURL = "https://api.anthropic.com/v1"
	}
	if a.model == "" {
		if v := os.Getenv("ANTHROPIC_MODEL"); v != "" {
			a.model = v
		} else {
			a.model = "claude-sonnet-4-5"
		}
	}
	a.apiKey = os.Getenv("ANTHROPIC_API_KEY")

	var llm sdk.LLMService
	if err := c.Inject("ctx.llm", &llm); err != nil {
		return nil, err
	}
	d := llm.RegisterAdapter(a)
	// 默认模型仍是 openai 插件设置;仅当用户 /model claude-* 时路由到本适配器
	return d, nil
}

// Adapter 实现 sdk.LLMAdapter + sdk.ModelRouter。
type Adapter struct {
	client    *http.Client
	baseURL   string
	model     string
	apiKey    string
	maxTokens int
}

func (a *Adapter) Name() string { return "llm-anthropic-compat" }

// Models 声明支持的模型前缀(路由:模型名以 claude 开头 → 本适配器)。
func (a *Adapter) Models() []string { return []string{"claude"} }

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
	Type   string            `json:"type"`
	Source wireImageSource   `json:"source"`
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
	model := req.Model
	if model == "" {
		model = a.model
	}
	wire := wireReq{Model: model, MaxTokens: a.maxTokens, Stream: true, Temperature: req.Temperature}
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
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")
	hreq.Header.Set("anthropic-version", "2023-06-01")
	if a.apiKey != "" {
		hreq.Header.Set("x-api-key", a.apiKey)
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
		err := fmt.Errorf("llm-anthropic: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
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
