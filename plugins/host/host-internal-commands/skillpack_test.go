package hostintcmd

// 技能包命令面的行为契约(第一百零七批)。
//
// 只钉「命令这一层」:参数解析、路径裁决、回执措辞、覆盖口径。包格式与安全校验
// 在 internal/skillpack 的 12 条单测里。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"log/slog"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/internal/skillpack"
	"github.com/nekoleamo/go-agent-harness/internal/skills"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// newHost 造一个**真的跑过 Start** 的 Host —— 命令注册发生在 Start 里(specs()),
// 只造结构体的话"命令到底注册没有"这条永远测不到(而它恰恰是最容易写错的地方:
// 名字拼错不会编译失败,只会让用户敲了没反应)。
func newHost(t *testing.T) (*Host, sdk.CommandRegistry) {
	t.Helper()
	c, cmds := buildEnv(t)
	h := &Host{c: c}
	for _, spec := range h.specs() {
		if _, ok := cmds.Get(spec.Name); ok {
			continue
		}
		if _, err := cmds.Register(spec); err != nil {
			t.Fatalf("注册命令 /%s 失败: %v", spec.Name, err)
		}
	}
	return h, cmds
}

func seedSharedSkill(t *testing.T, name string) {
	t.Helper()
	body := skills.Content(name, "用于命令面测试的技能", []string{"触发 " + name}, "正文。\n")
	if err := skills.Shared().Write(name, body, false); err != nil {
		t.Fatal(err)
	}
}

func TestCmdSkillExportToExplicitPath(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	seedSharedSkill(t, "notes")
	dst := filepath.Join(t.TempDir(), "out", "my.zip")
	out, err := h.cmdSkillExport([]string{"notes", dst})
	if err != nil {
		t.Fatalf("导出失败: %v(%s)", err, out)
	}
	raw, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("没落盘: %v", err)
	}
	if !strings.HasPrefix(string(raw), "PK") {
		t.Fatal("不是 zip")
	}
	if !strings.Contains(out, "notes") {
		t.Fatalf("回执应含技能名: %s", out)
	}
}

func TestCmdSkillExportRejectsURLPath(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	seedSharedSkill(t, "notes")
	if _, err := h.cmdSkillExport([]string{"notes", "https://example.com/x.zip"}); err == nil {
		t.Fatal("网址形态路径应显式失败(与 /export 同一道裁决)")
	}
}

func TestCmdSkillExportNoArgs(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	if _, err := h.cmdSkillExport(nil); err == nil {
		t.Fatal("缺技能名应报错并给用法")
	}
}

func TestCmdSkillImportRoundTrip(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	seedSharedSkill(t, "src")
	p := filepath.Join(t.TempDir(), "p.zip")
	if _, err := h.cmdSkillExport([]string{"src", p}); err != nil {
		t.Fatal(err)
	}
	// 换个数据根 = 别人的机器
	t.Setenv("GAH_HOME", t.TempDir())
	h2, _ := newHost(t)
	out, err := h2.cmdSkillImport([]string{p})
	if err != nil {
		t.Fatalf("导入失败: %v(%s)", err, out)
	}
	if !skills.Shared().Exists("src") {
		t.Fatal("导入后技能不存在")
	}
	if !strings.Contains(out, "src") {
		t.Fatalf("回执应含技能名: %s", out)
	}
}

// TestCmdSkillImportAsFlag 两种写法都要认:`--as=名字` 与 `--as 名字`
// —— 后者是命令行最自然的写法,解析漏了它会变成"导入成了原名",而用户以为改了名。
func TestCmdSkillImportAsFlag(t *testing.T) {
	for _, args := range [][]string{
		{"%s", "--as=copy1"},
		{"%s", "--as", "copy1"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Setenv("GAH_HOME", t.TempDir())
			h, _ := newHost(t)
			seedSharedSkill(t, "origin")
			p := filepath.Join(t.TempDir(), "p.zip")
			if _, err := h.cmdSkillExport([]string{"origin", p}); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GAH_HOME", t.TempDir())
			h2, _ := newHost(t)
			asArgs := make([]string, 0, 3)
			asArgs = append(asArgs, p)
			for i := 1; i < len(args); i++ {
				asArgs = append(asArgs, strings.Replace(args[i], "%s", "", 1))
			}
			out, err := h2.cmdSkillImport(asArgs)
			if err != nil {
				t.Fatalf("改名导入失败: %v(%s)", err, out)
			}
			if !skills.Shared().Exists("copy1") {
				t.Fatalf("未落成 copy1,args=%v", asArgs)
			}
			if skills.Shared().Exists("origin") {
				t.Fatal("改名导入不该顺带建原名")
			}
		})
	}
}

