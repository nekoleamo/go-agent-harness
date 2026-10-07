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
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/searchfile"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// NewTool 外部化工厂(P1):外部进程入口的工具实例。
func NewTool() sdk.Tool {
	return &WebTool{client: newFetchClient()}
}

// Plugin 实现 tool-web。requires ctx.tools。
type Plugin struct{}

func (p *Plugin) Name() string { return "tool-web" }

// Start 注册 web_fetch + web_search 两个工具。
// web_search provider 经 data.provider 选择(注册表,缺省 searchfile.DefaultProvider;未知名显式失败)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	client := newHTTPClient()
	d1 := tools.Register(&WebTool{client: newFetchClient()}) // 守卫只在 fetch 侧(见 httpclient.go)
	name, _ := m.Data["provider"].(string)
	if name == "" {
		name = resolveFileProvider() // 生效配置兜底(env GAH_SEARCH_PROVIDER > 搜索配置文件)
	}
	if name == "" {
		name = searchfile.DefaultProvider // 最终缺省
	}
	factory, ok := searchProviders[name]
	if !ok {
		d1() // 未知名 provider 显式失败,不静默降级(先撤销已注册部分)
		return nil, fmt.Errorf("tool-web: 未知搜索 provider %q(可选: %s)",
			name, strings.Join(providerNames(), "/"))
	}
	d2 := tools.Register(NewSearchTool(factory(client)))
	// 搜索能力的只读自证入口(/search 命令用):回答「我现在在用哪家、端点哪个、key 有没有」。
	// 注册不了只是少一个诊断入口 —— 不撤销已注册的工具,也不让插件起不来(诊断入口不该
	// 反过来成为启动条件)。
	_ = c.Provide("ctx.search", NewSearchService())
	return func() { d1(); d2() }, nil
}

// WebTool 实现 sdk.Tool。
type WebTool struct {
	client *http.Client
}

func (t *WebTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:      "web_fetch",
		TimeoutMs: 35_000, // 覆盖 host-bridge 默认 3s 桥超时(http client 30s)
		Description: "抓取 URL 内容(纯 Go HTTP,无外部依赖):{url};返回状态码与文本/JSON 内容。" +
			"默认拒绝环回/私网/链路本地地址(防 SSRF)。",
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
		"url":    finalURL(req, resp),
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

// finalURL 返回最终落地 URL(重定向后可能已不是请求时的 URL)。
// 安全审计 F3 附带项:只回显初始 URL 时,用户看不出内容真正来自哪里。
func finalURL(req *http.Request, resp *http.Response) string {
	if resp != nil && resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.String()
	}
	if req != nil && req.URL != nil {
		return req.URL.String()
	}
	return ""
}
