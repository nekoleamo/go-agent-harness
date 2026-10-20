package tui

// 工具实时输出(C1)在 TUI 侧的用例。
//
// 要钉的是:
//  1. 增量**归位**到对应的工具调用行(按模型侧 call id),不是另起一行;
//  2. 渲染在工具行**下方**,且是弱化的附属内容(不抢结论的戏);
//  3. 找不到对应工具行 ⇒ 丢弃(宁可少显示,不可张冠李戴);
//  4. 限长:工具狂吐几 MB 时,界面不能被拖垮。

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// toolCallEv 造一条 tool/call 事件。
func toolCallEv(id, name string) *sdk.SessionEvent {
	return &sdk.SessionEvent{Kind: sdk.EventToolCall, Payload: sdk.ToolCallEvent{ID: id, Name: name, Arguments: `{}`}}
}

// progressEv 造一条 tool/progress 事件。
func progressEv(id, chunk string, seq int) *sdk.SessionEvent {
	return &sdk.SessionEvent{Kind: sdk.EventToolProgress, Payload: sdk.ToolProgressEvent{
		CallID: id, Name: "shell", Chunk: chunk, Seq: seq,
	}}
}

// TestToolProgressAttachesToToolRow 增量贴到对应工具调用行。
func TestToolProgressAttachesToToolRow(t *testing.T) {
	s := &State{}
	s.ApplySessionEvent(toolCallEv("call-1", "shell"))
	s.ApplySessionEvent(progressEv("call-1", "编译中", 1))
	s.ApplySessionEvent(progressEv("call-1", "链接中", 2))

	if len(s.Lines) != 1 {
		t.Fatalf("增量不该另起行(归位到工具行),got %d 行", len(s.Lines))
	}
	if s.Lines[0].Live != "编译中链接中" {
		t.Fatalf("实时输出应拼接到工具行,got %q", s.Lines[0].Live)
	}
}

// TestToolProgressUnknownCallDropped 找不到对应工具行 ⇒ 丢弃。
func TestToolProgressUnknownCallDropped(t *testing.T) {
	s := &State{}
	s.ApplySessionEvent(progressEv("call-x", "孤儿块", 1))
	if len(s.Lines) != 0 {
		t.Fatalf("无对应工具行时不该产生任何行,got %d", len(s.Lines))
	}
}

// TestToolProgressRendersUnderToolRow 渲染:实时输出在工具行下方,带缩进。
func TestToolProgressRendersUnderToolRow(t *testing.T) {
	s := &State{}
	s.ApplySessionEvent(toolCallEv("call-1", "shell"))
	s.ApplySessionEvent(progressEv("call-1", "编译中", 1))

	rows := flattenViewLines(s, 100)
	if len(rows) != 2 {
		t.Fatalf("应有工具行 + 实时输出行,got %d 行", len(rows))
	}
	if rows[0].kind != "tool" {
		t.Fatalf("第一行应是工具调用行,got %q", rows[0].kind)
	}
	if rows[1].kind != "toolout" {
		t.Fatalf("第二行应是实时输出行,got %q", rows[1].kind)
	}
	out := stripANSI(renderSessionRow(rows[1], s, 1))
	if !strings.Contains(out, "编译中") {
		t.Fatalf("实时输出行应含增量文本: %q", out)
	}
	if !strings.HasPrefix(out, "  ") {
		t.Fatalf("实时输出行应缩进(附属内容): %q", out)
	}
	// 实时输出不是行首:不该继承工具行的左缘竖线,也不该带折叠标记。
	if strings.HasPrefix(out, "▍ ") {
		t.Fatalf("实时输出行不该带工具左缘标记: %q", out)
	}
}

// TestToolProgressAfterSessionSwitch 切会话后旧行索引失效:迟到的增量不得写进别的行。
func TestToolProgressAfterSessionSwitch(t *testing.T) {
	s := &State{}
	s.ApplySessionEvent(toolCallEv("call-1", "shell"))
	s.ApplySessionEvent(progressEv("call-1", "编译中", 1))

	// 切会话:会话流清空重建(与 tui/app.go 的 afterSessionSwitch 同款)
	s.Lines = nil
	s.resetToolRows()
	s.ApplySessionEvent(progressEv("call-1", "迟到", 2)) // 不得 panic
	if len(s.Lines) != 0 {
		t.Fatalf("切会话后不应凭空长出行,got %d", len(s.Lines))
	}
}

// TestToolProgressStaleIndexNoPanic 索引失效但映射还在(未走重置路径)⇒ 不得越界崩掉。
//
// 越界 = 进程崩:一个迟到的增量块就能把整个 TUI 打没。
func TestToolProgressStaleIndexNoPanic(t *testing.T) {
	s := &State{}
	s.ApplySessionEvent(toolCallEv("call-1", "shell"))
	s.ApplySessionEvent(progressEv("call-1", "编译中", 1))
	// 会话流被清空重建(索引失效),但映射未清(防御性路径)
	s.Lines = nil
	s.ApplySessionEvent(progressEv("call-1", "迟到", 2)) // 不得 panic
}

// TestToolProgressTrimmed 长输出只保留尾部(界面不能被几 MB 文本拖垮)。
func TestToolProgressTrimmed(t *testing.T) {
	s := &State{}
	s.ApplySessionEvent(toolCallEv("call-1", "shell"))
	big := strings.Repeat("行\n", 5000)
	s.ApplySessionEvent(progressEv("call-1", big, 1))

	live := s.Lines[0].Live
	if len([]rune(live)) > liveMaxRunes+liveMaxLines*2 {
		t.Fatalf("实时输出应限长,got %d rune", len([]rune(live)))
	}
	if n := strings.Count(live, "\n"); n > liveMaxLines {
		t.Fatalf("实时输出应只留尾部若干行,got %d 行", n)
	}
	if !strings.HasSuffix(strings.TrimRight(live, "\n"), "行") {
		t.Fatalf("应保留尾部,got %q", live[max(0, len(live)-10):])
	}
}
