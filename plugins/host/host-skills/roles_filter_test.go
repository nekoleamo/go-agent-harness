// 角色联动:角色私有技能扫描/归属标注 + 可见性过滤 + 重扫(第七十八批)。
package hostskills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// writeSKILLMD 在 dir/name/SKILL.md 写一个最小技能。
func writeSKILLMD(t *testing.T, dir, name, desc string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "---\nname: " + name + "\ndescription: " + desc + "\n---\n正文-" + name
	if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScannerRoleTagAndDuplicates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	shared := filepath.Join(home, "skills")
	writeSKILLMD(t, shared, "shared-a", "共享技能")
	// 角色私有同名技能:应压过共享库(先扫角色目录)
	writeSKILLMD(t, filepath.Join(home, "roles", "r1", "skills"), "shared-a", "r1 覆盖版")
	writeSKILLMD(t, filepath.Join(home, "roles", "r1", "skills"), "priv-1", "r1 私有")

	sc := &Scanner{}
	got, err := sc.Scan(append(roleSkillDirs(), shared)...)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Skill{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if s, ok := byName["shared-a"]; !ok || s.Role != "r1" || !strings.Contains(s.Description, "覆盖版") {
		t.Fatalf("角色私有技能应压过共享库并标归属: %+v", s)
	}
	if s, ok := byName["priv-1"]; !ok || s.Role != "r1" {
		t.Fatalf("角色私有技能应标归属 r1: %+v", s)
	}
	// 被忽略的那份要可见(重名 first-wins 不再是静默行为)
	if len(sc.Duplicates()) != 1 || !strings.Contains(sc.Duplicates()[0], "shared-a") {
		t.Fatalf("重名应记入 Duplicates: %v", sc.Duplicates())
	}
	// 共享库技能无归属
	writeSKILLMD(t, shared, "shared-b", "共享技能B")
	got2, _ := (&Scanner{}).Scan(append(roleSkillDirs(), shared)...)
	for _, s := range got2 {
		if s.Name == "shared-b" && s.Role != "" {
			t.Fatalf("共享技能不应标归属: %+v", s)
		}
	}
	// roleOf:非角色路径一律空
	if r := roleOf(filepath.Join(shared, "shared-b", "SKILL.md")); r != "" {
		t.Fatalf("共享路径归属应为空,got %q", r)
	}
	if r := roleOf(filepath.Join(home, "roles", "r1", "role.yaml")); r != "" {
		t.Fatalf("非 skills 下的文件不应算角色技能: %q", r)
	}
}

func TestRegistryFilterRescanAndIndex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	global := filepath.Join(home, "skills")
	writeSKILLMD(t, global, "skill-a", "技能A")
	writeSKILLMD(t, global, "skill-b", "技能B")

	reg := newRegistry([]string{global}, nil)
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if len(reg.List()) != 2 {
		t.Fatalf("重扫应加载 2 个技能: %+v", reg.List())
	}
	if !strings.Contains(reg.IndexText(), "skill-a") {
		t.Fatal("无过滤时应列出全部技能")
	}
	// 过滤:只留 skill-a
	undo := reg.SetFilter(func(si sdk.SkillInfo) bool { return si.Name == "skill-a" })
	txt := reg.IndexText()
	if !strings.Contains(txt, "skill-a") || strings.Contains(txt, "skill-b") {
		t.Fatalf("过滤后索引不符: %s", txt)
	}
	// List 不受过滤影响(面板勾选用全量)
	if len(reg.List()) != 2 {
		t.Fatalf("List 应返回全量: %+v", reg.List())
	}
	if _, ok := reg.findVisible("skill-b"); ok {
		t.Fatal("被过滤技能不应可见")
	}
	if _, ok := reg.find("skill-b"); !ok {
		t.Fatal("find 应仍能看到被过滤技能(用于区分“不存在”与“未挂载”)")
	}
	// 撤销过滤器 → 恢复全量可见
	undo()
	if !strings.Contains(reg.IndexText(), "skill-b") {
		t.Fatal("撤销过滤后应恢复可见")
	}
	// 全部被过滤:索引给出可解释文案而非空白
	reg.SetFilter(func(sdk.SkillInfo) bool { return false })
	if msg := reg.IndexText(); !strings.Contains(msg, "未挂载任何技能") || !strings.Contains(msg, "2") {
		t.Fatalf("全过滤文案不符: %s", msg)
	}
	// 重扫发现新增技能(目录里后放的 SKILL.md 不需要重启)
	writeSKILLMD(t, global, "skill-c", "技能C")
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if len(reg.List()) != 3 {
		t.Fatalf("重扫未发现新技能: %+v", reg.List())
	}
}

func TestRegistryRescanPicksNewRoleDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	global := filepath.Join(home, "skills")
	writeSKILLMD(t, global, "skill-a", "技能A")
	reg := newRegistry([]string{global}, nil)
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	// 角色目录在启动后出现(新建角色)+ 私有技能
	writeSKILLMD(t, filepath.Join(home, "roles", "new-role", "skills"), "priv-new", "新角色私有")
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, si := range reg.List() {
		names[si.Name] = si.Role
	}
	if names["priv-new"] != "new-role" {
		t.Fatalf("重扫应发现新角色私有技能并标归属: %+v", names)
	}
}

func TestListSkillsToolHonorsFilter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	global := filepath.Join(home, "skills")
	writeSKILLMD(t, global, "skill-a", "技能A")
	writeSKILLMD(t, global, "skill-b", "技能B")
	reg := newRegistry([]string{global}, nil)
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	reg.SetFilter(func(si sdk.SkillInfo) bool { return si.Name == "skill-a" })
	out, err := (&listSkills{reg: reg}).Execute(context.Background(), "{}") // 工具不用 ctx 也不能传 nil(staticcheck SA1012)
	if err != nil {
		t.Fatal(err)
	}
	list, ok := out.([]Skill)
	if !ok || len(list) != 1 || list[0].Name != "skill-a" {
		t.Fatalf("list_skills 应只回可见技能: %+v", out)
	}
}
