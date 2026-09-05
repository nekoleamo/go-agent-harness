package tui

import (
	"context"
	"fmt"
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

// TestCursorMovement 光标移动:左/右边界钳制、头部/尾部跳转、Delete 删光标处。
func TestCursorMovement(t *testing.T) {
	s := &State{Input: "hello", Cursor: 2}
	s.CursorLeft()
	if s.Cursor != 1 {
		t.Fatalf("左移不符: %d", s.Cursor)
	}
	s.CursorHome()
	if s.Cursor != 0 {
		t.Fatalf("头部不符: %d", s.Cursor)
	}
	s.CursorLeft() // 头部越界钳制
	if s.Cursor != 0 {
		t.Fatalf("头部越界应钳制: %d", s.Cursor)
	}
	s.CursorEnd()
	if s.Cursor != 5 {
		t.Fatalf("尾部不符: %d", s.Cursor)
	}
	s.CursorRight() // 尾部越界钳制
	if s.Cursor != 5 {
		t.Fatalf("尾部越界应钳制: %d", s.Cursor)
	}
	// 光标中 Delete:删光标处字符
	s = &State{Input: "hello", Cursor: 1}
	s.CursorRight()
	if s.Input != "hello" || s.Cursor != 2 {
		t.Fatalf("右移后不符: %q %d", s.Input, s.Cursor)
	}
	// 直接验证 Delete(光标=1:删 'e')
	s2 := &State{Input: "hello", Cursor: 1}
	s2.Delete()
	if s2.Input != "hllo" || s2.Cursor != 1 {
		t.Fatalf("Delete 应删光标处: %q %d", s2.Input, s2.Cursor)
	}
	// 末尾 Delete = no-op
	s3 := &State{Input: "hi", Cursor: 2}
	s3.Delete()
	if s3.Input != "hi" {
		t.Fatalf("末尾 Delete 应 no-op: %q", s3.Input)
	}
	// CursorEnd 后插入(追加)
	s4 := &State{Input: "hi", Cursor: 2}
	s4.InsertRune('!')
	if s4.Input != "hi!" || s4.Cursor != 3 {
		t.Fatalf("尾部追加不符: %q %d", s4.Input, s4.Cursor)
	}
}

// TestScrollWindow 滚动窗口:文本超窗口后 offset 生效,钳制到最新窗口。
func TestScrollWindow(t *testing.T) {
	s := &State{}
	for i := 0; i < 10; i++ {
		s.Lines = append(s.Lines, Line{Kind: "meta", Text: fmt.Sprintf("L%d", i)})
	}
	// 窗口 3:默认跟随最新
	win := s.visible(3)
	if len(win) != 3 || win[2].Text != "L9" {
		t.Fatalf("默认应显示最新窗口: %+v", win)
	}
	// 上滚 5 行:窗口前移到更早内容(L2–L4)
	s.ScrollBy(5, 3)
	win = s.visible(3)
	if len(win) != 3 || win[0].Text != "L2" || win[2].Text != "L4" {
		t.Fatalf("上滚窗口不符: %+v", win)
	}
	if s.ScrollOffset != 5 {
		t.Fatalf("offset 不符: %d", s.ScrollOffset)
	}
	// 回底
	s.ScrollBy(-9, 3)
	if s.ScrollOffset != 0 {
		t.Fatalf("回底应为 0: %d", s.ScrollOffset)
	}
	// 越界上滚钳制到最大合法窗口
	s.ScrollBy(100, 3)
	win = s.visible(3)
	if win[0].Text != "L0" {
		t.Fatalf("极限上滚应显示最早窗口: %+v", win)
	}
}

// TestApplyReplaySkipsTurnEnd 重放跳过 turn/end 轮次分隔行(实时回合保留)。
func TestApplyReplaySkipsTurnEnd(t *testing.T) {
	s := &State{}
	s.ApplyReplay(&sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "hi"}})
	s.ApplyReplay(&sdk.SessionEvent{Kind: sdk.EventTurnEnd, Payload: "done"})
	s.ApplyReplay(&sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "ok"}})
	if len(s.Lines) != 2 {
		t.Fatalf("重放应跳过轮次分隔行: %d 行", len(s.Lines))
	}
	for _, l := range s.Lines {
		if strings.Contains(l.Text, "轮次结束") {
			t.Fatal("重放不应出现“轮次结束”meta 行")
		}
	}
	// 实时事件照常(不分隔)
	s.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventTurnEnd, Payload: "done"})
	if n := len(s.Lines); n != 3 || !strings.Contains(s.Lines[n-1].Text, "轮次结束") {
		t.Fatalf("实时 turn/end 应保留分隔行: %d 行", n)
	}
}

// TestAssistantToolCallsNoDup assistant 带工具声明后不再双写工具行(工具行由 EventToolCall 单发)。
func TestAssistantToolCallsNoDup(t *testing.T) {
	s := &State{}
	s.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "hi"}})
	s.ApplySessionEvent(&sdk.SessionEvent{
		Kind: sdk.EventAssistantMessage,
		Payload: sdk.AssistantMessage{Content: "", ToolCalls: []sdk.ToolCall{
			{ID: "c1", Name: "shell", Arguments: `{"command":"ls"}`},
		}},
	})
	s.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventToolCall, Payload: sdk.ToolCallEvent{ID: "c1", Name: "shell", Arguments: `{"command":"ls"}`}})
	s.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "c1", Name: "shell", Content: "ok"}})
	// 工具行:EventToolCall 1 条 + 结果 1 条,无 assistant 预铺重复行
	toolRows := 0
	for _, l := range s.Lines {
		if l.Kind == "tool" {
			toolRows++
		}
	}
	if toolRows != 2 {
		t.Fatalf("工具行应仅 EventToolCall+Result 两条(无 assistant 双写): %d 行 → %+v", toolRows, s.Lines)
	}
}