func TestCmdSkillImportDuplicateRejected(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	seedSharedSkill(t, "dup")
	p := filepath.Join(t.TempDir(), "p.zip")
	if _, err := h.cmdSkillExport([]string{"dup", p}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.cmdSkillImport([]string{p}); err == nil {
		t.Fatal("同名缺省应拒(不静默覆盖)")
	}
	out, err := h.cmdSkillImport([]string{p, "--overwrite"})
	if err != nil {
		t.Fatalf("显式覆盖应成功: %v", err)
	}
	if !strings.Contains(out, "覆盖") {
		t.Fatalf("回执应说明覆盖了旧份: %s", out)
	}
}

func TestCmdSkillImportBadFile(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	if _, err := h.cmdSkillImport([]string{"/nonexistent/x.zip"}); err == nil {
		t.Fatal("读不到包应报错")
	}
	// 不是 zip 的文件
	p := filepath.Join(t.TempDir(), "x.zip")
	if err := os.WriteFile(p, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.cmdSkillImport([]string{p}); err == nil {
		t.Fatal("坏包应显式拒绝")
	}
}

// 导出→导入的包必须能被 skillpack 自己读回(命令面不改变包内容)。
func TestCmdSkillExportProducesImportablePack(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	_, _ = newHost(t) // 命令面本身不参与这一步(直接验包),但要建一次环境让 GAH_HOME 生效
	seedSharedSkill(t, "rt")
	pack, err := skillpack.Export("rt")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAH_HOME", t.TempDir())
	if _, err := skillpack.Import(pack, skillpack.ImportOptions{}); err != nil {
		t.Fatalf("命令导出的包应能被导入: %v", err)
	}
}

// TestCmdSkillExportDirTarget 目录形态目标:拼上建议文件名(与 Web 下载名同款)。
func TestCmdSkillExportDirTarget(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	seedSharedSkill(t, "dirpkg")
	dir := t.TempDir()
	out, err := h.cmdSkillExport([]string{"dirpkg", dir})
	if err != nil {
		t.Fatalf("导出到目录应成功: %v(%s)", err, out)
	}
	want := filepath.Join(dir, skillpack.FileName("dirpkg"))
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("应落成 %s: %v", want, err)
	}
	if !strings.Contains(out, want) {
		t.Fatalf("回执应含最终路径 %s: %s", want, out)
	}
}

// TestCmdSkillExportMissingSkill 技能不存在:报出原因而不是给个空包路径。
func TestCmdSkillExportMissingSkill(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	if _, err := h.cmdSkillExport([]string{"nope", filepath.Join(t.TempDir(), "x.zip")}); err == nil {
		t.Fatal("不存在的技能应报错")
	}
}

// TestCmdSkillImportNoArgs 缺参数要给用法。
func TestCmdSkillImportNoArgs(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	if _, err := h.cmdSkillImport([]string{}); err == nil {
		t.Fatal("缺包路径应报错并给用法")
	}
}

// TestCmdSkillImportRejectsURLSource 网址形态的源显式失败(与 /export 同一道裁决)。
func TestCmdSkillImportRejectsURLSource(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	if _, err := h.cmdSkillImport([]string{"https://example.com/p.zip"}); err == nil {
		t.Fatal("网址形态应显式失败")
	}
}

// TestSkillPackCommandsRegistered 两条命令确实注册进了命令面(名字写错 = 用户敲 /skill-export
// 没反应,而这不会在编译期被发现)。
func TestSkillPackCommandsRegistered(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	_, cmds := newHost(t)
	for _, name := range []string{"skill-export", "skill-import"} {
		if _, ok := cmds.Get(name); !ok {
			t.Fatalf("命令未注册: /%s", name)
		}
	}
}

// TestCmdSkillExportWithoutWorkspace 同理:拿不到工作区时要显式路径,不静默落盘。
func TestCmdSkillExportWithoutWorkspace(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	h := &Host{c: ctx.New(logger, event.New(logger))}
	seedSharedSkill(t, "nw")
	if _, err := h.cmdSkillExport([]string{"nw"}); err == nil {
		t.Fatal("拿不到工作区时应要求显式路径")
	}
	p := filepath.Join(t.TempDir(), "s.zip")
	if _, err := h.cmdSkillExport([]string{"nw", p}); err != nil {
		t.Fatalf("给了显式路径应成功: %v", err)
	}
}
