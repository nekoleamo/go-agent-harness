// assembly.go:按配置装配 MCP 工具集(NOND-M1 第 2 步「按 server 门控」)。
// 供外部进程 extplugins/tool-mcp 使用(它只是 ServeTools(Assemble(...)) 的薄壳),
// 装配逻辑放包内 = 可单测(dialConn 为测试替换点)。
package mcpbridge

import (
	"context"
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/mcpconfig"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Conn 一个已握手的 MCP server 连接。*Client 满足;单测用替身(不真起进程)。
// 约定:Definitions() 里的 Name 与 Execute 的第一个参数用**同一个**字符串
// (*Client 实现里即 "mcp_<原始工具名>")。
type Conn interface {
	Definitions() []sdk.ToolDefinition
	Execute(ctx context.Context, name, args string) (string, error)
	Close()
}

// dialConn 连接工厂(单测替换点;同 kernel.go 的 sandboxExec 先例)。
var dialConn = func(command string, args []string) (Conn, error) { return NewClient(command, args) }

// Assemble 按配置装配工具集:
//   - enabled=false → 跳过(不连进程),记一行 note;
//   - mode=direct(默认,现状语义)→ 工具全量注册为 mcp_<server>_<工具>;server 名为空
//     (单 server 兼容 GAH_MCP_COMMAND)则保持 mcp_<工具> 不加前缀;
//   - mode=search → 该 server 的工具**不进注册表**,汇总进索引,只暴露
//     mcp_search(查清单)/mcp_call(按名调用)两个代理工具;
//   - 单个 server 连接失败 → 记一行 note 跳过,不拖垮其余(对齐宿主跳过失败插件语义);
//     name 冲突 → 保留先注册者并记 note(与历史行为一致)。
//
// 返回 notes 为人读问题行(调用方打印 stderr);全部不可用(len(tools)==0)返回错误,
// 由调用方 exit 1 —— 不静默空转成"零工具插件"。
func Assemble(specs []mcpconfig.Server) (map[string]sdk.Tool, []string, error) {
	tools := map[string]sdk.Tool{}
	ix := &searchIndex{}
	var notes []string
	var disabled []string
	conns := []Conn{}
	closer := func() {
		for _, cn := range conns {
			cn.Close()
		}
	}
	// fail 收尾:关掉已建连接(不留孤儿 MCP server 进程)并把原因带回调用方。
	fail := func(note string) (map[string]sdk.Tool, []string, error) {
		closer()
		return nil, append(notes, note), fmt.Errorf("tool-mcp: 无可用工具")
	}

	for _, sp := range specs {
		if !sp.IsEnabled() {
			disabled = append(disabled, serverLabel(sp))
			notes = append(notes, fmt.Sprintf("server %s 已停用(跳过)", serverLabel(sp)))
			continue
		}
		cn, err := dialConn(sp.Command, sp.Args)
		if err != nil {
			notes = append(notes, fmt.Sprintf("server %s 连接失败(跳过): %v", serverLabel(sp), err))
			continue
		}
		conns = append(conns, cn)
		defs := cn.Definitions()
		search := sp.ModeOrDefault() == mcpconfig.ModeSearch
		for _, d := range defs {
			name := displayToolName(sp.Name, d.Name)
			if search {
				ix.add(searchEntry{display: name, desc: d.Description, server: sp.Name, conn: cn, raw: d.Name})
				continue
			}
			if _, dup := tools[name]; dup {
				notes = append(notes, fmt.Sprintf("工具名冲突 %s(保留先注册,跳过)", name))
				continue
			}
			def := d
			tools[name] = &bridgeTool{conn: cn, name: name, raw: def.Name, def: def, display: name}
		}
		notes = append(notes, fmt.Sprintf("server %s 已连接:%d 个工具(%s 模式)", serverLabel(sp), len(defs), sp.ModeOrDefault()))
	}

	if ix.len() > 0 {
		// search 模式:两个代理工具(名字固定;若与某 direct 工具冲突则保留先注册者)。
		for _, mt := range []sdk.Tool{newSearchTool(ix), newCallTool(ix)} {
			n := mt.Definition().Name
			if _, dup := tools[n]; dup {
				notes = append(notes, fmt.Sprintf("代理工具 %s 与已有工具同名(保留先注册,跳过)", n))
				continue
			}
			tools[n] = mt
		}
	}
	if len(tools) == 0 {
		msg := "无可用工具(全部 server 连接失败或空)"
		if len(disabled) > 0 {
			msg = fmt.Sprintf("无可用工具(已停用:%s;其余连接失败或空)", strings.Join(disabled, ", "))
		}
		return fail(msg)
	}
	return tools, notes, nil
}

// displayToolName 注册名:多 server 带前缀(mcp_<server>_<工具>);单 server(无名)保持
// mcp_<工具>(历史兼容 —— 工具名变更会破坏既有会话/审批名单)。
// direct 与 search 两种模式用**同一个**命名函数:切换模式后 mcp_call 的名字不变。
func displayToolName(server, defName string) string {
	if server == "" {
		return defName
	}
	return "mcp_" + server + "_" + strings.TrimPrefix(defName, "mcp_")
}

// serverLabel 日志用 server 标识(无名 = 命令)。
func serverLabel(sp mcpconfig.Server) string {
	if sp.Name == "" {
		return sp.Command
	}
	return sp.Name + "(" + sp.Command + ")"
}

// bridgeTool 一个 MCP 工具的外部进程实现:Execute 经 conn 转发 tools/call。
// name = 注册名(宿主可见);raw = 连接内部名(两者解耦,Definitions RPC 暴露注册名)。
type bridgeTool struct {
	conn    Conn
	name    string
	raw     string
	display string
	def     sdk.ToolDefinition
}

func (t *bridgeTool) Definition() sdk.ToolDefinition {
	d := t.def
	d.Name = t.name
	return d
}

func (t *bridgeTool) Execute(ctx context.Context, args string) (any, error) {
	out, err := t.conn.Execute(ctx, t.raw, args)
	if err != nil {
		return map[string]any{"error": "MCP 调用失败: " + err.Error()}, nil
	}
	return map[string]any{"content": out}, nil
}
