// Package mcpbridge 提供 mcp-bridge 插件:MCP(Model Context Protocol)client 桥。
// stdio JSON-RPC 2.0 传输(每行一个消息,2024-11-05 协议):
//
//	initialize → notifications/initialized → tools/list → tools/call
//
// 外部 MCP server 的工具注册为 mcp_<name>,经 tools/call 转发(零额外依赖)。
package mcpbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 mcp-bridge。requires ctx.tools;data: {command, args[]}。
type Plugin struct{}

func (p *Plugin) Name() string { return "mcp-bridge" }

// Start 按配置 spawn MCP server 并注册其工具。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	if m == nil || m.Data == nil {
		return nil, fmt.Errorf("mcp-bridge: 需要 data.command 配置")
	}
	command, _ := m.Data["command"].(string)
	if command == "" {
		return nil, fmt.Errorf("mcp-bridge: 需要 data.command")
	}
	var args []string
	if a, ok := m.Data["args"].([]any); ok {
		for _, x := range a {
			if s, ok := x.(string); ok {
				args = append(args, s)
			}
		}
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	cli, err := spawn(command, args)
	if err != nil {
		return nil, err
	}
	rctx := context.Background()
	if err := cli.initialize(rctx); err != nil {
		cli.close()
		return nil, fmt.Errorf("mcp-bridge: initialize: %w", err)
	}
	defs, err := cli.toolsList(rctx)
	if err != nil {
		cli.close()
		return nil, fmt.Errorf("mcp-bridge: tools/list: %w", err)
	}
	disposers := []sdk.Disposer{}
	for _, def := range defs {
		sdkDef := sdk.ToolDefinition{Name: "mcp_" + def.Name, Description: def.Description, InputSchema: def.InputSchema}
		disposers = append(disposers, tools.Register(&mcpTool{cli: cli, def: sdkDef}))
	}
	return func() {
		for i := len(disposers) - 1; i >= 0; i-- {
			disposers[i]()
		}
		cli.close()
	}, nil
}

// —— JSON-RPC 消息 ——

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// mcpClient 一个 stdio MCP server 连接(顺序请求-响应)。
type mcpClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Reader
	nextID int
	mu     sync.Mutex
}

func spawn(command string, args []string) (*mcpClient, error) {
	cmd := exec.Command(command, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &mcpClient{cmd: cmd, stdin: stdin, out: bufio.NewReader(stdout)}, nil
}

func (m *mcpClient) close() {
	m.stdin.Close()
	m.cmd.Process.Kill()
	m.cmd.Wait()
}

// call 发请求并等对应 id 的响应(stdout 逐行;id 不匹配跳过)。
func (m *mcpClient) call(ctx context.Context, method string, params any, result any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	req := rpcReq{JSONRPC: "2.0", ID: m.nextID, Method: method, Params: params}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := m.stdin.Write(append(b, '\n')); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line, err := m.out.ReadBytes('\n')
		if err != nil {
			return err
		}
		var resp rpcResp
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		if resp.ID != req.ID {
			continue
		}
		if resp.Error != nil {
			return fmt.Errorf("mcp-rpc error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	}
}

// initialize 握手 + initialized 通知。
func (m *mcpClient) initialize(ctx context.Context) error {
	var r struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := m.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "gah", "version": "dev"},
	}, &r); err != nil {
		return err
	}
	// notifications/initialized(无响应)
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	_, err := m.stdin.Write(append(b, '\n'))
	return err
}

// toolDef MCP 工具清单条目。
type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (m *mcpClient) toolsList(ctx context.Context) ([]toolDef, error) {
	var r struct {
		Tools []toolDef `json:"tools"`
	}
	if err := m.call(ctx, "tools/list", map[string]any{}, &r); err != nil {
		return nil, err
	}
	return r.Tools, nil
}

// mcpTool 把 MCP 工具适配为 sdk.Tool。
type mcpTool struct {
	cli *mcpClient
	def sdk.ToolDefinition
}

func (t *mcpTool) Definition() sdk.ToolDefinition { return t.def }

func (t *mcpTool) Execute(ctx context.Context, args string) (any, error) {
	var arguments map[string]any
	if err := json.Unmarshal([]byte(args), &arguments); err != nil {
		arguments = map[string]any{"input": args}
	}
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := t.cli.call(ctx, "tools/call", map[string]any{
		"name":      t.def.Name[len("mcp_"):],
		"arguments": arguments,
	}, &r); err != nil {
		return map[string]any{"error": "MCP 调用失败: " + err.Error()}, nil
	}
	var sb string
	for _, c := range r.Content {
		sb += c.Text
	}
	return map[string]any{"content": sb}, nil
}
