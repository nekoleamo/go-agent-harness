// 命令面逐级确认护栏(宿主/插件命令):所有多子命令命令必须声明参数级(Args),
// 参数级回调不得 panic、枚举选项值不得为空;并锁定关键命令的级联可达性。
// 与 tui/commands_levels_guard_test.go(TUI 内部命令)互补。
package tests

import (
	"log/slog"
	"strings"
	"testing"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	imqqb "github.com/nekoleamo/go-agent-harness/bundles/im-qq"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildGuardCtx 装配 base + internal-commands/jobs/backup + im-qq(命令面最全的常规组合;
// 不启动回合,仅注册命令)。
func buildGuardCtx(t *testing.T) sdk.Ctx {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-cwd-sessions"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-commands"},
		{ID: "host-system-prompt"},
		{ID: "host-plugin-manager"},
		{ID: "host-internal-commands"},
		{ID: "host-jobs"},
		{ID: "host-backup"},
		{ID: "llm-mock", Data: map[string]any{"script": []any{map[string]any{"text": "ok", "finish": "stop"}}}},
		{ID: "host-agent-loop"},
		{ID: "ui-im-qq", Data: map[string]any{"mode": "allowlist", "base_url": "http://127.0.0.1:1", "token_url": "http://127.0.0.1:1/token"}},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := baseb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := imqqb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c
}

// TestHostCommandsArgLevelsGuard 宿主命令逐级确认护栏。
func TestHostCommandsArgLevelsGuard(t *testing.T) {
	c := buildGuardCtx(t)
	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	specs := cmds.List()
	if len(specs) < 10 {
		t.Fatalf("命令注册数异常: %d", len(specs))
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
		"settings", "export", "compact", "workspace", "session", "reload",
		"jobs", "backup", "qq", "im", "stop",
	} {
		if !seen[n] {
			t.Errorf("命令 /%s 未注册(装配缺失?)", n)
		}
	}
	// 级联回归断言:各命令应能"选到"目标路径
	must := func(name string, ok bool, msg string) {
		if !ok {
			t.Errorf("/%s: %s", name, msg)
		}
	}
	jobs := specOf(specs, "jobs")
	must("jobs", len(jobs.Args) == 2 && len(jobs.Args[0].Options([]string{"jobs"})) == 3, "一级应有 list/output/kill 枚举")
	backup := specOf(specs, "backup")
	lv1 := backup.Args[0].Options([]string{"backup"})
	hasNow, hasCustom := false, false
	for _, o := range lv1 {
		if o.Value == "__now__" {
			hasNow = true
		}
		if o.Value == "__custom__" {
			hasCustom = true
		}
	}
	must("backup", hasNow, "一级应含「立即备份」项(否则 /backup 无法只选一次即备份)")
	must("backup", hasCustom, "一级应含「自定义路径」哨兵")
	must("backup", len(backup.Args[1].FreeArgs([]string{"backup", "__custom__"})) == 1, "自定义路径应自由输入")
	settings := specOf(specs, "settings")
	svals := map[string]bool{}
	for _, o := range settings.Args[1].Options([]string{"settings", "history"}) {
		svals[o.Value] = true
	}
	must("settings", svals["off"] && svals["unlimited"] && svals["50"], "二级应含 off/unlimited/数字(逐级选条数)")
	qq := specOf(specs, "qq")
	qvals := map[string]bool{}
	for _, o := range qq.Args[1].Options([]string{"qq", "env"}) {
		qvals[o.Value] = true
	}
	must("qq", qvals["official"] && qvals["sandbox"], "env 应出 official/sandbox 枚举")
	wechat := specOf(specs, "wechat")
	if wechat.Name != "" {
		must("wechat", len(wechat.Args) >= 1 && len(wechat.Args[0].Options([]string{wechat.Name})) >= 2, "一级应有 login/status 枚举")
	}
}

// specOf 按名取命令(缺省返回零值)。
func specOf(specs []sdk.CommandSpec, name string) sdk.CommandSpec {
	for _, s := range specs {
		if s.Name == name {
			return s
		}
	}
	return sdk.CommandSpec{}
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
