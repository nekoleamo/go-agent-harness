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
	"sync"

	"github.com/nekoleamo/go-agent-harness/internal/searchfile"
)

// anysearchDefaultEndpoint 官方端点(api.anysearch.com;文档站为 SPA,端点形状取自实测)。
// providerLabelAnysearch 本文件这条路径的服务名(兜底报错要指名是谁在报错)。
const providerLabelAnysearch = "anysearch"

const anysearchDefaultEndpoint = "https://api.anysearch.com/v1/search"

// anysearchProvider 直连 AnySearch Search API。
type anysearchProvider struct {
	client   *http.Client
	endpoint string
	apiKey   string
	err      error // 配置解析错误(坏 search.yaml):Search 时显式失败
	// keyRejected 记「这次运行的 key 被服务端拒了」—— 拒一次之后就一直匿名。
	//
	// 为什么要有记忆而不是每次都重试:401 往返一次不多,但每次搜索都多一次失败请求,
	// 在批量搜索里会明显变慢。key 是**配置**,它不会自己在运行中变好,拒了就该当作没配。
	keyRejected bool
	mu          sync.Mutex
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
	// 带 key(提额用);**被拒过一次之后一律不带** —— 见 keyRejected 的注释。
	//
	// 为什么不是「宁可不带」(原注释那么写、代码却反着做):匿名才是这条路的前提,
	// 带 key 只是加分项。加分项把主功能打挂是本末倒置(2026-10-06 实测:一个失效 key 就 401)。
	p.mu.Lock()
	key := ""
	if !p.keyRejected {
		key = p.apiKey
	}
	p.mu.Unlock()
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
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
		// key 被拒 → **退回匿名重试一次**。匿名是这条路能工作的前提(key 只是提额),
		// 让一个加分项把主功能打挂是本末倒置 —— 这正是 2026-10-06 用户遇到的:
		// 搜不动,报错还只有一句「搜索服务返回 402」,看不出是没配 key 还是 key 坏了。
		if p.rememberKeyRejected() {
			return p.Search(ctx, query, n) // 已记住「key 无效」,这次不带 key
		}
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
		// 兜底也**必须说清是谁在报错**:「搜索服务返回 402」这种句子对用户零信息量 ——
		// 他既不知道是哪一家,也不知道下一步做什么(2026-10-06 实测:用户拿这句话来问,
		// 而真正的信息在它前面那个分支里,压根没被打印出来)。
		return nil, &SearchError{Kind: "http", Msg: fmt.Sprintf(
			"搜索服务(%s)返回 %d。排查:%s=%s 是当前 provider;anysearch 匿名可用(配了 key 也可能因失效而 402/401,会自动退回匿名);exa 是按量付费,402 即额度用尽,换 %s=%s 后 /reload",
			providerLabelAnysearch, resp.StatusCode,
			searchfile.EnvProvider, currentProviderName(),
			searchfile.EnvProvider, searchfile.ProviderAnysearch)}
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

// rememberKeyRejected 记下「key 被拒」并回报是否**本次**才发生(第一次才重试)。
func (p *anysearchProvider) rememberKeyRejected() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.apiKey == "" {
		return false // 已经是匿名在跑了,再退一次没有意义
	}
	if p.keyRejected {
		return false // 已经退过,别把每次搜索都变成两次请求
	}
	p.keyRejected = true
	return true
}
