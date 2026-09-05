// Command tool-mcp MCP client 桥外部化进程(M6.8 工具类全外部化):连接外部 MCP server,
// 工具注册为 mcp_<name>,经桥协议 Definitions/ExecuteNamed 暴露给宿主。
// 配置:环境变量 GAH_MCP_COMMAND 指定 MCP server 启动命令(空格分隔参数),
// 与内嵌 mcp-bridge 的 data.command 同语义;宿主侧由 host-bridge 扫描加载。
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	bridge "github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/plugins/mcp/mcp-bridge"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func main() {
	if v, ok := os.LookupEnv("GAH_PLUGIN"); !ok || v != "gah-external-tool" {
		fmt.Fprintln(os.Stderr, "外部插件缺少握手标识 GAH_PLUGIN")
		os.Exit(1)
	}
	command := os.Getenv("GAH_MCP_COMMAND")
	if strings.TrimSpace(command) == "" {
		fmt.Fprintln(os.Stderr, "tool-mcp: 缺少 GAH_MCP_COMMAND(MCP server 启动命令)")
		os.Exit(1)
	}
	parts := strings.Fields(command)
	cli, err := mcpbridge.NewClient(parts[0], parts[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "tool-mcp: MCP 连接失败:", err)
		os.Exit(1)
	}
	tools := map[string]sdk.Tool{}
	for _, d := range cli.Definitions() {
		def := d // 复制,闭包捕获
		tools[def.Name] = &mcpTool{cli: cli, def: def}
	}
	bridge.ServeTools(tools)
}

// mcpTool MCP 工具(外部进程实现,ExecuteNamed 经 Client.Execute 转发 tools/call)。
type mcpTool struct {
	cli *mcpbridge.Client
	def sdk.ToolDefinition
}

func (t *mcpTool) Definition() sdk.ToolDefinition { return t.def }

func (t *mcpTool) Execute(ctx context.Context, args string) (any, error) {
	out, err := t.cli.Execute(ctx, t.def.Name, args)
	if err != nil {
		return map[string]any{"error": "MCP 调用失败: " + err.Error()}, nil
	}
	return map[string]any{"content": out}, nil
}
