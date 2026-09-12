// shellpath_test.go:POSIX shell 解析用例。Windows 分支用平台变量覆盖,便于在
// macOS/Linux 上验证逻辑;真机行为仍需 Windows 复核(见 docs/NONDEV_ROADMAP.md §7)。
package sdk

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestResolvePOSIXShellExplicit 显式指定优先。
func TestResolvePOSIXShellExplicit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 /bin/sh")
	}
	const sh = "/bin/sh"
	t.Setenv(ShellPathEnv, sh)
	got, err := ResolvePOSIXShell()
	if err != nil || got != sh {
		t.Fatalf("ResolvePOSIXShell() = %q, %v;want %q", got, err, sh)
	}
}

// TestResolvePOSIXShellExplicitUnusable 显式指定不可用时必须失败,不得静默跳到下一步探测。
func TestResolvePOSIXShellExplicitUnusable(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{filepath.Join(dir, "nope-bash"), dir} {
		t.Setenv(ShellPathEnv, p)
		got, err := ResolvePOSIXShell()
		if err == nil {
			t.Fatalf("%s=%q 应显式失败,got %q", ShellPathEnv, p, got)
		}
		if !strings.Contains(err.Error(), ShellPathEnv) {
			t.Fatalf("错误应含 %s 指引: %v", ShellPathEnv, err)
		}
	}
}

// TestResolvePOSIXShellExplicitNotExecutable 无执行位的文件不算可用(非 Windows)。
func TestResolvePOSIXShellExplicitNotExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无可执行位语义")
	}
	p := filepath.Join(t.TempDir(), "bash")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ShellPathEnv, p)
	if _, err := ResolvePOSIXShell(); err == nil {
		t.Fatal("无执行位应失败")
	}
}

// TestResolvePOSIXShellDefault 未指定时:PATH 上的 sh(非 Windows);Windows 无 Git Bash 时
// 必须是带安装指引的显式错误。任何平台都不得返回空串。
func TestResolvePOSIXShellDefault(t *testing.T) {
	t.Setenv(ShellPathEnv, "")
	got, err := ResolvePOSIXShell()
	if runtime.GOOS == "windows" {
		if err != nil {
			if !strings.Contains(err.Error(), "Git for Windows") {
				t.Fatalf("Windows 缺失错误应给安装指引: %v", err)
			}
			return
		}
		if got == "" {
			t.Fatal("返回空 shell 路径")
		}
		return
	}
	if err != nil || got == "" {
		t.Fatalf("POSIX 平台应有 sh: %q, %v", got, err)
	}
}

// TestWinBashCandidatesNoRelative 环境变量缺失时不得产出相对候选(防「相对 cwd 探测」命中)。
func TestWinBashCandidatesNoRelative(t *testing.T) {
	t.Setenv("ProgramFiles", "")
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("LOCALAPPDATA", "")
	if got := winBashCandidates(); len(got) != 0 {
		t.Fatalf("无环境变量时应为空,got %v", got)
	}
}

// TestWinBashCandidatesPaths 有环境变量时给出 Git/bin/bash.exe 拼接位(含用户级安装)。
func TestWinBashCandidatesPaths(t *testing.T) {
	sep := string(filepath.Separator)
	pf, la := sep+"PF", sep+"LA"
	t.Setenv("ProgramFiles", pf)
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("LOCALAPPDATA", la)
	want := []string{
		filepath.Join(pf, "Git", "bin", "bash.exe"),
		filepath.Join(la, "Programs", "Git", "bin", "bash.exe"),
	}
	got := winBashCandidates()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestShellExecEnv MSYS 路径转换开关只在 Windows 语义下注入。
func TestShellExecEnv(t *testing.T) {
	old := winSemantics
	defer func() { winSemantics = old }()

	winSemantics = false
	if got := ShellExecEnv([]string{"A=1"}); len(got) != 1 {
		t.Fatalf("非 Windows 不应注入: %v", got)
	}
	winSemantics = true
	got := ShellExecEnv([]string{"A=1"})
	if len(got) != 3 || got[1] != "MSYS_NO_PATHCONV=1" || got[2] != "MSYS2_ARG_CONV_EXCL=*" {
		t.Fatalf("Windows 应注入 MSYS 开关: %v", got)
	}
}

// TestIsExecutableFile 目录/缺失/无执行位一律 false(Windows 只判非目录)。
func TestIsExecutableFile(t *testing.T) {
	dir := t.TempDir()
	if isExecutableFile(dir) {
		t.Fatal("目录不是可执行文件")
	}
	if isExecutableFile(filepath.Join(dir, "nope")) {
		t.Fatal("缺失路径不是可执行文件")
	}
	if runtime.GOOS == "windows" {
		return
	}
	noexec := filepath.Join(dir, "noexec")
	if err := os.WriteFile(noexec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isExecutableFile(noexec) {
		t.Fatal("无执行位不应通过")
	}
	exec := filepath.Join(dir, "exec")
	if err := os.WriteFile(exec, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !isExecutableFile(exec) {
		t.Fatal("有执行位应通过")
	}
}
