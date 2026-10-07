// searchinfo.go:搜索能力的**自证入口**(服务 ctx.search,供 `/search` 诊断命令用)。
//
// 为什么要有它(2026-10-06 用户反馈带出来的):「让 gah 分析年报,搜索一直报
// web_search: 搜索服务返回 402」—— 这句话里没有任何可操作信息:哪一家?走的哪个端点?
// key 有没有?而查这些只能去翻 gah-data/config/search.yaml,那份文件带凭据、被围栏拦、
// 路径里还有空格。于是排查花了很久,而**结论(gah 压根没用默认的 anysearch)在配置里写着**。
//
// 这一层的设计取舍:
//   - 只有**只读**方法:它做诊断,不提供换 provider 的能力(那属于设置面,改配置才是);
//   - 端点**只回报主机名**:自建端点可能把凭据放在路径或查询串里,整条 URL 回显就是泄漏;
//   - Key 只回报「有没有」与打码后的尾巴,不给全文;
//   - 命令侧只依赖本接口,不必 import tool-web 的内部类型(插件边界不能破)。
package sdk

import (
	"context"
	"strings"
)

// SearchInfo 搜索配置/连通性的只读快照(展示层直接用,不再自己判断)。
type SearchInfo struct {
	// Provider 实际生效的 provider 名(exa / anysearch / …)。
	Provider string
	// EndpointHost 端点**主机名**(不含路径与查询串 —— 凭据可能藏在里面)。
	EndpointHost string
	// HasKey 是否配了 key;ConfiguredKeyMasked 打码后的 key(空串 = 无 key)。
	HasKey            bool
	ConfiguredKeyMask string
	// LastProbe 最近一次实测(TryProbe)的结果;未测过时 Ok=false 且 Note 说明。
	Ok           bool
	ResultCount  int
	LatencyMS    int
	Err          string
	Note         string
	ResultTitles []string
}

// SearchService 服务(ctx.search):搜索能力的只读自证入口。
//
// **未装配就是没装**(tool-web 没加载):调用方必须显式处理"拿不到"这件事,
// 不能静默当成"没配置" —— 那会把问题指向错的地方。
type SearchService interface {
	// SearchInfo 当前配置快照(纯本地读配置,不发请求)。
	SearchInfo() SearchInfo
	// TryProbe 实跑一次搜索并回填 Ok/耗时/标题;失败时 Err 是给用户看的中文原因。
	TryProbe(ctx context.Context, query string, n int) SearchInfo
}

// SearchEndpointHost 从端点 URL 里取主机名。
//
// **刻意只取 host**:自建/代理端点常把凭据放在路径或查询串里(`?key=…`),
// 把整条 URL 显示到界面上就是泄漏;而"是哪个服务"这个问题 host 就足够回答。
// 解析失败返回 "(端点无法解析)" 而不是原串 —— 同样是为了不泄漏。
func SearchEndpointHost(endpoint string) string {
	e := strings.TrimSpace(endpoint)
	if e == "" {
		return ""
	}
	// 去掉 scheme
	if i := strings.Index(e, "://"); i >= 0 {
		e = e[i+3:]
	}
	// host 到第一个 / 、? 或 # 为止
	if i := strings.IndexAny(e, "/?#"); i >= 0 {
		e = e[:i]
	}
	// 去掉端口:「是哪一家」这个问题 host 就够回答,端口是本地自建端点才有的细节,
	// 留在输出里只会让人以为那是线上服务的一部分。
	if i := strings.LastIndex(e, ":"); i >= 0 && !strings.Contains(e[i:], "]") {
		e = e[:i]
	}
	if i := strings.Index(e, "@"); i >= 0 { // user:pass@host 形态也只留 host
		e = e[i+1:]
	}
	if e == "" {
		return "(端点无法解析)"
	}
	return e
}

// MaskSearchKey 打码搜索服务 key:只留首 3 尾 3,短的整条盖掉。
//
// 与 provider key 的既有口径一致 —— 诊断界面里出现完整 key,就等于把它写进了会话记录
// (而会话记录会进上下文、可能落盘、被导出)。
func MaskSearchKey(k string) string {
	k = strings.TrimSpace(k)
	switch {
	case k == "":
		return ""
	case len(k) <= 8:
		return strings.Repeat("*", len(k))
	default:
		return k[:3] + strings.Repeat("*", 4) + k[len(k)-3:]
	}
}
