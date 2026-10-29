// 壳侧基础件:日志、路径、平台常量。
//
// 日志位置沿用 Rust 壳的口径(Tauri `app_local_data_dir()`):mac 是
// `~/Library/Application Support/dev.gah.desktop/gah-shell.log`,win 是
// `%LOCALAPPDATA%\dev.gah.desktop\gah-shell.log`。**它不是 gah 的运行数据**,
// 所以不受「一切数据落 GAH_HOME 单根」那条纪律约束(壳不是 gah 本体,
// 也不该往 sidecar 的 gah-data/ 里塞自己的日志)。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// appID 与 Rust 壳的 tauri identifier 一致(升级/卸载路径、数据目录名沿用旧值,
// 免得老用户升级后日志与暂存目录换位置)。
const appID = "dev.gah.desktop"

var (
	logMu   sync.Mutex
	logPath string
)

// initLog 解析壳日志路径并写入本次运行的头几行。
func initLog(version string) {
	logPath = filepath.Join(localDataDir(), "gah-shell.log")
	shellLogf("=== gah 壳(go/wails v3)启动 pid=%d version=%s os=%s/%s ===",
		os.Getpid(), version, runtime.GOOS, runtime.GOARCH)
	shellLogf("壳日志: %s", logPath)
}

// localDataDir 壳自己的本地数据目录(mac 用 Application Support,win 用 LOCALAPPDATA)。
// 解析失败一律退到临时目录:壳日志写不出去不该阻止应用启动。
func localDataDir() string {
	switch runtime.GOOS {
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, appID)
		}
	case "darwin":
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, "Library", "Application Support", appID)
		}
	default:
		if v := os.Getenv("XDG_DATA_HOME"); v != "" {
			return filepath.Join(v, appID)
		}
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, ".local", "share", appID)
		}
	}
	return filepath.Join(os.TempDir(), appID)
}

// shellLogf 追加一行壳日志(带毫秒时间戳)。写失败一律忽略 —— 日志不该拖垮壳。
// 超过 1MiB 先轮转一份 .1(与 Rust 壳同阈值):壳日志是用户发回来定位问题的,
// 无限增长会变成负担,完全不写又会丢掉「到底走到哪一步」。
func shellLogf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	logMu.Lock()
	defer logMu.Unlock()
	if logPath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return
	}
	rotateIfTooBig(logPath)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	ts := time.Now().Format("2006-01-02T15:04:05.000")
	_, _ = fmt.Fprintf(f, "%s %s\n", ts, line)
}

// rotateIfTooBig 壳日志超过 1MiB 时把当前文件挪成 .1(覆盖旧的那份)。
func rotateIfTooBig(path string) {
	st, err := os.Stat(path)
	if err != nil || st.Size() < 1<<20 {
		return
	}
	_ = os.Remove(path + ".1")
	_ = os.Rename(path, path+".1")
}

// shellLogPath 供诊断页面显示「日志在哪」。
func shellLogPath() string { return logPath }
