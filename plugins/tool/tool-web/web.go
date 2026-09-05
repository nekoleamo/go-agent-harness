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

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// NewTool 外部化工厂(P1):外部进程入口的工具实例。
func NewTool() sdk.Tool {
	return &WebTool{client: newHTTPClient()}
}

// Plugin 实现 tool-web。requires ctx.tools。
type Plugin struct{}

func (p *Plugin) Name() string { return "tool-web" }

// Start 注册 web_fetch + web_search 两个工具。
// web_search provider 经 data.provider 选择(注册表,缺省 exa;未知名显式失败)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	client := newHTTPClient()
	d1 := tools.Register(&WebTool{client: client})
	name, _ := m.Data["provider"].(string)
	if name == "" {
		name = resolveFileProvider() // search.yaml 兜底(不依赖 bundle data)
	}
	if name == "" {
		name = "exa" // 最终缺省
	}
	factory, ok := searchProviders[name]
	if !ok {
		d1() // 未知名 provider 显式失败,不静默降级(先撤销已注册部分)
		return nil, fmt.Errorf("tool-web: 未知搜索 provider %q(可选: exa)", name)
	}
	d2 := tools.Register(NewSearchTool(factory(client)))
	return func() { d1(); d2() }, nil
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
	req.Header.Set("User-Agent", userAgent)
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
