// 技能库读写单测(路径口径 / 校验 / 原子写 / 回收站)。
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) Library {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	return Shared()
}

func TestValidateName(t *testing.T) {
	ok := []string{"a", "code-review", "my.skill_1", "x0123456789"}
	for _, n := range ok {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	bad := []string{"", ".hidden", "..", "A", "a/b", `a\b`, "a b", "带中文", "a" + strings.Repeat("b", 64)}
	for _, n := range bad {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", n)
		}
	}
}

func TestWriteReadOverwriteSemantics(t *testing.T) {
	lib := setup(t)
	c := Content("review", "代码审查", []string{"CR", "review"}, "# 步骤\n1. 读 diff")
	if err := lib.Write("review", c, false); err != nil {
		t.Fatal(err)
	}
	if !lib.Exists("review") {
		t.Fatal("写入后应存在")
	}
	got, err := lib.Read("review")
	if err != nil || got != c {
		t.Fatalf("读回不一致: %v\n%q", err, got)
	}
	// 已存在 + 不覆盖 → 显式失败(不静默改写)
	if err := lib.Write("review", c, false); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("已存在应显式失败: %v", err)
	}
	// 覆盖写
	c2 := Content("review", "代码审查 v2", nil, "改过了")
	if err := lib.Write("review", c2, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := lib.Read("review"); !strings.Contains(got, "改过了") {
		t.Fatalf("覆盖后内容未更新: %q", got)
	}
	// 非法名 → 显式失败,且不落盘
	if err := lib.Write("../escape", c, true); err == nil {
		t.Fatal("路径穿越名应被拒")
	}
	if _, err := os.Stat(filepath.Join(lib.Root, "..", "escape")); err == nil {
		t.Fatal("不应写出库外文件")
	}
	// 读不存在的技能
	if _, err := lib.Read("nope"); err == nil {
		t.Fatal("读不存在技能应失败")
	}
}

