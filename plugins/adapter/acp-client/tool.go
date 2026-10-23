// Package acpclient 的**工具层**:acp_run —— 把一次编码任务委派给外部 ACP agent。
//
// 一次调用 = 一个子进程 + 一个会话(不跨调用复用):复用会话要维护长生命周期的
// 进程池、会话归属与崩溃重连,而工具调用的天然边界就是"一次任务"。宁可每次重连,
// 也不要一个"上次那个 session 还在不在"的隐式状态。
//
// 权限请求走 gah 自己的审批通道(ctx.confirm),不另开一道放行:
// ACP 的 session/request_permission 会被翻成人话弹给用户,允许时挑**最窄**的那个
// allow 选项(once 优先于 always)。没有审批通道(headless/嵌入)时一律拒绝并如实回报
// —— 不是"没人看着就放行"。
package acpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// runner 一个配置好的外部 ACP agent 启动命令。
type runner struct {
	command string
	args    []string
}

// Tool acp_run 工具。
type Tool struct {
	c       sdk.Ctx // 用于按需 Inject("ctx.confirm"):审批服务可能在宿主启动后才装配
	runners map[string]runner
	timeout time.Duration
}

// toolName 工具名(进度上报与会话日志里都按它归位)。
const toolName = "acp_run"

// Definition 工具定义。
func (t *Tool) Definition() sdk.ToolDefinition {
	names := make([]string, 0, len(t.runners))
	for n := range t.runners {
		names = append(names, n)
	}
	return sdk.ToolDefinition{
		Name: toolName,
		Description: "把一个**编码任务**委派给外部 ACP agent(opencode / codex / claude-code / kimi / cursor 等)执行,返回它的最终答复。" +
			"外部 agent 在 cwd 下自己读写文件、跑命令,适合「改一个仓库/装依赖/跑测试」这类长任务。" +
			"它的权限请求会转成 gah 的审批弹窗给你(你拒绝即中止)。" +
			"已配置的 runner: " + strings.Join(names, ", ") + "。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"runner": map[string]any{
					"type":        "string",
					"description": "要用的外部 agent(" + strings.Join(names, "/") + ")",
					"enum":        names,
				},
				"message": map[string]any{
					"type":        "string",
					"description": "交给外部 agent 的任务描述(说清目标、约束、验收方式)",
				},
				"cwd": map[string]any{
					"type":        "string",
					"description": "工作目录(默认=当前工作根);外部 agent 在这里读写文件",
				},
				"timeout_seconds": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("超时秒数(默认 %d,上限 3600)", int(t.timeout/time.Second)),
				},
			},
			"required": []string{"runner", "message"},
		},
		// cwd 是**写意图**的路径参数:外部 agent 会在这个目录里落盘,必须经宿主的
		// 路径沙箱裁决(否则 `acp_run{cwd:"/etc"}` 就是一条绕开只读档的侧门)。
		PathParams: []sdk.PathParam{{Arg: "cwd", Access: sdk.PathWrite, Optional: true}},
	}
}

