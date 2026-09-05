// S1.3 模型层组合键分派测试:ctrl+p/n(历史)、ctrl+k/u(kill)、ctrl+z/shift+z
// (undo/redo)经 Update 到达状态机(KeyPressMsg 构造,不依赖真实终端)。
package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// keyPress 构造修饰键击键消息。
func keyPress(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod}
}

func TestKeyComboHistoryAndEdit(t *testing.T) {
	m := &Model{state: &State{}}
	m.state.Lines = append(m.state.Lines, Line{Kind: "user", Text: "历史问题"})
	m.state.RecordCmd("/sandbox ro")

	// Ctrl+P → 最新命令;再 P → user 历史
	if _, _ = m.Update(keyPress('p', tea.ModCtrl)); m.state.Input != "/sandbox ro" {
		t.Fatalf("Ctrl+P 应取最新命令: %q", m.state.Input)
	}
	if _, _ = m.Update(keyPress('p', tea.ModCtrl)); m.state.Input != "历史问题" {
		t.Fatalf("再次 Ctrl+P 应取 user 历史: %q", m.state.Input)
	}
	// Ctrl+N 回最新命令;再 N 还原草稿(空)
	if _, _ = m.Update(keyPress('n', tea.ModCtrl)); m.state.Input != "/sandbox ro" {
		t.Fatalf("Ctrl+N 应回最新命令: %q", m.state.Input)
	}
	if _, _ = m.Update(keyPress('n', tea.ModCtrl)); m.state.Input != "" {
		t.Fatalf("Ctrl+N 越过最新应还原草稿: %q", m.state.Input)
	}

	// 输入文本后 Ctrl+K 删至行尾、Ctrl+Z undo 恢复
	for _, r := range "hello world" {
		m.state.InsertRune(r)
	}
	m.state.Cursor = 5 // hello| world
	if _, _ = m.Update(keyPress('k', tea.ModCtrl)); m.state.Input != "hello" {
		t.Fatalf("Ctrl+K 应删至行尾: %q", m.state.Input)
	}
	if _, _ = m.Update(keyPress('z', tea.ModCtrl)); m.state.Input != "hello world" || m.state.Cursor != 5 {
		t.Fatalf("Ctrl+Z 应恢复 kill 前: %q c%d", m.state.Input, m.state.Cursor)
	}
	// Ctrl+Shift+Z redo
	if _, _ = m.Update(keyPress('z', tea.ModCtrl|tea.ModShift)); m.state.Input != "hello" {
		t.Fatalf("Ctrl+Shift+Z 应重做 kill: %q", m.state.Input)
	}
	// Ctrl+U 删至行首
	if _, _ = m.Update(keyPress('u', tea.ModCtrl)); m.state.Input != "" {
		t.Fatalf("Ctrl+U 应删至行首: %q", m.state.Input)
	}
	// Alt+←/→ 按词移动(光标在文本中)
	for _, r := range "go build" {
		m.state.InsertRune(r)
	}
	m.state.Cursor = len([]rune(m.state.Input)) // 行尾
	if _, _ = m.Update(keyPress(tea.KeyLeft, tea.ModAlt)); m.state.Cursor != 3 {
		t.Fatalf("Alt+← 应到 build 词首: c%d", m.state.Cursor)
	}
	if _, _ = m.Update(keyPress(tea.KeyRight, tea.ModAlt)); m.state.Cursor != 8 {
		t.Fatalf("Alt+→ 应到行尾: c%d", m.state.Cursor)
	}
}

// TestKeyComboPickerNoop 选择器激活时组合键不干扰(历史/编辑不生效)。
func TestKeyComboPickerNoop(t *testing.T) {
	m := &Model{state: &State{}}
	m.state.RecordCmd("/help")
	m.state.Input = "/"
	m.state.Pick = &Pick{Items: []sdk.Option{opt("help", "帮助")}}
	if _, _ = m.Update(keyPress('p', tea.ModCtrl)); m.state.Input != "/" {
		t.Fatalf("选择器激活时 Ctrl+P 不应改输入: %q", m.state.Input)
	}
	if m.state.Pick == nil {
		t.Fatal("选择器应保持激活")
	}
}
