// 角色包(导出/导入)测试。
//
// 两条主线:
//  1. 往返无损 —— 定义/规则/私有技能,导出再导入得回同一个角色(技能的正文连 frontmatter
//     都要逐字一致);
//  2. **恶意包不落地** —— 解包是本仓库唯一一处"内容来自外部"的入口,每条拒绝路径都要有据:
//     路径穿越、未知条目、超限、未知定义键、坏技能名……而且**一条都不许留下半成品**。
package rolepack

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/roles"
	"github.com/nekoleamo/go-agent-harness/internal/skills"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// store 角色存储(测试共用一份:roles.Store 是无状态空struct 值类型,不引入共享状态)。
var store = roles.Store{}

func setup(t *testing.T) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
}

// mkRole 建一个"什么都有"的角色:定义全字段 + 规则正文 + 两个私有技能。
func mkRole(t *testing.T, id string) sdk.RoleSpec {
	t.Helper()
	spec := sdk.RoleSpec{
		ID: id, Name: "分享角色", Description: "导出/导入用", Identity: "你是分享角色",
		ExcludeGlobal: true, Skills: []string{"skill-a"}, SkillsSet: true, SkillsInherit: true,
		Model: "role-model", Thinking: "low", ToolsExclude: []string{"tool-x"},
		Approval: "strict", Sandbox: "read-only",
	}
	if err := store.Create(spec, "工作规则正文\n第二行\n"); err != nil {
		t.Fatal(err)
	}
	lib := skills.ForRole(id)
	for name, body := range map[string]string{
		"skill-a": skills.Content("skill-a", "技能甲", []string{"触发甲"}, "甲正文"),
		"skill-b": "---\nname: skill-b\ndescription: 技能乙\n---\n\n乙正文\n",
	} {
		if err := lib.Write(name, body, false); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// zipOf 手工构造一个包(条目按传入顺序;同名重复用于测"重复条目")。
func zipOf(t *testing.T, entries [][2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func validManifest(id string) string {
	return `{"format":"gah-role","version":1,"id":"` + id + `","name":"分享角色","exported_at":"2026-09-29T00:00:00Z"}`
}

const validDef = "name: 分享角色\nidentity: 你是分享角色\nmodel: role-model\nthinking: low\n" +
	"tools_exclude:\n  - tool-x\napproval: strict\nsandbox: read-only\nskills:\n  - skill-a\nskills_inherit: true\nexclude_global: true\n"

// TestExportImportRoundTrip 导出→删→导入:定义、规则正文、私有技能三样都要回来。
func TestExportImportRoundTrip(t *testing.T) {
	setup(t)
	before := mkRole(t, "alpha")
	pack, err := Export("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(FileName("alpha"), "gah-role-alpha") || !strings.HasSuffix(FileName("alpha"), ".zip") {
		t.Fatalf("文件名口径:%q", FileName("alpha"))
	}
	if err := store.Delete("alpha"); err != nil {
		t.Fatal(err)
	}
	res, err := Import(pack, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "alpha" || res.Replaced || res.BackupName != "" {
		t.Fatalf("导入回执:%+v", res)
	}
	if res.Manifest.Format != Format || res.Manifest.Version != Version || res.Manifest.ExportedAt == "" {
		t.Fatalf("清单内容:%+v", res.Manifest)
	}
	if !reflect.DeepEqual(res.Skills, []string{"skill-a", "skill-b"}) {
		t.Fatalf("导入的私有技能:%v", res.Skills)
	}
	after, err := store.Get("alpha")
	if err != nil {
		t.Fatal(err)
	}
	// 定义逐字段(含第九十一/九十二批那几个新键 —— 少一个就是"分享出去少了一半角色")
	for _, f := range []struct {
		name     string
		got, exp any
	}{
		{"Name", after.Name, before.Name}, {"Description", after.Description, before.Description},
		{"Identity", after.Identity, before.Identity}, {"Model", after.Model, before.Model},
		{"Thinking", after.Thinking, before.Thinking}, {"Approval", after.Approval, before.Approval},
		{"Sandbox", after.Sandbox, before.Sandbox}, {"ExcludeGlobal", after.ExcludeGlobal, before.ExcludeGlobal},
		{"SkillsInherit", after.SkillsInherit, before.SkillsInherit}, {"SkillsSet", after.SkillsSet, before.SkillsSet},
		{"Skills", after.Skills, before.Skills}, {"ToolsExclude", after.ToolsExclude, before.ToolsExclude},
		{"AGENTS", after.AGENTS, before.AGENTS}, {"OwnSkills", after.OwnSkills, before.OwnSkills},
	} {
		if !reflect.DeepEqual(f.got, f.exp) {
			t.Fatalf("往返后 %s = %#v,期望 %#v", f.name, f.got, f.exp)
		}
	}
	// 技能正文逐字一致(含 frontmatter)
	for name, want := range map[string]string{
		"skill-a": skills.Content("skill-a", "技能甲", []string{"触发甲"}, "甲正文"),
		"skill-b": "---\nname: skill-b\ndescription: 技能乙\n---\n\n乙正文\n",
	} {
		got, err := skills.ForRole("alpha").Read(name)
		if err != nil || got != want {
			t.Fatalf("技能 %s 往返后不一致: %q %v", name, got, err)
		}
	}
	// 导入的包再导一次:内容等价(技能的确定性由下面的固定时钟用例钉住)
	pack2, err := Export("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(pack2) == 0 {
		t.Fatal("二次导出为空")
	}
}

// TestExportDeterministic 同一份角色 + 同一时刻 ⇒ 同一串字节(便于比对 diff/校验)。
func TestExportDeterministic(t *testing.T) {
	setup(t)
	mkRole(t, "alpha")
	at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	a, err := exportAt("alpha", at)
	if err != nil {
		t.Fatal(err)
	}
	b, err := exportAt("alpha", at)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("同一时刻导两次字节不同(条目顺序或时间戳不稳)")
	}
	zr, err := zip.NewReader(bytes.NewReader(a), int64(len(a)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	want := []string{ManifestName, roles.FileName, roles.AgentsName, "skills/skill-a/SKILL.md", "skills/skill-b/SKILL.md"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("包内条目顺序/集合:%v", names)
	}
}

// TestExportUnknownRole 导出不存在的角色:直接报错(不产出空包)。
func TestExportUnknownRole(t *testing.T) {
	setup(t)
	if pack, err := Export("ghost"); err == nil || len(pack) != 0 {
		t.Fatalf("导出不存在的角色应报错:%v %d 字节", err, len(pack))
	}
}

// TestImportOverrides 同名目标:默认拒绝(不静默覆盖);force 时旧份进回收站且可恢复。
func TestImportOverrides(t *testing.T) {
	setup(t)
	mkRole(t, "alpha")
	pack, err := Export("alpha")
	if err != nil {
		t.Fatal(err)
	}
	// 默认:拒绝
	if _, err := Import(pack, ImportOptions{}); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("同名目标应显式拒绝:%v", err)
	}
	// 覆盖:旧份进回收站
	res, err := Import(pack, ImportOptions{Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Replaced || res.BackupName == "" {
		t.Fatalf("覆盖回执应带备份名:%+v", res)
	}
	list := store.TrashList()
	if len(list) != 1 || list[0].Name != res.BackupName || list[0].ID != "alpha" {
		t.Fatalf("回收站应有被覆盖那份:%+v", list)
	}
	if _, err := store.Get("alpha"); err != nil {
		t.Fatalf("覆盖后新角色应可读:%v", err)
	}
}

// TestImportOverwriteActiveRoleRefused 当前角色拒绝覆盖(否则下一轮提示会静默少一层指令)。
func TestImportOverwriteActiveRoleRefused(t *testing.T) {
	setup(t)
	mkRole(t, "alpha")
	pack, err := Export("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetActive("alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(pack, ImportOptions{Overwrite: true}); err == nil || !strings.Contains(err.Error(), "正在使用中") {
		t.Fatalf("当前角色应拒绝覆盖:%v", err)
	}
	if got, err := store.Get("alpha"); err != nil || got.Name != "分享角色" {
		t.Fatalf("拒绝后原角色不该受影响:%v %v", got.Name, err)
	}
}

// TestImportAs 目标 ID 另起一个(分享包与本地已有角色并存的正路)。
func TestImportAs(t *testing.T) {
	setup(t)
	mkRole(t, "alpha")
	pack, err := Export("alpha")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Import(pack, ImportOptions{As: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("beta")
	if err != nil || got.ID != "beta" || got.Name != "分享角色" {
		t.Fatalf("导入为 beta:%+v %v", got, err)
	}
	if res.Manifest.ID != "alpha" {
		t.Fatalf("清单里应保留原始 ID:%+v", res.Manifest)
	}
	if _, err := skills.ForRole("beta").Read("skill-a"); err != nil {
		t.Fatalf("私有技能应落在新 ID 下:%v", err)
	}
}

// TestImportRejectsBadPackages 恶意/坏包一律拒绝,且**一条都不落盘**。
func TestImportRejectsBadPackages(t *testing.T) {
	ok := [][2]string{{ManifestName, validManifest("alpha")}, {roles.FileName, validDef}}
	cases := []struct {
		name string
		pack []byte
		want string
	}{
		{"不是 zip", []byte("hello"), "不是有效的角色包"},
		{"空包", nil, "空的"},
		{"缺清单", zipOf(t, ok[1:]), "格式标识"},
		{"格式不对", zipOf(t, [][2]string{{ManifestName, `{"format":"other","version":1,"id":"alpha"}`}, ok[1]}), "不是 gah 角色包"},
		{"版本过新", zipOf(t, [][2]string{{ManifestName, `{"format":"gah-role","version":9,"id":"alpha"}`}, ok[1]}), "格式版本不支持"},
		{"清单坏 JSON", zipOf(t, [][2]string{{ManifestName, "{"}, ok[1]}), "清单解析失败"},
		{"缺角色定义", zipOf(t, ok[:1]), "缺 role.yaml"},
		{"清单无 id", zipOf(t, [][2]string{{ManifestName, `{"format":"gah-role","version":1}`}, ok[1]}), "没有 id"},
		{"ID 非法", zipOf(t, [][2]string{{ManifestName, validManifest("Bad/ID")}, ok[1]}), "角色 ID"},
		{"条目不认识", zipOf(t, append(append([][2]string{}, ok...), [2]string{"extra.txt", "x"})), "不认识的条目"},
		{"重复条目", zipOf(t, append(append([][2]string{}, ok...), ok[1])), "重复条目"},
		{"路径穿越", zipOf(t, append(append([][2]string{}, ok...), [2]string{"../evil.txt", "x"})), "路径穿越"},
		{"绝对路径", zipOf(t, append(append([][2]string{}, ok...), [2]string{"/etc/passwd", "x"})), "绝对路径"},
		{"反斜杠", zipOf(t, append(append([][2]string{}, ok...), [2]string{`skills\a\SKILL.md`, "x"})), "反斜杠"},
		{"技能嵌一层", zipOf(t, append(append([][2]string{}, ok...), [2]string{"skills/a/b/SKILL.md", "x"})), "不认识的条目"},
		{"技能别文件名", zipOf(t, append(append([][2]string{}, ok...), [2]string{"skills/a/README.md", "x"})), "不认识的条目"},
		{"未知定义键", zipOf(t, [][2]string{{ManifestName, validManifest("alpha")},
			{roles.FileName, validDef + "plugin_set:\n  - x\n"}}), "不认识的键"},
		{"定义坏值", zipOf(t, [][2]string{{ManifestName, validManifest("alpha")},
			{roles.FileName, "approval: open\n"}}), "只能收紧"},
		{"规则超限", zipOf(t, [][2]string{{ManifestName, validManifest("alpha")}, {roles.FileName, validDef},
			{roles.AgentsName, strings.Repeat("x", roles.MaxAgentsBytes+1)}}), "超上限"},
		{"技能名非法", zipOf(t, append(append([][2]string{}, ok...), [2]string{"skills/Bad/SKILL.md", "x"})), "非法技能名"},
		{"技能超限", zipOf(t, append(append([][2]string{}, ok...),
			[2]string{"skills/skill-a/SKILL.md", strings.Repeat("x", skills.MaxBytes+1)})), "超上限"},
		{"frontmatter 名不一致", zipOf(t, append(append([][2]string{}, ok...),
			[2]string{"skills/skill-a/SKILL.md", "---\nname: other\n---\n正文\n"})), "不一致"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setup(t)
			if _, err := Import(tc.pack, ImportOptions{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("应报 %q,得到 %v", tc.want, err)
			}
			if store.Exist("alpha") || store.Exist("beta") {
				t.Fatal("拒绝的包不得留下任何目录")
			}
			if ps := store.Broken(); len(ps) != 0 {
				t.Fatalf("拒绝的包也不该留下坏角色:%+v", ps)
			}
		})
	}
}

// TestImportTargetOccupiedByFile 目标位置被同名**文件**占着:拒绝导入,且不许删掉那个文件。
// 为什么这条重要:Exist 只认目录,若照直往下写会先失败、再由回滚 RemoveAll 把用户那个文件**删掉** ——
// 回滚的职责是撤销自己做的事,不是销毁外物。
func TestImportTargetOccupiedByFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	pack := zipOf(t, [][2]string{
		{ManifestName, validManifest("alpha")}, {roles.FileName, validDef},
		{roles.AgentsName, "规则\n"},
	})
	if err := os.MkdirAll(roles.Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(roles.Dir("alpha"), []byte("占位"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Import(pack, ImportOptions{})
	if err == nil || !strings.Contains(err.Error(), "同名文件占着") {
		t.Fatalf("应显式拒绝:%v", err)
	}
	raw, err := os.ReadFile(roles.Dir("alpha"))
	if err != nil || string(raw) != "占位" {
		t.Fatalf("被占位时不许动那个文件:%q %v", raw, err)
	}
	// 清掉占位后同一份包要能正常导入(证明拒绝的只是"位置被占",不是包有问题)
	if err := os.Remove(roles.Dir("alpha")); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(pack, ImportOptions{}); err != nil {
		t.Fatalf("清掉占位后应能导入:%v", err)
	}
}

// TestRollbackRestoresBackup 回滚分支(有备份时):旧份要从回收站搬回原位,搬不回来要如实说。
func TestRollbackRestoresBackup(t *testing.T) {
	setup(t)
	mkRole(t, "alpha")
	backup, err := store.MoveToTrash("alpha")
	if err != nil {
		t.Fatal(err)
	}
	// 半成品:只写了定义(模拟"写到一半")
	if err := store.Save(sdk.RoleSpec{ID: "alpha", Name: "半成品"}); err != nil {
		t.Fatal(err)
	}
	err = rollback(store, "alpha", backup, errors.New("写入失败"))
	if err == nil || !strings.Contains(err.Error(), "已回滚") {
		t.Fatalf("应报已回滚:%v", err)
	}
	got, err := store.Get("alpha")
	if err != nil || got.Name != "分享角色" || got.AGENTS == "" || len(got.OwnSkills) != 2 {
		t.Fatalf("回滚后应是原来那份:%+v %v", got, err)
	}
	// 备份名不存在(手工删了/名字不对):不许谎称"已回滚"
	err = rollback(store, "alpha", "ghost-20260101-000000", errors.New("写入失败"))
	if err == nil || !strings.Contains(err.Error(), "回滚也失败") || !strings.Contains(err.Error(), roles.TrashDir()) {
		t.Fatalf("回滚失败要如实报并给回收站位置:%v", err)
	}
}

// TestImportAcceptsAllKnownKeys 一条把 roleFile **全部** yaml 键都写上的定义必须能导入。
// 为什么要有这条:白名单是反射自 roleFile 的 tag 集,永远不会漏 —— 但"漏了某个键的**解析**"
// (ParseDefinition 里少赋值一个字段)是另一回事,它会表现成"导入后某个字段消失"。
// 所以这里逐键写满,再把结果与期望逐字段比(第九十一/九十二批新增的三个键正在此列)。
func TestImportAcceptsAllKnownKeys(t *testing.T) {
	setup(t)
	full := strings.Join([]string{
		"name: 满载角色",
		"description: 全字段",
		"identity: 你是满载角色",
		"exclude_global: true",
		"skills:",
		"  - skill-a",
		"skills_inherit: true",
		"model: role-model",
		"thinking: low",
		"tools_exclude:",
		"  - tool-x",
		"approval: strict",
		"sandbox: read-only",
		"",
	}, "\n")
	pack := zipOf(t, [][2]string{{ManifestName, validManifest("alpha")}, {roles.FileName, full}})
	if _, err := Import(pack, ImportOptions{}); err != nil {
		t.Fatalf("全字段定义应能导入:%v", err)
	}
	got, err := store.Get("alpha")
	if err != nil {
		t.Fatal(err)
	}
	want := sdk.RoleSpec{ID: "alpha", Name: "满载角色", Description: "全字段", Identity: "你是满载角色",
		ExcludeGlobal: true, Skills: []string{"skill-a"}, SkillsSet: true, SkillsInherit: true,
		Model: "role-model", Thinking: "low", ToolsExclude: []string{"tool-x"},
		Approval: "strict", Sandbox: "read-only"}
	got.AGENTS, got.AGENTSBytes, got.OwnSkills, got.Seed = "", 0, nil, false
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("逐字段对比:\n得到 %+v\n期望 %+v", got, want)
	}
	// 同一个包再导一次(As 另起):证明导出的包在解析上与 import 对称
	pack2, err := Export("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Import(pack2, ImportOptions{As: "beta"}); err != nil {
		t.Fatalf("导出再导入:%v", err)
	}
}

// TestInspect 只读清单:能认的认出来,认不出的报错(覆盖同名文件前的"这到底是什么"闸门)。
func TestInspect(t *testing.T) {
	setup(t)
	mkRole(t, "alpha")
	pack, err := Export("alpha")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Inspect(pack)
	if err != nil || m.ID != "alpha" || m.Name != "分享角色" || m.Version != Version {
		t.Fatalf("Inspect:%+v %v", m, err)
	}
	for _, bad := range [][]byte{nil, []byte("not a zip"), zipOf(t, [][2]string{{"x.txt", "y"}}),
		zipOf(t, [][2]string{{ManifestName, `{"format":"other"}`}}),
		zipOf(t, [][2]string{{ManifestName, `{"format":"gah-role","version":9}`}})} {
		if _, err := Inspect(bad); err == nil {
			t.Fatalf("坏的包应报错:%q", bad)
		}
	}
}

// TestImportSizeCaps 包字节上限与条目上限(整包拒,不进解包)。
func TestImportSizeCaps(t *testing.T) {
	setup(t)
	big := make([]byte, MaxPackBytes+1)
	if _, err := Import(big, ImportOptions{}); err == nil || !strings.Contains(err.Error(), "超上限") {
		t.Fatalf("整包上限:%v", err)
	}
	// 条目过多:合法形态灌满
	var entries [][2]string
	for i := 0; i < maxEntries+1; i++ {
		entries = append(entries, [2]string{"skills/s" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "/SKILL.md", "x"})
	}
	if _, err := Import(zipOf(t, entries), ImportOptions{}); err == nil || !strings.Contains(err.Error(), "条目过多") {
		t.Fatalf("条目上限:%v", err)
	}
}

// TestParseSkillEntry 包内技能条目形态(与 skillEntry 成对,严格两段)。
func TestParseSkillEntry(t *testing.T) {
	for _, ok := range []string{"skills/a/SKILL.md", "skills/a..b/SKILL.md"} {
		if got, valid := parseSkillEntry(ok); !valid || skillEntry(got) != ok {
			t.Fatalf("%s 应被接受(往返也要一致):%q %v", ok, got, valid)
		}
	}
	for _, bad := range []string{"skills/a/b/SKILL.md", "skills/a/README.md", "skills//SKILL.md", "skill/a/SKILL.md", "skills/a/SKILL.md/x"} {
		if _, valid := parseSkillEntry(bad); valid {
			t.Fatalf("%s 不该被接受", bad)
		}
	}
}

// TestImportEmptyAgentsNoFile 包里没有规则正文时不凭空造一个空 AGENTS.md。
func TestImportEmptyAgentsNoFile(t *testing.T) {
	setup(t)
	pack := zipOf(t, [][2]string{{ManifestName, validManifest("alpha")}, {roles.FileName, "identity: 只有身份句\n"}})
	res, err := Import(pack, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.AgentsLen != 0 {
		t.Fatalf("没有规则正文时长度应为 0:%+v", res)
	}
	if _, err := os.Stat(filepath.Join(roles.Dir("alpha"), roles.AgentsName)); !os.IsNotExist(err) {
		t.Fatalf("不该写空 AGENTS.md:%v", err)
	}
	// 清单缺 name 时显示名回退 ID(与本地新建同口径)
	got, err := store.Get("alpha")
	if err != nil || got.Name != "alpha" {
		t.Fatalf("缺省显示名应为 ID:%+v %v", got, err)
	}
}
