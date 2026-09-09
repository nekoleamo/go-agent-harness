// M13 主题文件层测试:theme.yaml/themes/<名>.yaml 加载、缺文件语义、坏 yaml 显式报错、
// 非法主题名防护、列表排序。路径经 GAH_HOME 注入(对齐 exakey.go search.yaml 先例)。
package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTheme(t *testing.T, rel, content string) {
	t.Helper()
	home := t.TempDir()
	p := filepath.Join(home, "config", rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAH_HOME", home)
}

func TestThemeMainLoad(t *testing.T) {
	// 缺文件 = 无覆盖,不报错
	t.Setenv("GAH_HOME", t.TempDir())
	over, err := loadThemeMain()
	if err != nil || over != nil {
		t.Fatalf("缺文件应返回 nil,nil: over=%v err=%v", over, err)
	}

	// 空文件 = 空覆盖
	writeTheme(t, "theme.yaml", "")
	over, err = loadThemeMain()
	if err != nil || len(over) != 0 {
		t.Fatalf("空文件应返回空覆盖: over=%v err=%v", over, err)
	}

	// 正常覆盖
	writeTheme(t, "theme.yaml", "user: \"196\"\nstatus: \"240\"\n")
	over, err = loadThemeMain()
	if err != nil {
		t.Fatalf("loadThemeMain: %v", err)
	}
	if over["user"] != "196" || over["status"] != "240" {
		t.Errorf("覆盖解析错误: %v", over)
	}

	// 坏 yaml 显式报错(不静默)
	writeTheme(t, "theme.yaml", "user: [unclosed\n")
	if _, err := loadThemeMain(); err == nil {
		t.Error("坏 yaml 应显式报错")
	}
}

func TestThemeNamedLoad(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	// 缺文件显式报错(用户明确指定)
	if _, err := loadThemeNamed("dark"); err == nil {
		t.Error("指定主题缺文件应报错")
	}
	// 非法名(路径穿越/点开头)拒绝
	for _, bad := range []string{"", "a/b", "..", ".hidden", `a\b`} {
		if _, err := loadThemeNamed(bad); err == nil {
			t.Errorf("非法主题名 %q 应拒绝", bad)
		}
	}
	// 正常切换
	writeTheme(t, "themes/dark.yaml", "user: \"16\"\n")
	over, err := loadThemeNamed("dark")
	if err != nil {
		t.Fatalf("loadThemeNamed: %v", err)
	}
	if over["user"] != "16" {
		t.Errorf("主题覆盖解析错误: %v", over)
	}
}

func TestListThemes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if got := listThemes(); len(got) != 0 {
		t.Errorf("空目录应无主题: %v", got)
	}
	tdir := filepath.Join(home, "config", "themes")
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"b.yaml", "a.yaml", ".hidden.yaml"} {
		if err := os.WriteFile(filepath.Join(tdir, n), []byte("user: \"1\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := listThemes()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("列表应排序且排除隐藏文件: %v", got)
	}
}

// TestThemeOptions 枚举含 default 哨兵 + 主题列表(default 恒在首位)。
func TestThemeOptions(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	opts := themeOptions(nil)
	if len(opts) != 1 || opts[0].Value != themeResetSentinel {
		t.Fatalf("无主题时仅 default 哨兵: %v", opts)
	}
	writeTheme(t, "themes/ocean.yaml", "user: \"1\"\n")
	opts = themeOptions(nil)
	if len(opts) != 2 || opts[0].Value != themeResetSentinel || opts[1].Value != "ocean" {
		t.Errorf("默认哨兵+主题列表: %v", opts)
	}
}
