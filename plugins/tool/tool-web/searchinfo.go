// searchinfo.go:tool-web 侧实现 sdk.SearchService(为 /search 诊断命令)。
//
// 为什么需要它(2026-10-07 用户排查 web_search 报错时花掉的时间):
// 「我到底在用哪家搜索、走的哪个端点、key 有没有」这三个问题,gah 此前**没有任何出口**,
// 只能去翻 gah-data/config/search.yaml —— 那份文件带凭据(被围栏拦)、路径里还有空格
// (`~/Library/Application Support/…`)。用户因此先怀疑「是不是改错了文件」。
//
// 这一层**只读**:它报告现状、试跑一次,不提供「换 provider」的能力 —— 换 provider 是设置
// 面的事,改配置才是,顺手代劳等于替用户做配置决定。
package toolweb

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/searchfile"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// searchService 当前生效搜索配置的只读快照。
type searchService struct {
	provider string
	endpoint string
	apiKey   string
	client   *http.Client
	// provider 实例(用于 TryProbe);构造失败时为 nil,TryProbe 会如实说明。
	impl SearchProvider
	// err 配置/构造级错误(如未知 provider):SearchInfo 如实报,不静默兜底。
	err error
}

// NewSearchService 构造搜索自证服务(纯本地读配置,不发请求)。
func NewSearchService() sdk.SearchService {
	name := resolveFileProvider()
	if name == "" {
		name = searchfile.DefaultProvider
	}
	// 端点与 key 都按**当前 provider**取,不能用 resolveFileEndpoint —— 那个函数是
	// exa 专用的(内部硬编码 ProviderExa),拿它给 anysearch 会返回空或别人的端点。
	var endpoint, key string
	if cfg, err := loadSearchConfig(); err == nil {
		endpoint = cfg.EndpointFor(name)
		key = cfg.KeyFor(name)
	}
	// 配置里没写 endpoint 时补上官方默认 —— 诊断命令要回答的是「请求实际打到哪」,
	// 留空会让用户以为「没配端点所以不知道发到哪」,而真实目标一直是确定的。
	if endpoint == "" {
		switch name {
		case searchfile.ProviderExa:
			endpoint = exaDefaultEndpoint
		case searchfile.ProviderAnysearch:
			endpoint = anysearchDefaultEndpoint
		}
	}
	s := &searchService{
		provider: name,
		endpoint: endpoint,
		apiKey:   key,
		client:   newHTTPClient(),
	}
	factory, ok := searchProviders[name]
	if !ok {
		s.err = &SearchError{Kind: "config", Msg: "未知搜索 provider " + name + "(可选: " + strings.Join(providerNames(), "/") + ")"}
		return s
	}
	s.impl = factory(s.client)
	return s
}

// SearchInfo 当前配置快照。
func (s *searchService) SearchInfo() sdk.SearchInfo {
	info := sdk.SearchInfo{
		Provider:          s.provider,
		EndpointHost:      sdk.SearchEndpointHost(s.endpoint),
		HasKey:            strings.TrimSpace(s.apiKey) != "",
		ConfiguredKeyMask: sdk.MaskSearchKey(s.apiKey),
		Note:              searchNote(s.provider, s.endpoint),
	}
	return info
}

// searchNote 一句人话提醒:这个组合在真机上意味着什么。
//
// 「配置合法」不等于「能用」——匿名可用/需订阅/key 有效性都只有试跑才知道,所以这里
// 把已知的坑先说出来(用户排查时最费时间的正是这些「配得对但用不了」的情形)。
func searchNote(provider, endpoint string) string {
	switch provider {
	case searchfile.ProviderExa:
		return "exa 按量付费:匿名调用会被拒(402/401)。缺省且国内直连的是 anysearch"
	case searchfile.ProviderAnysearch:
		if strings.TrimSpace(endpoint) == "" || strings.Contains(endpoint, "api.anysearch.com") {
			return "anysearch 匿名即可用;配了 key 反而会因 key 失效而 401(会自动退回匿名)"
		}
		return "自定义 anysearch 端点:" + sdk.SearchEndpointHost(endpoint)
	default:
		return ""
	}
}

// TryProbe 实跑一次搜索,回填结果与耗时。
func (s *searchService) TryProbe(ctx context.Context, query string, n int) sdk.SearchInfo {
	info := s.SearchInfo()
	if s.err != nil {
		info.Err = s.err.Error()
		return info
	}
	if s.impl == nil {
		info.Err = "搜索实现未就绪"
		return info
	}
	if n <= 0 {
		n = 3
	}
	start := time.Now()
	res, err := s.impl.Search(ctx, query, n)
	info.LatencyMS = int(time.Since(start).Milliseconds())
	if err != nil {
		info.Err = err.Error()
		return info
	}
	info.Ok = true
	info.ResultCount = len(res)
	for i, r := range res {
		if i >= 3 {
			break
		}
		info.ResultTitles = append(info.ResultTitles, r.Title)
	}
	return info
}
