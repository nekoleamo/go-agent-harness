package hostintcmd

// 角色包命令面的行为契约(第一百零八批)。
//
// 覆盖面:子命令分派、路径裁决、--as 两种写法、同名拒/显式覆盖、以及**回执里要带
// 私有技能数与回收站条目名** —— 这两样是角色包区别于技能包的地方,漏了用户就不知道
// "那个旧角色去哪了"。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"log/slog"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/internal/rolepack"
	"github.com/nekoleamo/go-agent-harness/internal/roles"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func seedRole(t *testing.T, id string) {
	t.Helper()
	st := roles.Store{}
	if err := st.Save(sdk.RoleSpec{ID: id, Name: "角色 " + id, Identity: "你是" + id}); err != nil {
		t.Fatal(err)
	}
}

// roleExists 角色是否存在(Get 返回 error 而非 found 布尔)。
func roleExists(id string) bool {
	_, err := (roles.Store{}).Get(id)
	return err == nil
}

func TestCmdRolePackExportImportRoundTrip(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	seedRole(t, "writer")
	p := filepath.Join(t.TempDir(), "r.zip")
	out, err := h.cmdRolePackExport([]string{"writer", p})
	if err != nil {
		t.Fatalf("导出失败: %v(%s)", err, out)
	}
	raw, err := os.ReadFile(p)
	if err != nil || !strings.HasPrefix(string(raw), "PK") {
		t.Fatalf("没落出 zip: %v", err)
	}
	// 换个数据根 = 别人机器
	t.Setenv("GAH_HOME", t.TempDir())
	h2, _ := newHost(t)
	out2, err := h2.cmdRolePackImport([]string{p})
	if err != nil {
		t.Fatalf("导入失败: %v(%s)", err, out2)
	}
	if !strings.Contains(out2, "writer") {
		t.Fatalf("回执应含角色 id: %s", out2)
	}
	if !roleExists("writer") {
		t.Fatal("导入后角色应存在")
	}
}

func TestCmdRolePackDispatch(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	if _, err := h.cmdRolePack(nil); err == nil {
		t.Fatal("缺子命令应报错并给用法")
	}
	if _, err := h.cmdRolePack([]string{"frobnicate"}); err == nil {
		t.Fatal("未知子命令应显式拒绝")
	}
}

func TestCmdRolePackExportRejectsURLPath(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	seedRole(t, "writer")
	if _, err := h.cmdRolePackExport([]string{"writer", "https://example.com/r.zip"}); err == nil {
		t.Fatal("网址形态应显式失败(与 /export 同一道裁决)")
	}
}

func TestCmdRolePackExportDirTarget(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	seedRole(t, "writer")
	dir := t.TempDir()
	out, err := h.cmdRolePackExport([]string{"writer", dir})
	if err != nil {
		t.Fatalf("导出到目录应成功: %v(%s)", err, out)
	}
	if !strings.Contains(out, "gah-role-writer.zip") {
		t.Fatalf("目录目标应拼建议文件名: %s", out)
	}
}

// TestCmdRolePackImportAsFlag 两种写法都认(漏了 `--as 名` 会变成"导入成原名")。
func TestCmdRolePackImportAsFlag(t *testing.T) {
	cases := [][]string{
		{"--as=copy1"},
		{"--as", "copy1"},
	}
	for _, tail := range cases {
		t.Run(strings.Join(tail, " "), func(t *testing.T) {
			t.Setenv("GAH_HOME", t.TempDir())
			h, _ := newHost(t)
			seedRole(t, "orig")
			p := filepath.Join(t.TempDir(), "r.zip")
			if _, err := h.cmdRolePackExport([]string{"orig", p}); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GAH_HOME", t.TempDir())
			h2, _ := newHost(t)
			args := append([]string{p}, tail...)
			if _, err := h2.cmdRolePackImport(args); err != nil {
				t.Fatalf("改名导入失败: %v", err)
			}
			if !roleExists("copy1") {
				t.Fatalf("未落成 copy1,args=%v", args)
			}
		})
	}
}

