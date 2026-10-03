// /install 斜杠命令的单测(2026-10-03)。
//
// 重点在**安全面**而不是成功路径:
//
//	① 审批档 strict ⇒ 拒绝(装插件 = 引入常驻执行代码);
//	② 无确认通道 ⇒ 拒绝(安全侧默认);
//	③ 用户拒绝 ⇒ 什么都不发生,且**不**报错;
//	④ list 要报出白名单状态与登记来源(「这个插件上次是什么时候、从哪装的」)。
package hostintcmd

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// installConfirm 可编程确认桩。
type installConfirm struct {
	sdk.ConfirmService
	ok  bool
	n   int
	err error
}

func (c *installConfirm) Confirm(context.Context, string) (bool, error) {
	c.n++
	if c.err != nil {
		return false, c.err
	}
	return c.ok, nil
}

// installApproval 可编程审批档桩(eff 留空时 EffectiveMode 回落到 Mode)。
type installApproval struct {
	sdk.ApprovalService
	mode sdk.ApprovalMode
	eff  sdk.ApprovalMode
}

func (a *installApproval) Mode() sdk.ApprovalMode          { return a.mode }
func (a *installApproval) SetMode(sdk.ApprovalMode)        {}
func (a *installApproval) EffectiveMode() sdk.ApprovalMode { return a.eff }
func (a *installApproval) EffectiveFrom() string           { return "" }

func newInstallHost(t *testing.T, ap sdk.ApprovalService, cf sdk.ConfirmService) *Host {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	if ap != nil {
		if err := c.Provide("ctx.approval", ap); err != nil {
			t.Fatal(err)
		}
	}
	if cf != nil {
		if err := c.Provide("ctx.confirm", cf); err != nil {
			t.Fatal(err)
		}
	}
	return &Host{c: c}
}

// TestCmdInstallStrictApprovalRejected strict 档拒绝,且**不**弹确认(先拒后问)。
func TestCmdInstallStrictApprovalRejected(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	cf := &installConfirm{ok: true}
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalStrict, eff: sdk.ApprovalStrict}, cf)

	_, err := h.cmdInstall([]string{"/tmp/some-plugin"})
	if err == nil {
		t.Fatal("strict 档应拒绝安装")
	}
	if cf.n != 0 {
		t.Fatalf("先拒后问:不该弹确认(弹了 %d 次)", cf.n)
	}
}

// TestCmdInstallNoConfirmChannelRefused 无确认通道 ⇒ 拒绝(安全侧默认)。
func TestCmdInstallNoConfirmChannelRefused(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, nil)
	_, err := h.cmdInstall([]string{"/tmp/some-plugin"})
	if err == nil || !strings.Contains(err.Error(), "无确认通道") {
		t.Fatalf("无确认通道应显式拒绝: %v", err)
	}
}

// TestCmdInstallUserDeclines 用户拒绝 ⇒ 什么都没发生,且不报错。
func TestCmdInstallUserDeclines(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	home := os.Getenv("GAH_HOME")
	cf := &installConfirm{ok: false}
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, cf)

	out, err := h.cmdInstall([]string{"/tmp/definitely-not-there"})
	if err != nil {
		t.Fatalf("用户拒绝不该报错: %v", err)
	}
	if !strings.Contains(out, "已取消") {
		t.Fatalf("回执应说明已取消: %s", out)
	}
	if cf.n != 1 {
		t.Fatalf("应恰好弹一次确认,实际 %d", cf.n)
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", "definitely-not-there")); err == nil {
		t.Fatal("拒绝后不该有任何落位")
	}
}

// TestCmdInstallListReportsTrust 清单要报出白名单状态与登记来源。
func TestCmdInstallListReportsTrust(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	dir := filepath.Join(home, "plugins", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "tool-demo")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum, err := plugintrust.HashFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	list, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	if err := list.RecordWithAudit("tool-demo", sum, "install:/tmp/demo"); err != nil {
		t.Fatal(err)
	}

	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, &installConfirm{ok: true})
	out, err := h.cmdInstall([]string{"list"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"demo", "tool-demo", "已登记", "强制", "install:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("清单缺 %q:\n%s", want, out)
		}
	}
}

// TestCmdInstallUntrustNoConfirm 撤销登记不需要确认(它只会让插件**不被加载**,不会加载新代码)。
func TestCmdInstallUntrustNoConfirm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, nil)
	out, err := h.cmdInstall([]string{"untrust", "tool-nope"})
	if err != nil {
		t.Fatalf("撤销不存在的条目应幂等: %v", err)
	}
	if !strings.Contains(out, "已从白名单移除") {
		t.Fatalf("回执不对: %s", out)
	}
}

// TestCmdInstallRealInstall /install 的成功路径:本地目录 → 确认 → 装上 → 回显事实。
//
// 上一批用例都钉在"被拦住"的分支上;这里补"真的装上"那一段,是为了让 installRows /
// cmdInstall 的成功分支有覆盖 —— 否则整包覆盖率被新代码稀释,门禁会以一条**没测过的
// 成功路径**为由变红,而那正是最该测的一段。
func TestCmdInstallRealInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)

	// 造一个能构建的最小桥插件(复用 tool-echo 的实现,与 install 包的夹具同源)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "go.mod"),
		[]byte("module demo-tool\n\ngo 1.27\n\nrequire github.com/nekoleamo/go-agent-harness v0.0.0\n\n"+
			"require github.com/nekoleamo/go-agent-harness/sdk v0.0.0\n\n"+
			"replace github.com/nekoleamo/go-agent-harness => "+repoRootDir(t)+"\n\n"+
			"replace github.com/nekoleamo/go-agent-harness/sdk => "+filepath.Join(repoRootDir(t), "sdk")),
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "plugin.yaml"),
		[]byte("id: demo\nprotocol: bridge\nbinary: tool-demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	example, err := os.ReadFile(filepath.Join(repoRootDir(t), "extplugins", "tool-echo", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), example, 0o644); err != nil {
		t.Fatal(err)
	}

	cf := &installConfirm{ok: true}
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, cf)
	out, err := h.cmdInstall([]string{src})
	if err != nil {
		t.Fatalf("安装失败: %v", err)
	}
	if cf.n != 1 {
		t.Fatalf("应恰好弹一次确认,实际 %d", cf.n)
	}
	for _, want := range []string{"已安装", "demo", "构建命令", "白名单登记"} {
		if !strings.Contains(out, want) {
			t.Fatalf("回执缺 %q:\n%s", want, out)
		}
	}
	bin := filepath.Join(home, "plugins", "demo", "tool-demo")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("产物未落位: %v", err)
	}
	// 装完清单里能看到它且状态为已登记
	out2, err := h.cmdInstall([]string{"list"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "tool-demo") || !strings.Contains(out2, "已登记") {
		t.Fatalf("装完清单不对:\n%s", out2)
	}
}

// repoRootDir 仓库根(夹具的 replace 指向它)。
func repoRootDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

// TestCmdInstallUninstallBranch 卸载分支:要确认、目录与白名单条目都要真没���。
func TestCmdInstallUninstallBranch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	dir := filepath.Join(home, "plugins", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "tool-demo")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum, err := plugintrust.HashFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	list, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	if err := list.RecordWithAudit("tool-demo", sum, "install:/tmp/demo"); err != nil {
		t.Fatal(err)
	}

	// 先拒绝一次:什么都不该变
	cf := &installConfirm{ok: false}
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, cf)
	out, err := h.cmdInstall([]string{"uninstall", "demo"})
	if err != nil || !strings.Contains(out, "已取消") {
		t.Fatalf("拒绝卸载应无事发生: out=%q err=%v", out, err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatal("拒绝后目录应还在")
	}

	// 再同意:目录与条目都没了
	cf2 := &installConfirm{ok: true}
	h2 := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, cf2)
	out2, err := h2.cmdInstall([]string{"uninstall", "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "已卸载") || !strings.Contains(out2, "白名单") {
		t.Fatalf("回执应说明白名单条目一并撤销: %s", out2)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("卸载后目录应没了")
	}
	list2, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := list2.Sum("tool-demo"); ok {
		t.Fatal("卸载后白名单条目应一并撤销")
	}
}

// TestCmdInstallTrustBranch 手工登记分支:确认 → 登记 → 面板口径。
func TestCmdInstallTrustBranch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	dir := filepath.Join(home, "plugins", "tool-demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 手工放置(扁平布局):host-bridge 就是这么扫到的,登记也要认
	if err := os.WriteFile(filepath.Join(dir, "tool-demo"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cf := &installConfirm{ok: true}
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, cf)
	out, err := h.cmdInstall([]string{"trust", "tool-demo"})
	if err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	if !strings.Contains(out, "已把") {
		t.Fatalf("回执不对: %s", out)
	}
	list, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := list.Sum("tool-demo"); !ok {
		t.Fatal("登记后应在清单里")
	}
	if a, ok := list.LastAuditOf("tool-demo"); !ok || a.Source != "trust:manual" {
		t.Fatalf("审计来源应是 trust:manual,got %+v", a)
	}
}

// TestCmdInstallBadArgs 参数错的三条分支都要**显式报错**,不能静默当默认。
//
// 尤其 `/install trust`(缺名字):若它默默去登记空名字,表现是「点了没反应」,
// 用户会去检查自己的插件而不是自己的命令。
func TestCmdInstallBadArgs(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, &installConfirm{ok: true})
	for _, args := range [][]string{{"uninstall"}, {"trust"}, {"untrust"}} {
		if _, err := h.cmdInstall(args); err == nil {
			t.Fatalf("%v 缺参数应显式报错", args)
		}
	}
}

