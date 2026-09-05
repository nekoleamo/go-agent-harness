// /workspace 命令测试:目录校验(不存在/非目录拒绝且不 chdir)、切换成功
// (cwd 变更 + 会话 key 重绑 + 状态栏工作区名刷新)、无 cwdSessions 时仅 chdir。
package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeCwdSessions 记录 SwitchProject/会话命名调用 + 供 RecentProjects 最近使用列表。
type fakeCwdSessions struct {
	switchedKey string
	name        string
	recent      []sdk.ProjectInfo
}

func (f *fakeCwdSessions) Current() string                   { return "" }
func (f *fakeCwdSessions) Path() string                      { return "" }
func (f *fakeCwdSessions) List() []string                    { return nil }
func (f *fakeCwdSessions) Sessions() []sdk.SessionInfo       { return nil }
func (f *fakeCwdSessions) Open(string) error                 { return nil }
func (f *fakeCwdSessions) CurrentSession() string            { return "" }
func (f *fakeCwdSessions) New() (string, error)              { return "n1", nil }
func (f *fakeCwdSessions) RecentProjects() []sdk.ProjectInfo { return f.recent }
func (f *fakeCwdSessions) Rename(n string) error              { f.name = n; return nil }
func (f *fakeCwdSessions) SessionName() string                { return f.name }
func (f *fakeCwdSessions) SwitchProject(key string) (string, error) {
	f.switchedKey = key
	return "sp1", nil
}

// workspaceApp 构造带 fake cwdSessions 的 App。
func workspaceApp(fake *fakeCwdSessions) *App {
	reg := newMemRegistry()
	c := &stubCtx{svc: map[string]any{"ctx.commands": reg, "ctx.cwdSessions": fake}}
	return NewApp(c, stubLoop{}, stubLLM{}, "tui")
}

func TestCmdWorkspaceSwitch(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig) // 恢复,避免影响同包其它测试(cwd 依赖)

	base := t.TempDir()
	proj := filepath.Join(base, "proj-a")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := &fakeCwdSessions{}
	a := workspaceApp(fake)

	// 目录不存在:拒绝且不 chdir
	if _, err := a.cmdWorkspace([]string{filepath.Join(base, "nope")}); err == nil {
		t.Fatal("目录不存在应拒绝")
	}
	if wd, _ := os.Getwd(); wd != orig {
		t.Fatalf("失败不应 chdir: %s", wd)
	}
	// 非目录:拒绝
	if _, err := a.cmdWorkspace([]string{filepath.Join(base, "..", "..")}); err == nil {
		// base 是临时目录,其父级存在为目录——用文件路径验证非目录拒绝
	}
	f := filepath.Join(base, "afile.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.cmdWorkspace([]string{f}); err == nil {
		t.Fatal("非目录应拒绝")
	}
	// 切换成功(绝对路径)
	out, err := a.cmdWorkspace([]string{proj})
	if err != nil {
		t.Fatalf("切换失败: %v", err)
	}
	wd, _ := os.Getwd()
	exp, _ := filepath.EvalSymlinks(proj) // 归一化(如 /var → /private/var)
	if wd != exp {
		t.Fatalf("应 chdir 到项目: got %s want %s", wd, exp)
	}
	if a.model.state.Workspace != "proj-a" {
		t.Fatalf("状态栏工作区应刷新: %q", a.model.state.Workspace)
	}
	if fake.switchedKey != sdk.ProjectKey(wd) {
		t.Fatalf("会话 key 应重绑为新项目: got %q want %q", fake.switchedKey, sdk.ProjectKey(wd))
	}
	if !strings.Contains(out, "已切换工作区") {
		t.Fatalf("回执: %s", out)
	}
}

