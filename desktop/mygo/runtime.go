// 运行落点:sidecar 从哪来(验证分支只做"能跑起来"的最小解析)。
//
// 口径与 Wails 壳一致:GAH_SIDECAR 显式指定 > 与壳同目录 > 开发布局 binaries/gah-<triple>;
// 数据根由 sidecar 自己按"二进制同级 gah-data/"算,壳不传 GAH_HOME。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// sidecarName sidecar 在磁盘上的文件名。
func sidecarName() string {
	if runtime.GOOS == "windows" {
		return "gah.exe"
	}
	return "gah"
}

// findSidecar 找 sidecar 二进制。
func findSidecar() (string, error) {
	if v := os.Getenv("GAH_SIDECAR"); v != "" {
		if _, err := os.Stat(v); err != nil {
			return "", fmt.Errorf("GAH_SIDECAR 指向的文件不存在: %w", err)
		}
		return v, nil
	}
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		cands := []string{
			filepath.Join(dir, sidecarName()),
			// 开发布局:壳在 desktop/mygo/build/,产物在 desktop/mygo/binaries/ 或 desktop/wails/binaries/
			filepath.Join(dir, "binaries", sidecarName()+"-"+triple()),
			filepath.Join(dir, "..", "binaries", sidecarName()+"-"+triple()),
			filepath.Join(dir, "..", "..", "wails", "binaries", sidecarName()+"-"+triple()),
		}
		for _, c := range cands {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return c, nil
			}
		}
	}
	return "", fmt.Errorf("找不到 sidecar(设 GAH_SIDECAR,或先跑 bash scripts/gen-desktop-wails.sh)")
}

// defaultWorkspace sidecar 的初始工作目录(系统目录一律拒绝)。
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
			return len(clean) >= len(root) && equalFoldPrefix(clean, filepath.Clean(root))
		}
		return false
	}
	return clean == "/" || clean == "/System" || clean == "/usr"
}

// equalFoldPrefix 大小写不敏感的前缀判断(Windows 路径)。
func equalFoldPrefix(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return equalFold(s[:len(prefix)], prefix)
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
