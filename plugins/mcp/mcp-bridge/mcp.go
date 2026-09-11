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
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

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
	// holder 生命周期看护:进程崩溃自动重启(60s 节流),工具读取始终持当前连接
	h := &holder{cli: cli, command: command, args: args, throttle: 60 * time.Second, lg: c.Logger()}
	disposers := []sdk.Disposer{}
	for _, def := range defs {
		sdkDef := sdk.ToolDefinition{Name: "mcp_" + def.Name, Description: def.Description, InputSchema: def.InputSchema}
		disposers = append(disposers, tools.Register(&mcpTool{cli: h, def: sdkDef}))
	}
	go h.supervise()
	return func() {
		for i := len(disposers) - 1; i >= 0; i-- {
			disposers[i]()
		}
		h.close()
	}, nil
}

// holder MCP 连接生命周期看护(崩溃看护):进程退出后按节流自动 respawn,
// 工具调用经 current() 恒取当前活动连接。重启节流防崩溃循环(cmd.Wait 独占)。
type holder struct {
	mu       sync.RWMutex
	cli      *mcpClient
	command  string
	args     []string
	closed   bool
	respawn  time.Time
	throttle time.Duration
	lg       *slog.Logger
}

func (h *holder) current() *mcpClient {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cli
}

// supervise 阻塞等进程退出;崩溃则按节流重建(spawn+initialize+toolsList)。
// 进程被 disposer 杀掉时 closed=true → 直接退出,不再重启。
func (h *holder) supervise() {
	for {
		_ = h.cli.cmd.Wait() // 进程退出(崩溃/被杀/正常退出)
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			return
		}
		if !time.Now().After(h.respawn) {
			wait := time.Until(h.respawn)
			h.mu.Unlock()
			time.Sleep(wait)
			continue
		}
		h.respawn = time.Now().Add(h.throttle)
		h.mu.Unlock()

		nc, err := spawn(h.command, h.args)
		if err != nil {
			h.lg.Warn("mcp-bridge: 重启进程失败", "err", err)
			time.Sleep(h.throttle)
			continue
		}
		rctx := context.Background()
		if err := nc.initialize(rctx); err != nil {
			nc.close()
			h.lg.Warn("mcp-bridge: 重启 initialize 失败,待下轮", "err", err)
			continue
		}
		if _, err := nc.toolsList(rctx); err != nil {
			nc.close()
			h.lg.Warn("mcp-bridge: 重启 tools/list 失败,待下轮", "err", err)
			continue
		}
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			nc.close()
			return
		}
		h.cli = nc
		h.mu.Unlock()
		h.lg.Info("mcp-bridge: 崩溃自动恢复", "command", h.command)
	}
}

// close 停看护并杀当前进程(不 Wait:Wait 由 supervise 独占回收)。
func (h *holder) close() {
	h.mu.Lock()
	h.closed = true
	cur := h.cli
	h.mu.Unlock()
	if cur != nil {
		cur.closeKill()
	}
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

	// lines 后台读线程投递的 stdout 行(关闭 = 读结束,readErr 记原因);
	// 阻塞读放在独立 goroutine,调用侧才能 select ctx.Done 真正中断
	// (此前在持 m.mu 的情况下阻塞 ReadBytes:server 卡住即挂死整个回合且取消无效)。
	lines   chan []byte
	readErr error
}

func spawn(command string, args []string) (*mcpClient, error) {
	cmd := exec.Command(command, args...)
	// 凭据隔离:第三方 MCP server 不继承宿主凭据(滤除 *_API_KEY/*_TOKEN/AWS_* 等),
	// 也不继承 GAH_CB_*(宿主回调地址/token)。需要额外 env 的 server 请经启动命令显式配置。
	cmd.Env = sdk.SanitizedEnv(os.Environ())
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
	m := &mcpClient{cmd: cmd, stdin: stdin, out: bufio.NewReader(stdout), lines: make(chan []byte, linesCap)}
	m.startReader()
	return m, nil
}

// linesCap 读线程投递缓冲(取消后无人消费时读线程最多阻塞在写入上,不无限占用内存)。
const linesCap = 256

// startReader 后台逐行读 stdout:read 侧永不在持锁路径阻塞。
func (m *mcpClient) startReader() {
	go func() {
		for {
			line, err := m.out.ReadBytes('\n')
			if len(line) > 0 {
				m.lines <- line
			}
			if err != nil {
				m.readErr = err
				close(m.lines)
				return
			}
		}
	}()
}

func (m *mcpClient) close() {
	m.stdin.Close()
	m.cmd.Process.Kill()
	m.cmd.Wait()
}

// closeKill 只杀进程不 Wait(Wait 由 holder.supervise 独占回收;无看护路径用 close)。
func (m *mcpClient) closeKill() {
	m.stdin.Close()
	if m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
	}
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
		var line []byte
		select {
		case <-ctx.Done():
			// 中途取消:未读响应由后续调用按 id 跳过(JSON-RPC 有 id,不会错配)。
			return ctx.Err()
		case l, ok := <-m.lines:
			if !ok {
				if m.readErr != nil {
					return m.readErr
				}
				return io.EOF
			}
			line = l
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

// mcpTool 把 MCP 工具适配为 sdk.Tool(经 holder 取当前活动连接,崩溃重启后自动恢复)。
type mcpTool struct {
	cli *holder
	def sdk.ToolDefinition
}

func (t *mcpTool) Definition() sdk.ToolDefinition { return t.def }

func (t *mcpTool) Execute(ctx context.Context, args string) (any, error) {
	client := t.cli.current()
	if client == nil {
		return map[string]any{"error": "MCP 连接已关闭"}, nil
	}
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
	if err := client.call(ctx, "tools/call", map[string]any{
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
