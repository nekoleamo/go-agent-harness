// bubbletea 壳:事件分发与输入处理;渲染逻辑在 render.go,状态推进在 state.go。
package tui

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// sessionEventMsg 会话事件推送(经 session/event 广播)。
type sessionEventMsg struct{ ev *sdk.SessionEvent }

type statusMsg struct{ status string }

type agentDoneMsg struct{ err error }

type confirmMsg struct{ prompt string }

// Model 实现 tea.Model。
type Model struct {
	state *State
	w, h  int
	quit  bool

	onSubmit  func(input string)           // 普通输入提交(注入)
	onCommand func(cmd string) error       // 命令处理(注入)
	onConfirm func(ok bool)                // 确认答复(注入;见 app.Confirm)
	onCancel  func()                       // 取消进行中的回合(注入;Esc 触发)
	hints     func(prefix string) []string // 命令提示(注入;前缀=去掉 / 后的输入)
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case sessionEventMsg:
		m.state.ApplySessionEvent(msg.ev)
	case statusMsg:
		m.state.ApplyStatus(msg.status)
	case agentDoneMsg:
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				// 用户主动取消(Esc):提示而非报错
				m.state.Lines = append(m.state.Lines, Line{Kind: "meta", Text: "回合已取消"})
			} else {
				m.state.SetError("回合失败: " + msg.err.Error())
			}
		}
		m.state.Running = false
	case confirmMsg:
		m.state.ApplyConfirmPrompt(msg.prompt)
	case tea.KeyMsg:
		m.handleKey(msg)
	}
	if m.quit {
		return m, tea.Quit
	}
	return m, nil
}

func (m *Model) View() tea.View {
	v := tea.NewView(Render(m.state, m.w, m.h))
	v.AltScreen = true
	return v
}

// handleEscape Esc 键处理:运行中取消当前回合。
func (m *Model) handleEscape() {
	if m.state.Running && m.onCancel != nil {
		m.onCancel()
		m.state.Lines = append(m.state.Lines, Line{Kind: "meta", Text: "正在取消回合…"})
	}
}

func (m *Model) handleKey(msg tea.KeyMsg) {
	k := msg.Key()
	// 确认弹层优先:y/n 决定(任何确认态下的键入不再进输入框)
	if m.state.PendingConfirm != "" {
		switch k.Code {
		case 'y', 'Y':
			m.state.ResolveConfirm(true)
			m.onConfirm(true)
		case 'n', 'N':
			m.state.ResolveConfirm(false)
			m.onConfirm(false)
		}
		return
	}
	// Ctrl+C:输入为空退出,输入中清空
	if k.Mod&tea.ModCtrl != 0 && (k.Code == 'c' || k.Code == 'C') {
		if m.state.Input == "" {
			m.quit = true
		} else {
			m.state.ClearInput()
			m.syncHints()
		}
		return
	}
	switch k.Code {
	case tea.KeyEnter:
		m.submit()
	case tea.KeyBackspace:
		m.state.Backspace()
		m.syncHints()
	case tea.KeyEscape:
		// Esc:中断进行中的回合(取消链:turn → LLM 流 → 工具进程)
		m.handleEscape()
	default:
		if k.Text != "" {
			for _, r := range k.Text {
				m.state.InsertRune(r)
			}
			m.syncHints()
		}
	}
}

// syncHints 输入以 / 开头时按当前前缀刷新命令提示(注册表过滤动态生效)。
func (m *Model) syncHints() {
	input := m.state.Input
	if !strings.HasPrefix(input, "/") || m.hints == nil {
		m.state.Suggestions = nil
		return
	}
	m.state.Suggestions = m.hints(strings.TrimPrefix(input, "/"))
}

// submit 提交输入:命令走 onCommand,否则走 onSubmit(异步回合)。
func (m *Model) submit() {
	input := m.state.Input
	m.state.ClearInput()
	m.syncHints()
	if input == "" {
		return
	}
	if strings.HasPrefix(input, "/") {
		if err := m.onCommand(input); err != nil {
			m.state.SetError(err.Error())
		}
		return
	}
	m.onSubmit(input)
}
