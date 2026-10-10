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
	"encoding/binary"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/internal/install"
	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
	"github.com/nekoleamo/go-agent-harness/internal/testutil"
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
	return newInstallHostWithExtp(t, ap, cf, nil)
}

// newInstallHostWithExtp 同上,额外 Provide 一个外部插件控制面(批二启停用)。
func newInstallHostWithExtp(t *testing.T, ap sdk.ApprovalService, cf sdk.ConfirmService, extp sdk.ExternalPlugins) *Host {
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
	if extp != nil {
		if err := c.Provide("ctx.extplugins", extp); err != nil {
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
	bin := filepath.Join(home, "plugins", "demo", testutil.ExeName("tool-demo"))
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

// TestCmdInstallSourceCellAndCheck 批一:清单要显示来源三件,`/install check` 要只问不装。
//
// 覆盖的正是新加的两块:sourceCell(无记录/有记录两条分支)与 installCheck(有可查/无可查)。
func TestCmdInstallSourceCellAndCheck(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, &installConfirm{ok: true})

	// ① 账上什么都没有时:清单要有行,且来源格明说「无来源记录」而不是空白。
	if err := os.MkdirAll(filepath.Join(home, "plugins", "hand"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "plugins", "hand", "tool-hand"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := h.cmdInstall([]string{"list"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "无来源记录") {
		t.Errorf("手工放置的插件应明说无来源记录(空白会让用户以为是表格坏了):\n%s", out)
	}
	// ② `/install check`:无可查来源 ⇒ 说清为什么没得查,不是干打一行空。
	out, err = h.cmdInstall([]string{"check"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "没有可检查的来源") {
		t.Errorf("/install check 空结果文案不对: %s", out)
	}

	// ③ 账上有一条本地来源:清单显示它;check 仍跳过它(本地目录没有「更新」)。
	var ledger install.SourceLedger
	ledger.Record(install.SourceEntry{PluginID: "hand", Kind: install.KindLocal,
		Repo: "/src/hand", Commit: "9f2c1ab3e5f7aa11bb22cc33dd44ee55ff6607", Drifted: true})
	if err := install.WriteSources(home, &ledger); err != nil {
		t.Fatal(err)
	}
	out, err = h.cmdInstall([]string{"list"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/src/hand", "9f2c1ab3e5f7", "会移动"} {
		if !strings.Contains(out, want) {
			t.Errorf("清单缺 %q:\n%s", want, out)
		}
	}
	// ④ 账坏了 ⇒ 明确报错(不当空账:那会让用户以为这些插件「来源不明」而不是「账坏了」)。
	if err := os.WriteFile(install.SourcesPath(home), []byte("sources: [oops\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := h.cmdInstall([]string{"list"}); err == nil {
		t.Errorf("账坏了应报错,得到:\n%s", out)
	} else if !strings.Contains(err.Error(), "sources.yaml") {
		t.Errorf("报错应指明是来源账: %s", err.Error())
	}
}

// TestCmdInstallCheckRows `/install check` 的有结果分支(用本地裸仓库当远端,零网络)。
//
// 要点:① 每条检查都要打出来;② 结尾必须**明说不会自动装** —— 这个出口最容易被后来人
// 改成「顺便就装了」,而那正是本批定下「不做自动更新」的地方。
func TestCmdInstallCheckRows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGitCmd(t, filepath.Dir(remote), "init", "-q", "--bare", remote)
	// 往裸仓库塞一个 main 分支(内容不重要,ls-remote 只看 refs)
	work := t.TempDir()
	runGitCmd(t, work, "init", "-q")
	if err := os.WriteFile(filepath.Join(work, "x"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, work, "config", "user.email", "t@t")
	runGitCmd(t, work, "config", "user.name", "t")
	runGitCmd(t, work, "add", ".")
	runGitCmd(t, work, "commit", "-qm", "init")
	runGitCmd(t, work, "push", "-q", remote, "HEAD:refs/heads/main")

	var ledger install.SourceLedger
	ledger.Record(install.SourceEntry{PluginID: "demo", Kind: install.KindTag, Ref: "v1.0.0",
		Repo: remote, Commit: "9f2c1ab3e5f7aa11bb22cc33dd44ee55ff6607"})
	if err := install.WriteSources(home, &ledger); err != nil {
		t.Fatal(err)
	}
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, &installConfirm{ok: true})
	out, err := h.cmdInstall([]string{"check"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"demo", "remote.git", "v1.0.0", "不会自动装"} {
		if !strings.Contains(out, want) {
			t.Errorf("检查结果缺 %q:\n%s", want, out)
		}
	}
}

// runGitCmd 跑一条 git(失败即 t.Fatal —— 测试里 git 失败一定是夹具坏了)。
func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v(%s)", args, err, out)
	}
}

// fakeExtPlugins 控制面替身(批二)。
type fakeExtPlugins struct {
	disabled map[string]bool
	calls    []string
}

func (f *fakeExtPlugins) Reload(n string) error { f.calls = append(f.calls, "reload:"+n); return nil }
func (f *fakeExtPlugins) List() []sdk.ExternalPluginInfo {
	var out []sdk.ExternalPluginInfo
	for n, d := range f.disabled {
		out = append(out, sdk.ExternalPluginInfo{Name: n, Path: "/x/" + n, Loaded: !d, Disabled: d})
	}
	return out
}
func (f *fakeExtPlugins) Enable(n string) error {
	f.calls = append(f.calls, "enable:"+n)
	delete(f.disabled, n)
	return nil
}
func (f *fakeExtPlugins) Disable(n string) error {
	f.calls = append(f.calls, "disable:"+n)
	f.disabled[n] = true
	return nil
}

// TestCmdInstallLifecycle `/install disable|enable`:二次确认 + 回执要说清「停用 ≠ 卸载」。
//
// 回执那句是硬要求:用户以为插件被删掉、重启后它又出现,是停用最常见的误解。
func TestCmdInstallLifecycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	extp := &fakeExtPlugins{disabled: map[string]bool{}}
	h := newInstallHostWithExtp(t, &installApproval{mode: sdk.ApprovalSmart}, &installConfirm{ok: true}, extp)

	out, err := h.cmdInstall([]string{"disable", "tool-demo"})
	if err != nil {
		t.Fatalf("停用应成功: %v", err)
	}
	if !extp.disabled["tool-demo"] {
		t.Error("停用应落到控制面")
	}
	for _, want := range []string{"已停用", "文件与白名单条目留着", "uninstall"} {
		if !strings.Contains(out, want) {
			t.Errorf("回执缺 %q:\n%s", want, out)
		}
	}
	out, err = h.cmdInstall([]string{"enable", "tool-demo"})
	if err != nil {
		t.Fatalf("启用应成功: %v", err)
	}
	if extp.disabled["tool-demo"] {
		t.Error("启用应解开停用")
	}
	if !strings.Contains(out, "不需要重新登记") {
		t.Errorf("启用回执要说明不用重新登记:\n%s", out)
	}
}

// TestCmdInstallLifecycleCancel 用户拒绝 ⇒ 什么都不发生,且不报错。
func TestCmdInstallLifecycleCancel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	extp := &fakeExtPlugins{disabled: map[string]bool{}}
	h := newInstallHostWithExtp(t, &installApproval{mode: sdk.ApprovalSmart}, &installConfirm{ok: false}, extp)
	out, err := h.cmdInstall([]string{"disable", "tool-demo"})
	if err != nil {
		t.Fatalf("取消不应报错: %v", err)
	}
	if !strings.Contains(out, "已取消") {
		t.Errorf("回执应说明已取消:\n%s", out)
	}
	if len(extp.calls) != 0 {
		t.Errorf("取消时不应调控制面: %v", extp.calls)
	}
}

// TestCmdInstallLifecycleNoExtp profile 未装 host-bridge ⇒ 明确报错(不是假装成功)。
func TestCmdInstallLifecycleNoExtp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, &installConfirm{ok: true})
	if _, err := h.cmdInstall([]string{"disable", "tool-demo"}); err == nil {
		t.Error("未装配控制面应报错")
	} else if !strings.Contains(err.Error(), "host-bridge") {
		t.Errorf("文案应指明是 host-bridge: %s", err.Error())
	}
	// 参数缺失也要给出用法
	if _, err := h.cmdInstall([]string{"disable"}); err == nil {
		t.Error("缺参数应报错")
	}
}

// TestCmdInstallPrebuiltSubcommand `/install prebuilt <目录>`:走预编译产物路。
//
// 断两件事:① 子命令确实进了预编译路(URL 不可达 ⇒ 报错指向那个 URL,而**不是**源码构建错误);
// ② 缺参数给用法。
func TestCmdInstallPrebuiltSubcommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "plugin.yaml"),
		[]byte("id: demo\nprotocol: bridge\nbinary: tool-demo\nprebuilt:\n  "+
			runtime.GOOS+"/"+runtime.GOARCH+": https://example.invalid/tool-demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, &installConfirm{ok: true})
	_, err := h.cmdInstall([]string{"prebuilt", src})
	if err == nil {
		t.Fatal("URL 不可达时应报错")
	}
	if !strings.Contains(err.Error(), "example.invalid") {
		t.Errorf("错误应指向预编译产物 URL(证明走的是下载路而非构建路): %s", err.Error())
	}
	if _, err := h.cmdInstall([]string{"prebuilt"}); err == nil {
		t.Error("缺参数应给出用法")
	} else if !strings.Contains(err.Error(), "/install prebuilt") {
		t.Errorf("缺参数要给出用法: %s", err.Error())
	}
}

// TestCmdInstallArtifact `/install artifact <url> <id> <name>`:装下载来的产物。
//
// 与 CLI 同一条内核,这里要额外钉的是 TUI 侧的**二次确认**与回执:
// 产物是下载来的、没有独立校验这件事,必须出现在用户点确认之前和回执里。
func TestCmdInstallArtifact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fakeArchPayload(runtime.GOOS, runtime.GOARCH))
	}))
	defer srv.Close()
	cf := &installConfirm{ok: true}
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, cf)

	// ① 参数不合法 ⇒ 显式报错,且**不**弹确认(先拒后问)
	for _, args := range [][]string{
		{"artifact"}, {"artifact", srv.URL + "/t", "../evil", "tool-x"},
		{"artifact", srv.URL + "/t", "demo", "echo"},
	} {
		if _, err := h.cmdInstall(args); err == nil {
			t.Errorf("%v 应报错", args)
		}
	}
	if cf.n != 0 {
		t.Errorf("参数不合法时不该弹确认,实际 %d 次", cf.n)
	}
	// ② 正常路径:确认一次 + 回执说清代价
	out, err := h.cmdInstall([]string{"artifact", srv.URL + "/tool-demo", "demo", "tool-demo"})
	if err != nil {
		t.Fatalf("产物安装应成功: %v", err)
	}
	if cf.n != 1 {
		t.Fatalf("应恰好弹一次确认,实际 %d", cf.n)
	}
	for _, want := range []string{"未执行任何构建命令", "不验签名", "装完再换"} {
		if !strings.Contains(out, want) {
			t.Errorf("回执缺 %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", "demo", testutil.ExeName("tool-demo"))); err != nil {
		t.Errorf("产物未落位: %v", err)
	}
}

