// App:装配事件桥,驱动 bubbletea 程序。插件 ui-tui-app 仅做薄壳挂载。
package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// App 封装 TUI 程序与宿主桥接。
// 事件流:session/event + agent/status 广播 → program.Send → Update → 渲染。
// 输入:onSubmit → goroutine 跑 agentLoop.Run(回合异步,不阻塞 UI)。
type App struct {
	model   *Model
	program *tea.Program
	c       sdk.Ctx
	loop    sdk.AgentLoop
	llm     sdk.LLMService

	subs []sdk.Disposer
}

// NewApp 构造 TUI 应用。
func NewApp(c sdk.Ctx, loop sdk.AgentLoop, llm sdk.LLMService, profile string) *App {
	state := &State{Profile: profile}
	m := &Model{state: state}
	a := &App{model: m, c: c, loop: loop, llm: llm}
	m.onSubmit = a.submit
	m.onCommand = a.command
	a.program = tea.NewProgram(m)
	return a
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

// submit 普通输入:异步跑一轮。
func (a *App) submit(input string) {
	go func() {
		err := a.loop.Run(context.Background(), input)
		a.program.Send(agentDoneMsg{err})
	}()
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
	case "help":
		a.model.state.Lines = append(a.model.state.Lines,
			Line{Kind: "meta", Text: "命令:/model <名> 切换模型 | /help 帮助 | /exit 退出(其余命令 M4 接入)"})
	case "sandbox", "plugins", "sessions", "jobs", "settings", "export":
		return errString("/" + fields[0] + " 将在 M4 可用")
	default:
		return errString("未知命令 /" + fields[0] + "(输入 /help)")
	}
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }
