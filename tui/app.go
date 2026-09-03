// App:装配事件桥,驱动 bubbletea 程序。插件 ui-tui-app 仅做薄壳挂载。
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// App 封装 TUI 程序与宿主桥接。
// 事件流:session/event + agent/status 广播 → program.Send → Update → 渲染。
// 输入:onSubmit → goroutine 跑 agentLoop.Run(回合异步,不阻塞 UI)。
type App struct {
	model     *Model
	program   *tea.Program
	c         sdk.Ctx
	loop      sdk.AgentLoop
	llm       sdk.LLMService
	confirmCh chan bool // Confirm 阻塞等待用户答复
	subs      []sdk.Disposer

	cancelFn context.CancelFunc // 当前回合的取消函数(Esc 中断,见 model.onCancel)
}

// NewApp 构造 TUI 应用。
func NewApp(c sdk.Ctx, loop sdk.AgentLoop, llm sdk.LLMService, profile string) *App {
	state := &State{Profile: profile}
	m := &Model{state: state}
	a := &App{model: m, c: c, loop: loop, llm: llm, confirmCh: make(chan bool, 1)}
	m.onSubmit = a.submit
	m.onCommand = a.command
	m.onConfirm = a.confirmResult
	m.onCancel = a.cancelCurrent
	a.program = tea.NewProgram(m)
	return a
}

