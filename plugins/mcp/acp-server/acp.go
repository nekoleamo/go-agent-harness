// Package acpserver 提供 acp-server 插件(S-P2-3):把 gah 作为 **ACP agent** 暴露给编辑器
// (Zed / Neovim 等)与任何 ACP 客户端 —— 编辑器侧获得会话、流式回复、工具调用进度与
// 权限确认,而 gah 侧继续用自身沙箱/审批/工具链,不引入任何运行时依赖。
//
// 传输:stdio 上每行一个 JSON-RPC 2.0 消息(与 mcp-server 同一家族,但**双向**:
// 客户端可发请求/通知,服务端也会反向发请求 session/request_permission)。
//
// 已实现(v1,协议主版本 1):
//
//	initialize / session/new / session/prompt / session/cancel
//	session/update:agent_message_chunk、agent_thought_chunk、tool_call、tool_call_update、
//	                usage_update、available_commands_update
//	session/request_permission(← 权限请求:映射 gah 既有审批三档的 smart 档问答)
//
// 未实现(调用即显式报错/忽略,不静默假装成功):
//
//	session/load、session/list、session/resume、session/delete(声明不支持:不广告能力)
//	authenticate(gah 无认证概念:authMethods 恒空)
//	fs/read_text_file、fs/write_text_file、terminal/*(不委托客户端:工具走 gah 自身沙箱)
//	prompt 中的 image/audio/resource(声明不支持;基线必支持的 text/resource_link 已实现)
//	mcpServers(会话注入的 MCP server:gah 的 MCP 接入走自身配置与 GAH_MCP_COMMANDS)
//
// 单进程单工作区:一次 gah 进程绑定一个工作区(session/new 的 cwd),同工作区内可多会话
// (session/prompt 之间经 ctx.cwdSessions 切换);不同 cwd 的 session/new 显式报错,不静默
// 把工作区换掉(否则先前会话的沙箱根会被悄悄改写)。同一时刻只跑一个回合。
package acpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 acp-server。In/Out 留空 = 进程 stdin/stdout(测试注入内存管道)。
type Plugin struct {
	In  io.Reader
	Out io.Writer
}

func (p *Plugin) Name() string { return "acp-server" }

// maxLineBytes 单行上限(prompt 可能带长文本;默认 64 KiB 太小)。
const maxLineBytes = 4 << 20

// Start 装配 ACP 服务:注入回合能力 → 注册权限呈现者 → 订阅账本事件 → 起 stdio 服务循环。
// 硬依赖 ctx.agentLoop(无回合能力则无从服务)与 ctx.cwdSessions(会话 id/切换来源);
// 其余服务为可选注入(缺失时对应能力退化但显式可见,见各自调用点)。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		return nil, err
	}
	var sessions sdk.CwdSessions
	if err := c.Inject("ctx.cwdSessions", &sessions); err != nil {
		return nil, err
	}
	in, out := p.In, p.Out
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	s := &server{
		c:        c,
		loop:     loop,
		sessions: sessions,
		in:       in,
		out:      out,
		pending:  map[string]chan rpcMsg{},
		bysID:    map[string]*session{},
	}
	// 权限/提问映射:审批确认与结构化提问各注册一个渠道呈现者(经 host-confirm-fusion 广播)。
	// 缺 fusion 时不是"少一个功能"而是**审批无人应答 → 一律拒绝**:必须显式告警,不静默。
	if err := s.registerPresenters(); err != nil {
		return nil, err
	}
	// 账本事件 → session/update(单一订阅:Append 广播带 Seq/TS,顺序即事实顺序)
	s.unsub = c.Subscribe(sdk.EventSession, s.onSessionEvent)

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.serve(done)
	}()
	return func() {
		close(done)
		// 解开阻塞在 Read 上的读循环:stdio 对 acp-server 是独占资源,卸载即关闭。
		// 不开的话 serve 会卡在 Scan 上,后面的 wg.Wait 永远返回不了(热卸载即挂)。
		if cl, ok := in.(io.Closer); ok {
			_ = cl.Close()
		}
		s.unsub()
		s.closePresenters()
		s.cancelTurn() // 卸载即撤销:中断在跑回合,防其向已关闭输出继续写
		wg.Wait()
	}, nil
}

// server ACP 服务端状态。
type server struct {
	c        sdk.Ctx
	loop     sdk.AgentLoop
	sessions sdk.CwdSessions
	in       io.Reader
	out      io.Writer

	wmu sync.Mutex // 输出串行化(保证整行原子,多 goroutine 可并发产 update)

	pmu     sync.Mutex // pending 出站请求
	pending map[string]chan rpcMsg
	pid     int64

	initd atomic.Bool // initialize 已完成

	smu   sync.Mutex // 会话与当前回合
	bysID map[string]*session
	turn  *turn

	boundCwd string // 已绑定的工作区(首个 session/new 决定;单进程单工作区)

	permSeq atomic.Int64 // 权限请求序号

	unsub      sdk.Disposer
	presenters []sdk.Disposer
	warned     sync.Map // 告警去重(key: 文本)
}

