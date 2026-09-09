// P5.2 A 组测试:工具耗时显示(结果行挂耗时段,重放毫秒级自然抑制)+ 会话列表内容预览。
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestToolDurationInResult A1:ToolCall 计时 → Result 结算,>=100ms 挂结果行尾。
func TestToolDurationInResult(t *testing.T) {
	apply := func(ev sdk.SessionEvent) *State {
		s := &State{}
		s.ApplySessionEvent(&ev)
		return s
	}
	// 真实调用 → 结果:耗时 >=100ms(工具执行自然间隔,测试直接回放两事件)
	s := apply(sdk.SessionEvent{Kind: sdk.EventToolCall, Payload: sdk.ToolCallEvent{ID: "1", Name: "bash", Arguments: "{}"}})
	s.toolStart = time.Now().Add(-1250 * time.Millisecond) // 模拟运行 1.25s
	s.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "1", Name: "bash", Content: "ok\n"}})
	last := s.Lines[len(s.Lines)-1]
	if !strings.Contains(last.Text, "(1.") || !strings.Contains(last.Text, "s)") {
		t.Fatalf("结果行应含耗时 (1.x s): %q", last.Text)
	}
	if !strings.HasPrefix(last.Text, "✓ bash:") {
		t.Fatalf("结果行格式保持: %q", last.Text)
	}
	// 失败结果也显示耗时
	s2 := apply(sdk.SessionEvent{Kind: sdk.EventToolCall, Payload: sdk.ToolCallEvent{ID: "2", Name: "read", Arguments: "{}"}})
	s2.toolStart = time.Now().Add(-700 * time.Millisecond)
	s2.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "2", Name: "read", Content: "", Error: "缺失"}})
	if !strings.Contains(s2.Lines[len(s2.Lines)-1].Text, "(0.7s)") {
		t.Fatalf("失败结果行也应含耗时: %q", s2.Lines[len(s2.Lines)-1].Text)
	}
	// 几乎同时到达(重放毫秒级):<100ms 不显示
	s3 := apply(sdk.SessionEvent{Kind: sdk.EventToolCall, Payload: sdk.ToolCallEvent{ID: "3", Name: "read", Arguments: "{}"}})
	s3.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "3", Name: "read", Content: "ok"}})
	if strings.Contains(s3.Lines[len(s3.Lines)-1].Text, "(") {
		t.Fatalf("重放毫秒级不应挂耗时: %q", s3.Lines[len(s3.Lines)-1].Text)
	}
	// 无前置 ToolCall(异常路径):toolStart 零值 → 不显示虚构耗时
	s4 := &State{}
	s4.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "4", Name: "read", Content: "ok"}})
	if strings.Contains(s4.Lines[len(s4.Lines)-1].Text, "(") {
		t.Fatalf("无 ToolCall 起点不应显示耗时: %q", s4.Lines[len(s4.Lines)-1].Text)
	}
}

// TestSessionDescPreview A2:会话描述含首条用户消息预览(20 字截断);无预览/主会话不误加。
func TestSessionDescPreview(t *testing.T) {
	// 有预览:追加 "─ 预览"(截 20 rune)
	desc := sessionDesc(sdk.SessionInfo{ID: "abc", Preview: "这是一个比较长的用户消息预览内容用于截断验证"})
	if !strings.Contains(desc, "─ 这是一个比较长的用户消息预览内容用于截") {
		t.Fatalf("desc 应含截断预览: %q", desc)
	}
	if len([]rune(desc)) > 60 {
		t.Fatalf("desc 过长: %q", desc)
	}
	// 无预览:不加预览段
	d2 := sessionDesc(sdk.SessionInfo{ID: "abc", MTime: 0})
	if strings.Contains(d2, "─") {
		t.Fatalf("无预览不应加段: %q", d2)
	}
	// 主会话有预览也展示
	d3 := sessionDesc(sdk.SessionInfo{Name: "", Preview: "主会话首条"})
	if !strings.Contains(d3, "主会话 ─ 主会话首条") {
		t.Fatalf("主会话也应带预览: %q", d3)
	}
}