func TestCmdWorkspaceRelativeAndNoService(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	base := t.TempDir()
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(base, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// 无 cwdSessions 服务:仅 chdir + 工作区刷新,返回提示
	reg := newMemRegistry()
	c := &stubCtx{svc: map[string]any{"ctx.commands": reg}}
	a := NewApp(c, stubLoop{}, stubLLM{}, "tui")
	out, err := a.cmdWorkspace([]string{"sub"}) // 相对路径
	if err != nil {
		t.Fatalf("相对路径切换失败: %v", err)
	}
	wd, _ := os.Getwd()
	exp, _ := filepath.EvalSymlinks(sub)
	if wd != exp {
		t.Fatalf("应切到相对解析目录: got %s want %s", wd, exp)
	}
	if a.model.state.Workspace != "sub" {
		t.Fatalf("工作区名: %q", a.model.state.Workspace)
	}
	if !strings.Contains(out, "未装配 cwdSessions") {
		t.Fatalf("无服务应提示降级: %s", out)
	}
}

// TestWorkspaceOptionsAndSentinel 一级枚举 = 最近使用 + 哨兵;哨兵经 cmdWorkspace 去除后解析路径。
func TestWorkspaceOptionsAndSentinel(t *testing.T) {
	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	base := t.TempDir()
	projA := filepath.Join(base, "proj-a")
	os.MkdirAll(projA, 0o755)
	fake := &fakeCwdSessions{recent: []sdk.ProjectInfo{{Key: "k-a", Dir: projA, TS: 99}}}
	a := workspaceApp(fake)
	opts := a.workspaceOptions(nil)
	if len(opts) != 2 || opts[0].Value != projA || opts[1].Value != workspaceNewSentinel {
		t.Fatalf("一级选项应为 历史+哨兵: %+v", opts)
	}
	// 哨兵参数(选择器“新路径”入口断点后提交)→ cmdWorkspace 去哨兵取路径
	out, err := a.cmdWorkspace([]string{workspaceNewSentinel, projA})
	if err != nil {
		t.Fatalf("哨兵路径执行失败: %v", err)
	}
	if !strings.Contains(out, "已切换工作区") {
		t.Fatalf("回执: %s", out)
	}
	if wd, _ := os.Getwd(); wd != projA && !strings.HasSuffix(wd, "proj-a") {
		t.Fatalf("应切到 projA: %s", wd)
	}
}

// TestWorkspaceNoHistoryOptions 无历史记录 → 一级选项空(回退直接输入)。
func TestWorkspaceNoHistoryOptions(t *testing.T) {
	a := workspaceApp(&fakeCwdSessions{})
	if opts := a.workspaceOptions(nil); opts != nil {
		t.Fatalf("无历史应为空(直接输入路径): %+v", opts)
	}
}

// TestWorkspacePickerCascade 选择器级联:选历史目录直接执行;选“新路径”哨兵 → 自由级断点。
func TestWorkspacePickerCascade(t *testing.T) {
	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	base := t.TempDir()
	projA := filepath.Join(base, "proj-a")
	os.MkdirAll(projA, 0o755)
	fake := &fakeCwdSessions{recent: []sdk.ProjectInfo{{Key: "k-a", Dir: projA, TS: 100}}}
	a := workspaceApp(fake)
	// 选 workspace → 一级:历史 + 哨兵
	res := AdvanceEnter("/", &Pick{Level: 0, Items: []sdk.Option{{Value: "workspace", Desc: "切换工作区"}}}, a.levels)
	if res.Commit || res.Pick == nil || res.Pick.Level != 1 {
		t.Fatalf("应进入一级枚举: %+v", res)
	}
	if len(res.Pick.Items) != 2 || res.Pick.Items[0].Value != projA {
		t.Fatalf("一级应含最近工作区: %+v", res.Pick.Items)
	}
	// 高亮历史项(第 0 项)回车 → 直接执行
	res2 := AdvanceEnter(res.Input, res.Pick, a.levels)
	if !res2.Commit || res2.Input != "/workspace "+projA {
		t.Fatalf("选历史应直接执行: %q commit=%v", res2.Input, res2.Commit)
	}
	// 高亮哨兵回车 → 断点(继续输入路径)
	res.Pick.Cursor = 1
	res3 := AdvanceEnter(res.Input, res.Pick, a.levels)
	if res3.Commit || res3.Input != "/workspace "+workspaceNewSentinel+" " {
		t.Fatalf("哨兵应断点待输入: %q commit=%v", res3.Input, res3.Commit)
	}
	if len(res3.Hints) == 0 || !strings.Contains(strings.Join(res3.Hints, " "), "目录路径") {
		t.Fatalf("断点应提示输入目录: %v", res3.Hints)
	}
}

// TestCmdName /name 命名当前会话:显示名写入 cwdSessions、状态栏标签=名优先;
// 清除(-)后回退 id;未装配 ctx.cwdSessions 时显式报错不 panic。
func TestCmdName(t *testing.T) {
	fake := &fakeCwdSessions{}
	a := workspaceApp(fake)
	if err := a.command("/name 重构排期"); err != nil {
		t.Fatal(err)
	}
	if fake.name != "重构排期" {
		t.Fatalf("Rename 未收到显示名,got %q", fake.name)
	}
	if a.model.state.Session != "重构排期" {
		t.Fatalf("状态栏标签应为名,got %q", a.model.state.Session)
	}
	// 含空格名:Fields 拆分后自由参数字段应 Join 还原
	if err := a.command("/name 我的 实验"); err != nil {
		t.Fatal(err)
	}
	if fake.name != "我的 实验" {
		t.Fatalf("含空格名应还原,got %q", fake.name)
	}
	// 清除:回退空(未命名主会话 id 空 → 状态栏不显示)
	if err := a.command("/name -"); err != nil {
		t.Fatal(err)
	}
	if fake.name != "" || a.model.state.Session != "" {
		t.Fatalf("清除后应回退,got name=%q session=%q", fake.name, a.model.state.Session)
	}
	// 未装配 ctx.cwdSessions:显式错误
	c := &stubCtx{svc: map[string]any{"ctx.commands": newMemRegistry()}}
	na := NewApp(c, stubLoop{}, stubLLM{}, "tui")
	if err := na.command("/name x"); err == nil {
		t.Fatal("未装配 ctx.cwdSessions 应报错")
	}
}
