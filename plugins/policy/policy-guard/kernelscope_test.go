// 协作层与内核层的**共享写入面**单测(2026-10-03)。
//
// 这组用例钉的是本次改动唯一真正要守住的东西:
//
//	① 内核层在场 → 两层同源:内核放行的落点(jail/临时区/包缓存)协作层也放行,
//	   **不再出现「内核放行、协作拒绝」**(那正是用户报的「被回绝后再重新操作」);
//	② 内核层**不在场** → 协作层保持窄口径(只在工作区根内)。那时它是唯一的边界,
//	   放宽等于在 Windows / 无 seatbelt 的机器上直接失守;
//	③ 两个执行面各拿各的清单(shell 面不放系统临时区 —— 它的 TMPDIR 已重定向进 jail);
//	④ 两层都没放行的落点(真越界)仍然拒绝。
package policyguard

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/kernelsandbox"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// needKernel 跳过「本机内核层不在场」的用例(Windows / 无 /usr/bin/sandbox-exec),返回 jail。
func needKernel(t *testing.T) string {
	t.Helper()
	t.Setenv(kernelShellJailEnv, "")
	jail := kernelsandbox.EnsureJailDir()
	spec := kernelsandbox.Spec{
		Mode:   sdk.SandboxWorkspace,
		Root:   t.TempDir(),
		Jail:   jail,
		Switch: kernelToolSwitch,
	}
	if ok, why := kernelsandbox.WouldApply(spec); !ok {
		t.Skipf("本机内核层不在场,该用例不适用: %s", why)
	}
	return jail
}

// sysTemp 系统临时区里的一条**绝对路径**,落在工作区之外。
//
// 为什么不能直接写 "/tmp":① macOS 上 /tmp 是软链,内核按真实路径匹配,得用 ResolvePath;
// ② Windows 上 "/tmp/x" 不是绝对路径(没有盘符),filepath.Join 会把它拼到工作区**底下**,
// 于是「越界探针」反而变成区内路径 —— CI test-windows 就这样红过(AGENTS.md 跨平台纪律②)。
// os.TempDir() 在三个平台上都给出正确形态的绝对路径,且不在 t.TempDir() 里。
func sysTempProbe(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.TempDir(), "gah-scope-probe")
}

// resolvedSysTemp 系统临时区在**内核白名单里的那一份**(ResolvePath 后)。
func resolvedSysTemp(t *testing.T) string {
	t.Helper()
	return kernelsandbox.ResolvePath(os.TempDir())
}

// TestSharedScopeAllowsKernelWritable ①:内核放行的落点,协作层不再多此一举地拒。
func TestSharedScopeAllowsKernelWritable(t *testing.T) {
	jail := needKernel(t)
	root := t.TempDir()
	sp := &SandboxPolicy{root: root, mode: sdk.SandboxWorkspace}
	sp.SetKernelScopes(
		func() []string { return kernelWriteScope(sp, surfaceTool) },
		func() []string { return kernelWriteScope(sp, surfaceShell) },
	)
	if err := sp.ValidatePath(filepath.Join(jail, "gah-scope-probe")); err != nil {
		t.Fatalf("jail 是内核放行的落点,不该被协作层再拒一次: %v", err)
	}
	if err := sp.ValidatePath(sysTempProbe(t)); err != nil {
		t.Fatalf("系统临时区在外部插件面的内核白名单内: %v", err)
	}
	if err := sp.ValidatePath(filepath.Join(outsideProbe(), "x")); err == nil {
		t.Fatal("真正越界的落点仍应拒绝")
	}
}

// TestSurfacesGetOwnScope ③:两个执行面的清单不同 —— shell 面不放系统临时区。
//
// 这条是防止「为了省事用一份清单」的关键用例:若 shell 面也放行 /tmp,内核不会拒(它没放),
// 于是 `shell "echo x > /tmp/log"` 会从「路径层干净地拒」退化成「内核 EPERM 报错」。
func TestSurfacesGetOwnScope(t *testing.T) {
	needKernel(t)
	sp := &SandboxPolicy{root: t.TempDir(), mode: sdk.SandboxWorkspace}
	toolScope := kernelWriteScope(sp, surfaceTool)
	shellScope := kernelWriteScope(sp, surfaceShell)
	sysTmp := resolvedSysTemp(t)
	if !slices.Contains(toolScope, sysTmp) {
		t.Fatalf("工具面(外部插件)应放行系统临时区 %q: %v", sysTmp, toolScope)
	}
	if slices.Contains(shellScope, sysTmp) {
		t.Fatalf("shell 面不应放系统临时区(TMPDIR 已重定向进 jail): %v", shellScope)
	}
	// jail 两面都在(判据要精确到条目,不能只搜子串:测试自己的 TempDir 里也可能带 "jail" 字样)
	if !slices.ContainsFunc(shellScope, func(p string) bool { return strings.HasSuffix(p, "jail") }) {
		t.Fatalf("shell 面应含 jail: %v", shellScope)
	}
}

// TestNoKernelScopeKeepsNarrow ②:内核层不在场(未注入)→ 退回「只在工作区根内」。
func TestNoKernelScopeKeepsNarrow(t *testing.T) {
	p := &SandboxPolicy{root: t.TempDir(), mode: sdk.SandboxWorkspace}
	for _, target := range []string{sysTempProbe(t), filepath.Join(kernelsandbox.EnsureJailDir(), "gah-scope-probe")} {
		if target == "" {
			continue
		}
		if err := p.ValidatePath(target); err == nil {
			t.Fatalf("内核层不在场时,协作层必须自己守住窄口径(%s 被放行了)", target)
		}
	}
}

// TestShellJailOffDisablesScope 开关关掉 → 不放宽(可见的降级,不是静默不同源)。
func TestShellJailOffDisablesScope(t *testing.T) {
	t.Setenv(kernelShellJailEnv, "0")
	sp := &SandboxPolicy{root: t.TempDir(), mode: sdk.SandboxWorkspace}
	for _, surface := range []kernelSurface{surfaceTool, surfaceShell} {
		if got := kernelWriteScope(sp, surface); got != nil {
			t.Fatalf("%s: GAH_SHELL_JAIL=0 时不应放宽写入面,got %v", surface, got)
		}
	}
}

// TestFullAccessModeNoScope full-access 档下内核层不施加 ⇒ 无共享面,协作层也不该被"对齐"。
func TestFullAccessModeNoScope(t *testing.T) {
	t.Setenv(kernelShellJailEnv, "")
	sp := &SandboxPolicy{root: t.TempDir(), mode: sdk.SandboxFullAccess}
	if got := kernelWriteScope(sp, surfaceTool); got != nil {
		t.Fatalf("full-access 档内核层不施加,不应有共享写入面: %v", got)
	}
}

// TestEffectiveScopeContainsJail 共享面确实含 jail(钉住"与内核同源"这件事本身,
// 免得哪天 DefaultRWPaths 一改,这边静默对不齐)。
func TestEffectiveScopeContainsJail(t *testing.T) {
	needKernel(t)
	sp := &SandboxPolicy{root: t.TempDir(), mode: sdk.SandboxWorkspace}
	scope := kernelWriteScope(sp, surfaceTool)
	if len(scope) == 0 {
		t.Fatal("本机内核层在场时应拿到非空写入面")
	}
	if !strings.Contains(strings.Join(scope, "\n"), "jail") {
		t.Fatalf("共享写入面应含 jail 锚点: %v", scope)
	}
}
