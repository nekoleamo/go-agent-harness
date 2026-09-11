// home.go:数据根解析的插件侧唯一入口(便携纪律,R10 收敛)。
package sdk

import "os"

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
