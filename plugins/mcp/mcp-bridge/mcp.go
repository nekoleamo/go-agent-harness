// Package mcpbridge 提供 mcp-bridge 插件:MCP(Model Context Protocol)client 桥。
// stdio JSON-RPC 2.0 传输(每行一个消息,2024-11-05 协议):
//
//	initialize → notifications/initialized → tools/list → tools/call
//
// 外部 MCP server 的工具注册为 mcp_<name>,经 tools/call 转发(零额外依赖)。
package mcpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/kernelsandbox"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 mcp-bridge。requires ctx.tools。
//
// data 两种形态(2026-10-02 起):
//
//	stdio(缺省):{command, args[]}              —— 现状,本地起进程
//	http       :{transport:"http", url, headers} —— Streamable HTTP(远程 server)
//
// 凭据只在 headers 里,且来自 $GAH_HOME/config/mcp.yaml(0600);不经 env、不进日志
// (见 mcpconfig.Server.MarshalJSON 的打码与 transport 的 Redacted)。
type Plugin struct{}

func (p *Plugin) Name() string { return "mcp-bridge" }

// Start 按配置 spawn MCP server 并注册其工具。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	if m == nil || m.Data == nil {
		return nil, fmt.Errorf("mcp-bridge: 需要 data.command 配置")
	}
	spec, err := parseServerSpec(m.Data)
	if err != nil {
		return nil, err
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	cli, err := newClient(spec)
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
	// holder 生命周期看护(**只对 stdio**):进程崩溃自动重启(60s 节流),工具读取始终持当前连接。
	// http 传输没有本地进程 ⇒ supervise 直接返回(远端掉线由下一次调用如实报错)。
	h := &holder{cli: cli, spec: spec, throttle: 60 * time.Second, lg: c.Logger()}
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
	spec     serverSpec // 重建用的连接参数(凭据**不**进日志:spec.Redacted 负责)
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
	// http 传输**没有本地进程**:崩溃看护(等 cmd.Wait)对它没有意义,退出即可。
	// 远程 server 掉线由「下一次工具调用报错」如实暴露(不静默重试、不假装它还在)。
	if h.cli.tr.Kind() != "stdio" {
		return
	}
	for {
		waitTransportProcess(h.cli.tr) // 进程退出(崩溃/被杀/正常退出)
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

		nc, err := newClient(h.spec)
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
		h.lg.Info("mcp-bridge: 崩溃自动恢复", "target", h.spec.Redacted())
	}
}

// close 断开当前传输(http = 关空闲连接;stdio = 杀进程并 Wait)。
func (m *mcpClient) close() {
	if m.tr == nil {
		return
	}
	_ = m.tr.Close()
}

// closeKill 只杀进程不 Wait(holder 的 supervise 独占回收);http 传输直接 Close。
func (m *mcpClient) closeKill() {
	if m.tr == nil {
		return
	}
	if s, ok := m.tr.(*stdioTransport); ok {
		s.closeKill()
		return
	}
	_ = m.tr.Close()
}

// close 停看护并断开当前传输。
func (h *holder) close() {
	h.mu.Lock()
	h.closed = true
	cur := h.cli
	h.mu.Unlock()
	if cur != nil {
		// 「怎么关」由 mcpClient 按传输类型分派 —— 「什么时候关」才归 holder。
		// 两处各写一遍分派,将来加第三种传输就会漏一处。
		cur.closeKill()
	}
}

