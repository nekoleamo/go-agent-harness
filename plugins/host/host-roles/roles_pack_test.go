// 角色包命令面(`/role export|import`)的包内单测。
//
// 为什么要包内测一遍(第九十三批):e2e(tests/rolepack_e2e_test.go)跑的是**另一个二进制**,
// 覆盖率统计不到本包 —— 而这两条命令把"导出到哪、覆盖谁、旧份去哪"讲给了用户,
// 它们的成功回执与拒绝路径属于本包的行,必须自己钉住。
package hostroles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/rolepack"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// packHarness 造一个带命令面的角色环境,并返回 /role 命令 + 一个"该有的都有"的角色。
func packHarness(t *testing.T) (*harness, sdk.CommandSpec, string) {
	t.Helper()
	h := newHarness(t, true)
	spec, ok := h.cmds.Get("role")
	if !ok {
		t.Fatal("/role 未注册")
	}
	writeSkillDoc(t, filepath.Join(h.home, "skills"), "skill-a", "甲")
	if _, err := h.svc.Create(sdk.RoleSpec{
		ID: "finance", Name: "财务", Identity: "你是财务", ExcludeGlobal: true,
		Skills: []string{"skill-a"}, SkillsSet: true, Model: "role-model",
		ToolsExclude: []string{"shell"}, Approval: "strict",
	}, "规则正文\n"); err != nil {
		t.Fatal(err)
	}
	// 角色私有技能:导出必须连它一起带走(否则"分享出去少一半")
	writeSkillDoc(t, filepath.Join(h.home, "roles", "finance", "skills"), "tax", "税务口径")
	return h, spec, filepath.Join(h.home, "share", "gah-role-finance.zip")
}

