// extplugins.go:外部插件(host-bridge 装配的独立进程插件)的宿主侧控制面。
// 由 host-bridge Provide("ctx.extplugins");未装配 = 无该通道(消费方按可选注入处理)。
package sdk

// ExternalPlugins 外部插件运行时控制(宿主服务;sdk 只出契约,实现见 host-bridge)。
//
// 用途:NOND-M1 第 3 步 —— MCP server 配置($GAH_HOME/config/mcp.yaml)改完后,
// 让承接它的外部插件进程重读配置(工具集可能变:direct 全量 / search 只暴露代理工具),
// 免重启 gah。
type ExternalPlugins interface {
	// Reload 重启指定外部插件进程(名字 = 插件二进制的**文件基名**(去平台扩展名),
	// 如 "tool-mcp";发布布局 $GAH_HOME/plugins/tool-mcp/tool-mcp[.exe] 与
	// 扁平布局 $GAH_HOME/plugins/tool-mcp[.exe] 都能定位)。
	// 名字不存在/未安装 = 显式错误(不静默当成功,调用方须能区分"已重载"与"没生效")。
	Reload(name string) error
}
