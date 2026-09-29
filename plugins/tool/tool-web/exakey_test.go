// Exa key 配置链单测:env 优先 / 文件兜底 / 缺文件不报错 / GAH_HOME 隔离 / 坏 yaml 显式报错。
package toolweb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSearchConfig 在隔离 GAH_HOME 下写 search.yaml。
func writeSearchConfig(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	dir := filepath.Join(home, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "search.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestExaKeyEnvFirst env 优先于配置文件。
func TestExaKeyEnvFirst(t *testing.T) {
	writeSearchConfig(t, "api_key: file-key\n")
	t.Setenv("EXA_API_KEY", "env-key")
	k, err := resolveExaKey()
	if err != nil {
		t.Fatal(err)
	}
	if k != "env-key" {
		t.Fatalf("env 应优先: %q", k)
	}
}

// TestExaKeyFromFile 无 env → search.yaml api_key 兜底。
func TestExaKeyFromFile(t *testing.T) {
	writeSearchConfig(t, "api_key: file-key\n")
	t.Setenv("EXA_API_KEY", "")
	k, err := resolveExaKey()
	if err != nil {
		t.Fatal(err)
	}
	if k != "file-key" {
		t.Fatalf("文件应兜底: %q", k)
	}
}

// TestExaKeyMissingFile 无 env 无文件 → 空 key,不报错(Search 侧归一 401 提示)。
func TestExaKeyMissingFile(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	t.Setenv("EXA_API_KEY", "")
	k, err := resolveExaKey()
	if err != nil {
		t.Fatal(err)
	}
	if k != "" {
		t.Fatalf("缺文件应为空: %q", k)
	}
}

// TestExaKeyBadYAML 坏 yaml → 显式错误,不静默降级。
func TestExaKeyBadYAML(t *testing.T) {
	writeSearchConfig(t, "api_key: [unclosed\n")
	t.Setenv("EXA_API_KEY", "")
	if _, err := resolveExaKey(); err == nil {
		t.Fatal("坏 yaml 应显式报错")
	}
}

// TestExaKeyGAHHOMEIsolation 只读 GAH_HOME 下文件;外部文件不影响。
func TestExaKeyGAHHOMEIsolation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	t.Setenv("EXA_API_KEY", "")
	// 在另一个位置放 key 文件,不应被读到
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "config", "search.yaml"), []byte("api_key: other-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := resolveExaKey()
	if err != nil {
		t.Fatal(err)
	}
	if k != "" {
		t.Fatalf("非 GAH_HOME 下文件不应读取: %q", k)
	}
	if got := searchConfigPath(); !strings.HasPrefix(got, home) {
		t.Fatalf("路径应在 GAH_HOME 下: %s", got)
	}
}

// TestFileProviderFallback search.yaml provider 字段兜底;空 = 未声明。
func TestFileProviderFallback(t *testing.T) {
	writeSearchConfig(t, "provider: exa\napi_key: k\n")
	if got := resolveFileProvider(); got != "exa" {
		t.Fatalf("文件 provider 应兜底: %q", got)
	}
	t.Setenv("GAH_HOME", t.TempDir())
	if got := resolveFileProvider(); got != "" {
		t.Fatalf("无文件应为空: %q", got)
	}
}

// TestExaKeyBadYAMLSearchFails 坏 yaml 时 Search 显式失败(不产生 401 误导)。
func TestExaKeyBadYAMLSearchFails(t *testing.T) {
	writeSearchConfig(t, "api_key: [unclosed\n")
	t.Setenv("EXA_API_KEY", "")
	p := NewExaProvider(nil)
	if p.err == nil {
		t.Fatal("坏 yaml 应记录配置错误")
	}
	if _, err := p.Search(context.TODO(), "x", 1); err == nil {
		t.Fatal("Search 应显式失败")
	}
}

// TestEndpointAndProviderFromEnv 第八十五批:端点/provider 也有 env 通道
// (宿主按 Capabilities.ConfigEnv 注入;默认形态下插件进程读不到配置文件)。
func TestEndpointAndProviderFromEnv(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir()) // 无文件
	t.Setenv("GAH_SEARCH_ENDPOINT", "http://127.0.0.1:8787")
	t.Setenv("GAH_SEARCH_PROVIDER", "exa")
	if got := resolveFileEndpoint(); got != "http://127.0.0.1:8787" {
		t.Fatalf("端点应取 env: %q", got)
	}
	if got := resolveFileProvider(); got != "exa" {
		t.Fatalf("provider 应取 env: %q", got)
	}
	p := NewExaProvider(nil)
	if p.endpoint != "http://127.0.0.1:8787" || p.err != nil {
		t.Fatalf("provider 端点应来自 env:endpoint=%q err=%v", p.endpoint, p.err)
	}
	if p.apiKey != "" {
		t.Fatalf("未配 key 应为空(无鉴权头): %q", p.apiKey)
	}
}

// TestEnvKeyToleratesDeniedFile env 命中时"配置文件读不到"不致命
// (macOS 默认沙箱档下插件读 config/ 会被内核拒;宿主已按声明把值注入 env)。
func TestEnvKeyToleratesDeniedFile(t *testing.T) {
	writeSearchConfig(t, "api_key: [坏\n") // 模拟读得到的坏文件:同一条错误路径
	t.Setenv("EXA_API_KEY", "env-key")
	p := NewExaProvider(nil)
	if p.err != nil {
		t.Fatalf("env 命中时不应记录配置错误: %v", p.err)
	}
	if p.apiKey != "env-key" {
		t.Fatalf("key 应取 env: %q", p.apiKey)
	}
}

// TestNewSearchToolFromEnv 外部插件构造入口:缺省 exa;未知名不拖垮同进程其它工具。
func TestNewSearchToolFromEnv(t *testing.T) {
	t.Run("缺省 exa", func(t *testing.T) {
		t.Setenv("GAH_HOME", t.TempDir())
		if got := NewSearchToolFromEnv(nil).Definition().Name; got != "web_search" {
			t.Fatalf("工具名应为 web_search: %q", got)
		}
	})
	t.Run("未知名 provider 调用即显式报错", func(t *testing.T) {
		t.Setenv("GAH_HOME", t.TempDir())
		t.Setenv("GAH_SEARCH_PROVIDER", "not-a-provider")
		tool := NewSearchToolFromEnv(nil)
		out, err := tool.Execute(context.TODO(), `{"query":"x"}`)
		if err != nil {
			t.Fatalf("Execute 不应返回 err(错误走结果字段): %v", err)
		}
		m, ok := out.(map[string]any)
		if !ok || !strings.Contains(m["error"].(string), "未知搜索 provider") {
			t.Fatalf("应显式报未知名 provider,got %#v", out)
		}
	})
}
