// S-P0-2 异步提问与问题栈的 TUI 侧测试(作答态/Esc 退出/状态栏与提示符;见 docs/TUI_OPTIMIZE.md §4.4)。
package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestModelEscapeExitsAnswering Esc 退出作答态:提问与输入草稿均保留,不再劫持输入。
func TestModelEscapeExitsAnswering(t *testing.T) {
	a := commandTestApp()
	a.model.state.ApplyQuestionPrompt(sdk.Question{ID: "q1", Prompt: "选", Options: []sdk.QuestionOption{{Value: "a"}}})
	a.model.state.Input = "生产"
	a.model.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if a.model.state.Answering {
		t.Fatal("Esc 应退出作答态")
	}
	if a.model.state.Input != "生产" {
		t.Fatalf("Esc 不应清空输入,got %q", a.model.state.Input)
	}
	if a.model.state.ActiveQuestion() == nil {
		t.Fatal("提问应保留在栈内")
	}
	sent := make(chan string, 1)
	a.model.onSubmit = func(s string) { sent <- s }
	a.model.submit()
	select { // 退出作答态后输入走普通回合(不再被当作作答)
	case s := <-sent:
		if s != "生产" {
			t.Fatalf("应作为回合消息发出,got %q", s)
		}
	case <-time.After(time.Second):
		t.Fatal("未作为回合消息发出")
	}
}
