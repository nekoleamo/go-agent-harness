// token-compress 测试:引擎折叠单测 + 与 host-session-log 装配的集成验证。
package tokencompress

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host-session-log"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// appendTurn 一轮会话:用户问 + 助手答 + 工具结果(各约 40-50 字)。
func appendTurn(l sdk.SessionLog, round int) {
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

// TestEngineFold 引擎单测:直接调用 Fold,验证水位推进与摘要落盘。
func TestEngineFold(t *testing.T) {
	var evs []sdk.SessionEvent
	for i := 1; i <= 6; i++ {
		evs = append(evs,
			sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "用户问题 " + strings.Repeat("行", 40)}},
			sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "助手回答 " + strings.Repeat("果", 40)}},
			sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "c", Name: "shell", Content: "输出" + strings.Repeat("o", 30)}})
	}
	var summaries []string
	e := &Engine{}
	w := e.Fold(evs, -1, 300, func(s string) { summaries = append(summaries, s) })
	if w < 3 {
		t.Fatalf("应折叠至少一轮: 水位 %d", w)
	}
	if len(summaries) == 0 {
		t.Fatal("应有摘要回调")
	}
	last := summaries[len(summaries)-1]
	if !strings.Contains(last, "用户问题") {
		t.Fatalf("摘要应含用户痕迹: %q", last)
	}
	if len([]rune(last)) > 300/2+30 {
		t.Fatalf("摘要应限长: %d", len([]rune(last)))
	}
	// 最新用户轮不得被折叠(保留最新轮)
	lastUser := 15
	if w >= lastUser {
		t.Fatalf("水位不得越过最后一个用户轮(%d): %d", lastUser, w)
	}
	// 无事件可折叠时返回原水位
	w2 := e.Fold(nil, -1, 100, func(string) {})
	if w2 != -1 {
		t.Fatalf("空流应返回原水位: %d", w2)
	}
}

// buildEnv 装配 host-session-log + token-compress(真实注册路径)。
func buildEnv(t *testing.T, budget int) sdk.SessionLog {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&sessionlog.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{
		"token_budget_chars": budget,
	}}); err != nil {
		t.Fatal(err)
	}
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	return sessions
}

// TestRollingSummary 长会话集成:投影受限并出现滚动摘要;完整日志留盘。
func TestRollingSummary(t *testing.T) {
	l := buildEnv(t, 400)
	for i := 1; i <= 12; i++ {
		appendTurn(l, i)
		l.DeriveMessages() // 触发压缩检查(agent-loop 每步调用)
	}
	msgs := l.DeriveMessages()
	var total int
	for _, m := range msgs {
		total += len([]rune(m.Content))
	}
	if total > 400*4 {
		t.Fatalf("投影字符应受限: %d", total)
	}
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
	l := buildEnv(t, 300)
	for i := 1; i <= 8; i++ {
		appendTurn(l, i)
		l.DeriveMessages()
	}
	msgs := l.DeriveMessages()
	last := msgs[len(msgs)-1]
	if last.Role != sdk.RoleTool || last.ToolCallID != "c8" {
		t.Fatalf("最新块应保留(末尾为第 8 轮工具结果): %+v", last)
	}
	var summary string
	for _, m := range msgs {
		if m.Role == sdk.RoleSystem {
			summary = m.Content
		}
	}
	if summary == "" || !strings.Contains(summary, "用户问题 第") {
		t.Fatalf("摘要应含早期轮次痕迹: %q", summary)
	}
	if len([]rune(summary)) > 300/2+30 {
		t.Fatalf("摘要应限长: %d", len([]rune(summary)))
	}
}

// TestNoSummaryWhenBudgetOff 预算关闭(budget=0):不压缩,无摘要,全量投影。
func TestNoSummaryWhenBudgetOff(t *testing.T) {
	l := buildEnv(t, 0)
	for i := 1; i <= 5; i++ {
		appendTurn(l, i)
	}
	msgs := l.DeriveMessages()
	for _, m := range msgs {
		if m.Role == sdk.RoleSystem {
			t.Fatal("预算关闭不应出现摘要")
		}
	}
	if len(msgs) != 15 { // 5 轮 × 3 条
		t.Fatalf("预算关闭应全量投影: %d", len(msgs))
	}
	for _, ev := range l.Replay() {
		if ev.Kind == sdk.EventSummary {
			t.Fatal("预算关闭不应有摘要事件")
		}
	}
}