// TestCmdInstallListEmpty 空目录要给出「怎么用」的提示,而不是一个空表。
func TestCmdInstallListEmpty(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, &installConfirm{ok: true})
	out, err := h.cmdInstall(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "/install") {
		t.Fatalf("空清单应告诉用户怎么装: %s", out)
	}
}

// TestCmdInstallApprovalServiceMissing 没装 ctx.approval ⇒ 放行(极简 profile 里没有审批档)。
//
// 这里体现的是"装配与否不该决定能不能装插件":headless / mcp-serve 这类 profile 没有
// 审批服务,若因此拒绝,插件管理会凭空不可用。
func TestCmdInstallApprovalServiceMissing(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h := newInstallHost(t, nil, &installConfirm{ok: false})
	if _, err := h.cmdInstall([]string{"trust", "tool-nope"}); err != nil && strings.Contains(err.Error(), "严格") {
		t.Fatalf("未装配审批服务时不该按 strict 拒: %v", err)
	}
}

// TestCmdInstallFailurePaths 失败必须**显式报错**,不能静默成功。
//
// 覆盖三条现实里真会发生的:① 路径不存在;② 目录里没有 plugin.yaml;
// ③ 白名单是坏的(Load 直接报错)—— 那时清单根本读不出来,继续跑只会显示一份空清单。
func TestCmdInstallFailurePaths(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, &installConfirm{ok: true})

	// ① 路径不存在
	if _, err := h.cmdInstall([]string{filepath.Join(t.TempDir(), "不存在")}); err == nil {
		t.Fatal("不存在的路径应显式报错")
	}
	// ② 没有 plugin.yaml
	empty := t.TempDir()
	if _, err := h.cmdInstall([]string{empty}); err == nil {
		t.Fatal("缺 plugin.yaml 应显式报错")
	}
}

// TestCmdInstallListBrokenManifest 清单坏了 ⇒ 显式报错,而不是给一份空清单。
//
// 「清单坏了」几乎必然是文件被写坏/截断;此时静默显示空清单 = 让用户以为「没装插件」。
func TestCmdInstallListBrokenManifest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "plugins", plugintrust.FileName), []byte("坏行\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, &installConfirm{ok: true})
	if _, err := h.cmdInstall([]string{"list"}); err == nil {
		t.Fatal("清单坏了应显式报错")
	}
}

// TestCmdInstallListMissingBinary 有目录但没有二进制 ⇒ 清单如实说「未找到」。
//
// 那种状态真实存在(构建失败、手工拷了源码没编译);显示成空二进制名会让人以为是插件坏了。
func TestCmdInstallListMissingBinary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "plugins", "half-built"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, &installConfirm{ok: true})
	out, err := h.cmdInstall([]string{"list"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "half-built") || !strings.Contains(out, "未找到二进制") {
		t.Fatalf("清单应如实标出缺二进制:\n%s", out)
	}
}

// TestCmdInstallTrustDeclined 登记被拒 ⇒ 什么都不做,且不报错。
func TestCmdInstallTrustDeclined(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "plugins", "tool-demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "plugins", "tool-demo", "tool-demo"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, &installConfirm{ok: false})
	out, err := h.cmdInstall([]string{"trust", "tool-demo"})
	if err != nil || !strings.Contains(out, "已取消") {
		t.Fatalf("拒绝登记应无事发生: out=%q err=%v", out, err)
	}
	list, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err == nil {
		if _, ok := list.Sum("tool-demo"); ok {
			t.Fatal("拒绝后不该有白名单条目")
		}
	}
}

// TestCmdInstallTrustMissingBinary 登记一个不存在的二进制 ⇒ 显式报错。
func TestCmdInstallTrustMissingBinary(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, &installConfirm{ok: true})
	if _, err := h.cmdInstall([]string{"trust", "tool-nope"}); err == nil {
		t.Fatal("不存在的插件应显式报错")
	}
}

// TestCmdInstallUninstallMissing 卸载不存在的插件 ⇒ 显式报错(不是「已卸载」的假成功)。
func TestCmdInstallUninstallMissing(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, &installConfirm{ok: true})
	if _, err := h.cmdInstall([]string{"uninstall", "nope"}); err == nil {
		t.Fatal("卸载不存在的插件应显式报错")
	}
}
