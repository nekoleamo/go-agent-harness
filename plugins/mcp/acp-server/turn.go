// turn.go:会话建立与回合执行(ACP 生命周期 → gah 既有能力)。
//
// 映射原则:能用 gah 既有语义表达的就直接复用(会话 = host-cwd-sessions 的会话,
// 回合 = ctx.agentLoop.Run,斜杠命令 = ctx.commands 注册表,取消 = ctx.turnControl),
// 不为 ACP 另造一套平行状态机 —— 否则编辑器侧看到的与 TUI/Web 侧会不一致。
package acpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// onNewSession session/new:绑定工作区 + 新建 gah 会话。
func (s *server) onNewSession(m rpcMsg) {
	if !s.requireInit(m) {
		return
	}
	var p newSessionParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		s.writeError(m.ID, codeInvalidParams, "session/new 参数解析失败: "+err.Error())
		return
	}
	cwd := strings.TrimSpace(p.Cwd)
	if cwd == "" {
		s.writeError(m.ID, codeInvalidParams, "session/new 需要 cwd")
		return
	}
	// 不静默:gah 的 MCP 接入走自身配置(GAH_MCP_COMMANDS / provider.yaml),
	// 不接受按会话注入的外部进程;来自编辑器的声明必须让人看见被忽略了。
	if len(p.McpServers) > 0 {
		s.warn("session/new 带了 %d 个 mcpServers: gah 的 MCP 接入走自身配置(GAH_MCP_COMMANDS),未按会话注入", len(p.McpServers))
	}
	if len(p.AdditionalDirectories) > 0 {
		s.warn("session/new 带了 %d 个 additionalDirectories: 未支持(工作区单根 = cwd)", len(p.AdditionalDirectories))
	}

	s.smu.Lock()
	bound := s.boundCwd
	s.smu.Unlock()

	var (
		id  string
		err error
	)
	switch {
	case bound == "":
		// 首个会话:绑定工作区。cwd 与进程当前目录一致时只需新建会话;
		// 不一致则切工作区(host-cwd-sessions:chdir + 重绑项目 + 新会话)。
		if sameDir(cwd, wd()) {
			id, err = s.sessions.New()
		} else {
			id, err = s.sessions.SwitchDir(cwd)
		}
	case sameDir(cwd, bound):
		id, err = s.sessions.New() // 同工作区多会话(编辑器多面板/多线程)
	default:
		s.writeError(m.ID, codeInvalidParams, fmt.Sprintf(
			"gah ACP 进程单工作区:已绑定 %s,无法再绑定 %s(请为另一个目录单独启动一个 ACP agent)", bound, cwd))
		return
	}
	if err != nil {
		s.writeError(m.ID, codeInternalError, "创建会话失败: "+err.Error())
		return
	}
	s.smu.Lock()
	if s.boundCwd == "" {
		s.boundCwd = cwd
	}
	s.smu.Unlock()
	sess := &session{id: id, cwd: cwd}
	s.putSession(sess)

	s.writeResult(m.ID, map[string]any{"sessionId": id})
	// 可用命令:客户端斜杠菜单(v1 无独立执行方法:命令以提示文本发送,服务端识别 / 前缀)
	s.sendCommands(id)
}

// onPrompt session/prompt:开一轮(长请求 → 独立 goroutine,见 handleRequest 注释)。
func (s *server) onPrompt(m rpcMsg) {
	if !s.requireInit(m) {
		return
	}
	var p promptParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		s.writeError(m.ID, codeInvalidParams, "session/prompt 参数解析失败: "+err.Error())
		return
	}
	sess := s.sessionOf(p.SessionID)
	if sess == nil {
		s.writeError(m.ID, codeInvalidParams, "未知会话 id "+strconvQuote(p.SessionID)+"(先调用 session/new)")
		return
	}
	text, err := promptText(p.Prompt)
	if err != nil {
		s.writeError(m.ID, codeInvalidParams, err.Error())
		return
	}
	t := s.beginTurn(sess, m.ID)
	if t == nil {
		// 单进程单回合:并发回合会互相污染会话账本(不变量:模型可见即已记录),
		// 故显式拒绝而非排队(排队会让编辑器以为已经开跑)。
		s.writeError(m.ID, codeBusy, "已有回合在运行:gah 单进程一次只跑一轮(可先 session/cancel)")
		return
	}
	go s.runTurn(t, text)
}

