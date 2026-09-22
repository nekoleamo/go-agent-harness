// host-usage-stats 测试:事件累计/过滤/Reset + 模型窗口表解析(前缀/最长前缀/未知回退/显式覆盖)。
package hostusagestats

import (
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// usageEvent 便捷构造 usage 事件载荷。
func usageEvent(model string, u sdk.Usage) *sdk.SessionEvent {
	return &sdk.SessionEvent{Kind: sdk.EventUsage, Payload: sdk.UsageEvent{Model: model, Usage: u}}
}

// TestAccumulateFromUsageEvents 订阅事件累计:非 usage 事件忽略;窗口跟随最新模型。
func TestAccumulateFromUsageEvents(t *testing.T) {
	s := &Service{}
	evs := []*sdk.SessionEvent{
		usageEvent("deepseek-chat", sdk.Usage{PromptTokens: 100, CompletionTokens: 20, CachedTokens: 60}),
		{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "hi"}}, // 非 usage 忽略
		usageEvent("deepseek-chat", sdk.Usage{PromptTokens: 50, CompletionTokens: 10, CachedTokens: 30}),
	}
	for _, ev := range evs {
		s.HandleSessionEvent(ev)
	}
	st := s.Stats()
	if st.Requests != 2 || st.PromptTokens != 150 || st.CompletionTokens != 30 || st.CachedTokens != 90 {
		t.Fatalf("累计不符: %+v", st)
	}
	if st.Window != 128*1024 {
		t.Fatalf("deepseek-chat 窗口应为 128K: %+v", st)
	}
}

// TestReset 切换会话归零:统计清空、窗口按模型重解析(模型清空后回兜底)。
func TestReset(t *testing.T) {
	s := &Service{}
	s.HandleSessionEvent(usageEvent("claude-sonnet-4-5", sdk.Usage{PromptTokens: 999}))
	s.Reset()
	st := s.Stats()
	if st.Requests != 0 || st.PromptTokens != 0 || st.CachedTokens != 0 {
		t.Fatalf("Reset 应归零: %+v", st)
	}
	if st.Window != 0 {
		t.Fatalf("Reset 后无模型窗口应为 0(未知,仅显示用量): %+v", st)
	}
}

// TestBadPayloadIgnored 载荷不是 UsageEvent/Usage 时不计(防御;nil 不 panic)。
func TestBadPayloadIgnored(t *testing.T) {
	s := &Service{}
	s.HandleSessionEvent(nil)
	s.HandleSessionEvent(&sdk.SessionEvent{Kind: sdk.EventUsage, Payload: "not-usage"})
	if got := s.Stats().Requests; got != 0 {
		t.Fatalf("坏载荷不应累计,got %d", got)
	}
}

// TestLegacyUsagePayload 兼容旧日志:载荷为裸 sdk.Usage(无模型名)仍累计。
func TestLegacyUsagePayload(t *testing.T) {
	s := &Service{}
	s.HandleSessionEvent(&sdk.SessionEvent{Kind: sdk.EventUsage, Payload: sdk.Usage{PromptTokens: 7}})
	if st := s.Stats(); st.Requests != 1 || st.PromptTokens != 7 {
		t.Fatalf("旧载荷应累计: %+v", st)
	}
}

// TestWindowForModel 窗口表解析:精确/前缀/最长前缀优先/未知回退/显式覆盖。
func TestWindowForModel(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"deepseek-chat", 128 * 1024},
		{"deepseek-reasoner", 128 * 1024},
		{"claude-sonnet-4-5", 200 * 1024}, // claude- 命中 200K
		{"claude-opus-5", 1024 * 1024},    // 更长前缀 1M(优先于 claude- 的 200K)
		{"claude-3-5-sonnet", 200 * 1024}, // 未在 1M 名单 → 200K
		{"gpt-4o", 128 * 1024},            // gpt-4o 前缀长于 gpt-4
		{"gpt-4o-mini", 128 * 1024},       // gpt-4o 前缀命中
		{"o3-mini", 200 * 1024},           // o3- 命中
		{"gemini-2.5-pro", 1024 * 1024},   // gemini- 1M
		{"glm-5.3", 1024 * 1024},          // 新旗舰 1M(内置表随版本补新)
		{"glm-5.3-flash", 1024 * 1024},
		{"glm-5.2", 1024 * 1024},         // 长前缀 1M
		{"glm-4.7", 128 * 1024},          // glm-4 命中
		{"chatglm-4", 128 * 1024},        // chatglm 命中
		{"kimi-k2-thinking", 256 * 1024}, // kimi-k2 命中
		{"moonshot-v1-8k", 128 * 1024},   // moonshot-v1 命中
		{"qwen-plus", 131072},            // qwen-plus 命中
		{"qwen2.5-coder-7b", 32 * 1024},  // qwen2.5- 命中
		{"unknown-model-xyz", 0},         // 未知 → 兜底
		{"", 0},                          // 空 → 兜底
		// 大小写不敏感:同一端点会写 deepseek-ai/DeepSeek-V4-Flash 或 DeepSeek-V4-Flash-0731,
		// 大小写敏感会让后者漏进内置表(窗口未知 ⇒ 压缩阈值退化为绝对上限、展示层不显示占用比)。
		{"DeepSeek-V4-Flash-0731", 128 * 1024},
		{"deepseek-ai/DeepSeek-V4-Flash", 128 * 1024},
		{"GLM-5.3", 1024 * 1024},
		{"Claude-Sonnet-5", 1024 * 1024},
	}
	for _, c := range cases {
		if got := (&Service{}).windowForModel(c.model); got != c.want {
			t.Errorf("windowForModel(%q) = %d,want %d", c.model, got, c.want)
		}
	}
}

