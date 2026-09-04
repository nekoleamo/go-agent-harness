// /provider 与 /model 命令辅助单测:凭据打码、模型来源备注、URL 来源短名。
package tui

import (
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestMaskKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "(未设置)"},
		{"abc", "***"},
		{"abcdef", "***"},
		{"sk-abcdef1234", "***1234"},
	}
	for _, c := range cases {
		if got := maskKey(c.in); got != c.want {
			t.Fatalf("maskKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestModelDesc 模型来源备注格式:(来源);归属前缀与 ID 前缀不同时附注 (来源/归属)。
func TestModelDesc(t *testing.T) {
	cases := []struct{ id, ownedBy, src, want string }{
		{"deepseek-ai/DeepSeek-V3", "deepseek-ai", "siliconflow", "(siliconflow)"},
		{"Qwen/Qwen2.5-72B-Instruct", "Qwen", "siliconflow", "(siliconflow)"},
		{"gemini-2.5-pro", "google", "siliconflow", "(siliconflow/google)"}, // 归属前缀不同 → 附注
		{"deepseek-ai/DeepSeek-V3", "", "siliconflow", "(siliconflow)"},     // OwnedBy 空
	}
	for _, c := range cases {
		if got := modelDesc(c.id, c.ownedBy, c.src); got != c.want {
			t.Fatalf("modelDesc(%q,%q,%q) = %q, want %q", c.id, c.ownedBy, c.src, got, c.want)
		}
	}
}

// TestProviderShortFromURL URL → 来源短名(api.siliconflow.cn → siliconflow)。
func TestProviderShortFromURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://api.siliconflow.cn/v1", "siliconflow"},
		{"https://api.deepseek.com/v1", "deepseek"},
		{"http://localhost:11434/v1", "localhost"},
		{"https://api.anthropic.com/v1", "anthropic"},
		{"bad-url", "bad-url"}, // 非法 URL:回退原串
	}
	for _, c := range cases {
		if got := providerShortFromURL(c.in); got != c.want {
			t.Fatalf("providerShortFromURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestNextThinking 思考等级循环:前进 off→low→medium→high→off,后退反向。
func TestNextThinking(t *testing.T) {
	cases := []struct {
		cur  sdk.ThinkingLevel
		dir  int
		want string
	}{
		{sdk.ThinkingOff, 1, "low"},
		{sdk.ThinkingLow, 1, "medium"},
		{sdk.ThinkingMedium, 1, "high"},
		{sdk.ThinkingHigh, 1, "off"}, // 回环
		{sdk.ThinkingLow, -1, "off"}, // 后退
		{sdk.ThinkingOff, -1, "high"},
	}
	for _, c := range cases {
		got := nextThinking(c.cur, c.dir).String()
		if got != c.want {
			t.Fatalf("nextThinking(%v,%d) = %q, want %q", c.cur, c.dir, got, c.want)
		}
	}
}
