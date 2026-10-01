package skillpack

// 技能包的**安全与格式**契约(第一百零六批)。
//
// 解包是唯一一处「内容来自外部」的入口,所以测试按最坏情况写:
// 白名单条目 / 路径穿越 / 上限 / 坏格式 / 不静默覆盖 / 改名导入的 frontmatter 一致性。
// 其中「改名导入」这一条是实测会踩的:skills.Write 显式校验「frontmatter 的 name ==
// 目录名」,不改就会被拒 —— 那正是它该有的行为,包里要改的是导入侧。

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/skills"
)

func setup(t *testing.T) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
}

// seed 写一个共享技能并返回名字。
func seed(t *testing.T, name, desc string) {
	t.Helper()
	body := skills.Content(name, desc, []string{"当需要 " + name + " 时"}, "正文:示范。\n")
	if err := skills.Shared().Write(name, body, false); err != nil {
		t.Fatal(err)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	setup(t)
	seed(t, "summarize", "把长文压成要点")
	pack, err := Export("summarize")
	if err != nil {
		t.Fatal(err)
	}
	if len(pack) > MaxPackBytes {
		t.Fatalf("包 %d 字节,超上限", len(pack))
	}
	// 换个数据根导入(模拟"给别人用")
	t.Setenv("GAH_HOME", t.TempDir())
	res, err := Import(pack, ImportOptions{})
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if res.Name != "summarize" || res.Replaced {
		t.Fatalf("导入结果不对: %+v", res)
	}
	got, err := skills.Shared().Read("summarize")
	if err != nil {
		t.Fatalf("导入后读不到: %v", err)
	}
	if !strings.Contains(got, "把长文压成要点") {
		t.Fatalf("导入后正文不符: %q", got)
	}
}

// TestExportDeterministic 同一份技能 + 同一时刻 ⇒ 同一份包(否则每次导出都产生 diff)。
func TestExportDeterministic(t *testing.T) {
	setup(t)
	seed(t, "det", "确定性")
	now := time.Now()
	a, err := exportAt("det", now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := exportAt("det", now)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("同一时刻两次导出字节不同")
	}
}

func TestExportMissingSkill(t *testing.T) {
	setup(t)
	if _, err := Export("nope"); err == nil {
		t.Fatal("导出不存在的技能应报错,不能产出半截包")
	}
}

func TestExportIllegalName(t *testing.T) {
	setup(t)
	if _, err := Export("../escape"); err == nil {
		t.Fatal("非法技能名应被拒(它是写盘路径的来源)")
	}
}

// makePack 造一个包(可塞任意条目,用来测白名单与穿越)。
func makePack(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, c := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func goodManifest(name string) string {
	m := Manifest{Format: Format, Version: Version, Name: name, ExportedAt: time.Now().UTC().Format(time.RFC3339)}
	b, _ := json.Marshal(m)
	return string(b)
}

func TestImportRejectsUnknownEntry(t *testing.T) {
	setup(t)
	pack := makePack(t, map[string]string{
		ManifestName:    goodManifest("x"),
		"SKILL.md":      "x",
		"../../evil.sh": "boom",
	})
	if _, err := Import(pack, ImportOptions{}); err == nil {
		t.Fatal("不认识的条目(尤其路径穿越形态)必须显式拒绝")
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "evil.sh")); err == nil {
		t.Fatal("不该落盘")
	}
}

func TestImportRejectsBackslashEntry(t *testing.T) {
	setup(t)
	// Windows 上 zip 里可能出现反斜杠;归一化后仍要按白名单判
	pack := makePack(t, map[string]string{
		ManifestName:   goodManifest("x"),
		`sub\SKILL.md`: "x",
	})
	if _, err := Import(pack, ImportOptions{}); err == nil {
		t.Fatal("反斜杠条目名应被拒")
	}
}

func TestImportRejectsBadFormatAndVersion(t *testing.T) {
	setup(t)
	bad := makePack(t, map[string]string{ManifestName: `{"format":"other","version":1,"name":"x"}`, "SKILL.md": "x"})
	if _, err := Import(bad, ImportOptions{}); err == nil {
		t.Fatal("格式标识不认识必须拒(不猜)")
	}
	old := makePack(t, map[string]string{ManifestName: `{"format":"gah-skill","version":0,"name":"x"}`, "SKILL.md": "x"})
	if _, err := Import(old, ImportOptions{}); err == nil {
		t.Fatal("包版本不认识必须拒(不静默丢字段)")
	}
	// 未知键也拒:包里可能来自更新的 gah
	unk := makePack(t, map[string]string{ManifestName: `{"format":"gah-skill","version":1,"name":"x","future":1}`, "SKILL.md": "x"})
	if _, err := Import(unk, ImportOptions{}); err == nil {
		t.Fatal("未知键必须拒")
	}
}

func TestImportRejectsMissingEntries(t *testing.T) {
	setup(t)
	onlyMan := makePack(t, map[string]string{ManifestName: goodManifest("x")})
	if _, err := Import(onlyMan, ImportOptions{}); err == nil {
		t.Fatal("缺 SKILL.md 必须拒")
	}
	onlySkill := makePack(t, map[string]string{"SKILL.md": "x"})
	if _, err := Import(onlySkill, ImportOptions{}); err == nil {
		t.Fatal("缺清单必须拒")
	}
}

func TestImportRejectsOversize(t *testing.T) {
	setup(t)
	big := strings.Repeat("a", skills.MaxBytes+1)
	pack := makePack(t, map[string]string{ManifestName: goodManifest("big"), "SKILL.md": big})
	if _, err := Import(pack, ImportOptions{}); err == nil {
		t.Fatal("超上限的正文必须拒(不许导入一个本地写不进去的技能)")
	}
}

func TestImportRejectsIllegalTargetName(t *testing.T) {
	setup(t)
	pack := makePack(t, map[string]string{ManifestName: goodManifest("x"), "SKILL.md": "x"})
	if _, err := Import(pack, ImportOptions{As: "../escape"}); err == nil {
		t.Fatal("非法目标名必须拒(写盘路径由它拼)")
	}
}

// TestImportDoesNotOverwriteSilently 缺省不覆盖(与本地写入、角色包同款)。
func TestImportDoesNotOverwriteSilently(t *testing.T) {
	setup(t)
	seed(t, "dup", "原来的")
	pack, err := Export("dup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Import(pack, ImportOptions{}); err == nil {
		t.Fatal("缺省应显式拒绝覆盖")
	}
	got, _ := skills.Shared().Read("dup")
	if !strings.Contains(got, "原来的") {
		t.Fatal("被拒的导入不该改动原技能")
	}
	// 显式覆盖:允许(回执要如实说"替换过")
	res, err := Import(pack, ImportOptions{Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Replaced {
		t.Fatal("覆盖时回执应标明 replaced")
	}
}

// TestImportAsRenamesFrontmatter 「导入为另一个名字」必须同时改写正文 frontmatter 的 name
// —— skills.Write 显式校验两者一致,不改就会被拒(这正是它该有的行为)。
func TestImportAsRenamesFrontmatter(t *testing.T) {
	setup(t)
	seed(t, "origin", "原始技能")
	pack, err := Export("origin")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAH_HOME", t.TempDir())
	res, err := Import(pack, ImportOptions{As: "renamed"})
	if err != nil {
		t.Fatalf("改名导入失败(frontmatter 应被同步改写): %v", err)
	}
	if res.Name != "renamed" || res.From != "origin" {
		t.Fatalf("回执应说清新名与原名: %+v", res)
	}
	got, err := skills.Shared().Read("renamed")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "name: renamed") {
		t.Fatalf("改名后 frontmatter 未同步: %q", got)
	}
	// 原名不该同时存在
	if skills.Shared().Exists("origin") {
		t.Fatal("改名导入不该顺带建一个原名技能")
	}
}
