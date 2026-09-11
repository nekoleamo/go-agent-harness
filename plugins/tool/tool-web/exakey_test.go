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