// session 一次 ACP 会话:线上 id 直接复用 gah 会话 id(不另造一套映射),
// 便于 /sessions 等既有能力与编辑器侧一一对应。
type session struct {
	id  string // gah 会话 id(host-cwd-sessions 口径;非空)
	cwd string // session/new 的 cwd(工作区绑定)
}

// serve 顺序读行处理(EOF/客户端断开即结束 → 请求宿主退出,不留僵尸进程)。
func (s *server) serve(done chan struct{}) {
	sc := bufio.NewScanner(s.in)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		select {
		case <-done:
			return
		default:
		}
		if len(line) == 0 {
			continue
		}
		s.handleLine(append([]byte(nil), line...))
	}
	// 客户端关闭(stdin EOF):ACP 进程失去对端即无用,通知宿主退出(system/shutdown 为
	// main 的退出信号,与 TUI 退出同路);失败忽略(嵌入/测试环境无订阅方)。
	select {
	case <-done: // 插件被卸载(非客户端断开):不代宿主决定退出
		return
	default:
	}
	_, _ = s.c.Emit(context.Background(), "system/shutdown", nil, sdk.Emit)
}

// handleLine 单行分发:请求(有 method + id)/ 通知(有 method 无 id)/ 响应(无 method 有 id)。
func (s *server) handleLine(line []byte) {
	var m rpcMsg
	if err := json.Unmarshal(line, &m); err != nil {
		s.writeError(nil, codeParseError, "Parse error")
		return
	}
	if m.JSONRPC != "" && m.JSONRPC != "2.0" {
		s.writeError(m.ID, codeInvalidRequest, "Invalid Request: jsonrpc 必须为 2.0")
		return
	}
	if m.Method == "" {
		if len(m.ID) == 0 {
			s.writeError(nil, codeInvalidRequest, "Invalid Request: 既无 method 也无 id")
			return
		}
		s.deliverResponse(m)
		return
	}
	if len(m.ID) == 0 || string(m.ID) == "null" { // 通知:不响应
		s.handleNotification(m)
		return
	}
	s.handleRequest(m)
}

// handleRequest 处理请求。session/prompt 是长请求(整轮),**必须**放到独立 goroutine,
// 否则读循环被占住 → 期间收到的 session/cancel 与权限裁决全部处理不了(规范要求
// 取消时立即中断;占住读循环等于把取消能力废掉)。
func (s *server) handleRequest(m rpcMsg) {
	switch m.Method {
	case "initialize":
		s.onInitialize(m)
	case "session/new":
		s.onNewSession(m)
	case "session/prompt":
		s.onPrompt(m)
	default:
		s.writeError(m.ID, codeMethodNotFound, "Method not found: "+m.Method)
	}
}

// handleNotification 处理通知(当前只有 session/cancel;未知通知按规范忽略)。
func (s *server) handleNotification(m rpcMsg) {
	switch m.Method {
	case "session/cancel":
		s.onCancel(m)
	default:
		// 规范:未知通知忽略(不报错)—— 通知无 id,报错也无从关联。
	}
}

// onInitialize 版本协商与能力声明。
// 版本:客户端发支持的版本,本实现只支持 protocolVersion;若客户端版本不同,
// 回本实现版本并记日志(由客户端决定是否继续,规范如此)。
func (s *server) onInitialize(m rpcMsg) {
	var p initializeParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		s.writeError(m.ID, codeInvalidParams, "initialize 参数解析失败: "+err.Error())
		return
	}
	if p.ProtocolVersion != protocolVersion {
		s.warn("initialize: 客户端协议版本 %d 与本实现 %d 不同,按 %d 应答(客户端决定是否继续)",
			p.ProtocolVersion, protocolVersion, protocolVersion)
	}
	// 能力声明:promptCapabilities 全 false = 只接受基线内容(text/resource_link)。
	// loadSession false:gah 的会话历史由 editor 侧自管(会话切换是进程内动作,不做回放)。
	s.writeResult(m.ID, map[string]any{
		"protocolVersion": protocolVersion,
		"agentCapabilities": map[string]any{
			"loadSession": false,
			"promptCapabilities": map[string]any{
				"image":           false,
				"audio":           false,
				"embeddedContext": false,
			},
		},
		"authMethods": []any{}, // gah 的凭据在 provider.yaml,无握手认证
		"agentInfo":   implInfo{Name: agentName, Version: serverVersion()},
	})
	s.initd.Store(true)
}

