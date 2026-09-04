package sessionlog

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// mockLog 构造带预算的日志。
func mockLog(budget int) *Log {
	l := newLog("")
	l.budget = budget
	return l
}

// appendTurn 一轮会话:用户问 + 助手答 + 工具结果(各约 40-50 字)。
func appendTurn(l *Log, round int) {
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{
		Content: "用户问题 第" + strings.Repeat("行", 40) + itoa(round)}})
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{
		Content: "助手回答 第" + strings.Repeat("果", 40) + itoa(round)}})
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{
		CallID: "c" + itoa(round), Name: "shell", Content: "输出" + strings.Repeat("o", 30)}})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestRollingSummary 长会话:投影受限并出现滚动摘要;完整日志留盘(Replay 原样)。
func TestRollingSummary(t *testing.T) {
	l := mockLog(400)
	for i := 1; i <= 12; i++ {
		appendTurn(l, i)
		l.DeriveMessages() // 触发压缩检查(agent-loop 每步调用)
	}
	msgs := l.DeriveMessages()
	// 投影字符受限(单块边界允许少量超出)
	var total int
	for _, m := range msgs {
		total += len([]rune(m.Content))
	}
	if total > 400*4 {
		t.Fatalf("投影字符应受限: %d", total)
	}
	// 摘要出现:system 角色,含对话痕迹
	var sawSummary bool
	for _, m := range msgs {
		if m.Role == sdk.RoleSystem && strings.Contains(m.Content, "用户") && strings.Contains(m.Content, "滚动摘要") {
			sawSummary = true
			break
		}
	}
	if !sawSummary {
		t.Fatalf("应有滚动摘要(system 消息): %+v", msgs)
	}
	// 完整日志留盘:Replay 含全部用户事件 + 摘要事件
	events := l.Replay()
	var userCount, summaryCount int
	for _, ev := range events {
		switch ev.Kind {
		case sdk.EventUserMessage:
			userCount++
		case sdk.EventSummary:
			summaryCount++
		}
	}
	if userCount != 12 {
		t.Fatalf("完整日志应保留全部 12 轮用户事件: %d", userCount)
	}
	if summaryCount == 0 {
		t.Fatal("应有摘要事件落盘")
	}
}

// TestSummaryRollsForward 滚动:摘要保留早期轮次痕迹,最新块完整投影。
func TestSummaryRollsForward(t *testing.T) {
	l := mockLog(300)
	for i := 1; i <= 8; i++ {
		appendTurn(l, i)
		l.DeriveMessages()
	}
	msgs := l.DeriveMessages()
	// 最新块保留(末尾为第 8 轮工具结果)
	last := msgs[len(msgs)-1]
	if last.Role != sdk.RoleTool || last.ToolCallID != "c8" {
		t.Fatalf("最新块应保留(末尾为第 8 轮工具结果): %+v", last)
	}
	// 摘要含早期轮次痕迹(第 1 轮用户问题)
	var summary string
	for _, m := range msgs {
		if m.Role == sdk.RoleSystem {
			summary = m.Content
		}
	}
	if summary == "" || !strings.Contains(summary, "用户问题 第") {
		t.Fatalf("摘要应含早期轮次痕迹: %q", summary)
	}
	// 累计摘要限长(防自身膨胀)
	if len([]rune(summary)) > 300/2+30 {
		t.Fatalf("摘要应限长: %d", len([]rune(summary)))
	}
}

// TestNoSummaryWhenWithinBudget 预算充足:不压缩,无摘要事件。
func TestNoSummaryWhenWithinBudget(t *testing.T) {
	l := mockLog(1000000)
	for i := 1; i <= 3; i++ {
		appendTurn(l, i)
	}
	msgs := l.DeriveMessages()
	for _, m := range msgs {
		if m.Role == sdk.RoleSystem {
			t.Fatal("预算充足不应出现摘要")
		}
	}
	for _, ev := range l.Replay() {
		if ev.Kind == sdk.EventSummary {
			t.Fatal("预算充足不应有摘要事件")
		}
	}
}

// TestBudgetOffByDefault 默认(budget=0):不压缩,行为与旧版一致。
func TestBudgetOffByDefault(t *testing.T) {
	l := mockLog(0)
	for i := 1; i <= 5; i++ {
		appendTurn(l, i)
	}
	msgs := l.DeriveMessages()
	if len(msgs) != 15 { // 5 轮 × 3 条
		t.Fatalf("默认应全量投影: %d", len(msgs))
	}
}
