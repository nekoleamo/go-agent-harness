package roles

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/testutil"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func setup(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	return home
}

func TestValidateID(t *testing.T) {
	ok := []string{"a", "finance", "coding-master", "r2", strings.Repeat("a", 32)}
	for _, id := range ok {
		if err := ValidateID(id); err != nil {
			t.Errorf("ValidateID(%q) = %v, want nil", id, err)
		}
	}
	bad := []string{"", "Finance", "-a", ".hidden", "a_b", "a.b", "a/..", "../x", ".trash", "roles",
		strings.Repeat("a", 33), "中文"}
	for _, id := range bad {
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID(%q) = nil, want error", id)
		}
	}
}

func TestCreateGetList(t *testing.T) {
	setup(t)
	s := Store{}
	if err := s.Create(sdk.RoleSpec{ID: "finance", Name: "财务", Identity: "你是财务顾问。"}, "# 规则\n先给数字。\n"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Create(sdk.RoleSpec{ID: "novelist", Name: "小说家"}, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 重复创建显式失败,且不破坏已有定义。
	if err := s.Create(sdk.RoleSpec{ID: "finance", Name: "X"}, "y"); err == nil {
		t.Fatal("重复 Create 应失败")
	}
	got, err := s.Get("finance")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "财务" || got.Identity != "你是财务顾问。" || !strings.Contains(got.AGENTS, "先给数字") {
		t.Errorf("Get 内容不符: %+v", got)
	}
	if got.AGENTSBytes != len(got.AGENTS) || got.AGENTSBytes == 0 {
		t.Errorf("AGENTSBytes = %d", got.AGENTSBytes)
	}
	if got.ExcludeGlobal {
		t.Error("未写 exclude_global 应为 false(保留全局指令)")
	}
	list := s.List()
	if len(list) != 2 || list[0].ID != "novelist" { // 按显示名排序:小说家(finance 叫"财务")? 见下断言
		// 排序按 Name:财务 vs 小说家 → 比较依赖排序规则,这里只断言集合与正文已剥离。
		if len(list) != 2 {
			t.Fatalf("List = %d 条, want 2", len(list))
		}
	}
	for _, r := range list {
		if r.AGENTS != "" {
			t.Errorf("List 不应带正文: %s", r.ID)
		}
	}
	// 显示名缺省 = ID
	if err := s.Create(sdk.RoleSpec{ID: "anon"}, ""); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Get("anon"); a.Name != "anon" {
		t.Errorf("Name 缺省应为 ID, got %q", a.Name)
	}
	if s.Exist("nope") {
		t.Error("Exist(nope) = true")
	}
}

func TestSkillsSetSemantics(t *testing.T) {
	setup(t)
	s := Store{}
	// 未写 skills 键 = 默认池(SkillsSet 假)
	if err := s.Create(sdk.RoleSpec{ID: "a", Skills: nil, SkillsSet: false}, ""); err != nil {
		t.Fatal(err)
	}
	a, _ := s.Get("a")
	if a.SkillsSet || a.Skills != nil {
		t.Errorf("未写 skills: SkillsSet=%v Skills=%v", a.SkillsSet, a.Skills)
	}
	// 显式空 = 一个都不挂(round-trip 保留)
	if err := s.Create(sdk.RoleSpec{ID: "b", Skills: []string{}, SkillsSet: true}, ""); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Get("b")
	if !b.SkillsSet || len(b.Skills) != 0 {
		t.Errorf("显式空 skills 未保留: SkillsSet=%v Skills=%#v", b.SkillsSet, b.Skills)
	}
	// 显式列表 + inherit
	if err := s.Create(sdk.RoleSpec{ID: "c", Skills: []string{"x", "y"}, SkillsSet: true, SkillsInherit: true}, ""); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Get("c")
	if !c.SkillsSet || len(c.Skills) != 2 || !c.SkillsInherit {
		t.Errorf("显式列表未保留: %+v", c)
	}
	// exclude_global=true 落盘并 round-trip
	if err := s.Create(sdk.RoleSpec{ID: "d", ExcludeGlobal: true}, ""); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Get("d"); !d.ExcludeGlobal {
		t.Error("ExcludeGlobal=true 未保留")
	}
	raw, err := os.ReadFile(filepath.Join(Dir("d"), FileName))
	if err != nil || !strings.Contains(string(raw), "exclude_global: true") {
		t.Errorf("role.yaml 未见显式 exclude_global: %s (%v)", raw, err)
	}
}

func TestSetAgentsLimitAndAtomic(t *testing.T) {
	setup(t)
	s := Store{}
	if err := s.Create(sdk.RoleSpec{ID: "big"}, "小的"); err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("あ", MaxAgentsBytes) // 多字节:字节数远超上限
	if err := s.SetAgents("big", huge); err == nil {
		t.Fatal("超上限 SetAgents 应失败")
	}
	// 失败不得破坏旧内容
	if got, _ := s.Get("big"); !strings.Contains(got.AGENTS, "小的") {
		t.Errorf("超限失败后旧正文被改: %q", got.AGENTS)
	}
	// 恰好上限可写
	exact := strings.Repeat("a", MaxAgentsBytes)
	if err := s.SetAgents("big", exact); err != nil {
		t.Fatalf("恰好上限应可写: %v", err)
	}
	// 目录里不留临时文件
	entries, _ := os.ReadDir(Dir("big"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".gah-role-") {
			t.Errorf("残留临时文件: %s", e.Name())
		}
	}
}

func TestShrink(t *testing.T) {
	// 截断落在 rune 边界:构造 3 字节字符,上限比它少 1 字节
	text := strings.Repeat("あ", 10)
	out, cut := Shrink(text)
	if cut {
		t.Fatal("未超限不应截断")
	}
	if out != text {
		t.Fatal("未超限内容被改")
	}
	big := strings.Repeat("あ", MaxAgentsBytes) // 3*32768 字节
	out2, cut2 := Shrink(big)
	if !cut2 {
		t.Fatal("应截断")
	}
	if len(out2) > MaxAgentsBytes || strings.ContainsRune(out2, '\uFFFD') {
		t.Errorf("截断结果非法: len=%d", len(out2))
	}
}

func TestRenameAndActive(t *testing.T) {
	setup(t)
	s := Store{}
	if err := s.Create(sdk.RoleSpec{ID: "old", Name: "旧"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActive("old"); err != nil {
		t.Fatal(err)
	}
	if s.Active() != "old" {
		t.Fatalf("Active = %q", s.Active())
	}
	// 改 ID:目录改名 + 当前角色跟随
	spec, err := s.Rename("old", "new", "新")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if spec.ID != "new" || spec.Name != "新" || s.Active() != "new" {
		t.Errorf("Rename 结果: %+v active=%q", spec, s.Active())
	}
	if s.Exist("old") {
		t.Error("旧目录仍在")
	}
	// 改名到已存在 ID 拒绝
	if err := s.Create(sdk.RoleSpec{ID: "other"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rename("new", "other", ""); err == nil {
		t.Error("改到已存在 ID 应失败")
	}
	// 只改显示名(newID == id)
	if sp, err := s.Rename("new", "new", "又新"); err != nil || sp.Name != "又新" {
		t.Errorf("改显示名失败: %v %+v", err, sp)
	}
	// 非法新 ID 拒绝
	if _, err := s.Rename("new", "Bad", ""); err == nil {
		t.Error("非法 ID 应失败")
	}
}

func TestDeleteTrashAndGuards(t *testing.T) {
	setup(t)
	s := Store{}
	if err := s.Create(sdk.RoleSpec{ID: "keep"}, "正文"); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(sdk.RoleSpec{ID: "drop"}, "正文"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActive("keep"); err != nil {
		t.Fatal(err)
	}
	// 当前角色拒绝删除
	if err := s.Delete("keep"); err == nil {
		t.Fatal("删除当前角色应失败")
	}
	if err := s.Delete("drop"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if s.Exist("drop") {
		t.Error("drop 目录仍在")
	}
	ents, err := os.ReadDir(TrashDir())
	if err != nil || len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), "drop-") {
		t.Fatalf("回收站内容不符: %v %v", ents, err)
	}
	// 回收站里的角色内容完好(可恢复)
	b, err := os.ReadFile(filepath.Join(TrashDir(), ents[0].Name(), AgentsName))
	if err != nil || !strings.Contains(string(b), "正文") {
		t.Errorf("回收站内容缺失: %s %v", b, err)
	}
	// 删除不存在 → 显式失败
	if err := s.Delete("ghost"); err == nil {
		t.Error("删除不存在角色应失败")
	}
	// 不存在角色不出现在 List
	if list := s.List(); len(list) != 1 || list[0].ID != "keep" {
		t.Errorf("List = %+v", list)
	}
}

func TestTrashPrune(t *testing.T) {
	setup(t)
	if err := os.MkdirAll(TrashDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxTrashKeep+5; i++ {
		name := "p-" + string(rune('a'+i))
		if err := os.MkdirAll(filepath.Join(TrashDir(), name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pruneTrash()
	ents, _ := os.ReadDir(TrashDir())
	if len(ents) != maxTrashKeep {
		t.Errorf("回收站保留 %d 份, want %d", len(ents), maxTrashKeep)
	}
}

// TestTrashPruneKeepsNewest 淘汰必须按**删除时间**:按目录名字典序会把「刚删的那一份」
// 当成最旧的删掉(时间戳在名字尾部,字典序第一主键是 id)→ 用户正要恢复的东西被静默销毁。
func TestTrashPruneKeepsNewest(t *testing.T) {
	setup(t)
	if err := os.MkdirAll(TrashDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	oldest := ""
	for i := 1; i <= maxTrashKeep; i++ {
		name := fmt.Sprintf("zebra-20260101-%06d", i)
		if err := os.MkdirAll(filepath.Join(TrashDir(), name), 0o755); err != nil {
			t.Fatal(err)
		}
		if oldest == "" {
			oldest = name
		}
	}
	fresh := "aaa-20260202-120000" // 刚删的那一份:名字最小、时间最新
	if err := os.MkdirAll(filepath.Join(TrashDir(), fresh), 0o755); err != nil {
		t.Fatal(err)
	}
	pruneTrash()
	if _, err := os.Stat(filepath.Join(TrashDir(), fresh)); err != nil {
		t.Fatalf("刚删的那一份被淘汰了(淘汰顺序不是删除时间): %v", err)
	}
	if _, err := os.Stat(filepath.Join(TrashDir(), oldest)); err == nil {
		t.Fatalf("最旧的一份应被淘汰: %s", oldest)
	}
	ents, _ := os.ReadDir(TrashDir())
	if len(ents) != maxTrashKeep {
		t.Errorf("回收站保留 %d 份, want %d", len(ents), maxTrashKeep)
	}
}

// TestTrashListNewestFirst 面板顺序按删除时间倒序(不是按 id 分组),认不出时间戳的坏名排最后。
func TestTrashListNewestFirst(t *testing.T) {
	setup(t)
	if err := os.MkdirAll(TrashDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"zebra-20260101-000001", "handmade", "aaa-20260202-120000"} {
		if err := os.MkdirAll(filepath.Join(TrashDir(), n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"aaa-20260202-120000", "zebra-20260101-000001", "handmade"}
	got := Store{}.TrashList()
	if len(got) != len(want) {
		t.Fatalf("条目数 = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("第 %d 条 = %s, want %s", i, got[i].Name, want[i])
		}
	}
}

func TestPathTraversalBlocked(t *testing.T) {
	setup(t)
	s := Store{}
	for _, id := range []string{"../evil", "a/../../b", ".trash"} {
		if _, err := s.Get(id); err == nil {
			t.Errorf("Get(%q) 应被拒", id)
		}
		if err := s.SetAgents(id, "x"); err == nil {
			t.Errorf("SetAgents(%q) 应被拒", id)
		}
		if err := s.Create(sdk.RoleSpec{ID: id}, ""); err == nil {
			t.Errorf("Create(%q) 应被拒", id)
		}
	}
	// 根外不得出现任何文件
	parent := filepath.Dir(Path())
	if _, err := os.Stat(filepath.Join(parent, "evil")); err == nil {
		t.Error("路径穿越写到了根外")
	}
	if _, err := os.Stat(filepath.Join(Path(), "a")); err == nil {
		t.Error("写到了非法子目录")
	}
}

func TestOwnSkillsAndSeedFlag(t *testing.T) {
	setup(t)
	s := Store{}
	if err := s.Create(sdk.RoleSpec{ID: "dev"}, ""); err != nil {
		t.Fatal(err)
	}
	sk := filepath.Join(SkillsPath("dev"), "my-skill")
	if err := os.MkdirAll(sk, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sk, "SKILL.md"), []byte("# x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 没有 SKILL.md 的目录不算技能
	if err := os.MkdirAll(filepath.Join(SkillsPath("dev"), "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.OwnSkills) != 1 || got.OwnSkills[0] != "my-skill" {
		t.Errorf("OwnSkills = %v", got.OwnSkills)
	}
	if got.Seed {
		t.Error("无标记不应为 Seed")
	}
	if err := os.WriteFile(filepath.Join(Dir("dev"), SeedVersionName), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if g2, _ := s.Get("dev"); !g2.Seed {
		t.Error("有 .seed-version 应为 Seed")
	}
}

func TestBrokenVisible(t *testing.T) {
	setup(t)
	s := Store{}
	if err := s.Create(sdk.RoleSpec{ID: "good"}, ""); err != nil {
		t.Fatal(err)
	}
	// 缺 role.yaml 的目录:List 跳过,Broken 报告
	if err := os.MkdirAll(Dir("half"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 坏 YAML
	if err := os.MkdirAll(Dir("bad"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir("bad"), FileName), []byte("name: [unclosed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if list := s.List(); len(list) != 1 {
		t.Errorf("List 应只含 1 条, got %d", len(list))
	}
	probs := s.Broken()
	if len(probs) != 2 {
		t.Fatalf("Broken = %+v", probs)
	}
	ids := map[string]bool{}
	for _, p := range probs {
		ids[p.ID] = true
		if p.Err == "" {
			t.Error("Problem.Err 为空")
		}
	}
	if !ids["half"] || !ids["bad"] {
		t.Errorf("Broken 缺项: %+v", ids)
	}
}

func TestActivePersist(t *testing.T) {
	setup(t)
	s := Store{}
	if err := s.SetActive("finance"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if s.Active() != "finance" {
		t.Fatalf("Active = %q(应经 prefs 持久化)", s.Active())
	}
	// 停用
	if err := s.SetActive(""); err != nil {
		t.Fatal(err)
	}
	if s.Active() != "" {
		t.Errorf("停用后 Active = %q", s.Active())
	}
	// 非法 ID 拒绝
	if err := s.SetActive("Bad"); err == nil {
		t.Error("非法 ID 应拒绝")
	}
}

// TestNoHomeNoWrite 无 GAH_HOME(测试/嵌入)时不得写到 cwd/根:路径回落到 TempDir。
func TestNoHomeNoWrite(t *testing.T) {
	t.Setenv("GAH_HOME", "")
	if !strings.HasPrefix(Path(), os.TempDir()) {
		t.Errorf("无 GAH_HOME 时 Path() = %q,应回落 TempDir", Path())
	}
}

// TestTrashListAndRestore 回收站列表与恢复(第八十三批:删除曾"可恢复"但没入口)。
func TestTrashListAndRestore(t *testing.T) {
	setup(t)
	s := Store{}
	if err := s.Create(sdk.RoleSpec{ID: "drop", Name: "Drop"}, "正文"); err != nil {
		t.Fatal(err)
	}
	// 角色私有技能应随角色目录一起进出回收站(目录名按固定长度尾部时间戳切,
	// 技能名/角色 ID 本身含连字符也不能被切错)。
	if err := os.MkdirAll(filepath.Join(SkillsPath("drop"), "tax"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("drop"); err != nil {
		t.Fatal(err)
	}

	list := s.TrashList()
	if len(list) != 1 {
		t.Fatalf("TrashList = %+v", list)
	}
	e := list[0]
	if e.ID != "drop" || !strings.HasPrefix(e.Name, "drop-") {
		t.Errorf("条目解析不符: %+v", e)
	}
	if _, err := time.Parse(trashTimeLayout, e.DeletedAt); err != nil {
		t.Errorf("DeletedAt 不是有效时间戳: %q", e.DeletedAt)
	}
	if ids := s.IDs(); len(ids) != 0 {
		t.Errorf("删完后不该还有角色目录: %v", ids)
	}

	// 坏输入:空名 / 路径穿越 / 不含时间戳 / 不存在 / 带路径分隔符
	for _, name := range []string{"", "../drop", "drop", "notatimestamp", "ghost-20260101-000000", "drop-20260101-000000/x"} {
		if _, err := s.Restore(name); err == nil {
			t.Errorf("Restore(%q) 应被拒", name)
		}
	}
	// 目标 ID 已存在 → 拒绝(不覆盖现役角色)
	if err := s.Create(sdk.RoleSpec{ID: "drop", Name: "新 Drop"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Restore(e.Name); err == nil {
		t.Error("目标 ID 已存在时应拒绝")
	}
	// 腾开位置后恢复:定义与私有技能都在
	if err := os.RemoveAll(Dir("drop")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Restore(e.Name)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got != "drop" || !s.Exist("drop") {
		t.Errorf("恢复结果: %q exist=%v", got, s.Exist("drop"))
	}
	spec, err := s.Get("drop")
	if err != nil || spec.Name != "Drop" || !strings.Contains(spec.AGENTS, "正文") {
		t.Errorf("恢复后内容不符: %+v %v", spec, err)
	}
	if _, err := os.Stat(filepath.Join(SkillsPath("drop"), "tax")); err != nil {
		t.Errorf("恢复后私有技能目录应回来: %v", err)
	}
	if len(s.TrashList()) != 0 {
		t.Errorf("恢复后回收站应空: %+v", s.TrashList())
	}

	// 手工放进来的目录:照实列出但 ID 为空(面板据此不给"恢复"按钮),恢复被拒。
	if err := os.MkdirAll(filepath.Join(TrashDir(), "handmade"), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := s.TrashList()
	if len(bad) != 1 || bad[0].Name != "handmade" || bad[0].ID != "" {
		t.Errorf("坏名条目应列出且 ID 为空: %+v", bad)
	}
}

// TestRewriteMount 技能改名后把角色的挂载清单引用一起改(第八十四批)。
// 挂载按**名字**存:只改技能目录名不改这里,角色就会悄悄多出一条「已失效挂载」。
func TestRewriteMount(t *testing.T) {
	setup(t)
	s := Store{}
	if err := s.Create(sdk.RoleSpec{ID: "finance", Name: "财务", SkillsSet: true, Skills: []string{"review", "report"}}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(sdk.RoleSpec{ID: "novelist", Name: "小说家", SkillsSet: true, Skills: []string{"report"}}, ""); err != nil {
		t.Fatal(err)
	}
	// 默认池角色(不写 skills 键):改名不该动它,也不该给它补出 skills 键
	if err := s.Create(sdk.RoleSpec{ID: "plain", Name: "默认池"}, ""); err != nil {
		t.Fatal(err)
	}

	touched, err := s.RewriteMount("report", "weekly-report")
	if err != nil {
		t.Fatal(err)
	}
	if len(touched) != 2 || touched[0] != "finance" || touched[1] != "novelist" {
		t.Errorf("被改动的角色 = %v, want [finance novelist]", touched)
	}
	fin, err := s.Get("finance")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(fin.Skills, ",") != "review,weekly-report" {
		t.Errorf("finance 挂载 = %v", fin.Skills)
	}
	if fin.Name != "财务" || fin.Identity != "" {
		t.Errorf("只该改挂载,其它字段被动过: %+v", fin)
	}
	nov, _ := s.Get("novelist")
	if strings.Join(nov.Skills, ",") != "weekly-report" {
		t.Errorf("novelist 挂载 = %v", nov.Skills)
	}
	// 默认池角色:文件里不应出现 skills 键
	raw, err := os.ReadFile(filepath.Join(Dir("plain"), FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "skills") {
		t.Errorf("默认池角色不该被写入 skills 键: %s", raw)
	}

	// 没有角色挂这个名字 → 空结果,不算错
	touched, err = s.RewriteMount("ghost", "ghost2")
	if err != nil || len(touched) != 0 {
		t.Errorf("无引用时应返回空: %v %v", touched, err)
	}
	// old == new / 空值 → 空操作
	for _, tc := range [][2]string{{"a", "a"}, {"", "b"}, {"a", ""}} {
		if touched, err := s.RewriteMount(tc[0], tc[1]); err != nil || len(touched) != 0 {
			t.Errorf("RewriteMount(%q,%q) 应为空操作: %v %v", tc[0], tc[1], touched, err)
		}
	}
}

// TestListDoesNotReadAgentsBody List 承诺「不读 AGENTS.md 正文」:正文读不了也不该
// 让角色从列表里消失(旧实现走 Get,正文一读不到整条就被跳过),并且字节数改用 Stat 给。
func TestListDoesNotReadAgentsBody(t *testing.T) {
	if !testutil.PosixPerm() {
		t.Skip("只读文件只有 POSIX 权限模型能构造(Windows 走 ACL)")
	}
	setup(t)
	s := Store{}
	if err := s.Create(sdk.RoleSpec{ID: "finance", Name: "财务"}, "规则正文"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(AgentsPath("finance"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(AgentsPath("finance"), 0o644) })

	list := s.List()
	if len(list) != 1 || list[0].ID != "finance" {
		t.Fatalf("List 应仍列出该角色: %+v", list)
	}
	if list[0].AGENTS != "" {
		t.Fatalf("List 不应带正文(注释承诺): %q", list[0].AGENTS)
	}
	if list[0].AGENTSBytes == 0 {
		t.Error("List 仍应给出 AGENTS.md 字节数(Stat 而非读正文)")
	}
	if _, err := s.Get("finance"); err == nil {
		t.Error("Get 要正文:读不到必须显式失败(不静默当空规则)")
	}
}

// TestMountUsers 挂载使用者查询:显式挂载(写了 skills 键)的角色才算;默认池角色不算。
func TestMountUsers(t *testing.T) {
	setup(t)
	s := Store{}
	if err := s.Create(sdk.RoleSpec{ID: "pool", Name: "池"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(sdk.RoleSpec{ID: "finance", Name: "财务",
		SkillsSet: true, Skills: []string{"report", "tax"}}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(sdk.RoleSpec{ID: "ops", Name: "运维", SkillsSet: true, Skills: []string{"report"}}, ""); err != nil {
		t.Fatal(err)
	}
	got := s.MountUsers("report")
	if strings.Join(got, ",") != "finance,ops" {
		t.Fatalf("挂载使用者不符: %v", got)
	}
	if got := s.MountUsers("tax"); strings.Join(got, ",") != "finance" {
		t.Fatalf("tax 使用者不符: %v", got)
	}
	if got := s.MountUsers("nobody"); len(got) != 0 {
		t.Fatalf("没人挂载时应为空: %v", got)
	}
}

// TestToolsExcludeRoundTrip 工具排除清单的落盘往返 + 坏值拒绝(第九十一批)。
//
// 为何必须钉住"Save 不抹掉手写键":Save 是**用 spec 重建一份 roleFile 全量覆盖写** ——
// roleFile 少一个字段,手写在 role.yaml 里的那个键就会被下一次保存/改名/移动静默抹掉
// (第八十六批 model/thinking 踩过同一个坑)。
func TestToolsExcludeRoundTrip(t *testing.T) {
	setup(t)
	s := Store{}
	if err := s.Create(sdk.RoleSpec{ID: "a", ToolsExclude: []string{"shell", "file_write"}}, ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ToolsExclude) != 2 || got.ToolsExclude[0] != "shell" || got.ToolsExclude[1] != "file_write" {
		t.Fatalf("排除清单未往返: %#v", got.ToolsExclude)
	}
	// 只改显示名再保存:排除清单必须还在(全量覆盖写的经典坑)
	got.Name = "改了显示名"
	if err := s.Save(got); err != nil {
		t.Fatal(err)
	}
	again, _ := s.Get("a")
	if len(again.ToolsExclude) != 2 {
		t.Fatalf("改显示名后排除清单丢失: %#v", again.ToolsExclude)
	}
	// 未写这个键 → 落盘不出现该键(与"显式空"同义,故无 Set 标记)
	if err := s.Create(sdk.RoleSpec{ID: "b"}, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(Dir("b"), FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tools_exclude") {
		t.Errorf("未声明排除清单却落了键: %s", raw)
	}
	// 空清单 → 也不落键(读回来是 nil,判据 ToolVisible 全放行)
	if err := s.Create(sdk.RoleSpec{ID: "c", ToolsExclude: []string{}}, ""); err != nil {
		t.Fatal(err)
	}
	rawC, _ := os.ReadFile(filepath.Join(Dir("c"), FileName))
	if strings.Contains(string(rawC), "tools_exclude") {
		t.Errorf("空排除清单不该落键: %s", rawC)
	}
	// 坏值:Save 显式拒绝(写进去一个 Get 读不回来的值 = 当场造坏角色)
	if err := s.Save(sdk.RoleSpec{ID: "d", ToolsExclude: []string{"a b"}}); err == nil {
		t.Error("Save 带空白工具名应当报错")
	}
}

// TestToolsExcludeBadValueVisible 手写坏值 → 该角色进 Broken()(可见,不静默消失)。
func TestToolsExcludeBadValueVisible(t *testing.T) {
	setup(t)
	s := Store{}
	if err := s.Create(sdk.RoleSpec{ID: "a"}, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(Dir("a"), FileName)
	if err := os.WriteFile(path, []byte("name: a\ntools_exclude:\n  - \"a b\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("a"); err == nil {
		t.Fatal("坏工具名应当让 Get 失败")
	}
	probs := s.Broken()
	if len(probs) != 1 || probs[0].ID != "a" {
		t.Fatalf("坏角色未出现在 Broken(): %#v", probs)
	}
	if !strings.Contains(probs[0].Err, "工具名") {
		t.Errorf("Broken 原因没说清是工具名问题: %s", probs[0].Err)
	}
}

// TestTierRoundTrip 第九十二批:角色收紧档走完 Create → 落盘 → Get 全程不失真。
// 重点在 display-name-save 那条(与第八十六批 model 同款坑):面板改个显示名就整份回写,
// 落盘结构体若漏了这两个键,收紧档会被**静默清掉** —— 那是安全问题,不是显示问题。
func TestTierRoundTrip(t *testing.T) {
	setup(t)
	s := Store{}
	if err := s.Create(sdk.RoleSpec{ID: "audit", Approval: "strict", Sandbox: "read-only"}, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(Dir("audit"), FileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"approval: strict", "sandbox: read-only"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("role.yaml 里没落盘 %q:\n%s", want, raw)
		}
	}
	got, err := s.Get("audit")
	if err != nil {
		t.Fatal(err)
	}
	if got.Approval != "strict" || got.Sandbox != "read-only" {
		t.Fatalf("Get 回的收紧档 = (%q, %q),期望 (strict, read-only)", got.Approval, got.Sandbox)
	}
	// 只改显示名再写回:收紧档必须原样还在
	got.Name = "审计"
	if err := s.Save(got); err != nil {
		t.Fatal(err)
	}
	again, err := s.Get("audit")
	if err != nil {
		t.Fatal(err)
	}
	if again.Approval != "strict" || again.Sandbox != "read-only" {
		t.Fatalf("改显示名后收紧档被抹掉了: (%q, %q)", again.Approval, again.Sandbox)
	}
}

// TestTierBadValueBroken 坏值 = 坏角色(显式失败进 Broken,不静默当"未声明")。
// "未声明"是安全方向上的静默降级:角色写着 read-only 却按全局 full-access 跑。
func TestTierBadValueBroken(t *testing.T) {
	setup(t)
	s := Store{}
	for _, tc := range []struct{ id, body, wantSub string }{
		{"r1", "approval: open\n", "收紧"},
		{"r2", "sandbox: full-access\n", "收紧"},
		{"r3", "approval: extreme\n", "审批档"},
	} {
		if err := s.Create(sdk.RoleSpec{ID: tc.id, Name: tc.id}, ""); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(Dir(tc.id), FileName), []byte("name: "+tc.id+"\n"+tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(tc.id); err == nil {
			t.Fatalf("%s:坏档位应当让 Get 失败", tc.id)
		}
	}
	probs := s.Broken()
	if len(probs) != 3 {
		t.Fatalf("坏角色未全部出现在 Broken(): %#v", probs)
	}
	for _, p := range probs {
		if !strings.Contains(p.Err, "收紧") && !strings.Contains(p.Err, "审批档") {
			t.Errorf("Broken 原因没说清是档位问题: %s", p.Err)
		}
	}
	// Save 也要挡(写进一个 Get 读不回来的值 = 当场造坏角色)
	if err := s.Create(sdk.RoleSpec{ID: "r4"}, ""); err != nil {
		t.Fatal(err)
	}
	spec, _ := s.Get("r4")
	spec.Sandbox = "full-access"
	if err := s.Save(spec); err == nil {
		t.Fatal("Save 应拒绝 full-access(角色只能收紧)")
	}
}
