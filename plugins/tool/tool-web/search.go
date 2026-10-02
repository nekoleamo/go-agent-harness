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
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nekoleamo/go-agent-harness/internal/searchfile"
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

// searchProviders 注册表:data.provider 选默认,缺省 searchfile.DefaultProvider;
// 启动读,consumer 零感知。新增 provider = 在此登记一行(键须与 searchfile 的 provider 常量一致)。
var searchProviders = map[string]func(client *http.Client) SearchProvider{
	searchfile.ProviderAnysearch: func(client *http.Client) SearchProvider { return NewAnysearchProvider(client) },
	searchfile.ProviderExa:       func(client *http.Client) SearchProvider { return NewExaProvider(client) },
}

// NewSearchToolFromEnv **外部插件**用的构造入口(第八十五批):provider 名取生效配置
// (env GAH_SEARCH_PROVIDER > 配置文件 provider,缺省 searchfile.DefaultProvider;
// 宿主按 Capabilities.ConfigEnv 注入),
// 未知名/配置坏时**不拖垮同进程的其它工具** —— tool-basic 还挂着 shell/文件/记忆/todo,
// 一个拼写错误不该让它们全不可用,故返回一个“调用即显式报错”的搜索工具(错误在用的时候可见)。
// 进程内装配仍走 Plugin.Start 的严格口径(未知 provider = 装配期显式失败),两者只差失败时机。
func NewSearchToolFromEnv(client *http.Client) sdk.Tool {
	name := resolveFileProvider()
	if name == "" {
		name = searchfile.DefaultProvider
	}
	factory, ok := searchProviders[name]
	if !ok {
		return NewSearchTool(&errProvider{err: fmt.Errorf("未知搜索 provider %q(可选: %s)",
			name, strings.Join(providerNames(), "/"))})
	}
	return NewSearchTool(factory(client))
}

// providerNames 注册表里的 provider 名(排序;错误文案用,避免文案与注册表漂移)。
func providerNames() []string {
	names := make([]string, 0, len(searchProviders))
	for n := range searchProviders {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// errProvider 只报错的 provider(配置级失败时占位)。
type errProvider struct{ err error }

func (p *errProvider) Search(context.Context, string, int) ([]SearchResult, error) {
	return nil, &SearchError{Kind: "config", Msg: p.err.Error()}
}

// exaProvider 默认 provider:直连 Exa Search API(Authorization: Bearer <key>)。
// 凭据通道:key 只经 env 或 $GAH_HOME/config/search.yaml 到达本进程 —— 外部插件由宿主按
// Capabilities.ConfigEnv 声明从配置文件读值注入 env(默认形态下插件进程读不到 config/,见
// internal/searchfile 与 sdk/credentialpath.go);shell 子进程 env 也经 sdk.SanitizedEnv 滤除
// *_API_KEY,模型拿不到。
type exaProvider struct {
	client   *http.Client
	endpoint string // 可注入(测试/自建端点/search.yaml 兜底)
	apiKey   string
	err      error // 配置解析错误(坏 search.yaml):Search 时显式失败
}

// NewExaProvider 构造 Exa provider。key/endpoint 解析链见 internal/searchfile.Resolve:
// env(EXA_API_KEY / GAH_SEARCH_ENDPOINT)> $GAH_HOME/config/search.yaml。
// endpoint/apiKey 同包可注入(测试)。
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
		return nil, &SearchError{Kind: "auth", Msg: "搜索服务未授权(搜索 key 无效或缺失),请检查 $GAH_HOME/config/search.yaml 的 api_key/exa_api_key 或环境变量 " + searchfile.EnvExaAPIKey}
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, &SearchError{Kind: "rate", Msg: "搜索服务限流(429),请稍后重试"}
	case resp.StatusCode == http.StatusPaymentRequired:
		// exa 是按量付费的:额度耗尽就是 402,用户侧看着像「一搜索就坏」。
		// 文案必须给出可执行的下一步(换 provider),否则无从自愈。
		return nil, &SearchError{Kind: "quota", Msg: fmt.Sprintf(
			"搜索服务额度已用尽(402)。换 provider:%s 写 %s(匿名可用、国内直连)后重载插件",
			searchfile.EnvProvider, searchfile.ProviderAnysearch)}
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
	// 剔除搜索引擎结果页(见 search_filter.go):这些 URL 在境内基本不可达,
	// 留着只会让模型去 fetch 一个必然失败的地址,还白烧一次域名审批。
	kept, dropped := dropSearchEngineResults(results)
	out := map[string]any{"query": query, "results": kept}
	if truncated {
		out["truncated"] = true
	}
	if dropped > 0 {
		out["dropped_search_engine_results"] = dropped
	}
	if len(kept) == 0 && dropped > 0 {
		out["error"] = "web_search: 全部结果都指向无法访问的搜索引擎结果页,已全部剔除;" +
			"请换一个更具体的关键词,或改用 web_fetch 直接访问已知站点"
	}
	return out, nil
}
