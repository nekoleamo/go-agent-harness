// Command tool-mcp MCP client 桥外部化进程(M6.8 工具类全外部化):连接外部 MCP server,
// 工具经桥协议 Definitions/ExecuteNamed 暴露给宿主。
//
// 配置(NOND-M1 第 2 步起以文件为主,env 向后兼容):
//
//	$GAH_HOME/config/mcp.yaml - servers: [{name, command, args, enabled, mode}]
//	                            mode: direct(默认,工具全量注册)/ search(只暴露
//	                            mcp_search + mcp_call 两个代理工具,省固定前缀)。
//	GAH_MCP_COMMANDS          - 每行 "name=command args"(# 注释/空行忽略),工具 mcp_<server>_<name>
//	GAH_MCP_COMMAND           - 单 server 启动命令(空格分隔参数),工具 mcp_<name>
//
// 优先级:文件条目优先(同名以文件为准,便于 GUI 改模式),env 独有条目照旧装配
// (在设置面板里标为"环境变量"只读)。单个 server 连接失败记 stderr 跳过(对齐宿主跳过
// 失败插件语义,不静默降级:全部不可用则 exit 1)。
package main

import (
	"fmt"
	"os"

	"github.com/nekoleamo/go-agent-harness/internal/mcpconfig"
	bridge "github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/plugins/mcp/mcp-bridge"
)

func main() {
	if v, ok := os.LookupEnv("GAH_PLUGIN"); !ok || v != "gah-external-tool" {
		fmt.Fprintln(os.Stderr, "外部插件缺少握手标识 GAH_PLUGIN")
		os.Exit(1)
	}
	specs, notes, err := mcpconfig.Load()
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, "tool-mcp:", n)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tool-mcp:", err)
		os.Exit(1)
	}
	if len(specs) == 0 {
		fmt.Fprintln(os.Stderr, "tool-mcp: 未配置任何 MCP server(设置面板「MCP」分区或",
			mcpconfig.Path(), "或 GAH_MCP_COMMANDS)")
		os.Exit(1)
	}
	tools, notes, err := mcpbridge.Assemble(specs)
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, "tool-mcp:", n)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tool-mcp:", err)
		os.Exit(1)
	}
	bridge.ServeTools(tools)
}
