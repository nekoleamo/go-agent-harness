package tui

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestContextUsageStyle 分档着色:<70% 中性 · 70–90% 提示 · ≥90% 警示。
//
// 为什么值得分档:占用数字要用户自己算才知道"快满了",而"快满了"是需要**立刻**反应的事
// (该 /compact 或换窗口更大的模型)—— 颜色让它在余光里就被看见。
func TestContextUsageStyle(t *testing.T) {
	if contextUsageStyle(10) != styleStatus {
		t.Fatalf("低占用应为中性色: %v", contextUsageStyle(10))
	}
	if contextUsageStyle(69) != styleStatus {
		t.Fatalf("69%% 应仍为中性色: %v", contextUsageStyle(69))
	}
	if contextUsageStyle(70) != styleBusy {
		t.Fatalf("70%% 应为提示色: %v", contextUsageStyle(70))
	}
	if contextUsageStyle(89) != styleBusy {
		t.Fatalf("89%% 应仍为提示色: %v", contextUsageStyle(89))
	}
	if contextUsageStyle(90) != styleError {
		t.Fatalf("90%% 应为警示色: %v", contextUsageStyle(90))
	}
	if contextUsageStyle(100) != styleError {
		t.Fatalf("100%% 应为警示色: %v", contextUsageStyle(100))
	}
}

// TestMetricLineUnknownUsage 未知占用显示 "?" 而不是 "0/128K (0%)"。
//
// 后者是**假数字**(说"没占",其实是"不知道")—— 界面说假话比不说更糟。
func TestMetricLineUnknownUsage(t *testing.T) {
	s := &State{Stats: sdk.UsageStats{Requests: 2, PromptTokens: 4096, Window: 65536}}
	out := stripColor(renderMetricLine(s))
	if !strings.Contains(out, "上下文 ?") {
		t.Fatalf("未知占用应显 ?: %q", out)
	}
	if strings.Contains(out, "0/64.0K") || strings.Contains(out, "(0%)") {
		t.Fatalf("不应显示假的 0%% 占用: %q", out)
	}
}

// TestMetricLineAutoSummaryMark 自动压缩过的上下文要标出来。
//
// 为什么:自动压缩后占用会变小,而模型"忘了前面说过什么"恰恰是这时候发生的;
// 不标的话用户看到占用突然变小却不知道为什么。
func TestMetricLineAutoSummaryMark(t *testing.T) {
	s := &State{Stats: sdk.UsageStats{Requests: 3, LastPromptTokens: 16000, Window: 65536, AutoSummary: true}}
	out := stripColor(renderMetricLine(s))
	if !strings.Contains(out, "含自动摘要") {
		t.Fatalf("应标出含自动摘要: %q", out)
	}
	// 没发生过自动压缩时不占位
	s2 := &State{Stats: sdk.UsageStats{Requests: 3, LastPromptTokens: 16000, Window: 65536}}
	if out := stripColor(renderMetricLine(s2)); strings.Contains(out, "摘要") {
		t.Fatalf("无自动压缩时不应出现摘要标记: %q", out)
	}
}

// TestMetricLineCacheHitRateCumulative 状态栏的缓存命中率是**累计**口径。
//
// 为什么状态栏用累计:它只放一个数,两个口径摆在一起反而没人看得懂;
// 逐轮命中率的诊断价值在 /cache(那里同时给累计与最近一轮)。
func TestMetricLineCacheHitRateCumulative(t *testing.T) {
	s := &State{Stats: sdk.UsageStats{
		Requests: 5, PromptTokens: 100000, CachedTokens: 90000,
		LastPromptTokens: 20000, LastCachedTokens: 0, Window: 65536,
	}}
	out := stripColor(renderMetricLine(s))
	if !strings.Contains(out, "缓存 90%") {
		t.Fatalf("状态栏应显示累计命中率: %q", out)
	}
}
