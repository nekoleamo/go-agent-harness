package main

import (
	"runtime"
	"strings"
	"testing"
)

// TestRevealCommand 定位命令按平台给对(纯函数,不需要真开文件管理器)。
func TestRevealCommand(t *testing.T) {
	cmd, err := revealCommand("/tmp/a b.txt")
	if err != nil {
		t.Fatalf("应给出命令: %v", err)
	}
	switch runtime.GOOS {
	case "darwin":
		if cmd.Args[0] != "open" || cmd.Args[1] != "-R" {
			t.Fatalf("mac 应 open -R: %v", cmd.Args)
		}
	case "windows":
		if !strings.HasSuffix(strings.ToLower(cmd.Args[0]), "explorer") || cmd.Args[1] != "/select," {
			t.Fatalf("win 应 explorer /select,: %v", cmd.Args)
		}
	default:
		if cmd.Args[0] != "xdg-open" {
			t.Fatalf("linux 应 xdg-open: %v", cmd.Args)
		}
	}
	if _, err := revealCommand(""); err == nil {
		t.Fatal("空路径应报错(否则会打开当前目录,用户会莫名其妙)")
	}
}

// TestIsSystemDir 系统目录不能当工作区(旧壳的真机坑:壳的 cwd 常常是
// C:\WINDOWS\system32 或 /,拿它当工作区会莫名其妙)。
// 反向验证:把 isSystemDir 改成恒 false,第一条断言必红。
func TestIsSystemDir(t *testing.T) {
	switch runtime.GOOS {
	case "windows":
		t.Setenv("SystemRoot", `C:\Windows`)
		if !isSystemDir(`C:\Windows\system32`) {
			t.Fatal("SystemRoot 下应判为系统目录")
		}
		if isSystemDir(`C:\Users\me\proj`) {
			t.Fatal("用户目录不该判为系统目录")
		}
	default:
		for _, d := range []string{"/", "/usr", "/usr/local/bin", "/System/Library"} {
			if !isSystemDir(d) {
				t.Fatalf("%q 应判为系统目录", d)
			}
		}
		for _, d := range []string{"/Users/me/proj", "/tmp/x"} {
			if isSystemDir(d) {
				t.Fatalf("%q 不该判为系统目录", d)
			}
		}
	}
}

// TestOpenExternalSchemeGuard 只放行 http/https:页面里的一行链接不能让系统去打开本地路径。
// 反向验证:去掉 scheme 判断,file:// 一条必红。
func TestOpenExternalSchemeGuard(t *testing.T) {
	sh := &shell{}
	// 非 http(s) 应在**调用系统之前**就被拒(所以不需要真壳实例也能验)。
	for _, bad := range []string{"file:///etc/passwd", "javascript:alert(1)", "ftp://x/y", ""} {
		if err := sh.openExternal(bad); err == nil {
			t.Fatalf("%q 应被拒绝", bad)
		}
	}
}
