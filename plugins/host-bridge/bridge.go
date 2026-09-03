// 宿主侧桥:扫描外部插件目录,加载 tool-* 二进制,注册为 sdk.Tool。
package hostbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/rpc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/go-plugin"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-bridge。requires ctx.tools;data.dir 指定外部插件目录。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-bridge" }

// Start 扫描目录并注册外部工具。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var dir string
	if m != nil && m.Data != nil {
		if d, ok := m.Data["dir"].(string); ok && d != "" {
			dir = d
		}
	}
	if dir == "" {
		dir = "extplugins"
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	disposers := []sdk.Disposer{}
	loaded := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasPrefix(d.Name(), "tool-") {
			return nil
		}
		t, kill, lerr := loadExternalTool(path)
		if lerr != nil {
			return fmt.Errorf("host-bridge: 加载 %s: %w", path, lerr)
		}
		disposers = append(disposers, tools.Register(t))
		disposers = append(disposers, kill)
		loaded++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if loaded == 0 {
		return func() {}, nil
	}
	return func() {
		for i := len(disposers) - 1; i >= 0; i-- {
			disposers[i]()
		}
	}, nil
}

// loadExternalTool 启动外部插件进程并返回工具包装(崩溃隔离:RPC 失败转结构化错误)。
func loadExternalTool(bin string) (sdk.Tool, sdk.Disposer, error) {
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: handshake,
		Plugins: map[string]plugin.Plugin{
			pluginName: &toolPluginBridge{},
		},
		Cmd: exec.Command(bin),
	})
	rpcClient, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, nil, err
	}
	raw, err := rpcClient.Dispense(pluginName)
	if err != nil {
		client.Kill()
		return nil, nil, err
	}
	tc, ok := raw.(*toolRPCClient)
	if !ok {
		client.Kill()
		return nil, nil, fmt.Errorf("host-bridge: 意外的插件类型 %T", raw)
	}
	return tc, func() {
		rpcClient.Close()
		client.Kill()
	}, nil
}

// rpcTimeout 外部 RPC 调用超时(崩溃隔离:死进程快速失败而非死等)。
const rpcTimeout = 3 * time.Second

// toolPluginBridge 桥插件:连接 net/rpc,Client() 返回 gob 转发客户端。
type toolPluginBridge struct{}

func (p *toolPluginBridge) Server(*plugin.MuxBroker) (any, error) {
	return nil, fmt.Errorf("server 侧由外部插件提供")
}
func (p *toolPluginBridge) Client(b *plugin.MuxBroker, c *rpc.Client) (any, error) {
	return &toolRPCClient{client: c}, nil
}

// toolRPCClient 实现 sdk.Tool(经 RPC 转发)。
type toolRPCClient struct {
	client *rpc.Client
}

func (t *toolRPCClient) Definition() sdk.ToolDefinition {
	var defRaw string
	if err := t.client.Call("Plugin.Definition", struct{}{}, &defRaw); err != nil {
		return sdk.ToolDefinition{Name: "external-error", Description: "外部插件不可达: " + err.Error()}
	}
	var def sdk.ToolDefinition
	_ = json.Unmarshal([]byte(defRaw), &def)
	return def
}

func (t *toolRPCClient) Execute(ctx context.Context, args string) (any, error) {
	type rpcOut struct {
		reply ExecReply
		err   error
	}
	ch := make(chan rpcOut, 1)
	go func() {
		var reply ExecReply
		err := t.client.Call("Plugin.Execute", &ExecArgs{JSONArgs: args}, &reply)
		ch <- rpcOut{reply, err}
	}()
	// RPC 无内置超时:3s 超时 + ctx 取消 → 结构化"不可达"(崩溃隔离:外部进程死亡后调用快速失败)
	var out rpcOut
	select {
	case out = <-ch:
	case <-ctx.Done():
		return map[string]any{"error": "外部插件调用取消 " + ctx.Err().Error()}, nil
	case <-time.After(rpcTimeout):
		return map[string]any{"error": "外部插件不可达(进程崩溃或超时)"}, nil
	}
	if out.err != nil {
		return map[string]any{"error": "外部插件不可达(进程崩溃?): " + out.err.Error()}, nil
	}
	if out.reply.Error != "" {
		return map[string]any{"error": out.reply.Error}, nil
	}
	var val any
	if err := json.Unmarshal([]byte(out.reply.Content), &val); err == nil {
		return val, nil
	}
	return out.reply.Content, nil
}
