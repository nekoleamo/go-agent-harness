// 角色包端到端(第九十三批):导出的包**换一台机器**导得回来,而且导回来就能用。
//
// 单测/端口测试各自只盯一层,这里串起真实链路:命令面(/role export|import)→ internal/rolepack
// → internal/roles + internal/skills 落盘 → host-roles 服务的索引重载 → 下一轮系统提示
// 真的带上了这个角色的身份与规则(否则"导入成功"只是个文件操作,不是"角色能用了")。
package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/rolepack"
	"github.com/nekoleamo/go-agent-harness/internal/roles"
	"github.com/nekoleamo/go-agent-harness/internal/skills"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestRolePackEndToEnd(t *testing.T) {
	home := t.TempDir()
	c, _ := buildRolesEnv(t, home)
	var svc sdk.RoleService
	if err := c.Inject("ctx.roles", &svc); err != nil {
		t.Fatal(err)
	}
	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		t.Fatal(err)
	}
	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	roleCmd, ok := cmds.Get("role")
	if !ok {
		t.Fatal("/role 命令应已注册")
	}

	// 造一个"该有的都有"的角色:定义(含收紧档/模型/工具排除)+ 工作规则 + 私有技能。
	spec := sdk.RoleSpec{
		ID: "finance", Name: "财务分析师", Description: "记账与报表", Identity: "你是资深财务分析师。",
		ExcludeGlobal: true, Skills: []string{"skill-a"}, SkillsSet: true,
		Model: "role-model", Thinking: "low", ToolsExclude: []string{"shell"},
		Approval: "strict", Sandbox: "read-only",
	}
	if _, err := svc.Create(spec, "先确认口径,再给数字。\n"); err != nil {
		t.Fatal(err)
	}
	tax := "---\nname: tax\ndescription: 税务口径\n---\n\n增值税按季申报。\n"
	if err := skills.ForRole("finance").Write("tax", tax, false); err != nil {
		t.Fatal(err)
	}

	// —— 导出(命令面:与用户在 TUI 里敲的是同一条)——
	pack := filepath.Join(home, "share", "gah-role-finance.zip")
	if err := os.MkdirAll(filepath.Dir(pack), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := roleCmd.Run([]string{"export", "finance", pack})
	if err != nil {
		t.Fatalf("/role export: %v", err)
	}
	for _, want := range []string{"已导出角色", "财务分析师", "1 个私有技能"} {
		if !strings.Contains(out, want) {
			t.Fatalf("导出回执缺 %q:\n%s", want, out)
		}
	}
	raw, err := os.ReadFile(pack)
	if err != nil {
		t.Fatalf("导出的包应落盘: %v", err)
	}
	if man, err := rolepack.Inspect(raw); err != nil || man.ID != "finance" {
		t.Fatalf("导出的包应可被认出:%+v %v", man, err)
	}
	// 再导一次到同一个路径:允许(覆盖的是自己的包),但不许覆盖别人的文件
	if _, err := roleCmd.Run([]string{"export", "finance", pack}); err != nil {
		t.Fatalf("重导出(覆盖自己的包)应允许: %v", err)
	}
	other := filepath.Join(home, "share", "note.txt")
	if err := os.WriteFile(other, []byte("我的笔记"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := roleCmd.Run([]string{"export", "finance", other}); err == nil ||
		!strings.Contains(err.Error(), "不是角色包") {
		t.Fatalf("不该覆盖非角色包文件: %v", err)
	}
	if b, _ := os.ReadFile(other); string(b) != "我的笔记" {
		t.Fatalf("非角色包文件被改了:%q", b)
	}

	// —— 模拟"换一台机器":删掉角色(含私有技能)后导入 ——
	if _, err := roleCmd.Run([]string{"rm", "finance"}); err != nil {
		t.Fatalf("/role rm: %v", err)
	}
	imp, err := roleCmd.Run([]string{"import", pack})
	if err != nil {
		t.Fatalf("/role import: %v", err)
	}
	if !strings.Contains(imp, "已导入角色 财务分析师(finance)") || !strings.Contains(imp, "tax") {
		t.Fatalf("导入回执不符:\n%s", imp)
	}
	// 定义逐字段回来(收紧档/模型/工具排除都在 —— 少一个就是"分享出去少了一半角色")
	got, ok := svc.Get("finance")
	if !ok {
		t.Fatal("导入后角色索引应已重载(命令里 Reload)")
	}
	if got.Name != "财务分析师" || got.Identity != spec.Identity || got.Model != "role-model" ||
		got.Thinking != "low" || got.Approval != "strict" || got.Sandbox != "read-only" ||
		!got.ExcludeGlobal || len(got.ToolsExclude) != 1 || got.ToolsExclude[0] != "shell" ||
		got.AGENTS != "先确认口径,再给数字。\n" {
		t.Fatalf("导入后的定义与导出前不一致:\n%+v", got)
	}
	if body, err := skills.ForRole("finance").Read("tax"); err != nil || body != tax {
		t.Fatalf("私有技能应逐字回来:%q %v", body, err)
	}

	// —— 导回来就能用:切过去后下一轮系统提示带上身份 + 规则 + 收紧档 —
	if _, err := roleCmd.Run([]string{"use", "finance"}); err != nil {
		t.Fatalf("/role use: %v", err)
	}
	sys := sp.Assemble(nil, nil)[0].Content
	if !strings.Contains(sys, "当前角色:财务分析师(finance)") ||
		!strings.Contains(sys, "你是资深财务分析师。") ||
		!strings.Contains(sys, "先确认口径,再给数字。") {
		t.Fatalf("导入的角色未能进入系统提示: %.600s", sys)
	}

	// —— 覆盖语义:同名默认拒绝,force 才覆盖且旧份进回收站 ——
	if _, err := roleCmd.Run([]string{"import", pack}); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("同名目标应显式拒绝(当前角色更该拒): %v", err)
	}
	// 当前角色不可覆盖:即便加了 force 也要拒(否则下一轮提示会静默少一层指令)
	if _, err := roleCmd.Run([]string{"import", pack, "force"}); err == nil || !strings.Contains(err.Error(), "正在使用中") {
		t.Fatalf("当前角色应拒绝覆盖: %v", err)
	}
	// 切回基线后再覆盖:旧份进回收站
	if _, err := roleCmd.Run([]string{"none"}); err != nil {
		t.Fatalf("/role none: %v", err)
	}
	out, err = roleCmd.Run([]string{"import", pack, "force"})
	if err != nil {
		t.Fatalf("force 覆盖: %v", err)
	}
	if !strings.Contains(out, "覆盖了同名角色") || !strings.Contains(out, ".trash") {
		t.Fatalf("覆盖回执要说清旧份进回收站:\n%s", out)
	}
	// 回收站里应有两条:前面 /role rm 的那份 + 这次覆盖备份的那份(都在,谁都没被顶掉)
	if list := (roles.Store{}).TrashList(); len(list) != 2 || list[0].ID != "finance" || list[1].ID != "finance" {
		t.Fatalf("回收站里应有 rm 与覆盖各一份:%+v", list)
	}
	// —— 并存:as 另起一个 ID(分享包与本地同名角色共存的唯一正路)——
	if _, err := roleCmd.Run([]string{"import", pack, "as", "finance-copy"}); err != nil {
		t.Fatalf("as 导入: %v", err)
	}
	if _, ok := svc.Get("finance-copy"); !ok {
		t.Fatal("as 导入的角色应可读")
	}
	if body, err := skills.ForRole("finance-copy").Read("tax"); err != nil || body != tax {
		t.Fatalf("as 导入也要带私有技能:%q %v", body, err)
	}

	// —— 命令面的拒绝路径:坏参数要说清怎么用,不能静默当成成功 ——
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"export"}, "/role export <id>"},
		{[]string{"export", "ghost"}, "角色不存在"},
		{[]string{"import"}, "/role import <角色包路径>"},
		{[]string{"import", filepath.Join(home, "nope.zip")}, "读取失败"},
		{[]string{"import", pack, "as"}, "as 后面要跟目标 ID"},
		{[]string{"import", pack, "--force"}, "不认识的参数"},
	} {
		_, err := roleCmd.Run(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v 应报 %q,得到 %v", tc.args, tc.want, err)
		}
	}
}
