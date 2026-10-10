package sdk

import (
	"runtime"
	"strings"
	"testing"
)

func TestExeSuffix(t *testing.T) {
	want := ""
	if runtime.GOOS == "windows" {
		want = ".exe"
	}
	if got := ExeSuffix(); got != want {
		t.Fatalf("ExeSuffix() = %q, want %q", got, want)
	}
}

// TestBinaryName 补平台扩展名:非 Windows 恒为恒等;Windows 补 .exe(已带则不重复补)。
func TestBinaryName(t *testing.T) {
	if ExeSuffix() == "" {
		if got := BinaryName("tool-x"); got != "tool-x" {
			t.Fatalf("非 Windows 应恒等, got %q", got)
		}
	} else {
		if got := BinaryName("tool-x"); got != "tool-x.exe" {
			t.Fatalf("Windows 应补 .exe, got %q", got)
		}
	}
	// 已带扩展名(含大写)不重复补 —— 作者显式写的名字 / 已是平台名的都不该变成 .exe.exe
	for _, in := range []string{"tool-x.exe", "TOOL-X.EXE"} {
		if got := BinaryName(in); got != in {
			t.Fatalf("BinaryName(%q) = %q,不该重复补", in, got)
		}
	}
	if got := BinaryName("tool-x"); strings.Contains(got, ".exe.exe") {
		t.Fatalf("不得双后缀: %q", got)
	}
}
