// Package toolweb 提供 tool-web 插件(M6.4):纯 Go HTTP fetch 工具(零外部依赖)。
// 默认超时 30s,响应体上限 1MB;返回文本/JSON 内容与状态码。
// 读类工具:read-only 沙箱放行;workspace-write/full 放行(网络读取不改写本地)。
package toolweb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 常量:超时与响应上限。
const (
	fetchTimeout = 30 * time.Second
	maxBody      = 1 << 20 // 1MB
)

// NewTool 外部化工厂(P1):外部进程入口的工具实例。
func NewTool() sdk.Tool {
	return &WebTool{client: &http.Client{Timeout: fetchTimeout}}
}

// Plugin 实现 tool-web。requires ctx.tools。
type Plugin struct{}

func (p *Plugin) Name() string { return "tool-web" }

// Start 注册 web_fetch 工具。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	return tools.Register(&WebTool{client: &http.Client{Timeout: fetchTimeout}}), nil
}

// WebTool 实现 sdk.Tool。
type WebTool struct {
	client *http.Client
}

func (t *WebTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "web_fetch",
		Description: "抓取 URL 内容(纯 Go HTTP,无外部依赖):{url};返回状态码与文本/JSON 内容。",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"url"},
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "http(s) URL"},
			},
		},
	}
}

func (t *WebTool) Execute(ctx context.Context, raw string) (any, error) {
	var a struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("web_fetch: args: %w", err)
	}
	if a.URL == "" {
		return map[string]any{"error": "web_fetch: 缺少 url"}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("web_fetch: 请求构造失败: %v", err)}, nil
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("web_fetch: %v", err)}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("web_fetch: 读响应: %v", err)}, nil
	}
	out := map[string]any{
		"status": resp.StatusCode,
		"url":    req.URL.String(),
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		out["content_type"] = ct
	}
	// JSON 响应结构化返回,其余按文本
	var v any
	if err := json.Unmarshal(body, &v); err == nil {
		out["json"] = v
	} else {
		out["content"] = string(body)
	}
	if len(body) >= maxBody {
		out["truncated"] = true // 超过 1MB 上限截断
	}
	return out, nil
}
