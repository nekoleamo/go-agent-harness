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
	// DeepSeek:deepseek-chat / deepseek-reasoner(V3.x)128K;
	// **V4 系列 1M**(2026-09 官方发布,稀疏注意力 KV 压缩;V4.1 Flash 原名 deepseek-flash)
	// —— 实测 2026-10-03:未单列时 `DeepSeek-V4.1-Flash` 会落进下面的 128K 兜底,
	// 状态栏把 1M 的模型显示成 128K,压缩阈值也跟着算错。
	{"deepseek-v4.1", 1024 * 1024},
	{"deepseek-v4-flash", 1024 * 1024},
	{"deepseek-v4-pro", 1024 * 1024},
	{"deepseek-v4", 1024 * 1024},
	{"deepseek-flash", 1024 * 1024},
	{"deepseek-reasoner", 128 * 1024},
	{"deepseek-chat", 128 * 1024},
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

// matchWindow 前缀匹配取最长命中(辅助;空模型名/未命中 → 0;大小写不敏感,见 windowForModel)。
//
// 兼容**厂商前缀形态**(`deepseek-ai/DeepSeek-V4-Flash`、`openai/gpt-4o`):这类名字以
// 厂商段开头,不剥掉就永远匹配不到真正的模型前缀,只能掉进最短的兜底条目 —— 表现为
// 「1M 的模型被当成 128K」。故对 `厂商/模型` 形态额外用斜杠后的段参与一次匹配,
// 两边都命中时取**更长**的前缀;同长则原串优先(避免语义被改写)。
func matchWindow(model string, tbl []modelEntry) int {
	best, bestLen := 0, 0
	for _, cand := range matchCandidates(model) {
		w, wl := matchOne(cand, tbl)
		if wl > bestLen {
			bestLen, best = wl, w
		}
	}
	return best
}

// matchCandidates 参与匹配的候选串:原串 + 斜杠后的最后一段(无斜杠时只有原串)。
func matchCandidates(model string) []string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 && i+1 < len(m) {
		return []string{m, m[i+1:]}
	}
	return []string{m}
}

// matchOne 单个候选串的最长前缀命中(辅助,纯逻辑)。
func matchOne(m string, tbl []modelEntry) (window, prefixLen int) {
	for _, e := range tbl {
		if strings.HasPrefix(m, strings.ToLower(e.prefix)) && len(e.prefix) > prefixLen {
			prefixLen, window = len(e.prefix), e.window
		}
	}
	return window, prefixLen
}

// windowForModel 按模型名解析窗口:配置层覆盖(data.model_windows)> 错误驱动学习(learned)> 内置表 > 0(未知)。
// 未知/空模型返回 0 → 展示层只显示使用量,不显示总量/百分比(避免假精确误导)。
// **匹配统一按小写**(模型名大小写不统一:同一端点会写 deepseek-ai/DeepSeek-V4-Flash 或
// DeepSeek-V4-Flash-0731;大小写敏感会让后者漏进内置表 = 窗口未知)。
func (s *Service) windowForModel(model string) int {
	m := strings.ToLower(model)
	// 配置层覆盖(最优先);与内置表同口径参与匹配(含厂商前缀形态 `厂商/模型`)。
	best, bestLen := 0, 0
	for _, cand := range matchCandidates(m) {
		for p, w := range s.extraWindows {
			if strings.HasPrefix(cand, strings.ToLower(p)) && len(p) > bestLen {
				bestLen, best = len(p), w
			}
		}
	}
	if best > 0 {
		return best
	}
	// 错误驱动学习:该模型实测窗口(新模型自动获取优先于内置表)
	if w, ok := s.learned[strings.ToLower(model)]; ok && w > 0 {
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