// —— JSON-RPC 消息 ——

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	// ID 用 int64:两个传输的 id 生成与匹配都走它(通知不带 id ⇒ 序列化时省略,见 ID 指针语义)。
	ID     int64  `json:"id,omitempty"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// mcpClient 一个 MCP server 连接(顺序请求-响应;传输可替换,见 transport.go)。
type mcpClient struct {
	tr     transport
	nextID int64
	// mu 保护 nextID 与「同一 id 只发一次」;各传输内部另有自己的串行保证。
	mu sync.Mutex
}

// extKernelSandboxEnv MCP server 内核级沙箱的显式关闭开关("0" = 关)。
//
// 命名沿用 GAH_EXT_* (外部进程面):MCP server 与外部插件同属“第三方进程”这一类。
const extKernelSandboxEnv = "GAH_EXT_KERNEL_SANDBOX"

// extRWPathsEnv 额外可写路径(冒号分隔):server 需要写自己的 DB/数据目录时由用户显式点名。
const extRWPathsEnv = "GAH_EXT_RW_PATHS"

// extCredReadDenyEnv 内核层凭据**读**拒绝开关("1" = 开;默认关)。
//
// 为何默认关:读 ~/.aws/credentials、~/.config/gcloud 之类是 server 的正当职责,
// 默认拒会大面积打断(与 shell 不同 —— shell 里读凭据几乎只有“被诱导”一种解释)。
const extCredReadDenyEnv = "GAH_EXT_CRED_READ_DENY"

// kernelSpec 组装 MCP server 的内核沙箱规格。
//
// 档位来源:宿主在启动本插件进程时注入的 GAH_EXT_SANDBOX_MODE / _ROOT
// (插件拿不到 ctx.sandbox 服务,与 tool-shell 经 SandboxHint 取档位同一道理)。
// 未注入 = 无沙箱宿主/旧宿主 → Mode 为空 → 不施加且不告警(不猜档位)。
func kernelSpec() kernelsandbox.Spec {
	spec := kernelsandbox.Spec{
		Mode:           sdk.SandboxMode(os.Getenv("GAH_EXT_SANDBOX_MODE")),
		Root:           os.Getenv("GAH_EXT_SANDBOX_ROOT"),
		Jail:           kernelsandbox.EnsureJailDir(),
		RW:             append(kernelsandbox.DefaultRWPaths(), kernelsandbox.RWPathsFromEnv(extRWPathsEnv)...),
		Switch:         extKernelSandboxEnv,
		Label:          "mcp-bridge(MCP server)",
		ReadDenySwitch: extCredReadDenyEnv,
	}
	if os.Getenv(extCredReadDenyEnv) == "1" {
		spec.ReadDeny = sdk.CredentialDenyDirs()
	}
	return spec
}

// mcpArgv 组装 MCP server 的启动 argv(含内核沙箱包装;纯函数便于断言)。
//
// 内核级写限制的意义:MCP server 是**第三方代码**,而协作层的路径裁决只覆盖经 mcp_* 工具传入的
// 参数 —— server 自己选定的写落点(DB/缓存/临时文件)完全看不见。白名单依据 2026-09-27 spike
// (真实 MCP server 只给 workspace+jail 时 npx 因写 ~/.npm 而启动失败;加上包管理器缓存与 TMPDIR
// 后正常起、区外写仍被内核拒)。见 internal/kernelsandbox。
//
// 第二个返回值 = 是否真的施加了(决定要不要给子进程打 MarkerEnv)。
func mcpArgv(command string, args []string) ([]string, bool) {
	// Wrap 自己处理“档位未知 / 全权档 / 已在内核沙箱内(不可嵌套)/ 显式关闭”四种情况
	pre := kernelsandbox.Wrap(kernelSpec())
	if len(pre) == 0 {
		return append([]string{command}, args...), false
	}
	return kernelsandbox.PrefixedArgv(pre, command, args...), true
}

// initialize 握手 + initialized 通知。
// initialize 握手 + initialized 通知(两个传输**同一段** —— 协议面不该因传输而分叉)。
func (m *mcpClient) initialize(ctx context.Context) error {
	var r struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := m.call(ctx, "initialize", initializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities:    map[string]any{},
		ClientInfo:      map[string]string{"name": "gah", "version": "dev"},
	}, &r); err != nil {
		return err
	}
	// notifications/initialized(无响应,两种传输都允许 202/无 body)
	return m.tr.Notify(ctx, "notifications/initialized", nil)
}

// initializeParams initialize 的参数(单独一个类型:http 传输重握手时要用同一份形状,
// 两处各写一遍字面量早晚会漂)。
type initializeParams struct {
	ProtocolVersion string            `json:"protocolVersion"`
	Capabilities    map[string]any    `json:"capabilities"`
	ClientInfo      map[string]string `json:"clientInfo"`
}

// call 发请求并解出 result(协议错误的报错口径由 transport 统一给出)。
func (m *mcpClient) call(ctx context.Context, method string, params any, result any) error {
	m.mu.Lock()
	m.nextID++
	id := m.nextID
	m.mu.Unlock()
	resp, err := m.tr.RoundTrip(ctx, rpcReq{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return err
	}
	return decodeResult(resp, result)
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
