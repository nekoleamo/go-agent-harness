// 搜索结果清洗(2026-10-03):把「搜索引擎的结果页」从 web_search 结果里剔掉。
//
// 动因(用户实测):国内网络访问不了 DuckDuckGo/Google/Bing 这类站点,而搜索服务会把
// 「xxx 的搜索结果页」当成一条正常结果返回 —— 模型拿到的 URL 一 fetch 就超时/被墙,
// 于是变成「搜了但用不上」,还白烧一次 web_fetch 与一次域名审批。
//
// 判定口径:**只看 host**,不猜 query 参数(结果页的形态太杂:`?q=` / `?wd=` / 路径式
// /s.html?q=… / `/url?q=…`,参数判不完,而 host 是稳定的)。国内可直连的引擎
// (baidu / sogou / so.com / cn.bing)与正经站点共处时优先保留 —— 只剔真正访问不了的。
//
// 为什么不整条丢弃结果而是标注:引擎域名的**子域**(如 `html.duckduckgo.com`、
// `lite.duckduckgo.com`)同样是结果页;而 `sogou.com` 下的 `zhihu.sogou.com` 是真站点。
// 故按 host 后缀精确匹配到引擎域本身或其已知子域。
package toolweb

import (
	"net/url"
	"strings"
)

// blockedResultHosts 剔除的搜索引擎结果页域名(小写 host,含子域)。
//
// 收录标准:**在境内网络基本不可达**(用户实测反馈),或结果页对 agent 无价值。
// 刻意不收 cn.bing.com / baidu 子站等境内可达的引擎 —— 剔了就等于替用户做了地域判断。
var blockedResultHosts = map[string]bool{
	"duckduckgo.com":       true,
	"html.duckduckgo.com":  true,
	"lite.duckduckgo.com":  true,
	"start.duckduckgo.com": true,
	"google.com":           true,
	"www.google.com":       true,
	"bing.com":             true,
	"www.bing.com":         true,
	"search.yahoo.com":     true,
	"yandex.com":           true,
	"yandex.ru":            true,
	"search.brave.com":     true,
	"ecosia.org":           true,
	"search.naver.com":     true,
	"search.yahoo.co.jp":   true,
	"searx.be":             true,
}

// hostOf 取 URL 的小写 host(解析失败 = 空)。
func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// isSearchEngineResult 该结果是不是「搜索引擎的结果页」(true = 应剔除)。
func isSearchEngineResult(r SearchResult) bool {
	host := hostOf(r.URL)
	if host == "" {
		return false
	}
	if blockedResultHosts[host] {
		return true
	}
	// 已知引擎的子域(ddg 的 html./lite. 形态等):按后缀边界匹配,避免误伤
	// `notgoogle.com` 这类无关域名。
	for base := range blockedResultHosts {
		if strings.HasSuffix(host, "."+base) {
			return true
		}
	}
	return false
}

// dropSearchEngineResults 剔除引擎结果页,返回清洗后的列表与被剔条数。
//
// 位置刻意在 **SearchTool 层**而不是各 provider 内:这是「给模型看的搜索结果」的统一
// 口径,换 provider(下一个)不该重新实现一遍。
func dropSearchEngineResults(rs []SearchResult) ([]SearchResult, int) {
	kept := make([]SearchResult, 0, len(rs))
	dropped := 0
	for _, r := range rs {
		if isSearchEngineResult(r) {
			dropped++
			continue
		}
		kept = append(kept, r)
	}
	return kept, dropped
}
