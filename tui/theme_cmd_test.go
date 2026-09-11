// /theme 命令链路测试(M13 主题外部化):注册 + 一级枚举 Args(主题列表+default 哨兵)、
// 运行期切换(config/themes/<名>.yaml)、default 恢复启动活动覆盖链(data.palette+theme.yaml)、
// 指定缺失主题显式报错。启动加载链优先级:默认表 < data.palette < theme.yaml。
package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// themeTestApp 装配最小 App(同 search_cmd_test 模式)+ 注入启动 palette 覆盖。
func themeTestApp(t *testing.T, palette map[string]string) *App {
	t.Helper()
	reg := newMemRegistry()
	c := &stubCtx{svc: map[string]any{"ctx.commands": reg}}
	return NewApp(c, stubLoop{}, stubLLM{}, "tui", palette)
}

func TestThemeCommandRegistered(t *testing.T) {
	defer ResetTheme()
	a := themeTestApp(t, nil)
	spec, ok := a.cmds.Get("theme")
	if !ok {
		t.Fatal("/theme 应已注册到 ctx.commands")
	}
	if len(spec.Args) != 1 || spec.Args[0].Options == nil {
		t.Fatal("/theme 应声明一级枚举 Args(主题列表 + default 哨兵)")
	}
	opts := spec.Args[0].Options(nil)
	if len(opts) < 1 || opts[0].Value != themeResetSentinel {
		t.Fatalf("枚举首项应为 default 哨兵: %v", opts)
	}
}

func TestThemeCommandSwitchAndReset(t *testing.T) {
	defer ResetTheme()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	// 启动链:data.palette(user=196) + theme.yaml(user=120,优先级更高)
	cfg := filepath.Join(home, "config")
	if err := os.MkdirAll(filepath.Join(cfg, "themes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "theme.yaml"), []byte("user: \"120\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "themes", "dark.yaml"), []byte("user: \"16\"\nassistant: \"231\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := themeTestApp(t, map[string]string{"user": "196"})
	// 启动链生效:theme.yaml(120) 覆盖 data.palette(196)
	if got := colorVal(TokUser); got != "120" {
		t.Fatalf("启动链 theme.yaml 应覆盖 data.palette: TokUser = %q,want 120", got)
	}

	// 运行期切换 dark 主题
	out, err := a.cmdTheme([]string{"dark"})
	if err != nil {
		t.Fatalf("cmdTheme dark: %v", err)
	}
	if got := colorVal(TokUser); got != "16" {
		t.Errorf("切换后 TokUser = %q,want 16", got)
	}
	if got := colorVal(TokAssistant); got != "231" {
		t.Errorf("切换后 TokAssistant = %q,want 231(部分覆盖另一 token)", got)
	}
	if out == "" {
		t.Error("切换应返回提示文本")
	}

	// default 恢复启动活动覆盖链
	if _, err := a.cmdTheme([]string{themeResetSentinel}); err != nil {
		t.Fatalf("cmdTheme default: %v", err)
	}
	if got := colorVal(TokUser); got != "120" {
		t.Errorf("default 应恢复启动链: TokUser = %q,want 120", got)
	}
}

func TestThemeCommandMissingAndInvalid(t *testing.T) {
	defer ResetTheme()
	t.Setenv("GAH_HOME", t.TempDir())
	a := themeTestApp(t, nil)
	// 指定缺失主题显式报错
	if _, err := a.cmdTheme([]string{"nope"}); err == nil {
		t.Error("缺失主题应显式报错(不静默)")
	}
	// 非法名拒绝
	for _, bad := range []string{"../x", "a/b"} {
		if _, err := a.cmdTheme([]string{bad}); err == nil {
			t.Errorf("非法主题名 %q 应报错", bad)
		}
	}
	// 无参提示用法
	if _, err := a.cmdTheme(nil); err == nil {
		t.Error("无参应提示用法")
	}
}

func TestThemeStartupBrokenYamlReported(t *testing.T) {
	defer ResetTheme()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	cfg := filepath.Join(home, "config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "theme.yaml"), []byte("user: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := themeTestApp(t, nil)
	// 坏 theme.yaml:显式错误行(不静默),渲染回落默认表
	found := false
	for _, l := range a.model.state.Lines {
		if l.Kind == "error" && l.Text != "" {
			found = true
		}
	}
	if !found {
		t.Error("坏 theme.yaml 应在启动时记录显式错误行")
	}
	if got := colorVal(TokUser); got != DefaultPalette[TokUser] {
		t.Errorf("坏主题应回落默认表: TokUser = %q", got)
	}
}

// TestThemeAffectsRenderedStyles 回归:/theme 必须改变**渲染输出**,而不只是
// active map —— 旧实现把 fg(...) 固化进包级 lipgloss.Style(init 期),渲染
// 时用的是旧色,而测试只断言 colorVal 所以永远绿。
func TestThemeAffectsRenderedStyles(t *testing.T) {
	defer ResetTheme()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	cfg := filepath.Join(home, "config")
	if err := os.MkdirAll(filepath.Join(cfg, "themes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "themes", "dark.yaml"), []byte("user: \"16\"\nassistant: \"231\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := themeTestApp(t, nil)
	if _, err := a.cmdTheme([]string{"dark"}); err != nil {
		t.Fatalf("cmdTheme dark: %v", err)
	}
	user := colorVal(TokUser)
	got := styleForKind("user").Render("x")
	if !strings.Contains(got, user) {
		t.Fatalf("用户行样式未随主题更新: 色值 %q 未出现在渲染输出 %q", user, got)
	}
	// 与另一 token 交叉验证:两者色值不同,渲染必须各自命中
	asst := colorVal(TokAssistant)
	if got := styleForKind("assistant").Render("x"); !strings.Contains(got, asst) {
		t.Fatalf("助手行样式未随主题更新: 色值 %q 未出现在渲染输出 %q", asst, got)
	}
}

// TestWrapSegmentExpandsTabs 制表符按 8 列 tab stop 展开为空格,不再被丢弃
// (丢弃会让代码/补丁缩进全失,整行仅含 \t 时物理行直接消失)。
func TestWrapSegmentExpandsTabs(t *testing.T) {
	if got := wrapSegment("\tx", 20); len(got) != 1 || got[0] != "        x" {
		t.Fatalf("制表符应按 tab stop 展开: %q", got)
	}
	if got := wrapSegment("ab\tc", 20); len(got) != 1 || got[0] != "ab      c" {
		t.Fatalf("制表符应对齐到下一个 8 列: %q", got)
	}
	if got := wrapSegment("\t", 20); len(got) != 1 || got[0] != "        " {
		t.Fatalf("整行仅制表符不应整行丢失: %q", got)
	}
}
