// 联网搜索工具(M6.14):web_search,默认 Exa 直连(EXA_API_KEY)。
// 包内极简 Provider seam:consumer/schema 与具体 provider 解耦,
// 新 provider 实现 SearchProvider 并登记 searchProviders 即换(data.provider 一行配置)。
// 与 web_fetch 协作:本工具只返回标题/URL/摘要,需原文正文时模型再调 web_fetch。
package toolweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

const maxResults = 10 // num_results 上限

// SearchResult 归一化搜索结果(与 provider 解耦的公共结构)。
type SearchResult struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	Snippet       string `json:"snippet"`
	PublishedDate string `json:"published_date,omitempty"`
}

// SearchProvider 搜索服务 seam:新 provider 实现本接口并登记 searchProviders 即换。
type SearchProvider interface {
	Search(ctx context.Context, query string, n int) ([]SearchResult, error)
}

// SearchError 归一化搜索错误:Kind 区分类别,供错误文案与后续重试语义。
type SearchError struct {
	Kind string // auth | rate | server | http | data | network
	Msg  string
}

func (e *SearchError) Error() string { return e.Msg }

// searchProviders 注册表:data.provider 选默认,缺省 exa;启动读,consumer 零感知。
var searchProviders = map[string]func(client *http.Client) SearchProvider{
	"exa": func(client *http.Client) SearchProvider { return NewExaProvider(client) },
}

// exaProvider 默认 provider:直连 Exa Search API(Authorization: Bearer <key>)。
// 凭据通道:host-bridge 起外部工具进程全量透传 os.Environ,shell 才白名单化,故 key 在进程可读、模型不可见。
type exaProvider struct {
	client   *http.Client
	endpoint string // 可注入(测试/自建端点/search.yaml 兜底)
	apiKey   string
	err      error // 配置解析错误(坏 search.yaml):Search 时显式失败
}

// NewExaProvider 构造 Exa provider。key 解析链:env EXA_API_KEY > search.yaml > 空;
// endpoint:search.yaml 兜底(官方端点缺省)。endpoint/apiKey 同包可注入(测试)。
func NewExaProvider(client *http.Client) *exaProvider {
	if client == nil {
		client = newHTTPClient()
	}
	key, err := resolveExaKey()
	if err != nil {
		return &exaProvider{client: client, endpoint: "https://api.exa.ai/search", err: err}
	}
	endpoint := resolveFileEndpoint()
	if endpoint == "" {
		endpoint = "https://api.exa.ai/search"
	}
	return &exaProvider{
		client:   client,
		endpoint: endpoint,
		apiKey:   key,
	}
}

func (p *exaProvider) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	if p.err != nil {
		return nil, &SearchError{Kind: "config", Msg: p.err.Error()}
	}
	if n < 1 {
		n = 1
	}
	if n > maxResults {
		n = maxResults
	}
	body, err := json.Marshal(map[string]any{"query": query, "numResults": n})
	if err != nil {
		return nil, &SearchError{Kind: "data", Msg: "搜索请求构造失败: " + err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, &SearchError{Kind: "network", Msg: "搜索请求构造失败: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, &SearchError{Kind: "network", Msg: err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, &SearchError{Kind: "network", Msg: "读搜索响应失败: " + err.Error()}
	}
	// 错误归一:401/429/5xx 给结构化文案,可重试语义对齐 llm 适配器
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, &SearchError{Kind: "auth", Msg: "搜索服务未授权(EXA_API_KEY 无效或缺失),请检查环境变量 EXA_API_KEY"}
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, &SearchError{Kind: "rate", Msg: "搜索服务限流(429),请稍后重试"}
	case resp.StatusCode >= 500:
		return nil, &SearchError{Kind: "server", Msg: fmt.Sprintf("搜索服务暂时不可用(%d),可稍后重试", resp.StatusCode)}
	case resp.StatusCode != http.StatusOK:
		return nil, &SearchError{Kind: "http", Msg: fmt.Sprintf("搜索服务返回 %d", resp.StatusCode)}
	}
	type exaResult struct {
		Title         string `json:"title"`
		URL           string `json:"url"`
		PublishedDate string `json:"publishedDate"`
		Text          string `json:"text"`
		Highlight     string `json:"highlight"`
	}
	var out struct {
		Results []exaResult `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &SearchError{Kind: "data", Msg: "搜索响应解析失败: " + err.Error()}
	}
	results := make([]SearchResult, 0, len(out.Results))
	for _, r := range out.Results {
		snippet := r.Highlight
		if snippet == "" {
			snippet = r.Text
		}
		results = append(results, SearchResult{
			Title:         r.Title,
			URL:           r.URL,
			Snippet:       truncate(snippet, 300), // 摘要截断 ~300 字
			PublishedDate: r.PublishedDate,
		})
	}
	return results, nil
}

// truncate 按字符(非字节)截断,多字节安全;超限加省略号。
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// SearchTool 实现 sdk.Tool(web_search)。provider 注入,便于测试与换源。
type SearchTool struct {
	provider SearchProvider
}

// NewSearchTool 构造 web_search 工具。
func NewSearchTool(provider SearchProvider) sdk.Tool {
	return &SearchTool{provider: provider}
}

func (t *SearchTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "web_search",
		TimeoutMs:   35_000, // 覆盖 host-bridge 默认 3s 桥超时(http client 30s)
		Description: "联网搜索:{query(必填), num_results(默认 5,上限 10)};返回标题/URL/摘要列表,需要原文正文时再调 web_fetch。",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"query"},
			"properties": map[string]any{
				"query":       map[string]any{"type": "string", "description": "搜索关键词"},
				"num_results": map[string]any{"type": "integer", "description": "结果数,默认 5,上限 10"},
			},
		},
	}
}

func (t *SearchTool) Execute(ctx context.Context, raw string) (any, error) {
	var a struct {
		Query      string `json:"query"`
		NumResults int    `json:"num_results"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("web_search: args: %w", err)
	}
	query := strings.TrimSpace(a.Query)
	if query == "" {
		return map[string]any{"error": "web_search: 缺少 query"}, nil
	}
	if t.provider == nil {
		return map[string]any{"error": "web_search: 未配置搜索 provider"}, nil
	}
	n, truncated := a.NumResults, false
	if n == 0 {
		n = 5 // 默认
	} else if n > maxResults {
		n, truncated = maxResults, true // 超上限截断
	}
	results, err := t.provider.Search(ctx, query, n)
	if err != nil {
		var se *SearchError
		if errors.As(err, &se) {
			return map[string]any{"error": "web_search: " + se.Msg}, nil
		}
		return map[string]any{"error": "web_search: " + err.Error()}, nil
	}
	out := map[string]any{"query": query, "results": results}
	if truncated {
		out["truncated"] = true
	}
	return out, nil
}
