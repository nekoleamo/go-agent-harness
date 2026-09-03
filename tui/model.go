// bubbletea 壳:事件分发与输入处理;渲染逻辑在 render.go,状态推进在 state.go。
package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// sessionEventMsg 会话事件推送(经 session/event 广播)。
type sessionEventMsg struct{ ev *sdk.SessionEvent }

type statusMsg struct{ status string }

type agentDoneMsg struct{ err error }

// Model 实现 tea.Model。
type Model struct {
	state *State
	w, h  int
	quit  bool

	onSubmit  func(input string)     // 普通输入提交(注入)
	onCommand func(cmd string) error // 命令处理(注入)
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
			m.state.SetError("回合失败: " + msg.err.Error())
		}
		m.state.Running = false
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

func (m *Model) handleKey(msg tea.KeyMsg) {
	k := msg.Key()
	// Ctrl+C:输入为空退出,输入中清空
	if k.Mod&tea.ModCtrl != 0 && (k.Code == 'c' || k.Code == 'C') {
		if m.state.Input == "" {
			m.quit = true
		} else {
			m.state.ClearInput()
		}
		return
	}
	switch k.Code {
	case tea.KeyEnter:
		m.submit()
	case tea.KeyBackspace:
		m.state.Backspace()
	default:
		if k.Text != "" {
			for _, r := range k.Text {
				m.state.InsertRune(r)
			}
		}
	}
}

// submit 提交输入:命令走 onCommand,否则走 onSubmit(异步回合)。
func (m *Model) submit() {
	input := m.state.Input
	m.state.ClearInput()
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
