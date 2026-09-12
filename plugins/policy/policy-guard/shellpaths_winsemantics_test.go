// shellpaths_winsemantics_test.go:Windows/MSYS 命令语义分支用例。
// 用平台变量覆盖,便于在 macOS/Linux 上验证逻辑;真机行为仍需 Windows 复核
// (见 docs/NONDEV_ROADMAP.md §7)。仓内 Windows CI 会真跑本文件(变量默认 true)。
package policyguard

import (
	"strings"
	"testing"
)

// withWinSemantics 临时切换 Windows 命令语义开关(测试结束自动还原)。
func withWinSemantics(t *testing.T, on bool) {
	t.Helper()
	old := shellWinSemantics
	shellWinSemantics = on
	t.Cleanup(func() { shellWinSemantics = old })
}

// TestWinRootRelativePath 只有「Windows 语义 + 无卷名 + 前导斜杠」才算 MSYS 根相对路径。
func TestWinRootRelativePath(t *testing.T) {
	withWinSemantics(t, false)
	for _, p := range []string{"/tmp/x", "/c/repo/f", `\foo`, `C:\repo\f`} {
		if winRootRelativePath(p) {
			t.Fatalf("非 Windows 语义下不应判定: %q", p)
		}
	}
	withWinSemantics(t, true)
	for _, p := range []string{"/tmp/x", "/c/repo/f", "/usr/bin/env", `\foo`} {
		if !winRootRelativePath(p) {
			t.Fatalf("Windows 语义下应判定为根相对: %q", p)
		}
	}
	// 带卷名绝对路径与普通相对路径不受影响(UNC 依赖 Windows 上的 filepath.VolumeName,
	// 本机无法覆盖 → 列入真机清单)。
	for _, p := range []string{`C:\repo\f`, `c:/repo/f`, "rel/x", ".", "sub/dir"} {
		if winRootRelativePath(p) {
			t.Fatalf("有卷名/相对路径不应判定: %q", p)
		}
	}
}

// TestMakeShellPathWinRootRelativeIsUnresolvable Windows 语义下根相对写目标 = 不可裁决(拒绝)。
func TestMakeShellPathWinRootRelativeIsUnresolvable(t *testing.T) {
	const root = "/ws"
	withWinSemantics(t, true)
	for _, raw := range []string{"/tmp/o", "/c/o", "/usr/bin/env"} {
		p, ok := makeShellPath(raw, true, true, root)
		if !ok || !p.Unresolvable || !p.Write {
			t.Fatalf("%q 应判为不可裁决写目标: %+v ok=%v", raw, p, ok)
		}
	}
	// 带卷名绝对路径、工作区内相对路径不受影响
	for _, raw := range []string{`C:\ws\o`, "rel/o", "./o"} {
		p, ok := makeShellPath(raw, true, true, root)
		if !ok || p.Unresolvable {
			t.Fatalf("%q 不应被判不可裁决: %+v ok=%v", raw, p, ok)
		}
	}
	// 非 Windows 语义:/tmp/o 仍是普通绝对路径(行为不变,防回归)
	withWinSemantics(t, false)
	if p, _ := makeShellPath("/tmp/o", true, true, root); p.Unresolvable {
		t.Fatalf("非 Windows 语义下 /tmp/o 不应不可裁决: %+v", p)
	}
}

// TestShellCmdPathsWinRootRelative 端到端:命令文本里的 MSYS 根相对写目标被标为不可裁决
// (CheckShellCommand 据此拒绝)。
func TestShellCmdPathsWinRootRelative(t *testing.T) {
	withWinSemantics(t, true)
	for _, cmd := range []string{`echo x > /tmp/o`, `echo x > /c/o`, `rm -rf /tmp/x`} {
		var found bool
		for _, p := range shellCmdPaths(cmd) {
			if p.Write && p.Unresolvable {
				found = true
			}
		}
		if !found {
			t.Fatalf("%q 应含不可裁决写目标,got %+v", cmd, shellCmdPaths(cmd))
		}
	}
	// 读语义不阻断(`cat /tmp/x` 只做凭据类判定,不因根相对而拒)
	for _, p := range shellCmdPaths(`cat /tmp/x`) {
		if p.Unresolvable {
			t.Fatalf("读路径不应因 MSYS 根相对被判不可裁决: %+v", p)
		}
	}
}

// TestDevicePathWindowsNames Windows 保留设备名只在 Windows 语义下豁免。
func TestDevicePathWindowsNames(t *testing.T) {
	withWinSemantics(t, false)
	if isDevicePath("NUL") {
		t.Fatal("非 Windows 语义不应豁免 NUL(POSIX 上一个真名叫 nul 的文件仍需裁决)")
	}
	withWinSemantics(t, true)
	for _, p := range []string{"NUL", "nul", "con", "PRN", "aux"} {
		if !isDevicePath(p) {
			t.Fatalf("Windows 语义应豁免设备名: %q", p)
		}
	}
	for _, p := range []string{"/tmp/x", "out.txt", "/dev/null"} {
		if isDevicePath(p) != strings.HasPrefix(p, "/dev/") {
			t.Fatalf("设备判定与预期不符: %q", p)
		}
	}
}
