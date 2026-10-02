// 本文件:toolkit 二进制里的 tool-mcp 角色。
//
// 2026-10-02 瘦身:原来 tool-basic / tool-workflow / tool-mcp / tool-subagent 是**四个**
// 独立二进制,各自静态链一遍运行时 + host-bridge + sdk —— 实测四份合计 30.8 MiB,
// 而合成一个只要 9.6 MiB(省 69%)。合成的是**二进制**,不是**进程**:宿主仍按角色逐个
// 起进程(崩溃隔离不丢),配置文件里的插件 id 一个都没变。
//
// 自合并以来本文件内容**逐字未改**,只把 main() 改成由 toolkit 分派调用。
package main

import (
	"fmt"
	"os"

	"github.com/nekoleamo/go-agent-harness/internal/mcpconfig"
	bridge "github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/plugins/mcp/mcp-bridge"
)

// 由 tool-kit 的分派调用(见 main.go)。
func runMCP() {
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
		// 自述空闲:没有配置任何 server = 没用上这个功能,不是故障。声明标记后以 0 退出,
		// 宿主记 INFO 跳过 —— 否则用户每次启动都会看到一条「外部插件加载失败」的 ERROR。
		// (配了 server 但全部连不上仍走下面的 exit 1:那才是真失败,不静默降级。)
		fmt.Fprintln(os.Stderr, bridge.IdleMarker+"未配置任何 MCP server(设置面板「MCP server」段或 "+
			mcpconfig.Path()+" 或 GAH_MCP_COMMANDS)")
		os.Exit(0)
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
