// resolveConfig 配置解析链单测:模型/base_url 恢复独立于 apiKey(重启不丢模型)。
package llmopenai

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/providerfile"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// dataManifest 构造 data 样板(对齐 bundle-base:仅 base_url)。
func dataManifest() *sdk.Manifest {
	return &sdk.Manifest{ID: "llm-openai-compat", Data: map[string]any{
		"base_url": "https://api.deepseek.com/v1",
	}}
}

// TestResolveConfigModelRestoredWithoutKey 修复:provider.yaml 仅存 model(无 key/base_url,
// 如 env 提供 key 或仅 /model 持久化)时,模型仍恢复(旧实现被 apiKey gate 吞掉)。
func TestResolveConfigModelRestoredWithoutKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	t.Setenv("DEEPSEEK_API_KEY", "") // env key 置空隔离
	// 场景:用户 /model 后 provider.yaml 只含 model(UpdateModel 仅当 provider 存在才写;
	// 此处构造仅 model 的极端持久化,验证恢复逻辑不依赖其它字段)
	pv := providerfile.Provider{Model: "deepseek-ai/DeepSeek-V3"}
	if err := providerfile.Add(pv); err != nil {
		t.Fatal(err)
	}
	base, key, mod, err := resolveConfig(dataManifest())
	if err != nil {
		t.Fatal(err)
	}
	if mod != "deepseek-ai/DeepSeek-V3" {
		t.Fatalf("模型应从 provider.yaml 恢复: %q", mod)
	}
	// base/key 不受 model 影响:base 走 data 样板,key 空(无 env/provider key)
	if base != "https://api.deepseek.com/v1" {
		t.Fatalf("base_url 应回退 data 样板: %q", base)
	}
	if key != "" {
		t.Fatalf("无 key 来源应保持空: %q", key)
	}
}

// TestResolveConfigEnvKeyWithProviderModel env 提供 key + provider.yaml 持久化 model:
// 两字段各自生效(key 取 env,model 取 provider)——旧实现 gate 导致 env key 时 model 丢。
func TestResolveConfigEnvKeyWithProviderModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	t.Setenv("DEEPSEEK_API_KEY", "sk-env-123456")
	if err := providerfile.Add(providerfile.Provider{BaseURL: "https://api.siliconflow.cn/v1", APIKey: "sk-old", Model: "deepseek-ai/DeepSeek-V3"}); err != nil {
		t.Fatal(err)
	}
	base, key, mod, err := resolveConfig(&sdk.Manifest{ID: "x", Data: nil})
	if err != nil {
		t.Fatal(err)
	}
	if key != "sk-env-123456" {
		t.Fatalf("key 应 env 显式优先: %q", key)
	}
	if mod != "deepseek-ai/DeepSeek-V3" {
		t.Fatalf("model 应从 provider 恢复(独立于 env key): %q", mod)
	}
	if base != "https://api.siliconflow.cn/v1" {
		t.Fatalf("base_url 应从 provider 恢复: %q", base)
	}
}

// TestResolveConfigPrecedence 完整优先级:env > provider > data > 默认(逐字段)。
func TestResolveConfigPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	t.Setenv("DEEPSEEK_BASE_URL", "https://env.example.com/v1")
	t.Setenv("DEEPSEEK_MODEL", "env-model")
	if err := providerfile.Add(providerfile.Provider{BaseURL: "https://pv.example.com/v1", Model: "pv-model"}); err != nil {
		t.Fatal(err)
	}
	base, _, mod, err := resolveConfig(dataManifest())
	if err != nil {
		t.Fatal(err)
	}
	if base != "https://env.example.com/v1" || mod != "env-model" {
		t.Fatalf("env 显式应覆盖 provider/data: %q %q", base, mod)
	}
}

// TestResolveConfigBadYaml 坏 provider.yaml 显式报错(不静默降级样板)。
func TestResolveConfigBadYaml(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(providerfile.Path(), []byte("base_url: [broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := resolveConfig(dataManifest()); err == nil {
		t.Fatal("坏 provider.yaml 应显式报错")
	}
}

// TestResolveConfigDefaults 无任何配置:base/data 缺省、内置默认。
func TestResolveConfigDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	for _, k := range []string{"DEEPSEEK_BASE_URL", "DEEPSEEK_API_KEY", "DEEPSEEK_MODEL", "OPENAI_BASE_URL", "OPENAI_API_KEY", "OPENAI_MODEL"} {
		t.Setenv(k, "")
	}
	base, key, mod, err := resolveConfig(nil) // 无 manifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if base != "https://api.openai.com/v1" {
		t.Fatalf("默认端点: %q", base)
	}
	if key != "" {
		t.Fatalf("默认无 key: %q", key)
	}
	if mod != "deepseek-chat" {
		t.Fatalf("默认模型: %q", mod)
	}
}