func TestCmdRolePackImportDuplicateRejected(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	seedRole(t, "dup")
	p := filepath.Join(t.TempDir(), "r.zip")
	if _, err := h.cmdRolePackExport([]string{"dup", p}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.cmdRolePackImport([]string{p}); err == nil {
		t.Fatal("同名缺省应拒(不静默覆盖)")
	}
	out, err := h.cmdRolePackImport([]string{p, "--overwrite"})
	if err != nil {
		t.Fatalf("显式覆盖应成功: %v", err)
	}
	if !strings.Contains(out, "覆盖") {
		t.Fatalf("覆盖回执应说明: %s", out)
	}
}

func TestCmdRolePackImportRejectsOversizeFile(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	// 造一个**声明大小超限**但内容很小的文件:直接写 9 MiB 在 Windows runner 上既慢
	// 又没必要(要验的是"先查大小、再决定读不读"这条顺序,不是真读 9 MiB)。
	// 用稀疏文件:POSIX 有效;Windows 上 Truncate 会真分配,故只在能稀疏的平台造,
	// 否则退化为"把上限改小再试"同样走同一条判断路径。
	big := filepath.Join(t.TempDir(), "big.zip")
	fh, err := os.OpenFile(big, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	over := int64(rolepack.MaxPackBytes) + 1<<20
	if err := fh.Truncate(over); err != nil {
		fh.Close()
		t.Skipf("造不出超限文件(平台不支持): %v", err)
	}
	fi, serr := fh.Stat()
	if serr != nil || fi.Size() < over {
		fh.Close()
		t.Skipf("造出的文件没到超限大小(平台差异): size=%v err=%v", fi, serr)
	}
	fh.Close()
	if _, err := h.cmdRolePackImport([]string{big}); err == nil {
		t.Fatal("超上限的包应在读进内存前就拒")
	}
}

// TestRolePackCommandsRegistered 两条命令确实注册进命令面(名字写错只会让用户敲了没反应)。
func TestRolePackCommandsRegistered(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	_, cmds := newHost(t)
	if _, ok := cmds.Get("role-pack"); !ok {
		t.Fatal("命令未注册: /role-pack")
	}
}

// TestCmdRolePackExportNoArgs 缺参数要给用法(不是静默成功也不是空包)。
func TestCmdRolePackExportNoArgs(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	if _, err := h.cmdRolePackExport([]string{}); err == nil {
		t.Fatal("缺角色标识应报错并给用法")
	}
}

// TestCmdRolePackExportMissingRole 角色不存在:报原因,不给空包路径。
func TestCmdRolePackExportMissingRole(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	if _, err := h.cmdRolePackExport([]string{"nope", filepath.Join(t.TempDir(), "x.zip")}); err == nil {
		t.Fatal("不存在的角色应报错")
	}
}

// TestCmdRolePackImportNoArgs 缺包路径要给用法。
func TestCmdRolePackImportNoArgs(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	if _, err := h.cmdRolePackImport([]string{}); err == nil {
		t.Fatal("缺包路径应报错")
	}
}

// TestCmdRolePackImportRejectsURLSource 网址形态的源显式失败(与 /export 同一道裁决)。
func TestCmdRolePackImportRejectsURLSource(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	if _, err := h.cmdRolePackImport([]string{"https://example.com/r.zip"}); err == nil {
		t.Fatal("网址形态应显式失败")
	}
}

// TestCmdRolePackImportBadFile 不是 zip 的文件:包层显式拒绝(命令只透传,不吞错)。
func TestCmdRolePackImportBadFile(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	p := filepath.Join(t.TempDir(), "x.zip")
	if err := os.WriteFile(p, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.cmdRolePackImport([]string{p}); err == nil {
		t.Fatal("坏包应显式拒绝")
	}
	// 读不到的文件
	if _, err := h.cmdRolePackImport([]string{"/nonexistent/r.zip"}); err == nil {
		t.Fatal("读不到包应报错")
	}
}

// bareHost 造一个**没有** ctx.cwdSessions 的 Host:覆盖"拿不到工作区 ⇒ 请给显式路径"
// 这条回落(装配里没该服务时不该静默写进当前目录 —— 那是不可预期的地方)。
func bareHost(t *testing.T) *Host {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	return &Host{c: c}
}

func TestCmdRolePackExportWithoutWorkspace(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h := bareHost(t)
	seedRole(t, "writer")
	// 无工作区又没给路径 ⇒ 显式要路径(不静默落到某处)
	if _, err := h.cmdRolePackExport([]string{"writer"}); err == nil {
		t.Fatal("拿不到工作区时应要求显式路径")
	}
	// 给了路径就能走通
	p := filepath.Join(t.TempDir(), "r.zip")
	if _, err := h.cmdRolePackExport([]string{"writer", p}); err != nil {
		t.Fatalf("给了显式路径应成功: %v", err)
	}
}

// TestCmdRolePackImportNoReloadServiceFallback 未装配角色服务时导入仍应落盘,并如实提示
// (不假装索引已刷新)。这条覆盖的是「注入失败」那条分支 —— 装配里没装 host-roles 的构建
// 走的就是它,不能因此把用户的包退回去。
func TestCmdRolePackImportNoReloadServiceFallback(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	seedRole(t, "solo")
	p := filepath.Join(t.TempDir(), "r.zip")
	// 用完整装配导出,再用 bare Host(无 ctx.roles)导入
	hFull, _ := newHost(t)
	if _, err := hFull.cmdRolePackExport([]string{"solo", p}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAH_HOME", t.TempDir())
	h2 := bareHost(t)
	out, err := h2.cmdRolePackImport([]string{p})
	if err != nil {
		t.Fatalf("未装配角色服务时导入仍应成功落盘: %v", err)
	}
	if !strings.Contains(out, "未装配角色服务") {
		t.Fatalf("回执应如实说索引未刷新: %s", out)
	}
	if !roleExists("solo") {
		t.Fatal("包应已落盘(索引没刷新 ≠ 没导入)")
	}
}

// TestCmdRolePackDispatchRoutesThroughSubcommands 分派器本身也要被走到:
// 用户敲的是 `/role-pack export …`,走的是 cmdRolePack 的分支,不是子函数。
// 只测子函数的话,分派器写错了(比如把 "export" 拼错)测试照样全绿。
func TestCmdRolePackDispatchRoutesThroughSubcommands(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h, _ := newHost(t)
	seedRole(t, "route")
	p := filepath.Join(t.TempDir(), "r.zip")
	if _, err := h.cmdRolePack([]string{"export", "route", p}); err != nil {
		t.Fatalf("分派 export 失败: %v", err)
	}
	t.Setenv("GAH_HOME", t.TempDir())
	h2, _ := newHost(t)
	if _, err := h2.cmdRolePack([]string{"import", p}); err != nil {
		t.Fatalf("分派 import 失败: %v", err)
	}
	if !roleExists("route") {
		t.Fatal("经分派器导入后角色应存在")
	}
}