// requireInit 会话类方法的前置检查(规范:Clients MUST 先 initialize)。
func (s *server) requireInit(m rpcMsg) bool {
	if s.initd.Load() {
		return true
	}
	s.writeError(m.ID, codeNotInitialized, m.Method+" 之前必须完成 initialize")
	return false
}

// —— 出站请求(服务端 → 客户端) ——

// call 发起一次出站请求并等待应答(尊重 ctx:取消/超时即返回错误,不悬挂)。
func (s *server) call(ctx context.Context, method string, params any, out any) error {
	id := atomic.AddInt64(&s.pid, 1)
	key := strconv.FormatInt(id, 10)
	ch := make(chan rpcMsg, 1)
	s.pmu.Lock()
	s.pending[key] = ch
	s.pmu.Unlock()
	defer func() {
		s.pmu.Lock()
		delete(s.pending, key)
		s.pmu.Unlock()
	}()

	if err := s.writeLine(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case msg := <-ch:
		if msg.Error != nil {
			return fmt.Errorf("%s 失败: %s", method, msg.Error.Message)
		}
		if out == nil || len(msg.Result) == 0 {
			return nil
		}
		if err := json.Unmarshal(msg.Result, out); err != nil {
			return fmt.Errorf("%s 应答解析失败: %w", method, err)
		}
		return nil
	}
}

// deliverResponse 把客户端应答交给等待中的出站请求(未知 id 忽略并记日志)。
func (s *server) deliverResponse(m rpcMsg) {
	key := string(m.ID)
	s.pmu.Lock()
	ch, ok := s.pending[key]
	s.pmu.Unlock()
	if !ok {
		s.warn("收到无对应出站请求的应答 id=%s(忽略)", key)
		return
	}
	select {
	case ch <- m:
	default: // 已有应答(重复投递):忽略
	}
}

// —— 会话/回合状态访问 ——

// currentTurn 取当前回合(nil = 空闲)。
func (s *server) currentTurn() *turn {
	s.smu.Lock()
	defer s.smu.Unlock()
	return s.turn
}

// sessionOf 取会话(未知 id 返回 nil)。
func (s *server) sessionOf(id string) *session {
	s.smu.Lock()
	defer s.smu.Unlock()
	return s.bysID[id]
}

// putSession 登记会话。
func (s *server) putSession(sess *session) {
	s.smu.Lock()
	defer s.smu.Unlock()
	s.bysID[sess.id] = sess
}

// cancelTurn 中断当前回合(无回合 = no-op);供 Disposer 与停止路径共用。
func (s *server) cancelTurn() {
	if t := s.currentTurn(); t != nil {
		t.markCancelled()
		s.cancelLoop()
	}
}

// cancelLoop 取消在跑的宿主回合(ctx.turnControl:Cancel 幂等,无回合时为 no-op)。
func (s *server) cancelLoop() {
	var tc sdk.TurnControl
	if err := s.c.Inject("ctx.turnControl", &tc); err != nil || tc == nil {
		s.warn("ctx.turnControl 未装配: 无法中断在跑回合")
		return
	}
	tc.Cancel()
}

// warn 去重告警(协议层同类问题只提示一次,避免刷屏)。
func (s *server) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if _, dup := s.warned.LoadOrStore(msg, true); dup {
		return
	}
	if log := s.c.Logger(); log != nil {
		log.Warn("acp: " + msg)
	}
}

// —— 输出 ——

// writeLine 写一行 JSON(整行原子)。
func (s *server) writeLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.out.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// writeResult 应答成功(id 原样回显:客户端 id 可能是字符串或数字)。
func (s *server) writeResult(id json.RawMessage, result any) {
	if err := s.writeLine(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil {
		s.warn("应答写出失败: %v", err)
	}
}

// writeError 应答错误(标准 JSON-RPC 错误对象)。
func (s *server) writeError(id json.RawMessage, code int, msg string) {
	if err := s.writeLine(map[string]any{
		"jsonrpc": "2.0", "id": id, "error": rpcError{Code: code, Message: msg},
	}); err != nil {
		s.warn("错误应答写出失败: %v", err)
	}
}

// notify 发通知(session/update / 无 id 的方法)。
func (s *server) notify(method string, params any) {
	if err := s.writeLine(map[string]any{"jsonrpc": "2.0", "method": method, "params": params}); err != nil {
		s.warn("通知 %s 写出失败: %v", method, err)
	}
}

// serverVersion 版本贯通:优先 GAH_VERSION(boot 由 main.version 注入),回退 dev。
func serverVersion() string {
	if v := os.Getenv("GAH_VERSION"); v != "" {
		return v
	}
	return "dev"
}

// oneLine 压缩为单行并截断(标题类字段用:编辑器标题栏容不下多行长文本)。
func oneLine(s string, max int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