// onCancel session/cancel:中断当前回合(会话不匹配或无回合 = 幂等忽略)。
func (s *server) onCancel(m rpcMsg) {
	var p cancelParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		s.warn("session/cancel 参数解析失败: %v", err)
		return
	}
	t := s.currentTurn()
	if t == nil || t.sess.id != p.SessionID {
		return
	}
	t.markCancelled()
	s.cancelLoop()
}

// promptText 把提示内容块拼为 gah 的输入文本。
// 基线(text + resource_link)必须支持;需能力声明的内容类型本实现声明为不支持 → 显式报错,
// 不静默丢弃(丢了用户会以为模型"没看到"还继续等回复)。
func promptText(blocks []contentBlock) (string, error) {
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text", "":
			if s := strings.TrimSpace(b.Text); s != "" {
				parts = append(parts, s)
			}
		case "resource_link":
			parts = append(parts, "[资源] "+oneLine(strings.TrimSpace(b.Name+" "+b.URI), 300))
		case "image", "audio":
			return "", fmt.Errorf("暂不支持 %s 输入(initialize 未声明该能力;可改为把文件路径写进提示)", b.Type)
		case "resource":
			return "", fmt.Errorf("暂不支持内嵌 resource 输入(initialize 未声明 embeddedContext)")
		default:
			return "", fmt.Errorf("未知内容块类型 %q", b.Type)
		}
	}
	text := strings.Join(parts, "\n")
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("空提示")
	}
	return text, nil
}

// runTurn 执行一轮:切会话 → 斜杠命令或 agent 回合 → 结束时应答 session/prompt。
func (s *server) runTurn(t *turn, text string) {
	defer s.finishTurn(t)
	defer func() { // 协议服务长跑:单轮 panic 不得拖垮进程,也要给出应答
		if r := recover(); r != nil {
			s.warn("回合 panic: %v", r)
			s.emitText(t, fmt.Sprintf("错误: 内部错误 %v", r), false)
		}
	}()

	if err := s.switchSession(t.sess); err != nil {
		s.emitText(t, "错误: 切换会话失败: "+err.Error(), false)
		return
	}
	if raw := strings.TrimSpace(text); strings.HasPrefix(raw, "/") {
		// 斜杠命令:与 TUI 同语义(查注册表执行,不经模型;输出作为助手消息回给编辑器)
		out := s.runSlash(raw)
		if strings.TrimSpace(out) != "" {
			s.emitText(t, out, false)
		}
		return
	}
	if err := s.loop.Run(t.ctx, text); err != nil {
		if t.isCancelled() {
			return // 取消:由 finishTurn 回 cancelled,不再刷错误
		}
		// 账本里已记 agent/error(编辑器看不到),这里把原因作为助手消息明示
		s.emitText(t, "错误: "+err.Error(), false)
	}
}

// switchSession 把 gah 当前会话切到该 ACP 会话(编辑器多会话并存时每次 prompt 都要切)。
func (s *server) switchSession(sess *session) error {
	if s.sessions.CurrentSession() == sess.id {
		return nil
	}
	return s.sessions.Open(sess.id)
}

// runSlash 执行斜杠命令(与 TUI 的 command 分发同口径:sdk.SplitArgs 分词 + 注册表查找)。
func (s *server) runSlash(raw string) string {
	var cmds sdk.CommandRegistry
	if err := s.c.Inject("ctx.commands", &cmds); err != nil || cmds == nil {
		return "错误: 命令不可用(ctx.commands 未装配)"
	}
	fields := sdk.SplitArgs(strings.TrimPrefix(raw, "/"))
	if len(fields) == 0 {
		return ""
	}
	spec, ok := cmds.Get(fields[0])
	if !ok {
		return "未知命令 /" + fields[0] + "(输入 /help 查看全部)"
	}
	out, err := spec.Run(fields[1:])
	if err != nil {
		return "错误: " + err.Error()
	}
	return out
}

// sendCommands 推送 available_commands_update(客户端斜杠菜单数据源)。
func (s *server) sendCommands(sessID string) {
	var cmds sdk.CommandRegistry
	if err := s.c.Inject("ctx.commands", &cmds); err != nil || cmds == nil {
		return
	}
	list := cmds.List()
	out := make([]availableCommand, 0, len(list))
	for _, c := range list {
		desc := c.Desc
		if desc == "" {
			desc = c.Usage
		}
		out = append(out, availableCommand{Name: c.Name, Description: desc})
	}
	if len(out) == 0 {
		return
	}
	s.notify("session/update", sessionUpdateParams{
		SessionID: sessID,
		Update:    commandsUpdate{SessionUpdate: "available_commands_update", AvailableCommands: out},
	})
}

