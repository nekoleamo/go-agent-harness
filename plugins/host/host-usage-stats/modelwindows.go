// 模型上下文窗口知识库:窗口表 + 解析器(纯逻辑,零宿主依赖)。
// 职责:把"模型名 → 上下文窗口"的领域知识集中一处,维护新模型只改本文件表。
// 窗口来源优先级(windowForModel):配置层覆盖(model_windows)> 错误驱动学习(learned,
// 实测窗口)> 内置表(发行版本基线)> 0(未知,展示层只显示使用量,不假精确)。
package hostusagestats

import (
	"regexp"
	"strconv"
	"strings"
)

// modelEntry 模型名前缀 → 上下文窗口。
type modelEntry struct {
	prefix string // 模型名前缀(如 claude-sonnet-5;精确/前缀匹配)
	window int
}

// modelWindows 常见模型上下文窗口表(前缀匹配,最长前缀优先)。
// 数据来源:各厂商官方文档(2025 查证;注释注明家族档位)。
// 该表是发行版本携带的基线,随版本更新补新条目;如需在版本间保持最新,
// 用配置层 data.model_windows 按前缀覆盖(新模型/文档更新时无需改代码)。
var modelWindows = []modelEntry{
	// OpenAI o 系列 / gpt 系列
	{"o1-", 200 * 1024},
	{"o3-", 200 * 1024},
	{"o4-", 200 * 1024},
	{"gpt-4o", 128 * 1024}, // gpt-4o / gpt-4o-mini 128K
	{"gpt-4-turbo", 128 * 1024},
	{"gpt-4-32k", 32 * 1024},
	{"gpt-4", 8 * 1024},
	{"gpt-3.5", 16 * 1024},
	// Anthropic Claude:新旗舰 1M(Fable/Mythos 5、Opus 5、Sonnet 5);其余 200K
	{"claude-fable", 1024 * 1024},
	{"claude-mythos", 1024 * 1024},
	{"claude-opus-5", 1024 * 1024},
	{"claude-sonnet-5", 1024 * 1024},
	{"claude-", 200 * 1024}, // 4.x 及其他 200K
	// DeepSeek:deepseek-chat / deepseek-reasoner / v4 系列 128K
	{"deepseek-", 128 * 1024},
	// Gemini 1M(2.0/2.5 pro/flash)
	{"gemini-", 1024 * 1024},
	// GLM:5.3/5.2 → 1M;5.1/5 → 200K;4.x → 128K
	{"glm-5.3-flash", 1024 * 1024},
	{"glm-5.3", 1024 * 1024},
	{"glm-5.2", 1024 * 1024},
	{"glm-5.1", 200 * 1024},
	{"glm-5", 200 * 1024},
	{"glm-4", 128 * 1024},
	{"glm-4v", 128 * 1024},
	{"chatglm", 128 * 1024},
	// Kimi/Moonshot:k3 → 1M;k2 → 256K;v1 → 128K
	{"kimi-k3", 1024 * 1024},
	{"kimi-k2", 256 * 1024},
	{"moonshot-v1", 128 * 1024},
	{"moonshot", 128 * 1024},
	// Qwen:max/long 大窗口;其余保守
	{"qwen-long", 1024 * 1024},
	{"qwen3-max", 131072},
	{"qwen2.5-max", 131072},
	{"qwen-turbo", 131072},
	{"qwen-plus", 131072},
	{"qwen2.5-", 32 * 1024},
	{"qwen-", 32 * 1024},
	// 常见国产兼容端点
	{"glm", 128 * 1024},
}

// matchWindow 前缀匹配取最长命中(辅助;空模型名/未命中 → 0)。
func matchWindow(model string, tbl []modelEntry) int {
	best, bestLen := 0, 0
	for _, e := range tbl {
		if strings.HasPrefix(model, e.prefix) && len(e.prefix) > bestLen {
			bestLen, best = len(e.prefix), e.window
		}
	}
	return best
}

// windowForModel 按模型名解析窗口:配置层覆盖(data.model_windows)> 错误驱动学习(learned)> 内置表 > 0(未知)。
// 未知/空模型返回 0 → 展示层只显示使用量,不显示总量/百分比(避免假精确误导)。
func (s *Service) windowForModel(model string) int {
	// 配置层覆盖(最优先)
	best, bestLen := 0, 0
	for p, w := range s.extraWindows {
		if strings.HasPrefix(model, p) && len(p) > bestLen {
			bestLen, best = len(p), w
		}
	}
	if best > 0 {
		return best
	}
	// 错误驱动学习:该模型实测窗口(精确匹配;新模型自动获取优先于内置表)
	if w, ok := s.learned[model]; ok && w > 0 {
		return w
	}
	return matchWindow(model, modelWindows)
}

// windowErrPatterns 超限错误中的窗口数字提取模式(OpenAI/DeepSeek 格式 maximum context
// length is N tokens;Anthropic/部分端点 context window 带数字;取首个命中)。
var windowErrPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)maximum context length[^0-9]*(\d+)`), // ...maximum context length is 128000 tokens
	regexp.MustCompile(`(?i)(\d+)[^0-9]*maximum context length`), // 200000 maximum context length(Anthropic 风格)
	regexp.MustCompile(`(?i)context window[^0-9]*(\d+)`),         // ...context window is 200000 tokens
	regexp.MustCompile(`(?i)context length[^0-9]*(\d+)`),
}

// parseWindowFromError 从错误文本解析上下文窗口数字(未命中 → 0)。纯函数可测。
func parseWindowFromError(msg string) int {
	for _, re := range windowErrPatterns {
		if m := re.FindStringSubmatch(msg); len(m) == 2 {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}
