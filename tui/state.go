// Package tui 提供 ui-tui-app 的界面实现(bubbletea v2 + lipgloss)。
// 状态机与渲染为纯逻辑,可脱离终端单测;bubbletea 壳仅做事件分发。
package tui

import (
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Line 会话流展示行。
type Line struct {
	Kind      string // user|assistant|tool|meta|error
	Text      string
	Streaming bool // assistant 流式增量中
}

// State TUI 展示状态(事件驱动,线程安全由调用方保证)。
type State struct {
	Lines          []Line
	Running        bool
	Model          string
	Profile        string
	Error          string
	Input          string
	Cursor         int
	LastTool       string
	Sandbox        string   // 沙箱档位显示(read-only|workspace-write|full-access)
	PendingConfirm string   // 非空 = 有待确认的危险操作(确认弹层)
	Suggestions    []string // 输入 / 前缀时的命令提示(注册表过滤结果,渲染于输入行下方)
	Pick           *Pick    // 非空 = 交互式选择器激活(↑/↓ 移动,Enter 应用)
	PickDismissed  bool     // Esc/断点后抑制自动激活,直至输入变化
}

// ApplySessionEvent 把会话事件推进到展示状态(纯逻辑,可测)。
func (s *State) ApplySessionEvent(ev *sdk.SessionEvent) {
	switch ev.Kind {
	case sdk.EventUserMessage:
		if u, ok := ev.Payload.(sdk.UserMessage); ok {
			s.Lines = append(s.Lines, Line{Kind: "user", Text: u.Content})
		}
	case sdk.EventAssistantChunk:
		if cev, ok := ev.Payload.(sdk.LLMStreamEvent); ok && cev.Delta != "" {
			s.appendStreaming(cev.Delta)
		}
	case sdk.EventAssistantMessage:
		if a, ok := ev.Payload.(sdk.AssistantMessage); ok {
			s.finishStreaming(a.Content)
			if len(a.ToolCalls) > 0 {
				for _, tc := range a.ToolCalls {
					s.Lines = append(s.Lines, Line{Kind: "tool", Text: toolCallText(tc)})
				}
			}
		}
	case sdk.EventToolCall:
		if tc, ok := ev.Payload.(sdk.ToolCallEvent); ok {
			s.Lines = append(s.Lines, Line{Kind: "tool", Text: toolCallText(sdk.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})})
		}
	case sdk.EventToolResult:
		if r, ok := ev.Payload.(sdk.ToolResultEvent); ok {
			sum := r.Content
			if len(sum) > 160 {
				sum = sum[:160] + "…"
			}
			kind := "tool"
			label := "✓"
			if r.Error != "" {
				kind = "error"
				label = "✗"
				sum = r.Error
			}
			s.Lines = append(s.Lines, Line{Kind: kind, Text: label + " " + r.Name + ": " + sum})
		}
	case sdk.EventTurnEnd:
		s.Lines = append(s.Lines, Line{Kind: "meta", Text: "—— 轮次结束 ——"})
	case sdk.EventAgentError:
		if err, ok := ev.Payload.(error); ok {
			s.Error = err.Error()
			s.Lines = append(s.Lines, Line{Kind: "error", Text: "agent error: " + err.Error()})
		}
	}
}

// ApplyStatus 处理 agent/status(running/idle)。
func (s *State) ApplyStatus(status string) {
	switch status {
	case "running":
		s.Running = true
	case "idle":
		s.Running = false
	}
}

// SetError 设置错误(输入处理失败等)。
// ApplyConfirmPrompt 显示确认弹层。
func (s *State) ApplyConfirmPrompt(prompt string) {
	s.PendingConfirm = prompt
}

// ResolveConfirm 用户答复后清除弹层,返回决定。
func (s *State) ResolveConfirm(ok bool) bool {
	s.PendingConfirm = ""
	return ok
}

func (s *State) SetError(msg string) {
	s.Error = msg
	s.Lines = append(s.Lines, Line{Kind: "error", Text: msg})
}

func (s *State) appendStreaming(delta string) {
	if n := len(s.Lines); n > 0 && s.Lines[n-1].Kind == "assistant" && s.Lines[n-1].Streaming {
		s.Lines[n-1].Text += delta
		return
	}
	s.Lines = append(s.Lines, Line{Kind: "assistant", Text: delta, Streaming: true})
}

func (s *State) finishStreaming(final string) {
	if n := len(s.Lines); n > 0 && s.Lines[n-1].Kind == "assistant" && s.Lines[n-1].Streaming {
		s.Lines[n-1].Text = final
		s.Lines[n-1].Streaming = false
		return
	}
	if final != "" {
		s.Lines = append(s.Lines, Line{Kind: "assistant", Text: final})
	}
}

// InsertText 光标处插入一段文本(粘贴支持;bracketed paste 单行化:
// 命令行语义,换行转空格——API key/URL 复制常带尾换行,防破坏渲染)。
func (s *State) InsertText(text string) {
	text = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(text)
	t := []rune(text)
	if len(t) == 0 {
		return
	}
	b := []rune(s.Input)
	out := make([]rune, 0, len(b)+len(t))
	out = append(out, b[:s.Cursor]...)
	out = append(out, t...)
	out = append(out, b[s.Cursor:]...)
	s.Input = string(out)
	s.Cursor += len(t)
}

// InsertRune 输入字符。
func (s *State) InsertRune(r rune) {
	b := []rune(s.Input)
	b = append(b[:s.Cursor], append([]rune{r}, b[s.Cursor:]...)...)
	s.Input = string(b)
	s.Cursor++
}

// Backspace 删除光标前一字符。
func (s *State) Backspace() {
	if s.Cursor <= 0 || s.Input == "" {
		return
	}
	b := []rune(s.Input)
	s.Input = string(append(b[:s.Cursor-1], b[s.Cursor:]...))
	s.Cursor--
}

// ClearInput 提交后清空输入。
func (s *State) ClearInput() {
	s.Input = ""
	s.Cursor = 0
}

func toolCallText(tc sdk.ToolCall) string {
	args := tc.Arguments
	if len(args) > 80 {
		args = args[:80] + "…"
	}
	return "⚙ " + tc.Name + " " + args
}

// visible 滚动窗口:只渲染最近 n 行。
func (s *State) visible(n int) []Line {
	if len(s.Lines) <= n {
		return s.Lines
	}
	return s.Lines[len(s.Lines)-n:]
}

// RecentLines 供测试:返回当前展示行文本。
func (s *State) RecentLines(n int) []string {
	out := make([]string, 0, len(s.Lines))
	for _, l := range s.visible(n) {
		out = append(out, l.Kind+": "+l.Text)
	}
	return out
}

// InputText 命令判定:以 / 开头。
func (s *State) IsCommand() bool {
	return strings.HasPrefix(s.Input, "/")
}
