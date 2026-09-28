// 技能库读写单测(路径口径 / 校验 / 原子写 / 回收站)。
package skills

import (
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
