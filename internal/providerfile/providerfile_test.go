// provider.yaml 持久化单测:0600 权限/内容往返/清空/缺文件不报错。GAH_HOME 隔离。
package providerfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadClear(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)

	p := Provider{BaseURL: "https://api.siliconflow.cn/v1", APIKey: "sk-test-123456", Model: "deepseek-ai/DeepSeek-V3"}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	path := Path()
	if !filepath.HasPrefix(path, home) {
		t.Fatalf("路径应在 GAH_HOME 下: %s", path)
	}
	// 0600 权限(凭据文件,不落其它可读位)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("provider.yaml 权限应为 0600, got %o", fi.Mode().Perm())
	}
	// 内容往返
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Fatalf("Load 往返不符: %+v vs %+v", got, p)
	}
	// 清空
	if err := Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Clear 后文件应删除: %v", err)
	}
	// 缺文件 Load 返回空 Provider 不报错
	got2, err := Load()
	if err != nil || got2 != (Provider{}) {
		t.Fatalf("缺文件应空 Provider 无错误: %+v %v", got2, err)
	}
	// 重复 Clear 幂等
	if err := Clear(); err != nil {
		t.Fatalf("重复 Clear 应幂等: %v", err)
	}
}

func TestLoadMalformed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("base_url: [broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("坏 yaml 应显式报错(不静默返回空配置)")
	}
}

func TestLoadEmptyFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil || got != (Provider{}) {
		t.Fatalf("空文件应空 Provider: %+v %v", got, err)
	}
}
