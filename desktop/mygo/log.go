// 壳日志:GUI 进程没有终端,崩了就是窗口凭空消失,日志是唯一能查的东西。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

var (
	logMu   sync.Mutex
	logFile *os.File
	logPath string
)

// logDir 壳日志所在目录(验证分支:用户日志目录,不进数据根)。
func logDir() string {
	if runtime.GOOS == "windows" {
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "gah")
		}
	}
	if v := os.Getenv("HOME"); v != "" {
		return filepath.Join(v, "Library", "Logs", "gah")
	}
	return os.TempDir()
}

// initLog 打开壳日志(失败则只写 stderr,不阻断启动)。
func initLog(version string) {
	logPath = filepath.Join(logDir(), "shell-mygo.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err == nil {
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			logFile = f
		}
	}
	shellLogf("=== gah 壳(mygo)启动 version=%s pid=%d ===", version, os.Getpid())
	if logPath != "" {
		shellLogf("日志: %s", logPath)
	}
}

// shellLogf 写一行壳日志(stderr + 文件)。
func shellLogf(format string, args ...any) {
	line := fmt.Sprintf("%s %s", time.Now().Format("2006-01-02 15:04:05.000"), fmt.Sprintf(format, args...))
	logMu.Lock()
	defer logMu.Unlock()
	fmt.Fprintln(os.Stderr, line)
	if logFile != nil {
		_, _ = fmt.Fprintln(logFile, line)
	}
}

// fail 致命错误:落日志 + stderr(有窗口前退出)。
func fail(format string, args ...any) {
	shellLogf("致命: "+format, args...)
	os.Exit(1)
}
