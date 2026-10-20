// cacheusage.go:缓存命中 token 的**多形状**解析。
//
// 为什么需要它(2026-10-20 优化批次 A3):同一个"缓存命中"数字,不同厂商报法不同:
//
//	OpenAI 形状:usage.prompt_tokens_details.cached_tokens
//	DeepSeek 原生:usage.prompt_cache_hit_tokens / prompt_cache_miss_tokens
//
// 原实现只认第一种 ⇒ 经某些网关(tokenplus / aiping 转发)时 details 可能整个缺失,
// **命中率恒显示 0%**;用户据此判断"缓存没生效",而其实命中了 —— 这是会误导决策的假数据。
//
// 取值口径:两形状都取,**取大者**。
// 同一请求里两字段本应一致;不一致时以大的为准(宁高不低:漏报命中比多报更伤 ——
// 漏报会让用户去改那些根本没坏的代码)。
package llmopenai

// wirePromptCache OpenAI 形状的缓存明细(prompt_tokens_details)。
type wirePromptCache struct {
	CachedTokens int `json:"cached_tokens"`
}

// cachedTokensOf 汇总缓存命中 token(两形状,取大者)。
//
// 两形状的来源:
//   - OpenAI:usage.prompt_tokens_details.cached_tokens
//   - DeepSeek 原生:usage.prompt_cache_hit_tokens
//
// 同一请求里两字段本应一致;不一致时以大的为准(宁高不低:漏报命中比多报更伤 ——
// 漏报会让用户去改那些根本没坏的代码)。
func cachedTokensOf(details *wirePromptCache, deepseekHit int) int {
	best := deepseekHit
	if details != nil && details.CachedTokens > best {
		best = details.CachedTokens
	}
	return best
}
