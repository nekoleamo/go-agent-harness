// home.go:数据根解析的插件侧唯一入口(便携纪律,R10 收敛)。
package sdk

import (
	"os"
	"path/filepath"
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
