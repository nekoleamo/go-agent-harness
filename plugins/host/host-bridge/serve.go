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
// 可选变参 commands(命令名→实现,M14 外部命令桥):外部插件可同时提供工具与命令;
// 不传命令 = 纯工具插件(旧行为不变)。
// 握手标识 GAH_PLUGIN=gah-external-tool 缺失即拒绝启动(防误跑)。
func ServeTools(tools map[string]sdk.Tool, commands ...map[string]sdk.CommandSpec) {
	if v, ok := os.LookupEnv("GAH_PLUGIN"); !ok || v != "gah-external-tool" {
		log.Fatalf("外部插件缺少握手标识 GAH_PLUGIN(gah-external-tool)")
	}
	if len(tools) == 0 && len(commands) == 0 {
		log.Fatal("外部插件未提供任何工具或命令")
	}
	tb := &toolServerBridge{tools: tools}
	if len(commands) > 0 {
		tb.commands = commands[0]
	}
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: handshake,
		Plugins: map[string]plugin.Plugin{
			pluginName: tb,
		},
	})
}

// toolServerBridge go-plugin 服务端:Server() 返回 net/rpc server 实现。
type toolServerBridge struct {
	tools    map[string]sdk.Tool
	commands map[string]sdk.CommandSpec
}

func (p *toolServerBridge) Server(*plugin.MuxBroker) (any, error) {
	return &toolServer{tools: p.tools, commands: p.commands}, nil
}
func (p *toolServerBridge) Client(b *plugin.MuxBroker, c *rpc.Client) (any, error) {
	return nil, fmt.Errorf("外部插件不需要 client 侧(宿主侧经 host-bridge)")
}

// toolServer 桥协议服务端(net/rpc 方法签名对齐桥约定)。
type toolServer struct {
	tools    map[string]sdk.Tool
	commands map[string]sdk.CommandSpec
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

// Commands 枚举命令定义(M14;JSON 数组,按名排序;无命令 = 空数组,宿主视为无命令)。
func (s *toolServer) Commands(args struct{}, reply *string) error {
	names := make([]string, 0, len(s.commands))
	for n := range s.commands {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []CommandDTO
	for _, n := range names {
		spec := s.commands[n]
		cd := CommandDTO{Name: spec.Name, Usage: spec.Usage, Desc: spec.Desc, TimeoutMs: spec.TimeoutMs}
		if cd.Name == "" {
			cd.Name = n // 兜底:map key 与 spec.Name 一致
		}
		for _, l := range spec.Args {
			d := CommandArgDTO{}
			switch {
			case l.Options != nil:
				d.Enum = true // 枚举级:选项运行期经 CommandOptions 求值
			case l.FreeArgs != nil:
				d.FreeArgs = l.FreeArgs(nil) // 自由级:静态参数名序列(定义时求值)
			}
			cd.Args = append(cd.Args, d)
		}
		out = append(out, cd)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	*reply = string(b)
	return nil
}

// CommandOptions 枚举级选项求值(宿主选择器请求;非枚举级/越界 = 空即无选项)。
func (s *toolServer) CommandOptions(args *CmdOptionsArgs, reply *string) error {
	spec, ok := s.commands[args.Name]
	if !ok || args.Level < 0 || args.Level >= len(spec.Args) || spec.Args[args.Level].Options == nil {
		*reply = ""
		return nil
	}
	opts := spec.Args[args.Level].Options(args.Picked)
	b, err := json.Marshal(opts)
	if err != nil {
		return err
	}
	*reply = string(b)
	return nil
}

// RunCommand 命令执行(host→external;输出文本 + 结构化错误,对齐 ExecReply)。
func (s *toolServer) RunCommand(args *RunCommandArgs, reply *ExecReply) error {
	spec, ok := s.commands[args.Name]
	if !ok {
		reply.Error = fmt.Sprintf("外部插件无此命令 %q", args.Name)
		return nil
	}
	out, err := spec.Run(args.Args)
	if err != nil {
		reply.Error = err.Error()
		reply.Content = out
		return nil
	}
	reply.Content = out
	return nil
}
