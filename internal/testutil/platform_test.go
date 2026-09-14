package testutil

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 本包是跨平台测试辅助,自身也要覆盖:覆盖率门不允许非豁免包覆盖率为 0
// (见 scripts/coverage-check.sh 豁免表,本包不在其中 —— 辅助包理应有测试)。

func TestExeName(t *testing.T) {
	got := ExeName("tool-echo")
	if IsWindows() {
		if got != "tool-echo.exe" {
			t.Fatalf("ExeName(tool-echo) = %q, Windows 上应补 .exe", got)
		}
	} else if got != "tool-echo" {
		t.Fatalf("ExeName(tool-echo) = %q, 非 Windows 应原样返回", got)
	}
	// 契约:入参只应是基名。误传已带 .exe 的名字会得到 x.exe.exe ——
	// 固化该行为,避免有人以为它会做幂等判断。
	if ExeName("x.exe") != "x.exe"+map[bool]string{true: ".exe"}[IsWindows()] {
		t.Fatal("ExeName 对已含后缀的基名行为改变,需同步调用方")
	}
}

func TestIsWindows(t *testing.T) {
	if IsWindows() != (runtime.GOOS == "windows") {
		t.Fatalf("IsWindows() 与 runtime.GOOS(%s) 不一致", runtime.GOOS)
	}
}

func TestPosixPerm(t *testing.T) {
	if PosixPerm() != (runtime.GOOS != "windows") {
		t.Fatalf("PosixPerm() 与 runtime.GOOS(%s) 不一致", runtime.GOOS)
	}
	// 两者必须互斥:支持 POSIX 权限位 ⇔ 非 Windows
	if PosixPerm() == IsWindows() {
		t.Fatal("PosixPerm 与 IsWindows 不是互斥关系")
	}
}

func TestShellPath(t *testing.T) {
	in := filepath.Join("C:", "Users", "a", "ws")
	got := ShellPath(in)
	if strings.Contains(got, `\`) {
		t.Fatalf("ShellPath(%q) = %q,不应含反斜杠(sh 会当转义符吃掉)", in, got)
	}
	if !IsWindows() && got != in {
		t.Fatalf("POSIX 平台应原样返回,得到 %q", got)
	}
}

func TestSkipNoPTY(t *testing.T) {
	// Windows 上跳过、其他平台放行。放进子测试:子测试的跳过不会带走父测试,
	// 两条分支(跳过 / 放行)都能被覆盖到。
	t.Run("平台分支", func(t *testing.T) {
		SkipNoPTY(t)
		if IsWindows() {
			t.Fatal("Windows 上 SkipNoPTY 应已跳过,不该执行到这里")
		}
	})
}
