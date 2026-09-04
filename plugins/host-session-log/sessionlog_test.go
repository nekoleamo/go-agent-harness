// host-session-log 测试:投影/持久化/历史注入 + 压缩器注入整合(token 压缩算法本身在 token-compress)。
package sessionlog

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubCompressor 测试压缩器:一次把水位后所有事件折叠为一条摘要(最简单折叠)。
// 用于验证 host-session-log 的注入整合(水位推进/摘要置顶/完整日志留盘)。
type stubCompressor struct {
	calls int
}

func (s *stubCompressor) Fold(evs []sdk.SessionEvent, watermark int, budget int, summary func(string)) int {
	s.calls++
	// 折叠水位后事件至最后一个用户轮之前(保留最新轮完整)
	start := watermark + 1
	end := lastUserIndexStub(evs) - 1
	if end < start {
		return watermark
	}
	var b strings.Builder
	for i := start; i <= end; i++ {
		switch p := evs[i].Payload.(type) {
		case sdk.UserMessage:
			b.WriteString("用户: " + p.Content[:min(20, len(p.Content))] + ";")
		case sdk.AssistantMessage:
			b.WriteString("助手;")
		case sdk.ToolResultEvent:
			b.WriteString("工具;")
		}
	}
	summary("stub:" + b.String())
	return end
}

func lastUserIndexStub(evs []sdk.SessionEvent) int {
	last := -1
	for i, ev := range evs {
		if ev.Kind == sdk.EventUserMessage {
			last = i
		}
	}
	return last
}

// appendTurn 一轮会话:用户问 + 助手答 + 工具结果。
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

// TestFullProjectionNoCompressor 未注册压缩器:全量投影,行为与旧版一致。
func TestFullProjectionNoCompressor(t *testing.T) {
	l := newLog("")
	for i := 1; i <= 5; i++ {
		appendTurn(l, i)
	}
	msgs := l.DeriveMessages()
	if len(msgs) != 15 { // 5 轮 × 3 条
		t.Fatalf("应全量投影: %d", len(msgs))
	}
}

// TestCompressorInjectIntegrate 压缩器注入整合:超预算触发 Fold,摘要置顶、压缩块跳过、完整日志留盘。
func TestCompressorInjectIntegrate(t *testing.T) {
	l := newLog("")
	for i := 1; i <= 6; i++ {
		appendTurn(l, i)
	}
	stub := &stubCompressor{}
	l.RegisterCompressor(200, stub)
	msgs := l.DeriveMessages()
	if stub.calls == 0 {
		t.Fatal("超预算应触发 Fold")
	}
	// 摘要置顶(system)
	if len(msgs) == 0 || msgs[0].Role != sdk.RoleSystem || !strings.Contains(msgs[0].Content, "stub:") {
		t.Fatalf("摘要应置顶: %+v", msgs)
	}
	// 最新轮保留(末尾为第 6 轮工具结果)
	last := msgs[len(msgs)-1]
	if last.Role != sdk.RoleTool || last.ToolCallID != "c6" {
		t.Fatalf("最新轮应保留: %+v", msgs)
	}
	// 完整日志留盘:全部用户事件 + 摘要事件
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
	if userCount != 6 {
		t.Fatalf("完整日志应保留 6 轮用户事件: %d", userCount)
	}
	if summaryCount == 0 {
		t.Fatal("应有摘要事件落盘")
	}
}

// TestCompressorWithinBudget 预算充足:不触发折叠,无摘要。
func TestCompressorWithinBudget(t *testing.T) {
	l := newLog("")
	for i := 1; i <= 3; i++ {
		appendTurn(l, i)
	}
	stub := &stubCompressor{}
	l.RegisterCompressor(1000000, stub)
	msgs := l.DeriveMessages()
	if stub.calls != 0 {
		t.Fatal("预算充足不应触发 Fold")
	}
	for _, m := range msgs {
		if m.Role == sdk.RoleSystem {
			t.Fatal("预算充足不应出现摘要")
		}
	}
}

// TestHistoryInjection 历史注入:-1 禁止 / N 最近 N 条消息(与压缩器无关的基础语义)。
func TestHistoryInjection(t *testing.T) {
	l := newLog("")
	for i := 1; i <= 3; i++ {
		appendTurn(l, i)
	}
	l.SetHistory(2)
	msgs := l.DeriveMessages()
	if len(msgs) != 2 { // 最近 2 条消息
		t.Fatalf("应保留最近 2 条消息: %d", len(msgs))
	}
	l.SetHistory(-1)
	if msgs := l.DeriveMessages(); msgs != nil {
		t.Fatalf("-1 应禁止注入: %+v", msgs)
	}
}

// TestSummaryEventProjection 摘要事件消费:水位内摘要恒置顶(令牌压缩后多次投影稳定)。
func TestSummaryEventProjection(t *testing.T) {
	l := newLog("")
	for i := 1; i <= 4; i++ {
		appendTurn(l, i)
	}
	stub := &stubCompressor{}
	l.RegisterCompressor(150, stub)
	first := l.DeriveMessages()
	second := l.DeriveMessages()
	// 已折叠完成,再次投影不应重复折叠(水位推进后预算内)
	if stub.calls > 2 {
		t.Fatalf("重复投影不应反复折叠: %d 次", stub.calls)
	}
	if len(first) != len(second) {
		t.Fatalf("折叠完成后投影应稳定: 前 %d 后 %d", len(first), len(second))
	}
	if first[0].Role != sdk.RoleSystem || second[0].Role != sdk.RoleSystem {
		t.Fatalf("摘要应持续置顶: %+v / %+v", first, second)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
