// anysearchProvider 默认 provider(2026-10-03 起由 exa 切来)。
//
// 为什么换:exa 按量付费,额度耗尽回 402,表现为「一搜索就坏」且用户无从自愈;
// anysearch 允许**匿名调用**(不带 Authorization 头,按客户端 IP 计日免费额度),
// 零配置即能搜、国内直连可用。exa 仍保留在注册表里,provider 一行切回。
//
// 协议(实测 2026-10-03):POST {endpoint},体 {"query":…,"num_results":N}
// → {"code":0,"data":{"results":[{title,url,snippet,content}]}}。
// 业务错(code != 0)也回 HTTP 200,故**必须查 code**,只看状态码会把"额度用完"当成功。
package toolweb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/searchfile"
)

// anysearchDefaultEndpoint 官方端点(api.anysearch.com;文档站为 SPA,端点形状取自实测)。
const anysearchDefaultEndpoint = "https://api.anysearch.com/v1/search"

// anysearchProvider 直连 AnySearch Search API。
type anysearchProvider struct {
	client   *http.Client
	endpoint string
	apiKey   string
	err      error // 配置解析错误(坏 search.yaml):Search 时显式失败
}

// NewAnysearchProvider 构造 anysearch provider。key 为空 = 匿名调用(合法)。
func NewAnysearchProvider(client *http.Client) *anysearchProvider {
	if client == nil {
		client = newHTTPClient()
	}
	cfg, err := loadSearchConfig()
	if err != nil {
		return &anysearchProvider{client: client, endpoint: anysearchDefaultEndpoint, err: err}
	}
	endpoint := strings.TrimSpace(cfg.EndpointFor(searchfile.ProviderAnysearch))
	if endpoint == "" {
		endpoint = anysearchDefaultEndpoint
	}
	return &anysearchProvider{client: client, endpoint: endpoint, apiKey: cfg.KeyFor(searchfile.ProviderAnysearch)}
}

func (p *anysearchProvider) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	if p.err != nil {
		return nil, &SearchError{Kind: "config", Msg: p.err.Error()}
	}
	if n < 1 {
		n = 1
	}
	if n > maxResults {
		n = maxResults
	}
	body, err := json.Marshal(map[string]any{"query": query, "num_results": n})
	if err != nil {
		return nil, &SearchError{Kind: "data", Msg: "搜索请求构造失败: " + err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, &SearchError{Kind: "network", Msg: "搜索请求构造失败: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	// 匿名调用不带 Authorization;带错 key 会 401/403 且**不会**回落匿名,所以宁可不带。
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
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return nil, &SearchError{Kind: "auth", Msg: fmt.Sprintf(
			"搜索服务拒绝鉴权(%d)。anysearch 匿名即可用,可把 %s 的配置清空;exa 需检查 $GAH_HOME/config/search.yaml 的 key",
			resp.StatusCode, searchfile.EnvAnysearchAPIKey)}
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, &SearchError{Kind: "rate", Msg: "搜索服务限流(429),请稍后重试"}
	case resp.StatusCode == http.StatusPaymentRequired:
		return nil, &SearchError{Kind: "quota", Msg: fmt.Sprintf(
			"搜索服务额度已用尽(402)。换 provider:%s 写 %s(匿名可用)后重载插件",
			searchfile.EnvProvider, searchfile.ProviderAnysearch)}
	case resp.StatusCode >= 500:
		return nil, &SearchError{Kind: "server", Msg: fmt.Sprintf("搜索服务暂时不可用(%d),可稍后重试", resp.StatusCode)}
	case resp.StatusCode != http.StatusOK:
		return nil, &SearchError{Kind: "http", Msg: fmt.Sprintf("搜索服务返回 %d", resp.StatusCode)}
	}
	var out struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Results []struct {
				Title   string `json:"title"`
				URL     string `json:"url"`
				Snippet string `json:"snippet"`
				Content string `json:"content"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &SearchError{Kind: "data", Msg: "搜索响应解析失败: " + err.Error()}
	}
	// 业务错也回 200:不查 code 就会把"额度用完/参数非法"当成"搜到 0 条"。
	if out.Code != 0 {
		return nil, &SearchError{Kind: "quota", Msg: fmt.Sprintf(
			"搜索服务返回错误(code=%d):%s。可换 provider:%s 写 %s(匿名可用)",
			out.Code, strings.TrimSpace(out.Message), searchfile.EnvProvider, searchfile.ProviderAnysearch)}
	}
	results := make([]SearchResult, 0, len(out.Data.Results))
	for _, r := range out.Data.Results {
		snippet := r.Snippet
		if snippet == "" {
			snippet = r.Content
		}
		results = append(results, SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: truncate(snippet, 300),
		})
	}
	return results, nil
}
