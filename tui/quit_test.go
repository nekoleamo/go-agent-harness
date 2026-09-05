// Ctrl+C 双按彻底退出(防误触)的行为测试:直接以 tea 消息驱动 Model.Update,
// 验证单次不退出、窗口内二次退出、超时/其他键解除、输入中清空不退出等状态机。
package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// ctrlC 模拟 Ctrl+C 按键(输入为空时的退出键)。
var ctrlC = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

// newQuitModel 构造最小 Model(注入回调可为 nil:测试不触达提交路径)。
func newQuitModel() *Model {
	return &Model{state: &State{}}
}

// press 以一次按键驱动 Update,返回返回的 cmd(超时解除命令检测用)。
func press(m *Model, msg tea.Msg) tea.Cmd {
	_, cmd := m.Update(msg)
	return cmd
}

// TestQuitDoublePress 输入为空时 Ctrl+C 需连按两次才退出:
// 第一次仅武装(不退出、界面提示),窗口内第二次置 quit。
func TestQuitDoublePress(t *testing.T) {
	m := newQuitModel()

	// 第一次:武装不退出,并返回 2s 超时解除命令
	cmd := press(m, ctrlC)
	if m.quit {
		t.Fatal("第一次 Ctrl+C 不应退出")
	}
	if !m.quitArmed || !m.state.QuitArmed {
		t.Fatalf("第一次应武装待确认: quitArmed=%v QuitArmed=%v", m.quitArmed, m.state.QuitArmed)
	}
	if cmd == nil {
		t.Fatal("武装后应返回超时解除命令(tea.Tick)")
	}

	// 窗口内第二次:彻底退出
	press(m, ctrlC)
	if !m.quit {
		t.Fatal("窗口内第二次 Ctrl+C 应退出")
	}
}

// TestQuitTimeoutDisarm 超时(disarmQuitMsg)自动解除武装:
// 解除后再按一次 Ctrl+C 只重新武装,不退出。
func TestQuitTimeoutDisarm(t *testing.T) {
	m := newQuitModel()
	press(m, ctrlC) // 武装

	press(m, disarmQuitMsg{}) // 模拟 2s 超时
	if m.quitArmed || m.state.QuitArmed {
		t.Fatal("超时应解除武装")
	}

	press(m, ctrlC) // 重新武装而非退出
	if m.quit {
		t.Fatal("超时解除后单次 Ctrl+C 不应退出")
	}
	if !m.quitArmed {
		t.Fatal("超时解除后再次 Ctrl+C 应重新武装")
	}
}

// TestQuitDisarmOnOtherKey 武装期间按其他任意键解除待退出(不退出);
// Esc 语义照常(运行中取消回合,空闲无操作)。
func TestQuitDisarmOnOtherKey(t *testing.T) {
	m := newQuitModel()
	press(m, ctrlC)

	press(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.quit || m.quitArmed || m.state.QuitArmed {
		t.Fatalf("武装中按字符键应解除且不退出: quit=%v armed=%v", m.quit, m.quitArmed)
	}

	m2 := newQuitModel()
	press(m2, ctrlC)
	press(m2, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m2.quit || m2.quitArmed {
		t.Fatal("武装中按 Esc 应解除且不退出")
	}
}

// TestQuitCtrlCClearsInput 输入中有文本时 Ctrl+C 仅清空输入(不退出、不武装)。
func TestQuitCtrlCClearsInput(t *testing.T) {
	m := newQuitModel()
	m.state.Input = "hi"
	m.state.Cursor = 2

	press(m, ctrlC)
	if m.quit || m.quitArmed {
		t.Fatalf("输入中 Ctrl+C 不应退出/武装: quit=%v armed=%v", m.quit, m.quitArmed)
	}
	if m.state.Input != "" {
		t.Fatalf("输入中 Ctrl+C 应清空输入,实际: %q", m.state.Input)
	}
}

// TestQuitIgnoredOnConfirm 确认弹层激活时 Ctrl+C 忽略(不退出、不武装)。
func TestQuitIgnoredOnConfirm(t *testing.T) {
	m := newQuitModel()
	m.state.PendingConfirm = "危险操作确认"

	press(m, ctrlC)
	if m.quit || m.quitArmed || m.state.QuitArmed {
		t.Fatal("确认弹层中 Ctrl+C 应被忽略(不退出不武装)")
	}
}
