// 运行期 provider 配置必须落到「真正服务当前模型的适配器」上。
// 2026-09-21 修:此前一律配通用适配器 → claude 走 anthropic 适配器、base_url 却配在 openai 适配器上
// (运行期切换对 claude 模型静默失效:请求仍打静态端点)。
package hostllm

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestProviderConfigRoutesToMatchingAdapter(t *testing.T) {
	s := &Service{adapters: map[string]sdk.LLMAdapter{}, order: []string{"claude", "generic"}, model: "claude-sonnet-4-5"}
	cl := &claudeAdapter{providerAdapter: providerAdapter{name: "claude", baseURL: "https://api.anthropic.com/v1"}}
	gen := &providerAdapter{name: "generic", baseURL: "https://api.deepseek.com/v1"}
	s.adapters["claude"], s.adapters["generic"] = cl, gen

	if err := s.SetProvider("https://proxy.example.com/anthropic", "sk-ant"); err != nil {
		t.Fatal(err)
	}
	if cl.configuredURL != "https://proxy.example.com/anthropic" || cl.configuredKey != "sk-ant" {
		t.Errorf("claude 模型应配到 claude 适配器,得 %q/%q", cl.configuredURL, cl.configuredKey)
	}
	if gen.configuredURL != "" {
		t.Error("通用适配器不该被触碰(配错适配器 = 切了等于没切)")
	}

	// 命中适配器不支持运行时配置 → 显式报错,不偷偷配到别的适配器上
	s2 := &Service{adapters: map[string]sdk.LLMAdapter{}, order: []string{"claude", "generic"}, model: "claude-sonnet-4-5"}
	s2.adapters["claude"] = &msgAdapter{name: "claude"}
	s2.adapters["generic"] = &providerAdapter{name: "generic"}
	if err := s2.SetProvider("https://x.example.com/v1", "k"); err == nil ||
		!strings.Contains(err.Error(), "不支持运行时 provider 配置") {
		t.Errorf("命中适配器不可配置时应显式报错,得 %v", err)
	}

	// 非 claude 模型 → 回落通用适配器(既有语义不变)
	s.model = "deepseek-chat"
	if err := s.SetProvider("https://api.siliconflow.cn/v1", "sk-2"); err != nil {
		t.Fatal(err)
	}
	if gen.configuredURL != "https://api.siliconflow.cn/v1" {
		t.Errorf("非 claude 模型应回落通用适配器,得 %q", gen.configuredURL)
	}
}
