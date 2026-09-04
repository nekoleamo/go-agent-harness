// SetProvider 运行时切换测试:命中通用适配器(非 ModelRouter),claude-* 前缀路由不受影响。
package hostllm

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// providerAdapter 通用适配器(fake):支持 Configure/ProviderInfo。
type providerAdapter struct {
	name          string
	baseURL, key  string
	configuredURL string
	configuredKey string
}

func (a *providerAdapter) Name() string { return a.name }
func (a *providerAdapter) Complete(ctx context.Context, req *sdk.LLMRequest, onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	return &sdk.LLMResponse{}, nil
}
func (a *providerAdapter) Configure(baseURL, apiKey string) error {
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return fmt.Errorf("base_url 须 http(s) 前缀")
	}
	a.configuredURL, a.configuredKey = baseURL, apiKey
	return nil
}
func (a *providerAdapter) ProviderInfo() (string, string) { return a.baseURL, a.key }

// claudeAdapter 前缀路由适配器:声明 claude-*,不应被 SetProvider 触碰。
type claudeAdapter struct{ providerAdapter }

func (a *claudeAdapter) Models() []string { return []string{"claude"} }

func TestSetProviderHitsGeneric(t *testing.T) {
	s := &Service{adapters: map[string]sdk.LLMAdapter{}, order: []string{"claude", "generic"}}
	cl := &claudeAdapter{providerAdapter: providerAdapter{name: "claude", baseURL: "https://api.anthropic.com/v1"}}
	gen := &providerAdapter{name: "generic", baseURL: "https://api.deepseek.com/v1"}
	s.adapters["claude"] = cl
	s.adapters["generic"] = gen

	if err := s.SetProvider("https://api.siliconflow.cn/v1", "sk-test"); err != nil {
		t.Fatal(err)
	}
	// 通用适配器被配置
	if gen.configuredURL != "https://api.siliconflow.cn/v1" || gen.configuredKey != "sk-test" {
		t.Fatalf("通用适配器应被配置: %s %s", gen.configuredURL, gen.configuredKey)
	}
	// claude 前缀适配器不受影响
	if cl.configuredURL != "" {
		t.Fatalf("claude-* 路由适配器不应被 SetProvider 触碰: %s", cl.configuredURL)
	}
	// ProviderInfo 返回通用适配器
	u, k, ok := s.ProviderInfo()
	if !ok || u != "https://api.deepseek.com/v1" || k != "" {
		t.Fatalf("ProviderInfo 应取通用适配器: %s %s %v", u, k, ok)
	}
}

func TestSetProviderNoGeneric(t *testing.T) {
	s := &Service{adapters: map[string]sdk.LLMAdapter{}, order: []string{"claude"}}
	cl := &claudeAdapter{providerAdapter: providerAdapter{name: "claude"}}
	s.adapters["claude"] = cl
	if err := s.SetProvider("https://x/v1", "k"); err == nil {
		t.Fatal("仅有前缀路由适配器时应显式报错(不能静默配置错适配器)")
	}
	if _, _, ok := s.ProviderInfo(); ok {
		t.Fatal("无通用适配器时 ProviderInfo 应为 false")
	}
}

func TestSetProviderInvalidURL(t *testing.T) {
	s := &Service{adapters: map[string]sdk.LLMAdapter{}, order: []string{"generic"}}
	gen := &providerAdapter{name: "generic"}
	s.adapters["generic"] = gen
	// 适配器 Configure 校验 http(s) 前缀
	if err := s.SetProvider("api.siliconflow.cn/v1", "k"); err == nil {
		t.Fatal("非 http(s) 前缀应被校验拒绝")
	}
	if gen.configuredURL != "" {
		t.Fatalf("校验失败不应写入: %s", gen.configuredURL)
	}
}

var _ = fmt.Sprintf // 保持 fmt 引用(避免未来 lint 误删)
