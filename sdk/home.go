// home.go:数据根解析的插件侧唯一入口(便携纪律,R10 收敛)。
package sdk

import (
	"os"
	"path/filepath"
	"strings"
)

// Home 返回 gah 数据根(boot 通过 GAH_HOME 恒设,插件经它派生子目录)。
//
// 空值只出现在不经 cmd/gah 的嵌入/单测:此时回 TempDir —— 便携纪律明确禁止
// 回退 ~/.gah、cwd 相对路径或系统根(2026-09 R8/R10 收敛;~/.gah 兜底已弃用)。
// 注意:GAH_HOME 是宿主内部贯通变量,用户不可经 env 指定(数据根唯一 = 二进制同级 gah-data/)。
func Home() string {
	if h := os.Getenv("GAH_HOME"); h != "" {
		return h
	}
	return os.TempDir()
}

// JailDir 数据根下的临时/缓存收敛根($GAH_HOME/jail)。
//
// 两类消费者共用它:shell 的环境 jail(GAH_SHELL_JAIL,把 TMPDIR/GOCACHE 等重定向到这里)
// 与外部进程的内核沙箱白名单锚点(MCP server / 插件的临时区)。
// 共用一根的理由:临时区是“可盘点、可整体清理”的东西,分成多个根只会让清理与白名单各自漂移。
func JailDir() string { return filepath.Join(Home(), "jail") }

// UserHomes 用户家目录**候选**(HOME → USERPROFILE → os.UserHomeDir),归一去重、按优先级返回。
//
// 为什么要候选而不是一家(2026-09-27 跨平台复核,第二轮):同一个进程里能读到的家目录在 Windows 上
// 不止一个来源 —— `$HOME` 是 MSYS/Git Bash 的口径(桌面壳启动的 gah.exe **常常没设**),
// `%USERPROFILE%` 是 Windows 原生口径(Go 的 `os.UserHomeDir` 在 Unix 上其实也只回 `$HOME`,
// 只有 Windows 才回 USERPROFILE)。此前 sdk 只认 `$HOME`、policy-guard 只认 `os.UserHomeDir()`,
// 于是同一台机器上"审批层拦、内核层放"(见 CredentialDenyDirs 的注释)。
//
// 两种用法(调用方按语义选,不要混):
//   - **拒绝面取并集**(宁多拦不漏):CredentialDenyDirs / 审批目录表;
//   - **展开面取首个**(UserHome,与用户 shell 的 `~` 同源):`~`/`$HOME` 展开。
func UserHomes() []string {
	var out []string
	seen := map[string]bool{}
	add := func(h string) {
		h = strings.TrimSpace(h)
		if h == "" {
			return
		}
		h = filepath.Clean(h)
		if seen[h] {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	add(os.Getenv("HOME"))
	add(os.Getenv("USERPROFILE"))
	if h, err := os.UserHomeDir(); err == nil {
		add(h)
	}
	return out
}

// UserHome 首选家目录(展开 `~`/`$HOME` 用);无任何候选时返回空串。
func UserHome() string {
	if hs := UserHomes(); len(hs) > 0 {
		return hs[0]
	}
	return ""
}
