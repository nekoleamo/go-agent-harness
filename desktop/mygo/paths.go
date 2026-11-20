// paths:sidecar 的命名与默认工作区(纯函数,便于单测)。
//
// 运行落点解析在 stage.go(resolveRuntime:便携/内嵌/磁盘三种来源);这里只留
// 「名字」与「工作区」这两件与平台有关、又到处被引用的小事。
package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// sidecarName sidecar 在磁盘上的文件名。
func sidecarName() string {
	if runtime.GOOS == "windows" {
		return "gah.exe"
	}
	return "gah"
}

// triple 当前平台的产物名后缀(沿用 gen-desktop 脚本的命名)。
func triple() string {
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}
	switch runtime.GOOS {
	case "darwin":
		return arch + "-apple-darwin"
	case "windows":
		return arch + "-pc-windows-msvc"
	default:
		return arch + "-unknown-linux-gnu"
	}
}

// bundlePath 当前运行所在的 .app 路径(不在 bundle 里返回空串)。
//
// 壳进程的 exe 形如 `/Applications/gah.app/Contents/MacOS/gah-desktop` ⇒ 取到 `.app` 那一级。
func bundlePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	i := strings.Index(exe, ".app/Contents/MacOS/")
	if i < 0 {
		return ""
	}
	return exe[:i+len(".app")]
}

// defaultWorkspace sidecar 的初始工作目录(系统目录一律拒绝)。
//
// 与旧壳同口径:能用壳自己的 cwd 就用(用户从终端/文件管理器在哪个目录启动,
// 就默认在哪个目录干活),但系统目录不认 —— 真机上壳的 cwd 常常是 `C:\WINDOWS\system32`
// 或 `/`。显式 GAH_DESKTOP_CWD 优先。
func defaultWorkspace() string {
	if v := os.Getenv("GAH_DESKTOP_CWD"); v != "" {
		if st, err := os.Stat(v); err == nil && st.IsDir() {
			return v
		}
	}
	if cwd, err := os.Getwd(); err == nil && !isSystemDir(cwd) {
		return cwd
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

// isSystemDir 判断目录是不是系统目录(不能当工作区)。
func isSystemDir(dir string) bool {
	clean := filepath.Clean(dir)
	if runtime.GOOS == "windows" {
		if root := os.Getenv("SystemRoot"); root != "" {
			return len(clean) >= len(root) && strings.EqualFold(clean[:len(root)], filepath.Clean(root))
		}
		return false
	}
	return clean == "/" || strings.HasPrefix(clean, "/System") || strings.HasPrefix(clean, "/usr")
}
