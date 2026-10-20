package tui

// 运行中实时耗时(C2a)的用例。
//
// 要钉的是:
//  1. **正在跑**的调用行显示已耗时(整数秒,一秒一跳);
//  2. 结果到达 ⇒ 收尾(耗时刻意交给结果行,不在调用行上留一份);
//  3. **重放**里的历史调用**不显示**耗时(它早跑完了 —— 挂个一直往上跳的数字是骗人);
//  4. 回合结束 ⇒ 没有工具在跑(悬着的调用不再计时)。

import (
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// toolResultEv 造一条 tool/result 事件。
func toolResultEv(id, name string) *sdk.SessionEvent {
	return &sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: id, Name: name, Content: "{}"}}
}

// renderedText 取会话流展平后的全部文本。
func renderedText(s *State) string {
	rows := flattenViewLines(s, 100)
	var b strings.Builder
	for _, p := range rows {
		b.WriteString(p.text)
		b.WriteString("\n")
	}
	return b.String()
}

// TestRunningToolShowsElapsed 运行中的调用行显示已耗时。
func TestRunningToolShowsElapsed(t *testing.T) {
	s := &State{}
	s.ApplySessionEvent(toolCallEv("call-1", "shell"))
	s.Lines[0].Since = time.Now().Add(-12 * time.Second)
	out := renderedText(s)
	if !strings.Contains(out, "(12s)") {
		t.Fatalf("运行中的调用行应显示已耗时,got %q", out)
	}
}

// TestRunningToolShowsStillRunningHint 超过阈值补一句「仍在运行」(分得清"在跑"与"卡住")。
func TestRunningToolShowsStillRunningHint(t *testing.T) {
	s := &State{}
	s.ApplySessionEvent(toolCallEv("call-1", "shell"))
	s.Lines[0].Since = time.Now().Add(-45 * time.Second)
	out := renderedText(s)
	if !strings.Contains(out, "仍在运行") {
		t.Fatalf("超时应提示仍在运行,got %q", out)
	}
	// 未到阈值不提示
	s2 := &State{}
	s2.ApplySessionEvent(toolCallEv("call-1", "shell"))
	s2.Lines[0].Since = time.Now().Add(-3 * time.Second)
	if out2 := renderedText(s2); strings.Contains(out2, "仍在运行") {
		t.Fatalf("未到阈值不该提示,got %q", out2)
	}
}

// TestToolResultStopsElapsed 结果到达 ⇒ 调用行不再计时(耗时刻意给结果行)。
func TestToolResultStopsElapsed(t *testing.T) {
	s := &State{}
	s.ApplySessionEvent(toolCallEv("call-1", "shell"))
	s.Lines[0].Since = time.Now().Add(-12 * time.Second)
	s.ApplySessionEvent(toolResultEv("call-1", "shell"))
	out := renderedText(s)
	if strings.Contains(out, "(12s)") {
		t.Fatalf("结果到达后调用行不该继续计时,got %q", out)
	}
}

// TestReplayToolCallHasNoElapsed 重放里的历史调用不显示耗时。
func TestReplayToolCallHasNoElapsed(t *testing.T) {
	s := &State{}
	s.ApplyReplay(toolCallEv("call-1", "shell"))
	out := renderedText(s)
	if strings.Contains(out, "(0s)") || strings.Contains(out, "s)") {
		t.Fatalf("重放里的历史调用不该显示实时耗时,got %q", out)
	}
}

// TestTurnEndStopsElapsed 回合结束 ⇒ 没有工具在跑(悬着的调用不再计时)。
func TestTurnEndStopsElapsed(t *testing.T) {
	s := &State{}
	s.ApplySessionEvent(toolCallEv("call-1", "shell"))
	s.Lines[0].Since = time.Now().Add(-12 * time.Second)
	s.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventTurnEnd})
	out := renderedText(s)
	if strings.Contains(out, "(12s)") {
		t.Fatalf("回合结束后不该继续计时,got %q", out)
	}
}