// TestCmdInstallArtifactCancel 用户拒绝 ⇒ 什么都不发生。
func TestCmdInstallArtifactCancel(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fakeArchPayload(runtime.GOOS, runtime.GOARCH))
	}))
	defer srv.Close()
	h := newInstallHost(t, &installApproval{mode: sdk.ApprovalSmart}, &installConfirm{ok: false})
	out, err := h.cmdInstall([]string{"artifact", srv.URL + "/tool-demo", "demo", "tool-demo"})
	if err != nil {
		t.Fatalf("取消不应报错: %v", err)
	}
	if !strings.Contains(out, "已取消") {
		t.Errorf("回执应说明已取消: %s", out)
	}
}

// fakeArchPayload 只含正确魔数的头(与 internal/install 的 archOf 同源)。
func fakeArchPayload(goos, goarch string) []byte {
	buf := make([]byte, 0x200)
	switch goos {
	case "linux":
		copy(buf, []byte{0x7f, 'E', 'L', 'F'})
		m := uint16(0x3e)
		if goarch == "arm64" {
			m = 0xb7
		}
		binary.LittleEndian.PutUint16(buf[18:20], m)
	case "darwin":
		buf[0], buf[1], buf[2], buf[3] = 0xcf, 0xfa, 0xed, 0xfe
		c := uint32(0x01000007)
		if goarch == "arm64" {
			c = 0x0100000c
		}
		binary.LittleEndian.PutUint32(buf[4:8], c)
	default:
		buf[0], buf[1] = 'M', 'Z'
		off := uint32(0x40)
		binary.LittleEndian.PutUint32(buf[0x3c:0x40], off)
		buf[off], buf[off+1], buf[off+2], buf[off+3] = 'P', 'E', 0, 0
		m := uint16(0x8664)
		if goarch == "arm64" {
			m = 0xaa64
		}
		binary.LittleEndian.PutUint16(buf[off+4:off+6], m)
	}
	return buf
}
