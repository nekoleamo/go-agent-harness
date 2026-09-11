// export.go:导出 MCP client(供 extplugins/tool-mcp 外部化进程复用,非插件装配)。
// 封装 mcpClient/toolDef:NewClient → Definitions → Execute(经 tools/call 转发)。
package mcpbridge

import (
	"context"
	"encoding/json"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Client MCP client(外部进程用;插件装配仍走 Plugin.Start,不经此构造)。
type Client struct {
	cli   *mcpClient
	defs  []sdk.ToolDefinition
	names map[string]string
}

// NewClient spawn MCP server 并完成 initialize/tools/list 握手。
func NewClient(command string, args []string) (*Client, error) {
	cli, err := spawn(command, args)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if err := cli.initialize(ctx); err != nil {
		cli.close()
		return nil, err
	}
	defs, err := cli.toolsList(ctx)
	if err != nil {
		cli.close()
		return nil, err
	}
	c := &Client{cli: cli, names: map[string]string{}}
	for _, d := range defs {
		name := "mcp_" + d.Name
		c.names[name] = d.Name
		// MCP server 不声明超时 → 桥默认 3s 会误杀浏览器/DB/代码执行类慢工具
		// (且外部进程仍在跑并持锁,后续调用级联超时)。声明较长默认;宿主 ctx 仍可中断。
		c.defs = append(c.defs, sdk.ToolDefinition{
			Name: name, Description: d.Description, InputSchema: d.InputSchema,
			TimeoutMs: mcpToolTimeoutMs,
		})
	}
	return c, nil
}

// mcpToolTimeoutMs MCP 工具默认超时(毫秒):MCP server 不提供超时声明,
// 桥默认 3s 对慢 server 过短,故统一声明一个较宽松的默认值。
const mcpToolTimeoutMs = 120_000

// Definitions 全部 MCP 工具定义(经桥协议暴露,命名 mcp_<name>)。
func (c *Client) Definitions() []sdk.ToolDefinition { return c.defs }

// Execute 转发一次 tools/call(按注册名 mcp_<name> 还原原始名)。
func (c *Client) Execute(ctx context.Context, name, args string) (string, error) {
	raw, ok := c.names[name]
	if !ok {
		return "", nil
	}
	var arguments map[string]any
	if json.Unmarshal([]byte(args), &arguments) != nil {
		arguments = map[string]any{"input": args}
	}
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := c.cli.call(ctx, "tools/call", map[string]any{
		"name":      raw,
		"arguments": arguments,
	}, &r); err != nil {
		return "", err
	}
	var sb string
	for _, ct := range r.Content {
		sb += ct.Text
	}
	return sb, nil
}

// Close 关闭 MCP server 进程。
func (c *Client) Close() { c.cli.close() }