// Confirm 实现 sdk.ConfirmService:弹层询问用户 y/n。
// 无 UI 运行(非 TTY 降级)时 UI 插件不装配本服务,策略拒绝(安全默认)。
func (a *App) Confirm(ctx context.Context, prompt string) (bool, error) {
	a.program.Send(confirmMsg{prompt})
	select {
	case ok := <-a.confirmCh:
		return ok, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (a *App) confirmResult(ok bool) {
	a.confirmCh <- ok
}

// Start 启动 TUI(goroutine 跑 Run),挂接事件订阅。
func (a *App) Start() error {
	// 会话事件 → UI
	d1 := a.c.Subscribe(sdk.EventSession, func(ctx context.Context, ev *sdk.Event) error {
		if sev, ok := ev.Payload.(*sdk.SessionEvent); ok {
			a.program.Send(sessionEventMsg{sev})
		}
		return nil
	})
	// agent/status → UI 状态栏
	d2 := a.c.Subscribe(sdk.EventAgentStatus, func(ctx context.Context, ev *sdk.Event) error {
		if s, ok := ev.Payload.(string); ok {
			a.program.Send(statusMsg{s})
		}
		return nil
	})
	a.subs = []sdk.Disposer{d1, d2}

	go func() {
		_, err := a.program.Run()
		if err != nil {
			a.model.state.SetError("TUI 退出异常: " + err.Error())
		}
		// UI 退出 = 应用退出(main 监听 system/shutdown 后关停)
		a.c.Emit(context.Background(), "system/shutdown", nil, sdk.Emit)
	}()
	return nil
}

// Close 撤销订阅并退出程序。
func (a *App) Close() {
	for _, d := range a.subs {
		d()
	}
	a.program.Quit()
}

// submit 普通输入:异步跑一轮(持有取消句柄,Esc 中断)。
func (a *App) submit(input string) {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelFn = cancel
	go func() {
		err := a.loop.Run(ctx, input)
		a.cancelFn = nil
		a.program.Send(agentDoneMsg{err})
	}()
}

// cancelCurrent 取消进行中的回合(取消链:turn → LLM 流 → 工具进程,见设计 §8)。
func (a *App) cancelCurrent() {
	if a.cancelFn != nil {
		a.cancelFn()
	}
}

// command 处理 / 命令(M3:help/model/exit;沙箱/插件管理在 M4 接入)。
func (a *App) command(raw string) error {
	fields := strings.Fields(strings.TrimPrefix(raw, "/"))
	if len(fields) == 0 {
		return nil
	}
	switch fields[0] {
	case "exit":
		a.program.Quit()
	case "model":
		if len(fields) < 2 {
			return errString("/model <名称> 切换模型")
		}
		a.llm.SetModel(fields[1])
		a.model.state.Model = fields[1]
	case "sandbox":
		return a.cmdSandbox(fields)
	case "plugins":
		return a.cmdPlugins(fields)
	case "settings":
		return a.cmdSettings(fields)
	case "export":
		return a.cmdExport(fields)
	case "help":
		a.model.state.Lines = append(a.model.state.Lines,
			Line{Kind: "meta", Text: "命令:/model <名> | /sandbox ro|ws|full | /plugins list|on|off|unload | /settings history N|off | /export | /help | /exit"})
	case "sessions":
		return a.cmdSessions()
	case "jobs":
		return errString("/jobs 尚未实现(host-jobs 延后)")
	default:
		return errString("未知命令 /" + fields[0] + "(输入 /help)")
	}
	return nil
}

func (a *App) cmdSandbox(fields []string) error {
	if len(fields) < 2 {
		return errString("/sandbox ro|ws|full(read-only|workspace-write|full-access)")
	}
	var sb sdk.Sandbox
	if err := a.c.Inject("ctx.sandbox", &sb); err != nil {
		return errString("ctx.sandbox 未装配: " + err.Error())
	}
	var mode sdk.SandboxMode
	switch fields[1] {
	case "ro":
		mode = sdk.SandboxReadOnly
	case "ws":
		mode = sdk.SandboxWorkspace
	case "full":
		mode = sdk.SandboxFullAccess
	default:
		return errString("/sandbox ro|ws|full")
	}
	sb.SetMode(mode)
	a.model.state.Sandbox = string(mode)
	return nil
}

func (a *App) cmdPlugins(fields []string) error {
	var mgr sdk.PluginManager
	if err := a.c.Inject("ctx.pluginManager", &mgr); err != nil {
		return errString("ctx.pluginManager 未装配: " + err.Error())
	}
	if len(fields) < 2 {
		rows := "插件:"
		for _, info := range mgr.List() {
			rows += "\n  " + info.ID + " [" + info.Type + "] " + info.State
		}
		a.model.state.Lines = append(a.model.state.Lines, Line{Kind: "meta", Text: rows})
		return nil
	}
	switch fields[1] {
	case "on", "load":
		if len(fields) < 3 {
			return errString("/plugins on <id>")
		}
		if err := mgr.Load(fields[2]); err != nil {
			return errString(err.Error())
		}
		return nil
	case "off", "unload":
		if len(fields) < 3 {
			return errString("/plugins off <id>")
		}
		if err := mgr.Unload(fields[2]); err != nil {
			return errString(err.Error())
		}
		return nil
	case "list":
		rows := "插件:"
		for _, info := range mgr.List() {
			rows += "\n  " + info.ID + " [" + info.Type + "] " + info.State
		}
		a.model.state.Lines = append(a.model.state.Lines, Line{Kind: "meta", Text: rows})
		return nil
	default:
		return errString("/plugins list|on|off <id>")
	}
}

func (a *App) cmdSettings(fields []string) error {
	var sessions sdk.SessionLog
	if err := a.c.Inject("ctx.sessions", &sessions); err != nil {
		return errString("ctx.sessions 未装配")
	}
	if len(fields) < 3 || fields[1] != "history" {
		return errString("/settings history N|off|unlimited")
	}
	var n int
	switch fields[2] {
	case "off":
		n = -1
	case "unlimited":
		n = 0
	default:
		if _, err := fmt.Sscanf(fields[2], "%d", &n); err != nil || n < 0 {
			return errString("/settings history N|off|unlimited")
		}
	}
	sessions.SetHistory(n)
	a.model.state.Lines = append(a.model.state.Lines,
		Line{Kind: "meta", Text: "/settings history -> " + fields[2]})
	return nil
}

func (a *App) cmdSessions() error {
	var cs sdk.CwdSessions
	if err := a.c.Inject("ctx.cwdSessions", &cs); err != nil {
		return errString("ctx.cwdSessions 未装配: " + err.Error())
	}
	rows := "当前会话: " + cs.Current() + "\n已有会话:"
	list := cs.List()
	if len(list) == 0 {
		rows += " (无)"
	}
	for _, k := range list {
		rows += "\n  " + k
	}
	a.model.state.Lines = append(a.model.state.Lines, Line{Kind: "meta", Text: rows})
	return nil
}

func (a *App) cmdExport(fields []string) error {
	var sessions sdk.SessionLog
	if err := a.c.Inject("ctx.sessions", &sessions); err != nil {
		return errString("ctx.sessions 未装配")
	}
	// 默认导出到当前会话存档路径(host-cwd-sessions),可指定 /export <path>
	path := ""
	if len(fields) > 1 {
		path = fields[1]
	} else {
		var cs sdk.CwdSessions
		if err := a.c.Inject("ctx.cwdSessions", &cs); err == nil {
			path = cs.Path()
		}
	}
	evs := sessions.Replay()
	if path == "" {
		// 无落盘配置:仅统计(兜底)
		a.model.state.Lines = append(a.model.state.Lines,
			Line{Kind: "meta", Text: "会话事件数: " + fmt.Sprint(len(evs))})
		return nil
	}
	var sb strings.Builder
	for _, ev := range evs {
		b, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errString("导出失败: " + err.Error())
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return errString("导出失败: " + err.Error())
	}
	a.model.state.Lines = append(a.model.state.Lines,
		Line{Kind: "meta", Text: fmt.Sprintf("已导出 %d 条事件 → %s", len(evs), path)})
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }
