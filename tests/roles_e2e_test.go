// 角色(role)端到端:tests/ 入口矩阵新增项(第七十八批)。
//
// 覆盖四件事:
//  1. 装配真实 base 子集(host-skills → host-roles):角色服务可用、当前角色为基线(未启用);
//  2. 切换角色的**可见效果**:下一轮 Assemble 系统提示出现身份槽 + 角色工作规则,
//     同时技能索引按挂载清单收缩(read_skill 同步拒绝未挂载技能);
//  3. /role 命令(经 ctx.commands 注册表)是人的写入口:use/new/rm 走同一条服务路径,
//     且**没有**模型可调的写工具(模型不能自改人格);
//  4. 卸载语义:DisposeAll 后 ctx.roles 与身份槽一起消失(注册即副作用、卸载即撤销)。
package tests

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// writeTestSkill 写一个最小 SKILL.md。
func writeTestSkill(t *testing.T, dir, name string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "---\nname: " + name + "\ndescription: 测试技能 " + name + "\n---\n正文-" + name
	if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildRolesEnv 装配角色链最小 base(tools/systemPrompt/skills/roles + 命令注册表)。
func buildRolesEnv(t *testing.T, home string) (sdk.Ctx, *plugin.Registry) {
	t.Helper()
	t.Setenv("GAH_HOME", home)
	// 共享技能库:skill-a / skill-b(角色只挂 a)
	writeTestSkill(t, filepath.Join(home, "skills"), "skill-a")
	writeTestSkill(t, filepath.Join(home, "skills"), "skill-b")
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-commands"},
		{ID: "host-tools"},
		{ID: "host-system-prompt"},
		{ID: "host-skills"},
		{ID: "host-roles"},
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
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c, reg
}

func TestRolesEndToEnd(t *testing.T) {
	home := t.TempDir()
	c, reg := buildRolesEnv(t, home)

	var svc sdk.RoleService
	if err := c.Inject("ctx.roles", &svc); err != nil {
		t.Fatalf("ctx.roles 应由 host-roles 提供: %v", err)
	}
	if svc.Current() != "" {
		t.Fatalf("基线当前角色应为空,got %q", svc.Current())
	}
	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		t.Fatal(err)
	}
	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	tools := toolsOf(t, c)

	// 角色 + 挂载清单(显式替换默认池)
	if _, err := svc.Create(sdk.RoleSpec{
		ID: "finance", Name: "财务", Identity: "你是资深财务分析师。",
		Skills: []string{"skill-a"}, SkillsSet: true, ExcludeGlobal: true,
	}, "先确认口径,再给数字。\n"); err != nil {
		t.Fatal(err)
	}
	roleCmd, ok := cmds.Get("role")
	if !ok {
		t.Fatal("/role 命令应已注册(host-roles 自注册,无需改 TUI)")
	}
	// 切换(经命令,模拟用户输入)
	out, err := roleCmd.Run([]string{"use", "finance"})
	if err != nil {
		t.Fatalf("/role use: %v", err)
	}
	if !strings.Contains(out, "已切换到角色") {
		t.Fatalf("/role use 回执不符: %s", out)
	}
	if svc.Current() != "finance" {
		t.Fatalf("Current = %q", svc.Current())
	}

	// 下一轮系统提示:身份槽 + 工作规则 + 技能索引收缩(切换无需重启/重装)
	sys := sp.Assemble(nil, nil)[0].Content
	if !strings.Contains(sys, "当前角色:财务(finance)") || !strings.Contains(sys, "你是资深财务分析师。") ||
		!strings.Contains(sys, "先确认口径,再给数字。") {
		t.Fatalf("系统提示未含角色身份槽: %.600s", sys)
	}
	if !strings.Contains(sys, "skill-a") || strings.Contains(sys, "skill-b") {
		t.Fatalf("技能索引应按挂载收缩: %.600s", sys)
	}
	// 未挂载技能不可读(索引与可读范围一致,显式报"未挂载")
	res, err := tools.Execute(context.Background(), "read_skill", `{"name":"skill-b"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "未挂载") {
		t.Fatalf("未挂载技能应被拒: %s", res.Content)
	}
	// 只读工具自查角色
	lr, err := tools.Execute(context.Background(), "list_roles", "{}")
	if err != nil || !strings.Contains(lr.Content, `"current":"finance"`) {
		t.Fatalf("list_roles: %v %s", err, lr.Content)
	}
	// 模型不得有写工具:角色写操作只存在于 /role 命令与 Web 面板(防不可信内容提权)
	for _, name := range []string{"switch_role", "write_role", "create_role", "delete_role"} {
		if def, ok := tools.Get(name); ok {
			t.Fatalf("不应存在模型可调的角色写工具 %s: %+v", name, def)
		}
	}

	// 新建 + 删除(写路径经命令;删除进回收站可恢复)
	if _, err := roleCmd.Run([]string{"new", "temp-role"}); err != nil {
		t.Fatalf("/role new: %v", err)
	}
	if _, ok := svc.Get("temp-role"); !ok {
		t.Fatal("新建角色应可读")
	}
	if _, err := roleCmd.Run([]string{"rm", "temp-role"}); err != nil {
		t.Fatalf("/role rm: %v", err)
	}
	if _, ok := svc.Get("temp-role"); ok {
		t.Fatal("删除后不应仍存在")
	}
	if _, err := os.Stat(filepath.Join(home, "roles", ".trash")); err != nil {
		t.Fatalf("删除应移入回收站: %v", err)
	}

	// 卸载:ctx.roles 与身份槽一起消失(注册即副作用、卸载即撤销)
	reg.DisposeAll()
	var gone sdk.RoleService
	if err := c.Inject("ctx.roles", &gone); err == nil {
		t.Fatal("卸载后 ctx.roles 应不可注入")
	}
	if strings.Contains(sp.Assemble(nil, nil)[0].Content, "当前角色:") {
		t.Fatal("卸载后系统提示不应再有身份槽")
	}
}
