// serve.go:外部插件进程的服务端通用入口(P1 外部化;随 tool-basic 等外部二进制编译)。
// ServeTools 暴露多工具:Definitions(复数)枚举 + ExecuteNamed 按名执行;
// 旧单工具协议(Definition/Execute)保持兼容(单工具进程亦可用)。
package hostbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/rpc"
	"os"
	"sort"

	"github.com/hashicorp/go-plugin"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// ServeTools 启动外部插件进程(gRPC 桥服务端),tools 为工具名→实现表。
// 握手标识 GAH_PLUGIN=gah-external-tool 缺失即拒绝启动(防误跑)。
func ServeTools(tools map[string]sdk.Tool) {
	if v, ok := os.LookupEnv("GAH_PLUGIN"); !ok || v != "gah-external-tool" {
		log.Fatalf("外部插件缺少握手标识 GAH_PLUGIN(gah-external-tool)")
	}
	if len(tools) == 0 {
		log.Fatal("外部插件未提供任何工具")
	}
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: handshake,
		Plugins: map[string]plugin.Plugin{
			pluginName: &toolServerBridge{tools: tools},
		},
	})
}

// toolServerBridge go-plugin 服务端:Server() 返回 net/rpc server 实现。
type toolServerBridge struct {
	tools map[string]sdk.Tool
}

func (p *toolServerBridge) Server(*plugin.MuxBroker) (any, error) {
	return &toolServer{tools: p.tools}, nil
}
func (p *toolServerBridge) Client(b *plugin.MuxBroker, c *rpc.Client) (any, error) {
	return nil, fmt.Errorf("外部插件不需要 client 侧(宿主侧经 host-bridge)")
}

// toolServer 桥协议服务端(net/rpc 方法签名对齐桥约定)。
type toolServer struct {
	tools map[string]sdk.Tool
}

// Definitions 返回全部工具定义(JSON 数组;新协议,host 侧优先)。
func (s *toolServer) Definitions(args struct{}, reply *string) error {
	type td struct {
		Name        string
		Description string
		InputSchema map[string]any
		TimeoutMs   int64
	}
	names := make([]string, 0, len(s.tools))
	for n := range s.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	var defs []td
	for _, n := range names {
		d := s.tools[n].Definition()
		defs = append(defs, td{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema, TimeoutMs: d.TimeoutMs})
	}
	b, err := json.Marshal(defs)
	if err != nil {
		return err
	}
	*reply = string(b)
	return nil
}

// Definition 兼容旧协议:单工具进程返回唯一定义;多工具进程报错(host 侧应用新协议)。
func (s *toolServer) Definition(args struct{}, reply *string) error {
	if len(s.tools) != 1 {
		return fmt.Errorf("多工具进程须用 Definitions(新协议)")
	}
	for _, t := range s.tools {
		b, err := json.Marshal(t.Definition())
		if err != nil {
			return err
		}
		*reply = string(b)
		return nil
	}
	return nil
}

// Execute 兼容旧协议(单工具进程)。
func (s *toolServer) Execute(args *ExecArgs, reply *ExecReply) error {
	if len(s.tools) != 1 {
		return fmt.Errorf("多工具进程须用 ExecuteNamed(新协议)")
	}
	for _, t := range s.tools {
		return s.exec(t, "", args.JSONArgs, reply)
	}
	return nil
}

// ExecuteNamed 新协议:按工具名执行。
func (s *toolServer) ExecuteNamed(args *ExecNamedArgs, reply *ExecReply) error {
	t, ok := s.tools[args.Name]
	if !ok {
		reply.Error = fmt.Sprintf("外部插件无此工具 %q", args.Name)
		return nil
	}
	return s.exec(t, args.Name, args.JSONArgs, reply)
}

// exec 执行并写入结果(参数错误/工具错误 → 结构化 reply.Error)。
func (s *toolServer) exec(t sdk.Tool, name, jsonArgs string, reply *ExecReply) error {
	res, err := t.Execute(context.Background(), jsonArgs)
	if err != nil {
		reply.Error = fmt.Sprintf("%s: %v", name, err)
		return nil
	}
	b, err := json.Marshal(res)
	if err != nil {
		reply.Error = fmt.Sprintf("%s: 结果序列化失败: %v", name, err)
		return nil
	}
	reply.Content = string(b)
	return nil
}
