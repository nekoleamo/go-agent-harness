// 两条「原本要真机手验」的链路,在这里用真实装配 + 真实数据根跑出来:
//
// ① **12 个预置角色逐个验收**:真机清单里「12 个角色逐个验收」是 12 次手工切角色;
//
//	这里改成一条用例把 12 个全过一遍 —— 每个角色:能被解析、不进 Broken()、身份槽真的
//	进了系统提示、私有技能真的可见且能读到、skills 键没有失效挂载。任一项不成立就红。
//
// ② **技能包两机互导**:真机清单里「技能包两机互导」要两台机器;这里用**两个数据根**
//
//	(A 导出 → B 导入)模拟「另一台机器」,并验 B 里导入的技能**真的能被技能库索引到**
//	(而不只是文件落了盘 —— 那两件事曾经真的分开过,见 host-skills 的相关登记)。
package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/embed"
	"github.com/nekoleamo/go-agent-harness/internal/skillpack"
	"github.com/nekoleamo/go-agent-harness/internal/skills"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 预置角色清单(与 internal/embed 的内容契约测试同源;这里再写一份是为了让本用例
// **独立地**知道「应该有几个」—— 两处都写死同一个数,任一处漏了角色另一处会红)。
var presetRoleIDs = []string{
	"assistant", "coding-master", "tech-lead", "reviewer", "ops-sre",
	"data-analyst", "finance", "novelist", "news-writer", "tech-writer",
	"translator", "teacher",
}

// TestPresetRolesAllRoundTrip 12 个预置角色逐个:释放 → 解析 → 切过去 → 身份槽与技能都对。
func TestPresetRolesAllRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if _, err := embed.EnsureRoles(home); err != nil {
		t.Fatal(err)
	}
	c, _ := buildRolesEnv(t, home)
	var svc sdk.RoleService
	if err := c.Inject("ctx.roles", &svc); err != nil {
		t.Fatal(err)
	}
	var sk sdk.SkillsService
	if err := c.Inject("ctx.skills", &sk); err != nil {
		t.Fatal(err)
	}
	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		t.Fatal(err)
	}

	for _, id := range presetRoleIDs {
		t.Run(id, func(t *testing.T) {
			spec, ok := svc.Get(id)
			if !ok {
				t.Fatalf("取角色 %s 失败(没释放或进了 Broken())", id)
			}
			if spec.Name == "" || spec.Description == "" || spec.Identity == "" {
				t.Fatalf("%s 内容不全(名字/说明/身份句缺一即等于「没换角色」):%+v", id, spec)
			}
			// 切过去(与用户在面板里点「切换」同一条)
			if err := svc.Use(id); err != nil {
				t.Fatalf("切到 %s: %v", id, err)
			}
			// 身份槽进了系统提示
			sys := systemText(t, sp, nil)
			if !strings.Contains(sys, spec.Name) {
				t.Fatalf("%s 的名字没进身份槽:\n%s", id, trunc(sys))
			}
			if !strings.Contains(sys, spec.Identity) {
				t.Fatalf("%s 的身份句没进身份槽:\n%s", id, trunc(sys))
			}
			// 私有技能可见(进了模型可见表 = 切角色立即改技能面)且正文真的读得到。
			// 两条都查:「挂载在清单里但模型读不到」正是失效挂载的症状。
			visible := map[string]bool{}
			for _, si := range sk.List() {
				visible[si.Name] = true
			}
			for _, name := range spec.Skills {
				if !visible[name] {
					t.Fatalf("%s 挂了 %q 但模型可见技能表里没有(失效挂载)", id, name)
				}
				body, err := skills.ForRole(id).Read(name)
				if err != nil {
					t.Fatalf("%s 的技能 %q 读不到: %v", id, name, err)
				}
				if strings.TrimSpace(body) == "" {
					t.Fatalf("%s 的技能 %q 正文为空", id, name)
				}
			}
			// 回到基线,免得影响下一个子用例
			if err := svc.Use(""); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestSkillPackTwoHomes 两个数据根之间互导:A 导出 → B 导入 → B 的技能库能读到。
func TestSkillPackTwoHomes(t *testing.T) {
	// —— A 机:造一个技能并导出 ——
	homeA := t.TempDir()
	t.Setenv("GAH_HOME", homeA)
	doc := "---\nname: cross-home\ndescription: 跨机互导验证用\n---\n\n这条记忆要能落到另一台机器上。\n"
	if err := skills.Shared().Write("cross-home", doc, false); err != nil {
		t.Fatal(err)
	}
	pack, err := skillpack.Export("cross-home")
	if err != nil {
		t.Fatalf("导出: %v", err)
	}
	if len(pack) == 0 {
		t.Fatal("导出为空")
	}

	// —— B 机:另一个数据根,先确认它**没有**这条技能 ——
	homeB := t.TempDir()
	t.Setenv("GAH_HOME", homeB)
	libB := skills.Shared()
	if libB.Exists("cross-home") {
		t.Fatal("B 机一开始就不该有这条技能(否则这个用例什么也没证明)")
	}
	res, err := skillpack.Import(pack, skillpack.ImportOptions{})
	if err != nil {
		t.Fatalf("B 机导入: %v", err)
	}
	if res.Name != "cross-home" {
		t.Fatalf("导入后的名字不对:%q", res.Name)
	}
	// 真的能读到了(不只是文件落了盘)
	body, err := libB.Read("cross-home")
	if err != nil {
		t.Fatalf("B 机导入后仍读不到技能: %v", err)
	}
	if !strings.Contains(body, "跨机互导") {
		t.Fatalf("导入的正文不对:\n%s", body)
	}
	// frontmatter 的 name 必须与目录名一致(否则 skills.Write 之后索引与正文会错位)
	if n := skills.ParseName(body); n != "cross-home" {
		t.Fatalf("frontmatter name=%q,与目录名不一致", n)
	}
	// 同名再导一次 ⇒ 显式拒绝(不静默覆盖用户在另一台机器上改过的技能)
	if _, err := skillpack.Import(pack, skillpack.ImportOptions{}); err == nil {
		t.Fatal("同名重复导入应显式拒绝(要覆盖得显式 Overwrite)")
	}
	// 文件确实落在 B 的数据根里(便携纪律:不写系统其它位置)
	p := filepath.Join(homeB, "skills", "cross-home", "SKILL.md")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("导入的技能应落在 B 的 $GAH_HOME/skills 下: %v", err)
	}
}

func trunc(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
