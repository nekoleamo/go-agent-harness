package tui

import (
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

func TestRecentLinesWindow(t *testing.T) {
	s := &State{}
	for i := 0; i < 12; i++ {
		s.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "m"}})
	}
	if n := len(s.RecentLines(5)); n != 5 {
		t.Fatalf("滚动窗口应截断为 5,got %d", n)
	}
}