// TestReplayTurnDivider 重放跨轮插细分隔线(轮界可分);首轮/同轮不插。
func TestReplayTurnDivider(t *testing.T) {
	s := &State{}
	s.ApplyReplay(&sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "q1"}})
	s.ApplyReplay(&sdk.SessionEvent{Kind: sdk.EventTurnEnd, Payload: "done"})
	s.ApplyReplay(&sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "a1"}})
	s.ApplyReplay(&sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "q2"}})
	s.ApplyReplay(&sdk.SessionEvent{Kind: sdk.EventTurnEnd, Payload: "done"})
	s.ApplyReplay(&sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "a2"}})
	// 期望:user(q1) assistant(a1) [分隔] user(q2) assistant(a2)
	hasDivider := false
	for _, l := range s.Lines {
		if l.Kind == "meta" && strings.Contains(l.Text, "─") {
			hasDivider = true
		}
	}
	if !hasDivider {
		t.Fatalf("跨轮应插细分隔线: %+v", s.Lines)
	}
	// 总数:q1 a1 [分隔] q2 a2 = 5 行(不含轮次结束)
	if len(s.Lines) != 5 {
		t.Fatalf("行数应为 5(q1 a1 ─ q2 a2), got %d: %+v", len(s.Lines), s.Lines)
	}
	if s.Lines[2].Kind != "meta" || s.Lines[2].Text != turnDivider {
		t.Fatalf("分隔线应位于第 3 行: %+v", s.Lines[2])
	}
	// 不出现全宽“轮次结束”meta
	for _, l := range s.Lines {
		if strings.Contains(l.Text, "轮次结束") {
			t.Fatal("重放不应出现全宽轮次结束行")
		}
	}
}

// TestScrollMetrics 滚动条度量:无需滚动全窗口/滑块比例/沉底/极限顶部。
func TestScrollMetrics(t *testing.T) {
	// 无需滚动:窗口=总行
	top, thumb := scrollMetrics(5, 5, 0)
	if top != 0 || thumb != 5 {
		t.Fatalf("无需滚动应全窗口: %d %d", top, thumb)
	}
	// 总10行窗口3:跟随最新 → 滑块沉底
	top, thumb = scrollMetrics(10, 3, 0)
	if thumb != 1 || top != 2 {
		t.Fatalf("沉底不符: top=%d thumb=%d", top, thumb)
	}
	// 极限上滚(offset=maxOff=7):滑块到顶
	top, thumb = scrollMetrics(10, 3, 7)
	if top != 0 || thumb != 1 {
		t.Fatalf("到顶不符: top=%d thumb=%d", top, thumb)
	}
	// 中间位置:滑块按比例
	top, thumb = scrollMetrics(10, 3, 3)
	if top != 1 {
		t.Fatalf("中间位置 top 不符: %d", top)
	}
	// 窗口 0 防御
	top, thumb = scrollMetrics(10, 0, 0)
	if thumb != 0 {
		t.Fatalf("win<=0 应 0: %d", thumb)
	}
}

// TestInsertTextPaste 粘贴插入:光标中/末尾插入、单行化(换行转空格)。
func TestInsertTextPaste(t *testing.T) {
	s := &State{}
	s.InsertText("sk-1234567890")
	if s.Input != "sk-1234567890" || s.Cursor != 13 {
		t.Fatalf("末尾插入不符: %q cursor=%d", s.Input, s.Cursor)
	}
	// 光标中插:构造光标位于 '-' 后(State 无左右键,直接设 Cursor)
	s2 := &State{Input: "sk-1234567890", Cursor: 5}
	s2.InsertText("X")
	if s2.Input != "sk-12X34567890" {
		t.Fatalf("光标中插不符: %q", s2.Input)
	}
	if s2.Cursor != 6 {
		t.Fatalf("中插后光标应前移: %d", s2.Cursor)
	}
	// 粘贴单行化:复制常见的尾换行/CRLF → 空格,不影响后续命令解析
	s.ClearInput()
	s.InsertText("https://api.siliconflow.cn/v1\n")
	if s.Input != "https://api.siliconflow.cn/v1 " {
		t.Fatalf("尾换行应转空格: %q", s.Input)
	}
	s.InsertText("sk-abc\r\n")
	if s.Input != "https://api.siliconflow.cn/v1 sk-abc " {
		t.Fatalf("CRLF 应转空格: %q", s.Input)
	}
}

func TestRenderContainsKeyParts(t *testing.T) {
	s := &State{Profile: "tui", Running: true, Input: "你好", Model: "mock-model"}
	s.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "hi"}})
	out := Render(s, 80, 24)
	for _, want := range []string{"tui", "思考中", "模型: mock-model", "hi", "❯"} {
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
