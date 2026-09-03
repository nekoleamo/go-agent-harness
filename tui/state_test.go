package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestApplyUserMessage(t *testing.T) {
	s := &State{Profile: "tui"}
	s.ApplySessionEvent(&sdk.SessionEvent{
		Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "hi"},
	})
	if n := len(s.Lines); n != 1 || s.Lines[0].Kind != "user" || s.Lines[0].Text != "hi" {
		t.Fatalf("user 行不符: %+v", s.Lines)
	}
}

func TestStreamingChunksAppend(t *testing.T) {
	s := &State{}
	chunk := func(d string) *sdk.SessionEvent {
		return &sdk.SessionEvent{Kind: sdk.EventAssistantChunk, Payload: sdk.LLMStreamEvent{Delta: d}}
	}
	s.ApplySessionEvent(chunk("你"))
	s.ApplySessionEvent(chunk("好"))
	if n := len(s.Lines); n != 1 || s.Lines[0].Text != "你好" || !s.Lines[0].Streaming {
		t.Fatalf("流式增量应合并到一行: %+v", s.Lines)
	}
	// 完成后 final 替换
	s.ApplySessionEvent(&sdk.SessionEvent{
		Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "你好哇"},
	})
	if s.Lines[0].Text != "你好哇" || s.Lines[0].Streaming {
		t.Fatalf("final 应替换流式行: %+v", s.Lines[0])
	}
}

func TestToolCallAndResult(t *testing.T) {
	s := &State{}
	s.ApplySessionEvent(&sdk.SessionEvent{
		Kind: sdk.EventToolCall, Payload: sdk.ToolCallEvent{ID: "c1", Name: "shell", Arguments: `{"command":"ls"}`},
	})
	s.ApplySessionEvent(&sdk.SessionEvent{
		Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "c1", Name: "shell", Content: `{"output":"a.txt"}`},
	})
	if n := len(s.Lines); n != 2 || s.Lines[1].Kind != "tool" {
		t.Fatalf("工具调用与结果行不符: %+v", s.Lines)
	}
	if !strings.Contains(s.Lines[1].Text, "a.txt") {
		t.Fatalf("结果摘要未含输出: %+v", s.Lines[1])
	}
	// 错误结果 → error 行
	s.ApplySessionEvent(&sdk.SessionEvent{
		Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "c2", Name: "shell", Error: "blocked"},
	})
	last := s.Lines[len(s.Lines)-1]
	if last.Kind != "error" || !strings.Contains(last.Text, "blocked") {
		t.Fatalf("错误结果应为 error 行: %+v", last)
	}
}

func TestInputOperations(t *testing.T) {
	s := &State{}
	s.InsertRune('h')
	s.InsertRune('i')
	if s.Input != "hi" || s.Cursor != 2 {
		t.Fatalf("输入不符: %q cursor=%d", s.Input, s.Cursor)
	}
	s.Backspace()
	if s.Input != "h" || s.Cursor != 1 {
		t.Fatalf("退格不符: %q cursor=%d", s.Input, s.Cursor)
	}
	s.ClearInput()
	if s.Input != "" {
		t.Fatalf("清空不符: %q", s.Input)
	}
	s.Input = "/model deepseek-chat"
	if !s.IsCommand() {
		t.Fatal("命令判定失败")
	}
}

func TestRenderContainsKeyParts(t *testing.T) {
	s := &State{Profile: "tui", Running: true, Input: "你好", Model: "mock-model"}
	s.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "hi"}})
	out := Render(s, 80, 24)
	for _, want := range []string{"tui", "运行中", "模型: mock-model", "hi", "❯"} {
		if !strings.Contains(out, want) {
			t.Fatalf("渲染缺少 %q:\n%s", want, out)
		}
	}
}

func TestCancelShowsMetaNotError(t *testing.T) {
	m := &Model{state: &State{Running: true}}
	updated, _ := m.Update(agentDoneMsg{err: context.Canceled})
	m2 := updated.(*Model)
	if m2.state.Running {
		t.Fatal("取消后应停止运行态")
	}
	last := m2.state.Lines[len(m2.state.Lines)-1]
	if last.Kind != "meta" || !strings.Contains(last.Text, "回合已取消") {
		t.Fatalf("取消应显示提示而非错误: %+v", last)
	}
}

func TestEscapeCancelsRunning(t *testing.T) {
	cancelled := false
	onCancel := func() { cancelled = true }
	m := &Model{state: &State{Running: true}, onCancel: onCancel}
	m.handleEscape()
	if !cancelled {
		t.Fatal("Running 时 Esc 应触发取消")
	}
	// 非运行态 Esc 不触发
	cancelled = false
	m.state.Running = false
	m.handleEscape()
	if cancelled {
		t.Fatal("非运行态 Esc 不应触发取消")
	}
}

func TestRecentLinesWindow(t *testing.T) {
	s := &State{}
	for i := 0; i < 12; i++ {
		s.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "m"}})
	}
	if n := len(s.RecentLines(5)); n != 5 {
		t.Fatalf("滚动窗口应截断为 5,got %d", n)
	}
}
