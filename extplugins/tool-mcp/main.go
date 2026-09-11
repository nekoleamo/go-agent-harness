// Command tool-mcp MCP client 桥外部化进程(M6.8 工具类全外部化):连接外部 MCP server,
// 工具注册为 mcp_<name>,经桥协议 Definitions/ExecuteNamed 暴露给宿主。
// 配置(两者可并存,取并集):
//
//	GAH_MCP_COMMAND  - 单 MCP server 启动命令(空格分隔参数),工具注册 mcp_<name>(兼容)。
//	GAH_MCP_COMMANDS - 多 MCP server,每行 "name=command args"(# 开头为注释,空行忽略),
//	                   工具注册 mcp_<server>_<name>;server 连接失败记 stderr 跳过
//	                   (对齐宿主跳过失败插件语义,不静默降级:全部失败则 exit 1)。
//
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

// serverSpec 单个 MCP server 描述;name 为空 = 兼容单 server(工具不带 server 前缀)。
type serverSpec struct {
	name string
	cmd  string
	args []string
}

func cleanName(n string) string {
	var sb strings.Builder
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}
	return sb.String()
}

// parseSpecs 解析 GAH_MCP_COMMAND(单)与 GAH_MCP_COMMANDS(多,每行 name=command)。
func parseSpecs() ([]serverSpec, error) {
	var specs []serverSpec
	if cmd := os.Getenv("GAH_MCP_COMMAND"); strings.TrimSpace(cmd) != "" {
		parts := strings.Fields(cmd)
		specs = append(specs, serverSpec{cmd: parts[0], args: parts[1:]})
	}
	raw, ok := os.LookupEnv("GAH_MCP_COMMANDS")
	if ok {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			name, cmd, found := strings.Cut(line, "=")
			name, cmd = strings.TrimSpace(name), strings.TrimSpace(cmd)
			if !found || name == "" || cmd == "" {
				fmt.Fprintf(os.Stderr, "tool-mcp: 忽略无效行(需 name=command): %q\n", line)
				continue
			}
			parts := strings.Fields(cmd)
			specs = append(specs, serverSpec{name: cleanName(name), cmd: parts[0], args: parts[1:]})
		}
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("缺少 MCP server 配置(GAH_MCP_COMMAND 单 server 或 GAH_MCP_COMMANDS 多 server)")
	}
	return specs, nil
}

func main() {
	if v, ok := os.LookupEnv("GAH_PLUGIN"); !ok || v != "gah-external-tool" {
		fmt.Fprintln(os.Stderr, "外部插件缺少握手标识 GAH_PLUGIN")
		os.Exit(1)
	}
	specs, err := parseSpecs()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tool-mcp:", err)
		os.Exit(1)
	}
	tools := map[string]sdk.Tool{}
	failures := []string{}
	for _, sp := range specs {
		cli, cerr := mcpbridge.NewClient(sp.cmd, sp.args)
		if cerr != nil {
			msg := fmt.Sprintf("tool-mcp: server %q 连接失败(跳过): %v", serverLabel(sp), cerr)
			fmt.Fprintln(os.Stderr, msg)
			failures = append(failures, msg)
			continue
		}
		for _, d := range cli.Definitions() {
			name := d.Name // 原始注册名 mcp_<name>
			if sp.name != "" {
				name = "mcp_" + sp.name + "_" + strings.TrimPrefix(d.Name, "mcp_")
			}
			if _, dup := tools[name]; dup {
				fmt.Fprintf(os.Stderr, "tool-mcp: 工具名冲突 %s(保留先注册,跳过)\n", name)
				continue
			}
			def := d // 复制,闭包捕获
			tools[name] = &mcpTool{cli: cli, name: name, def: def}
		}
	}
	if len(tools) == 0 {
		fmt.Fprintln(os.Stderr, "tool-mcp: 无可用工具(全部 server 连接失败或空):", strings.Join(failures, "; "))
		os.Exit(1)
	}
	bridge.ServeTools(tools)
}

func serverLabel(sp serverSpec) string {
	if sp.name == "" {
		return sp.cmd
	}
	return sp.name + "(" + sp.cmd + ")"
}

// mcpTool MCP 工具(外部进程实现,ExecuteNamed 经 Client.Execute 转发 tools/call)。
// name = 注册名(host 可见,多 server 带前缀);def 保留原始定义(Execute 按原始名
// mcp_<tool> 路由到对应 server,二者解耦——Definitions RPC 暴露注册名)。
type mcpTool struct {
	cli  *mcpbridge.Client
	name string
	def  sdk.ToolDefinition
}

func (t *mcpTool) Definition() sdk.ToolDefinition {
	d := t.def
	d.Name = t.name // 对外暴露注册名
	return d
}

func (t *mcpTool) Execute(ctx context.Context, args string) (any, error) {
	out, err := t.cli.Execute(ctx, t.def.Name, args)
	if err != nil {
		return map[string]any{"error": "MCP 调用失败: " + err.Error()}, nil
	}
	return map[string]any{"content": out}, nil
}