func TestRoleExportCommand(t *testing.T) {
	_, spec, path := packHarness(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := spec.Run([]string{"export", "finance", path})
	if err != nil {
		t.Fatalf("/role export: %v", err)
	}
	for _, want := range []string{"已导出角色 财务(finance)", path, "工作规则(13 字节)", "1 个私有技能(tax)", "/role import"} {
		if !strings.Contains(out, want) {
			t.Fatalf("导出回执缺 %q:\n%s", want, out)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("包应落盘: %v", err)
	}
	if man, err := rolepack.Inspect(raw); err != nil || man.ID != "finance" || man.ExportedAt == "" {
		t.Fatalf("包应可被认出:%+v %v", man, err)
	}

	// 缺参数 / 不存在的角色
	if _, err := spec.Run([]string{"export"}); err == nil || !strings.Contains(err.Error(), "/role export <id> [路径]") {
		t.Fatalf("缺 id 应给出用法: %v", err)
	}
	if _, err := spec.Run([]string{"export", "ghost", path}); err == nil || !strings.Contains(err.Error(), "角色不存在") {
		t.Fatalf("不存在的角色应拒绝: %v", err)
	}

	// 默认落点:没给路径时落在工作区(文件名与 Web 下载同口径)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(cwd, rolepack.FileName("finance"))) })
	out, err = spec.Run([]string{"export", "finance"})
	if err != nil {
		t.Fatalf("默认落点导出: %v", err)
	}
	if !strings.Contains(out, rolepack.FileName("finance")) {
		t.Fatalf("默认文件名不符:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(cwd, rolepack.FileName("finance"))); err != nil {
		t.Fatalf("默认落点文件应存在: %v", err)
	}

	// 覆盖规则:自己的包可以覆盖(重导一次),别人的文件一口回绝
	if out, err = spec.Run([]string{"export", "finance", path}); err != nil || !strings.Contains(out, "已覆盖同名角色包") {
		t.Fatalf("重导出(覆盖自己的包)应允许并说明: %v %s", err, out)
	}
	note := filepath.Join(filepath.Dir(path), "note.txt")
	if err := os.WriteFile(note, []byte("我的笔记"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Run([]string{"export", "finance", note}); err == nil || !strings.Contains(err.Error(), "不是角色包") {
		t.Fatalf("不该覆盖非角色包文件: %v", err)
	}
	if b, _ := os.ReadFile(note); string(b) != "我的笔记" {
		t.Fatalf("非角色包文件被改了:%q", b)
	}
}

func TestRoleImportCommand(t *testing.T) {
	h, spec, path := packHarness(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Run([]string{"export", "finance", path}); err != nil {
		t.Fatal(err)
	}
	// 同名且是**当前角色**:先要拒绝覆盖(否则下一轮提示会静默少一层指令)
	if _, err := spec.Run([]string{"use", "finance"}); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Run([]string{"import", path, "force"}); err == nil || !strings.Contains(err.Error(), "正在使用中") {
		t.Fatalf("当前角色应拒绝覆盖: %v", err)
	}
	// 不带 force 的同名:拒绝并给出两条正路(force / as)
	if _, err := spec.Run([]string{"none"}); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Run([]string{"import", path}); err == nil ||
		!strings.Contains(err.Error(), "已存在") || !strings.Contains(err.Error(), "force") {
		t.Fatalf("同名目标应显式拒绝并给出口: %v", err)
	}
	// force 覆盖:回执要说清旧份进回收站,且角色索引已重载(服务里看得到)
	out, err := spec.Run([]string{"import", path, "force"})
	if err != nil {
		t.Fatalf("/role import force: %v", err)
	}
	for _, want := range []string{"已导入角色 财务(finance)", ".trash", "技能"} {
		if !strings.Contains(out, want) {
			t.Fatalf("导入回执缺 %q:\n%s", want, out)
		}
	}
	if _, ok := h.svc.Get("finance"); !ok {
		t.Fatal("导入后角色索引应已重载")
	}
	// as 另起 ID:并存一份,且提示怎么启用
	out, err = spec.Run([]string{"import", path, "as", "finance-copy"})
	if err != nil {
		t.Fatalf("/role import as: %v", err)
	}
	if !strings.Contains(out, "包里原本是 finance") || !strings.Contains(out, "/role use finance-copy") {
		t.Fatalf("as 导入回执不清:\n%s", out)
	}
	if _, ok := h.svc.Get("finance-copy"); !ok {
		t.Fatal("as 导入的角色应存在")
	}
	// 参数错误:一条都不许静默当成功
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"import"}, "/role import <角色包路径>"},
		{[]string{"import", filepath.Join(h.home, "nope.zip")}, "读取失败"},
		{[]string{"import", path, "as"}, "as 后面要跟目标 ID"},
		{[]string{"import", path, "force", "extra"}, "不认识的参数"},
	} {
		if _, err := spec.Run(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v 应报 %q,得到 %v", tc.args, tc.want, err)
		}
	}
	// 坏包(不是 zip):报错且不留痕迹
	bad := filepath.Join(h.home, "share", "bad.zip")
	if err := os.WriteFile(bad, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Run([]string{"import", bad, "as", "frombad"}); err == nil ||
		!strings.Contains(err.Error(), "不是有效的角色包") {
		t.Fatalf("坏包应拒绝: %v", err)
	}
	if _, ok := h.svc.Get("frombad"); ok {
		t.Fatal("坏包不该留下角色")
	}
}

// TestRolePackRoundTripKeepsTier 导出→导入后,收紧档/模型/工具排除/私有技能一个都不能少。
// 这几项正是"分享出去少了一半角色"的高危字段(第九十一/九十二批新增)。
func TestRolePackRoundTripKeepsTier(t *testing.T) {
	h, spec, path := packHarness(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SetAgents("finance", "口径优先\n"); err != nil {
		t.Fatal(err)
	}
	before, _ := h.svc.Get("finance")
	if _, err := spec.Run([]string{"export", "finance", path}); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Run([]string{"import", path, "as", "copy"}); err != nil {
		t.Fatal(err)
	}
	after, ok := h.svc.Get("copy")
	if !ok {
		t.Fatal("导入的角色应可读")
	}
	if after.Name != before.Name || after.Identity != before.Identity || after.Model != before.Model ||
		after.Approval != before.Approval || after.Sandbox != before.Sandbox ||
		after.AGENTS != before.AGENTS || len(after.ToolsExclude) != len(before.ToolsExclude) ||
		len(after.OwnSkills) != len(before.OwnSkills) {
		t.Fatalf("往返后字段不一致:\n导入前 %+v\n导入后 %+v", before, after)
	}
	if len(after.ToolsExclude) != 1 || after.ToolsExclude[0] != "shell" {
		t.Fatalf("工具排除清单应逐字回来:%v", after.ToolsExclude)
	}
	if after.Approval != "strict" {
		t.Fatalf("收紧档应逐字回来:%q", after.Approval)
	}
}