// args acp_run 参数。
type args struct {
	Runner         string `json:"runner"`
	Message        string `json:"message"`
	CWD            string `json:"cwd,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// maxTimeout 单次调用上限:一小时。再长就该拆任务(而不是一个工具调用挂半天)。
const maxTimeout = time.Hour

// Execute 跑一次委派。
func (t *Tool) Execute(ctx context.Context, raw string) (any, error) {
	var a args
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("acp_run: 参数不是合法 JSON: %w", err)
	}
	if a.Message = strings.TrimSpace(a.Message); a.Message == "" {
		return nil, errors.New("acp_run: message 不能为空(要说清交给外部 agent 的目标)")
	}
	r, ok := t.runners[a.Runner]
	if !ok {
		return nil, fmt.Errorf("acp_run: 未配置的 runner %q(已配置: %s;配置见 config/bundle-base.yaml 的 acp-client.data.runners)",
			a.Runner, strings.Join(t.runnerNames(), ", "))
	}
	cwd, err := t.workdir(ctx, a.CWD)
	if err != nil {
		return nil, err
	}
	timeout := t.timeout
	if a.TimeoutSeconds > 0 {
		timeout = time.Duration(a.TimeoutSeconds) * time.Second
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	s, err := t.startSession(cctx, r, cwd)
	if err != nil {
		return nil, err
	}
	defer s.close()

	started := time.Now()
	res, err := s.prompt(cctx, a.Message)
	if err != nil {
		// 结构化错误回传模型(不中断 turn),同时把已捕获的输出带上 ——
		// 外部 agent 失败时它前半段的结论往往才是排查线索。
		return map[string]any{
			"error":      "acp_run: " + err.Error(),
			"runner":     a.Runner,
			"output":     s.output(),
			"elapsed_ms": time.Since(started).Milliseconds(),
		}, nil
	}
	return map[string]any{
		"runner":            a.Runner,
		"session_id":        s.id,
		"stop_reason":       res,
		"output":            s.output(),
		"tool_calls":        s.toolCallTitles(),
		"permissions_asked": s.permsAsked(),
		"cwd":               cwd,
		"elapsed_ms":        time.Since(started).Milliseconds(),
	}, nil
}

// workdir 决定工作目录:参数 > 本次调用的工作根 > 进程 cwd。
//
// 为什么允许"没有 cwd"就落到工作根:模型常常在同一仓库里连续委派,每次都让它抄一遍
// 绝对路径既费 token 又容易抄错。工作根由宿主按沙箱档位给出,是安全的那一个。
func (t *Tool) workdir(ctx context.Context, want string) (string, error) {
	if want != "" {
		st, err := os.Stat(want)
		if err != nil {
			return "", fmt.Errorf("acp_run: cwd %q 不存在或不可读: %w", want, err)
		}
		if !st.IsDir() {
			return "", fmt.Errorf("acp_run: cwd %q 不是目录", want)
		}
		return want, nil
	}
	if dir, ok := sdk.WorkRootOf(ctx); ok && dir != "" {
		return dir, nil
	}
	return os.Getwd()
}

func (t *Tool) runnerNames() []string {
	out := make([]string, 0, len(t.runners))
	for n := range t.runners {
		out = append(out, n)
	}
	return out
}

// session 一次 ACP 会话(子进程 + 连接 + 累计输出)。
type session struct {
	cmd   *exec.Cmd
	conn  *conn
	id    string
	stdin io.WriteCloser // 保留引用以便关闭(告知对端 EOF)

	mu       sync.Mutex
	text     strings.Builder
	thoughts strings.Builder
	tools    []string
	perms    int
	stderr   *tailBuffer
}

// startSession 起子进程并完成 initialize + session/new。
func (t *Tool) startSession(ctx context.Context, r runner, cwd string) (*session, error) {
	cmd := exec.CommandContext(ctx, r.command, r.args...)
	cmd.Dir = cwd
	setProcessGroup(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("acp_run: 取 stdin 失败: %w", err)
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("acp_run: 取 stdout 失败: %w", err)
	}
	errTail := &tailBuffer{max: 4096}
	cmd.Stderr = errTail
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("acp_run: 启动 %q 失败: %w(检查它是否在 PATH 上)", r.command, err)
	}
	s := &session{cmd: cmd, stderr: errTail}
	s.stdin = in
	s.conn = newConn(ctx, in, outPipe, callbacks{
		onRequest: func(ctx context.Context, method string, params json.RawMessage) (any, *rpcErr) {
			if method != "session/request_permission" {
				return nil, &rpcErr{Code: -32601, Message: "unsupported request: " + method}
			}
			return t.handlePermission(ctx, s, params), nil
		},
		onNotify: func(ctx context.Context, method string, params json.RawMessage) {
			if method == "session/update" {
				s.applyUpdate(ctx, params)
			}
		},
	})

	var initRes struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := s.conn.call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		// 能力声明保守:不让对端把文件读写/终端操作反委托回来(那是另一条审计通道,
		// 且与 gah 的沙箱/审批不在同一条路上)。gah 自己的工具负责这些。
		"clientCapabilities": map[string]any{},
	}, &initRes); err != nil {
		s.close()
		return nil, fmt.Errorf("acp_run: initialize 失败: %w%s", err, s.stderrSuffix())
	}
	var newRes struct {
		SessionID string `json:"sessionId"`
	}
	if err := s.conn.call(ctx, "session/new", map[string]any{
		"cwd":        cwd,
		"mcpServers": []any{},
	}, &newRes); err != nil {
		s.close()
		return nil, fmt.Errorf("acp_run: session/new 失败: %w%s", err, s.stderrSuffix())
	}
	if newRes.SessionID == "" {
		s.close()
		return nil, fmt.Errorf("acp_run: session/new 未返回 sessionId%s", s.stderrSuffix())
	}
	s.id = newRes.SessionID
	return s, nil
}

// prompt 发一轮任务并等终态,返回 stopReason。
func (s *session) prompt(ctx context.Context, message string) (string, error) {
	var res struct {
		StopReason string `json:"stopReason"`
	}
	if err := s.conn.call(ctx, "session/prompt", map[string]any{
		"sessionId": s.id,
		"prompt":    []any{map[string]any{"type": "text", "text": message}},
	}, &res); err != nil {
		return "", fmt.Errorf("%w%s", err, s.stderrSuffix())
	}
	return res.StopReason, nil
}

// applyUpdate 收 session/update 的增量:正文进输出、思维另存、工具调用记标题。
func (s *session) applyUpdate(ctx context.Context, params json.RawMessage) {
	var p struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"update"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	u := p.Update
	switch u.SessionUpdate {
	case "agent_message_chunk":
		if u.Content.Text == "" {
			return
		}
		s.mu.Lock()
		s.text.WriteString(u.Content.Text)
		s.mu.Unlock()
		sdk.ReportToolProgress(ctx, toolName, u.Content.Text)
	case "agent_thought_chunk":
		// 思维不进输出(它是过程):但要留在结果里,便于事后看"它当时在想什么"。
		s.mu.Lock()
		s.thoughts.WriteString(u.Content.Text)
		s.mu.Unlock()
	case "tool_call", "tool_call_update":
		if u.SessionUpdate == "tool_call" && u.Title != "" {
			s.mu.Lock()
			if len(s.tools) < 20 {
				s.tools = append(s.tools, u.Title)
			}
			s.mu.Unlock()
			// 外部 agent 的工具调用对用户是**重要过程信号**(它正在动哪个文件):
			// 用一行紧凑文本进进度通道,不塞进正文输出(那会污染最终答复)。
			sdk.ReportToolProgress(ctx, toolName, "\n["+u.Title+"]\n")
		}
	}
}

// output 累计的最终答复(外部 agent 的正文)。
func (s *session) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text.String()
}

// toolCallTitles 外部 agent 用过的工具标题(最多 20 条)。
func (s *session) toolCallTitles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tools...)
}

func (s *session) permsAsked() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.perms
}

// close 结束会话:先关 stdin(部分 agent 靠 EOF 收尾),再整组终止。
func (s *session) close() {
	if s == nil {
		return
	}
	if s.conn != nil {
		s.conn.close()
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	killProcessGroup(s.cmd)
	if s.cmd != nil && s.cmd.Process != nil {
		_, _ = s.cmd.Process.Wait()
	}
}

// stderrSuffix 失败时附上对端 stderr 的尾巴 —— 外部 CLI 的报错几乎都在那里
// (协议层只会回一个笼统的错误码)。
func (s *session) stderrSuffix() string {
	if s == nil || s.stderr == nil {
		return ""
	}
	if t := strings.TrimSpace(s.stderr.String()); t != "" {
		return "; stderr: " + truncate(t, 800)
	}
	return ""
}

// —— 权限请求 ——

// permOption ACP 的一个权限选项。
type permOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// permParams session/request_permission 的关键字段。
type permParams struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		Title string `json:"title"`
		Kind  string `json:"kind"`
	} `json:"toolCall"`
	Options []permOption `json:"options"`
}

// handlePermission 把外部 agent 的权限请求翻成 gah 的审批弹窗。
func (t *Tool) handlePermission(ctx context.Context, s *session, params json.RawMessage) any {
	var p permParams
	if err := json.Unmarshal(params, &p); err != nil {
		return cancelled()
	}
	s.mu.Lock()
	s.perms++
	s.mu.Unlock()

	prompt := describePermission(p)
	ok := false
	if svc, has := t.confirmService(); has {
		// 审批失败(通道断开/用户取消)= 拒绝:安全默认,与 sdk 的 confirm 契约一致。
		if granted, err := svc.Confirm(ctx, prompt); err == nil {
			ok = granted
		}
	} else {
		// 没有审批通道:只能拒绝。把原因写进 prompt 的旁路 —— 结果里会记 permissions_asked
		// 与实际发生的拒绝,事后看得见"为什么它没做"。
		sdk.ReportToolProgress(ctx, toolName, "\n[未装配审批通道,已拒绝一次权限请求]\n")
	}
	if !ok {
		return cancelled()
	}
	if id, has := chooseOption(p.Options, true); has {
		return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": id}}
	}
	return cancelled()
}

// cancelled ACP 的"取消"应答。
func cancelled() any {
	return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
}

// confirmService 按需取审批服务(UI 侧可能比本插件后装配,不能在 Start 时缓存)。
func (t *Tool) confirmService() (sdk.ConfirmService, bool) {
	if t.c == nil {
		return nil, false
	}
	var svc sdk.ConfirmService
	if err := t.c.Inject("ctx.confirm", &svc); err != nil || svc == nil {
		return nil, false
	}
	return svc, true
}

// describePermission 把权限请求写成人话(用户要能判断"该不该给")。
func describePermission(p permParams) string {
	var b strings.Builder
	b.WriteString("[外部编码 agent 请求权限] ")
	if p.ToolCall.Title != "" {
		b.WriteString(p.ToolCall.Title)
	} else {
		b.WriteString("(未命名操作)")
	}
	if p.ToolCall.Kind != "" {
		b.WriteString("(" + p.ToolCall.Kind + ")")
	}
	if len(p.Options) > 0 {
		names := make([]string, 0, len(p.Options))
		for _, o := range p.Options {
			names = append(names, o.Name)
		}
		b.WriteString("\n可选项: " + strings.Join(names, " / "))
	}
	b.WriteString("\n批准 = 允许它继续(用最窄的「仅本次」授权);拒绝 = 中止这次操作。")
	return b.String()
}

// chooseOption 从选项里挑一个:**允许取最窄(once 优先),拒绝取最窄(reject_once 优先)**。
//
// 为什么不直接取第一个:"allow_always" 可能是第一个,而用户只想批这一步 ——
// 一次点击变成永久放行,是这类审批最常见的坑。
func chooseOption(opts []permOption, ok bool) (string, bool) {
	prefer := []string{"reject_once", "reject_always"}
	if ok {
		prefer = []string{"allow_once", "allow_always"}
	}
	for _, want := range prefer {
		for _, o := range opts {
			if strings.EqualFold(o.Kind, want) && o.OptionID != "" {
				return o.OptionID, true
			}
		}
	}
	return "", false
}

// tailBuffer 只留最后 max 字节的 writer(stderr 可能很长,只要尾巴)。
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// truncate 按 rune 截断(用于 stderr 尾巴)。
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}