// TestExtraWindowsOverlay data.model_windows 配置层覆盖优先于内置表(最长前缀)。
func TestExtraWindowsOverlay(t *testing.T) {
	s := &Service{extraWindows: map[string]int{"deepseek-": 1024 * 1024}}
	s.HandleSessionEvent(usageEvent("deepseek-chat", sdk.Usage{PromptTokens: 1}))
	if st := s.Stats(); st.Window != 1024*1024 {
		t.Fatalf("配置层覆盖应优先于内置表(内置 deepseek 128K): %+v", st)
	}
	// 未覆盖的模型仍走内置表
	s2 := &Service{extraWindows: map[string]int{"deepseek-": 1024 * 1024}}
	s2.HandleSessionEvent(usageEvent("claude-sonnet-4-5", sdk.Usage{PromptTokens: 1}))
	if st := s2.Stats(); st.Window != 200*1024 {
		t.Fatalf("未覆盖模型应走内置表: %+v", st)
	}
	// 配置键与模型名同样大小写不敏感
	s4 := &Service{extraWindows: map[string]int{"MiniMax-": 512 * 1024}}
	s4.HandleSessionEvent(usageEvent("minimax-m2", sdk.Usage{PromptTokens: 1}))
	if st := s4.Stats(); st.Window != 512*1024 {
		t.Fatalf("配置层覆盖应大小写不敏感: %+v", st)
	}
	// 显式 context_window 仍最优先
	s3 := &Service{windowOverride: 32768, extraWindows: map[string]int{"deepseek-": 1024 * 1024}}
	s3.HandleSessionEvent(usageEvent("deepseek-chat", sdk.Usage{PromptTokens: 1}))
	if st := s3.Stats(); st.Window != 32768 {
		t.Fatalf("context_window 应优先于 model_windows: %+v", st)
	}
}

// TestWindowOverride 显式 data.context_window 总覆盖(不查表)。
func TestWindowOverride(t *testing.T) {
	s := &Service{windowOverride: 131072}
	s.HandleSessionEvent(usageEvent("gemini-2.5-pro", sdk.Usage{PromptTokens: 10}))
	if st := s.Stats(); st.Window != 131072 {
		t.Fatalf("显式覆盖应优先于模型表: %+v", st)
	}
	// Reset 后仍保持覆盖
	s.Reset()
	if st := s.Stats(); st.Window != 131072 {
		t.Fatalf("Reset 不应清窗口覆盖: %+v", st)
	}
}

// TestParseWindowFromError 超限错误文本 → 窗口数字(OpenAI/Anthropic 格式;未命中 → 0)。
func TestParseWindowFromError(t *testing.T) {
	cases := []struct {
		msg  string
		want int
	}{
		{"This model's maximum context length is 128000 tokens. However, you requested 200000 tokens", 128000},
		{"prompt is too long: 200001 > 200000 maximum context length", 200000},
		{"messages: reduce the length; context window is 200000 tokens", 200000},
		{"network timeout", 0},
		{"401 auth failed", 0},
	}
	for _, c := range cases {
		if got := parseWindowFromError(c.msg); got != c.want {
			t.Errorf("parseWindowFromError(%q) = %d,want %d", c.msg, got, c.want)
		}
	}
}

// TestLearnWindowFromError 错误驱动学习:模型空/无数字不记;学到了优先于内置表。
func TestLearnWindowFromError(t *testing.T) {
	s := &Service{}
	s.LearnWindowFromError("", "maximum context length is 9999 tokens") // 无模型忽略
	s.LearnWindowFromError("glm-5.3", "network timeout")                // 无数字忽略
	s.LearnWindowFromError("glm-5.3", "maximum context length is 1048576 tokens")
	// learned 优先于内置表(内置 glm-5.3 也是 1M,但用其它模型验证优先级)
	s.LearnWindowFromError("deepseek-chat", "maximum context length is 131072 tokens")
	s.HandleSessionEvent(usageEvent("deepseek-chat", sdk.Usage{PromptTokens: 1}))
	if st := s.Stats(); st.Window != 131072 {
		t.Fatalf("learned 应优先于内置表(内置 deepseek 128K): %+v", st)
	}
	// 配置层仍最优先
	s2 := &Service{extraWindows: map[string]int{"deepseek-": 1 << 20}}
	s2.LearnWindowFromError("deepseek-chat", "maximum context length is 131072 tokens")
	s2.HandleSessionEvent(usageEvent("deepseek-chat", sdk.Usage{PromptTokens: 1}))
	if st := s2.Stats(); st.Window != 1<<20 {
		t.Fatalf("配置层应优先于 learned: %+v", st)
	}
}
