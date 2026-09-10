// TUI 内部命令逐级确认护栏:所有多子命令命令必须声明参数级(Args),
// 参数级回调不 panic、枚举选项值非空;并锁定各命令的级联可达性
// (与 tests/commands_levels_guard_test.go 的宿主/插件命令互补)。
package tui

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestInternalCommandsArgLevelsGuard 内部命令参数级结构护栏(新增命令漏声明 Args 即失败)。
func TestInternalCommandsArgLevelsGuard(t *testing.T) {
	a := commandTestApp()
	specs := a.cmds.List()
	if len(specs) < 15 {
		t.Fatalf("内部命令注册数异常: %d", len(specs))
	}
	seen := map[string]bool{}
	for _, s := range specs {
		seen[s.Name] = true
		if strings.Contains(s.Usage, "|") && len(s.Args) == 0 {
			t.Errorf("/%s Usage %q 含多子命令但未声明参数级(无法逐级确认)", s.Name, s.Usage)
		}
		walkSpecLevels(t, s, []string{s.Name}, 0)
	}
	for _, n := range []string{
		"thinking", "model", "provider", "sandbox", "approval", "plugins",
		"settings", "export", "compact", "widgets", "reload", "search",
		"workspace", "fork", "clone", "tree", "session", "name", "theme", "help", "exit",
	} {
		if !seen[n] {
			t.Errorf("/%s 未注册(TUI 内部命令缺失)", n)
		}
	}
}

// TestInternalCommandsCascadeTargets 逐级确认可达性:各命令应能"选到"目标路径
// (子命令枚举 / 参数枚举 / 自由输入三类都有;锁定本轮补齐项防回退)。
func TestInternalCommandsCascadeTargets(t *testing.T) {
	a := commandTestApp()
	get := func(name string) sdk.CommandSpec {
		s, ok := a.cmds.Get(name)
		if !ok {
			t.Fatalf("/%s 未注册", name)
		}
		return s
	}
	optValues := func(opts []sdk.Option) map[string]bool {
		m := map[string]bool{}
		for _, o := range opts {
			m[o.Value] = true
		}
		return m
	}
	// 枚举级:thinking/sandbox/approval/widgets/theme
	for _, c := range []struct {
		name   string
		values []string
	}{
		{"thinking", []string{"off", "low", "medium", "high"}},
		{"sandbox", []string{"ro", "ws", "full"}},
		{"approval", []string{"open", "smart", "strict"}},
		{"widgets", []string{"on", "off"}},
		{"theme", []string{themeResetSentinel}},
	} {
		spec := get(c.name)
		if len(spec.Args) == 0 || spec.Args[0].Options == nil {
			t.Errorf("/%s 一级应为枚举", c.name)
			continue
		}
		vals := optValues(spec.Args[0].Options([]string{c.name}))
		for _, v := range c.values {
			if !vals[v] {
				t.Errorf("/%s 一级枚举缺 %q(现有 %v)", c.name, v, vals)
			}
		}
	}
	// 自由级:export / compact / search / name
	for _, name := range []string{"export", "compact", "search", "name"} {
		spec := get(name)
		if len(spec.Args) == 0 || spec.Args[0].FreeArgs == nil || len(spec.Args[0].FreeArgs([]string{name})) == 0 {
			t.Errorf("/%s 应声明自由参数级(选中后断点输入)", name)
		}
	}
	// 二级数字:settings history 应可选 off/unlimited/数字
	sv := optValues(get("settings").Args[1].Options([]string{"settings", "history"}))
	for _, v := range []string{"off", "unlimited", "20", "50", "100", "200"} {
		if !sv[v] {
			t.Errorf("/settings history 二级缺 %q(现有 %v)", v, sv)
		}
	}
	// fork:应有 Options 或 FreeArgs(无会话时回退手输 seq)
	f := get("fork")
	if len(f.Args) == 0 || (f.Args[0].Options == nil && f.Args[0].FreeArgs == nil) {
		t.Error("/fork 应支持逐级确认(枚举提问点或自由输入 seq)")
	}
	// session/plugins/provider/workspace/jobs 型命令:级联层级 ≥2
	for _, name := range []string{"session", "plugins", "provider", "workspace"} {
		if len(get(name).Args) < 2 {
			t.Errorf("/%s 应至少两级级联(子命令 + 参数)", name)
		}
	}
}

// walkSpecLevels 深度优先遍历参数级(≤3 级):回调不 panic、枚举选项值/自由参数名非空。
func walkSpecLevels(t *testing.T, spec sdk.CommandSpec, picked []string, depth int) {
	t.Helper()
	if depth >= len(spec.Args) || depth >= 3 {
		return
	}
	lv := spec.Args[depth]
	if lv.Options != nil {
		for _, o := range lv.Options(picked) {
			if strings.TrimSpace(o.Value) == "" {
				t.Errorf("/%s 第 %d 级枚举选项 Value 为空(选择器会写入空值)", spec.Name, depth+1)
			}
			walkSpecLevels(t, spec, append(append([]string{}, picked...), o.Value), depth+1)
		}
	}
	if lv.FreeArgs != nil {
		for _, f := range lv.FreeArgs(picked) {
			if strings.TrimSpace(f) == "" {
				t.Errorf("/%s 第 %d 级自由参数名为空", spec.Name, depth+1)
			}
		}
	}
}