func TestWriteGuardsSizeAndNameMismatch(t *testing.T) {
	lib := setup(t)
	big := strings.Repeat("x", MaxBytes+1)
	if err := lib.Write("big", big, false); err == nil || !strings.Contains(err.Error(), "超上限") {
		t.Fatalf("超上限应被拒: %v", err)
	}
	if lib.Exists("big") {
		t.Fatal("超限写入不应留下技能")
	}
	// frontmatter name 与目录名不一致 → 拒(否则扫描名/目录名两个口径打架)
	mismatch := Content("other", "描述", nil, "正文")
	if err := lib.Write("mine", mismatch, false); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("name 不一致应被拒: %v", err)
	}
	// 无 frontmatter 的正文:允许(扫描侧回退目录名),不因缺 name 报错
	if err := lib.Write("plain", "just body", false); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveMovesToTrashAndPrunes(t *testing.T) {
	lib := setup(t)
	if err := lib.Write("gone", Content("gone", "d", nil, "b"), false); err != nil {
		t.Fatal(err)
	}
	if err := lib.Remove("gone"); err != nil {
		t.Fatal(err)
	}
	if lib.Exists("gone") {
		t.Fatal("删除后不应仍在库里")
	}
	trash := filepath.Join(lib.Root, TrashName)
	if _, err := os.Stat(trash); err != nil {
		t.Fatalf("应落回收站: %v", err)
	}
	// 重复删除 → 显式失败
	if err := lib.Remove("gone"); err == nil {
		t.Fatal("重复删除应失败")
	}
	// 回收站轮转:造 maxTrashKeep+2 份后应被裁到上限
	for i := 0; i < maxTrashKeep+2; i++ {
		d := filepath.Join(trash, "x-20260101-0000"+string(rune('a'+i)))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pruneTrash(trash)
	entries, err := os.ReadDir(trash)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != maxTrashKeep {
		t.Fatalf("回收站应裁到 %d 份,实际 %d", maxTrashKeep, len(entries))
	}
}

// TestTrashPruneKeepsNewest 淘汰必须按**删除时间**:按目录名字典序会把「刚删的那一份」
// 当成最旧的删掉(时间戳在名字尾部,字典序第一主键是技能名)。
func TestTrashPruneKeepsNewest(t *testing.T) {
	lib := setup(t)
	trash := filepath.Join(lib.Root, TrashName)
	if err := os.MkdirAll(trash, 0o755); err != nil {
		t.Fatal(err)
	}
	oldest := ""
	for i := 1; i <= maxTrashKeep; i++ {
		name := fmt.Sprintf("zebra-20260101-%06d", i)
		if err := os.MkdirAll(filepath.Join(trash, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if oldest == "" {
			oldest = name
		}
	}
	fresh := "aaa-20260202-120000" // 刚删的那一份:名字最小、时间最新
	if err := os.MkdirAll(filepath.Join(trash, fresh), 0o755); err != nil {
		t.Fatal(err)
	}
	pruneTrash(trash)
	if _, err := os.Stat(filepath.Join(trash, fresh)); err != nil {
		t.Fatalf("刚删的那一份被淘汰了(淘汰顺序不是删除时间): %v", err)
	}
	if _, err := os.Stat(filepath.Join(trash, oldest)); err == nil {
		t.Fatalf("最旧的一份应被淘汰: %s", oldest)
	}
}

// TestTrashListNewestFirst 列表按删除时间倒序(不是按技能名分组),坏名排最后。
func TestTrashListNewestFirst(t *testing.T) {
	lib := setup(t)
	trash := filepath.Join(lib.Root, TrashName)
	if err := os.MkdirAll(trash, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"zebra-20260101-000001", "handmade", "aaa-20260202-120000"} {
		if err := os.MkdirAll(filepath.Join(trash, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"aaa-20260202-120000", "zebra-20260101-000001", "handmade"}
	got := lib.TrashList()
	if len(got) != len(want) {
		t.Fatalf("条目数 = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("第 %d 条 = %s, want %s", i, got[i].Name, want[i])
		}
	}
}

func TestForRolePathAndParsing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	lib := ForRole("finance")
	want := filepath.Join(home, "roles", "finance", "skills", "tax", FileName)
	if lib.Path("tax") != want {
		t.Fatalf("角色私有技能路径不符: %s", lib.Path("tax"))
	}
	if err := lib.Write("tax", Content("tax", "税务口径", []string{"税"}, "先看口径"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("角色私有技能未落盘: %v", err)
	}
	// frontmatter 解析:标准形 / 无 frontmatter / 键缺失
	if n := ParseName(Content("tax", "d", nil, "b")); n != "tax" {
		t.Fatalf("ParseName = %q", n)
	}
	if n := ParseName("正文"); n != "" {
		t.Fatalf("无 frontmatter 应返回空,got %q", n)
	}
	if n := ParseName("---\ndescription: d\n---\n正文"); n != "" {
		t.Fatalf("无 name 键应返回空,got %q", n)
	}
}

func TestContentQuotesTrickyValues(t *testing.T) {
	// 含冒号的描述必须被引起来,否则 frontmatter 解析会把 `A: B` 当嵌套映射
	c := Content("x", "口径: 先数字后结论", []string{"a: b"}, "正文")
	if !strings.Contains(c, `description: "口径: 先数字后结论"`) {
		t.Fatalf("含冒号的值应加引号:\n%s", c)
	}
	if !strings.Contains(c, `- "a: b"`) {
		t.Fatalf("触发词同样应加引号:\n%s", c)
	}
}

// TestTrashListAndRestore 回收站列表与恢复(第八十三批:删除曾"可恢复"但没入口)。
func TestTrashListAndRestore(t *testing.T) {
	lib := setup(t)
	// 技能名本身可含连字符 —— 解析只能按**固定长度的尾部时间戳**切,不能按"最后一个连字符"。
	if err := lib.Write("a-b-c", Content("a-b-c", "", nil, "body-a"), false); err != nil {
		t.Fatal(err)
	}
	if err := lib.Write("gone", Content("gone", "d", nil, "body-gone"), false); err != nil {
		t.Fatal(err)
	}
	if err := lib.Remove("gone"); err != nil {
		t.Fatal(err)
	}
	if err := lib.Remove("a-b-c"); err != nil {
		t.Fatal(err)
	}

	list := lib.TrashList()
	if len(list) != 2 {
		t.Fatalf("TrashList = %+v", list)
	}
	bySkill := map[string]TrashEntry{}
	for _, e := range list {
		bySkill[e.Skill] = e
	}
	if _, ok := bySkill["a-b-c"]; !ok {
		t.Errorf("含连字符的技能名解析失败: %+v", list)
	}
	if e, ok := bySkill["gone"]; !ok || e.DeletedAt == "" {
		t.Errorf("技能名/时间戳解析失败: %+v", list)
	}
	// 最近的在前(目录名字典序倒序 = 时间戳倒序)
	if list[0].Name < list[1].Name {
		t.Errorf("应最近在前: %+v", list)
	}

	// 坏输入:空名 / 原名 / 路径 / 不含时间戳 / 不存在
	for _, name := range []string{"", "gone", "a/b", "..", "ghost-20260101-000000"} {
		if _, err := lib.Restore(name); err == nil {
			t.Errorf("Restore(%q) 应被拒", name)
		}
	}
	// 同名已存在 → 拒绝(不静默覆盖现役技能)
	if err := lib.Write("gone", Content("gone", "", nil, "new"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Restore(bySkill["gone"].Name); err == nil {
		t.Error("同名已存在时应拒绝恢复")
	}
	// 腾开位置后恢复:正文回来
	if err := os.RemoveAll(filepath.Join(lib.Root, "gone")); err != nil {
		t.Fatal(err)
	}
	got, err := lib.Restore(bySkill["gone"].Name)
	if err != nil || got != "gone" {
		t.Fatalf("Restore = %q, %v", got, err)
	}
	if raw, err := lib.Read("gone"); err != nil || !strings.Contains(raw, "body-gone") {
		t.Errorf("恢复后正文不符: %q %v", raw, err)
	}

	// 手工放进来的目录:照实列出但 Skill 为空,恢复被拒(不猜)。
	if err := os.MkdirAll(filepath.Join(lib.Root, TrashName, "handmade"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Restore("handmade"); err == nil {
		t.Error("不含时间戳的目录应拒绝恢复")
	}
	found := false
	for _, e := range lib.TrashList() {
		if e.Name == "handmade" && e.Skill == "" {
			found = true
		}
	}
	if !found {
		t.Errorf("坏名条目应列出且 Skill 为空: %+v", lib.TrashList())
	}
}

// TestRelocate 技能改名 / 跨库移动(第八十四批):目录名即身份,frontmatter 的 name 必须一起改。
func TestRelocate(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	shared := Shared()
	role := ForRole("finance")
	if err := shared.Write("review", Content("review", "代码审查", []string{"CR"}, "# 步骤"), false); err != nil {
		t.Fatal(err)
	}

	// ① 只换库(名字不变):正文一字不动(frontmatter 无需改写)
	if got, err := shared.Relocate("review", role, ""); err != nil || got != "review" {
		t.Fatalf("换库 = %q, %v", got, err)
	}
	if shared.Exists("review") || !role.Exists("review") {
		t.Fatal("技能目录应已搬进角色私有库")
	}
	if raw, err := role.Read("review"); err != nil || !strings.Contains(raw, "name: review") || !strings.Contains(raw, "# 步骤") {
		t.Fatalf("换库后正文不符: %q %v", raw, err)
	}

	// ② 只改名(同库):目录名与 frontmatter name 一起改,其它键/正文不动
	if got, err := role.Relocate("review", role, "code-review"); err != nil || got != "code-review" {
		t.Fatalf("改名 = %q, %v", got, err)
	}
	if role.Exists("review") || !role.Exists("code-review") {
		t.Fatal("目录名未改")
	}
	raw, _ := role.Read("code-review")
	if !strings.Contains(raw, "name: code-review") || strings.Contains(raw, "name: review\n") {
		t.Errorf("frontmatter name 未同步: %q", raw)
	}
	if !strings.Contains(raw, "代码审查") || !strings.Contains(raw, "# 步骤") {
		t.Errorf("正文/其它键被改坏: %q", raw)
	}
	if err := ValidateName("code-review"); err != nil {
		t.Fatal(err)
	}

	// ③ 改名 + 换库一起(搬回共享库);Write 的不变量(署名 == 目录名)必须仍成立
	if got, err := role.Relocate("code-review", shared, "review2"); err != nil || got != "review2" {
		t.Fatalf("改名+换库 = %q, %v", got, err)
	}
	if !shared.Exists("review2") || role.Exists("code-review") {
		t.Fatal("改名+换库后位置不对")
	}
	if raw, _ := shared.Read("review2"); ParseName(raw) != "review2" {
		t.Errorf("归位后 frontmatter name 与目录名不一致: %q", raw)
	}

	// ④ 目标同名已存在 → 显式拒绝(不覆盖),两边都不动
	for _, n := range []string{"dup", "keep"} {
		if err := shared.Write(n, Content(n, "", nil, "b-"+n), false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := shared.Relocate("keep", shared, "dup"); err == nil || !strings.Contains(err.Error(), "同名") {
		t.Fatalf("目标同名应被拒: %v", err)
	}
	if !shared.Exists("keep") || !shared.Exists("dup") {
		t.Error("被拒时不应动任何一边")
	}

	// ⑤ 无变化 / 不存在 / 非法名字 → 显式失败,且旧技能留在原地
	for _, tc := range []struct{ from, to string }{
		{"review2", "review2"}, {"ghost", ""}, {"bad/name", ""}, {"review2", "Bad Name"},
	} {
		if _, err := shared.Relocate(tc.from, shared, tc.to); err == nil {
			t.Errorf("Relocate(%q→%q) 应被拒", tc.from, tc.to)
		}
	}
	if !shared.Exists("review2") {
		t.Error("被拒后旧技能应仍在")
	}

	// ⑥ 无 frontmatter / 无 name 键:改名不硬造键(扫描侧会回退目录名)
	if err := shared.Write("plain", "just body\n", false); err != nil {
		t.Fatal(err)
	}
	if _, err := shared.Relocate("plain", shared, "plain2"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := shared.Read("plain2"); raw != "just body\n" {
		t.Errorf("无 frontmatter 的正文被改: %q", raw)
	}
	if err := shared.Write("noname", "---\ndescription: d\n---\nbody\n", false); err != nil {
		t.Fatal(err)
	}
	if _, err := shared.Relocate("noname", shared, "noname2"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := shared.Read("noname2"); !strings.Contains(raw, "description: d") || strings.Contains(raw, "name:") {
		t.Errorf("无 name 键时不应造键: %q", raw)
	}
}

// TestFrontmatterNestedKeysUntouched 嵌套键(如 metadata 下的 name)不是顶层 name:
// 用 TrimSpace 匹配会连它一起改写 → 出现两个顶层 name → 扫描侧整段 meta 解析失败
// (description/trigger 静默丢失,索引里看不出异常)。
func TestFrontmatterNestedKeysUntouched(t *testing.T) {
	content := "---\nname: review\nmetadata:\n  name: 内部名\ndescription: d\n---\n正文"
	if got := ParseName(content); got != "review" {
		t.Fatalf("ParseName 应取顶层 name,得到 %q", got)
	}
	got := renameFrontmatterName(content, "code-review")
	if n := strings.Count(got, "\nname: "); n != 1 {
		t.Fatalf("应只改顶层 name(实际改了 %d 行):\n%s", n, got)
	}
	if !strings.Contains(got, "\n  name: 内部名") {
		t.Fatalf("嵌套键应原样保留:\n%s", got)
	}
	if !strings.Contains(got, "description: d") || !strings.HasPrefix(got, "---\nname: code-review\n") {
		t.Fatalf("其它键/正文前缀应原样保留:\n%s", got)
	}
}

// TestQuoteYAMLEscapesBackslash 双引号分支必须先转义反斜杠:YAML 双引号标量里 `\` 是
// 转义引导符,否则 Windows 路径要么生成非法 YAML(`\l` 未知转义),要么被静默改写(`\t` 变 TAB)。
func TestQuoteYAMLEscapesBackslash(t *testing.T) {
	if got, want := quoteYAML(`C:\temp\reports`), `"C:\\temp\\reports"`; got != want {
		t.Fatalf("quoteYAML = %s, want %s", got, want)
	}
	// 不含特殊字符的值保持裸写(不引入多余引号)
	if got := quoteYAML("普通描述"); got != "普通描述" {
		t.Fatalf("裸写值不该加引号: %s", got)
	}
}
