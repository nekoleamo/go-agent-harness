// Command tool-echo 示例外部工具插件(独立进程,崩溃不拖垮宿主,对齐 M5 外部插件桥)。
// 编译:go build -o <dir>/tool-echo ./extplugins/tool-echo;宿主 host-bridge 扫描该目录加载。
package main

import (
	"encoding/json"
	"fmt"
	"net/rpc"
	"os"
	"strings"

	"github.com/hashicorp/go-plugin"

	bridge "github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func main() {
	// 握手校验(go-plugin 惯例:环境变量)
	if v, ok := os.LookupEnv("GAH_PLUGIN"); !ok || v != "gah-external-tool" {
		fmt.Fprintln(os.Stderr, "外部插件缺少握手标识 GAH_PLUGIN")
		os.Exit(1)
	}
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: bridge.Handshake(),
		Plugins: map[string]plugin.Plugin{
			"tool": &echoPlugin{},
		},
	})
}

type echoPlugin struct{}

func (p *echoPlugin) Server(*plugin.MuxBroker) (any, error) { return &echoRPCServer{}, nil }
func (p *echoPlugin) Client(b *plugin.MuxBroker, c *rpc.Client) (any, error) {
	return nil, fmt.Errorf("外部插件不需要 client 侧")
}

// echoRPCServer 实现桥协议(net/rpc 方法签名对齐 bridge.ToolServer)。
type echoRPCServer struct{}

// Definition 返回工具定义 JSON。
func (s *echoRPCServer) Definition(args struct{}, reply *string) error {
	b, err := json.Marshal(sdk.ToolDefinition{
		Name:        "echo",
		Description: "外部插件演示工具:回显参数(独立进程,崩溃不影响宿主)",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"text": map[string]any{"type": "string"}},
			"required":   []any{"text"},
		},
	})
	if err != nil {
		return err
	}
	*reply = string(b)
	return nil
}

// Execute 回显 text 参数。
func (s *echoRPCServer) Execute(args *bridge.ExecArgs, reply *bridge.ExecReply) error {
	var a struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(args.JSONArgs), &a); err != nil {
		reply.Error = "echo: args: " + err.Error()
		return nil
	}
	b, _ := json.Marshal(map[string]any{"echo": "外部插件: " + a.Text})
	reply.Content = string(b)
	return nil
}

// Commands M14 外部命令桥示例:同一外部插件可同时提供工具与命令。
// 声明 /echo 命令(一级自由参数 文本;无枚举级)。
func (s *echoRPCServer) Commands(args struct{}, reply *string) error {
	b, err := json.Marshal([]bridge.CommandDTO{
		{Name: "echo", Usage: "/echo <文本>", Desc: "外部插件命令示例:回显文本(与工具共存)",
			Args: []bridge.CommandArgDTO{{FreeArgs: []string{"文本"}}}},
	})
	if err != nil {
		return err
	}
	*reply = string(b)
	return nil
}

// RunCommand 命令执行(宿主 /echo 分发 → 外部进程执行,输出文本回宿主 TUI meta 行)。
func (s *echoRPCServer) RunCommand(args *bridge.RunCommandArgs, reply *bridge.ExecReply) error {
	if args.Name != "echo" {
		reply.Error = fmt.Sprintf("外部插件无此命令 %q", args.Name)
		return nil
	}
	reply.Content = "外部命令 echo: " + strings.Join(args.Args, " ")
	return nil
}