// beginTurn 尝试占用回合槽(已有回合返回 nil)。
func (s *server) beginTurn(sess *session, reqID json.RawMessage) *turn {
	s.smu.Lock()
	defer s.smu.Unlock()
	if s.turn != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &turn{
		sess:     sess,
		reqID:    reqID,
		ctx:      ctx,
		cancel:   cancel,
		calls:    map[string]string{},
		pathCall: map[string]string{},
		open:     map[string]bool{},
	}
	s.turn = t
	return t
}

// finishTurn 结束回合:清槽 → 取消未终态工具调用 → 应答 session/prompt。
// 顺序:应答必须最后写(规范:所有 update 必须先于 session/prompt 的应答)。
func (s *server) finishTurn(t *turn) {
	if t.isCancelled() {
		s.failOpenCalls(t)
	}
	s.smu.Lock()
	if s.turn == t {
		s.turn = nil
	}
	s.smu.Unlock()
	reason := "end_turn"
	if t.isCancelled() {
		reason = "cancelled"
	}
	s.writeResult(t.reqID, map[string]any{"stopReason": reason})
	t.cancel()
}

// failOpenCalls 把未终态的工具调用标记 failed(ACP 状态机无 cancelled 状态:
// pending/in_progress/completed/failed —— 被中断的调用只能以 failed 收尾,内容说明取消)。
func (s *server) failOpenCalls(t *turn) {
	for id := range t.snapshotOpen() {
		s.notify("session/update", sessionUpdateParams{
			SessionID: t.sess.id,
			Update: toolUpdate{
				SessionUpdate: "tool_call_update",
				ToolCallID:    id,
				Status:        "failed",
				Content:       []any{contentItem{Type: "content", Content: textBlock{Type: "text", Text: "已中断(用户取消)"}}},
			},
		})
	}
}

// —— turn:一次回合的呈现状态(只被回合 goroutine 与事件订阅者访问,内部加锁) ——

type turn struct {
	sess   *session
	reqID  json.RawMessage
	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	msgSeq    int64             // 助手消息序号(step/start 递增 → messageId)
	chunked   bool              // 本步是否已流过文本增量(否则用 assistant/message 兜底)
	calls     map[string]string // toolCallId → 工具名
	pathCall  map[string]string // 文件 basename → toolCallId(file/change 挂 patch 用)
	open      map[string]bool   // 未终态工具调用
	cancelled bool
}

// messageID 当前助手消息 id(ACP 可选字段:同一消息的块共享,便于客户端归组)。
func (t *turn) messageID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return fmt.Sprintf("m%d", t.msgSeq)
}

// nextMessage 进入下一段助手消息(step/start 时调用)。
func (t *turn) nextMessage() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.msgSeq++
	t.chunked = false
}

// markChunked 记录本步已流过增量。
func (t *turn) markChunked() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.chunked = true
}

// currentChunked 本步是否已流过文本增量(未流过时用完整消息兜底)。
func (t *turn) currentChunked() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.chunked
}

// setCalls 记录工具调用的名称与文件关联。
func (t *turn) setCalls(id, name, path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls[id] = name
	t.open[id] = true
	if path != "" {
		t.pathCall[filepath.Base(path)] = id
	}
}

// resolveCall 工具终态:出 open 集合,返回工具名。
func (t *turn) resolveCall(id string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.open[id] {
		return "", false
	}
	delete(t.open, id)
	return t.calls[id], true
}

// callForPath 按文件名找回对应工具调用(file/change → 挂到该调用的 patch 上)。
func (t *turn) callForPath(p string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pathCall[filepath.Base(p)]
}

// snapshotOpen 未终态工具调用快照(取消收尾用)。
func (t *turn) snapshotOpen() map[string]bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]bool, len(t.open))
	for id := range t.open {
		out[id] = true
	}
	return out
}

func (t *turn) markCancelled() { t.mu.Lock(); t.cancelled = true; t.mu.Unlock() }
func (t *turn) isCancelled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cancelled
}

// sameDir 目录等价判断(realpath 归一:macOS /tmp → /private/tmp 之类软链必须视为同目录,
// 否则同一个工作区会被判成"另一个目录"而拒绝)。
func sameDir(a, b string) bool {
	return normDir(a) == normDir(b)
}

func normDir(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// wd 进程当前工作目录(取不到回空:调用方经 sameDir 退化为词法比较)。
func wd() string {
	d, err := os.Getwd()
	if err != nil {
		return ""
	}
	return d
}

// strconvQuote 错误信息里的 id 引号(空 id 也能看清)。
func strconvQuote(s string) string { return "\"" + s + "\"" }
