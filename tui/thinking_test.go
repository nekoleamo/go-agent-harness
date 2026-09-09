// B1 思维块测试:thinking 增量累积为独立行;折叠态渲染截断+展开提示;展开全文不丢;
// 正文增量与思维互斥不混行。
package tui

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// thinkEv thinking 增量事件。
func thinkEv(delta string) sdk.SessionEvent {
	return sdk.SessionEvent{Kind: sdk.EventAssistantChunk, Payload: sdk.LLMStreamEvent{Thinking: delta}}
}

// textEv 正文增量事件。
func textEv(delta string) sdk.SessionEvent {
	return sdk.SessionEvent{Kind: sdk.EventAssistantChunk, Payload: sdk.LLMStreamEvent{Delta: delta}}
}

func TestThinkingAppendSeparate(t *testing.T) {
	s := &State{}
	ev0 := thinkEv("第一条思考")
	s.ApplySessionEvent(&ev0)
	ev := thinkEv("续写")
	s.ApplySessionEvent(&ev)
	ev2 := textEv("正文回复")
	s.ApplySessionEvent(&ev2)
	n := len(s.Lines)
	if n != 2 {
		t.Fatalf("行数应 2(thinking+assistant),got %d", n)
	}
	if s.Lines[0].Kind != "thinking" || s.Lines[0].Text != "第一条思考续写" {
		t.Fatalf("thinking 行累积: %+v", s.Lines[0])
	}
	if s.Lines[1].Kind != "assistant" || s.Lines[1].Text != "正文回复" {
		t.Fatalf("正文应独立 assistant 行: %+v", s.Lines[1])
	}
}

func TestThinkingFoldRender(t *testing.T) {
	long := strings.Repeat("思考片段。", 30) // 150+ rune,超折叠阈值
	s := &State{}
	s.Lines = []Line{{Kind: "thinking", Text: long}}
	rows := flattenViewLines(s, 100)
	folded := renderSessionRow(rows[0], s, 0)
	if !strings.Contains(stripANSI(folded), "…(Ctrl+T 展开)") {
		t.Fatalf("折叠态应含展开提示: %q", stripANSI(folded))
	}
	if len([]rune(stripANSI(folded))) >= 140 {
		t.Fatalf("折叠态应截断: %q", stripANSI(folded))
	}
	if s.Lines[0].Text != long {
		t.Fatalf("折叠不应修改原始数据")
	}
	s.ThinkingFull = true
	rowsFull := flattenViewLines(s, 100)
	joined := ""
	for _, p := range rowsFull {
		joined += stripANSI(renderSessionRow(p, s, 0))
	}
	if joined != long {
		t.Fatalf("展开态应全文(折行后拼接): %q…", joined[:50])
	}
	s2 := &State{}
	s2.Lines = []Line{{Kind: "thinking", Text: "短思考"}}
	rows2 := flattenViewLines(s2, 100)
	if out := renderSessionRow(rows2[0], s2, 0); !strings.Contains(stripANSI(out), "短思考") {
		t.Fatalf("短思维完整显示: %q", stripANSI(out))
	}
}

func TestThinkingStyle(t *testing.T) {
	s := &State{}
	s.Lines = []Line{{Kind: "thinking", Text: "思考"}}
	rows := flattenViewLines(s, 100)
	out := renderSessionRow(rows[0], s, 0)
	if !strings.Contains(out, "38;5;242") || !strings.Contains(out, "3;") {
		t.Fatalf("thinking 行应灰斜体: %q", out)
	}
}
