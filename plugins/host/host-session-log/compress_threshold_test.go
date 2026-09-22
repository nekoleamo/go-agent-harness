// compress_threshold_test.go:阈值口径 + 长单轮截断 + 水位持久化的宿主侧单测。
// 策略在 token-compress(见其 planner_test.go),这里验"宿主按策略执行"与"投影不动日志"。
package sessionlog

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubPlanner 策略型压缩器:Plan 返回预设决定(并记录入参),Fold 一次折到最后一个用户轮前。
type stubPlanner struct {
	calls     int
	gotBudget int
	lastInput sdk.CompressInput
	decision  sdk.CompressDecision
}

func (s *stubPlanner) Plan(in sdk.CompressInput) sdk.CompressDecision {
	s.lastInput = in
	return s.decision
}

func (s *stubPlanner) Fold(evs []sdk.SessionEvent, watermark, budget int, summary func(string)) int {
	s.calls++
	s.gotBudget = budget
	start := watermark + 1
	end := lastUserIndexStub(evs) - 1
	if end < start {
		return watermark
	}
	summary("stub:折叠")
	return end
}

// TestPlannerDecisionDrivesBudget 策略给出的预算生效(哪怕注册的固定预算很大),
// 且喂给策略的观测事实正确(实测 prompt token / 窗口 / 当前投影字符)。
func TestPlannerDecisionDrivesBudget(t *testing.T) {
	l := newTestLog(t, "")
	for i := 1; i <= 6; i++ {
		appendTurn(l, i)
	}
	// 实测用量(agent-loop 每轮记录的那条)+ 窗口快照(经 usage/window 事件下发)
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventUsage, Payload: sdk.UsageEvent{
		Model: "m", Usage: sdk.Usage{PromptTokens: 90000}}})
	l.window = 128000

	stub := &stubPlanner{decision: sdk.CompressDecision{BudgetChars: 200}}
	l.RegisterCompressor(1000000, stub) // 固定预算充足:折叠必须来自策略
	msgs := l.DeriveMessages()

	if stub.calls == 0 {
		t.Fatalf("策略预算 200 应触发折叠(固定预算 1000000 不该掩盖它)")
	}
	if stub.gotBudget != 200 {
		t.Fatalf("Fold 应收到策略预算: %d", stub.gotBudget)
	}
	if stub.lastInput.LastPromptTokens != 90000 || stub.lastInput.Window != 128000 {
		t.Fatalf("观测事实不对: %+v", stub.lastInput)
	}
	if stub.lastInput.ProjectChars <= 0 || len(msgs) == 0 {
		t.Fatalf("应有投影字符数与投影结果: %+v", stub.lastInput)
	}
	if stub.lastInput.LastProjectChars != stub.lastInput.ProjectChars {
		// 首次调用(本进程还没派生过投影,不知道上次请求发了多少)⇒ 不估算增长:两者相等
		t.Fatalf("首次调用应不估算增长: %+v", stub.lastInput)
	}
}

// TestPlannerTrimClipsOldToolResults 长单轮:折叠够不着(水位不越最后一个用户轮)时,
// 从最旧工具结果起截断;最近一条工具结果与日志原文都不动。
func TestPlannerTrimClipsOldToolResults(t *testing.T) {
	l := newTestLog(t, "")
	huge1 := "头1" + strings.Repeat("一", 20000) + "尾1"
	huge2 := "头2" + strings.Repeat("二", 20000) + "尾2"
	small := "刚拿到的小输出"
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "长轮开始"}})
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "开始干活"}})
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "c1", Name: "shell", Content: huge1}})
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "c2", Name: "shell", Content: huge2}})
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "c3", Name: "shell", Content: small}})

	stub := &stubPlanner{decision: sdk.CompressDecision{BudgetChars: 10000000, TrimChars: 4000}}
	l.RegisterCompressor(10000000, stub)
	msgs := l.DeriveMessages()

	if stub.calls != 0 {
		t.Fatal("预算充足不应折叠")
	}
	if got := approxChars(msgs); got > 4000 {
		t.Fatalf("投影应被截到阈值内: %d", got)
	}
	var tools []sdk.LLMMessage
	for _, m := range msgs {
		if m.Role == sdk.RoleTool {
			tools = append(tools, m)
		}
	}
	if len(tools) != 3 {
		t.Fatalf("工具消息不得丢弃(tool_call 必须成对): %d", len(tools))
	}
	for i := 0; i < 2; i++ {
		if !strings.Contains(tools[i].Content, "[截断") {
			t.Fatalf("第 %d 条旧工具结果应被截断: %q", i+1, tools[i].Content)
		}
		// 头部保留:开头仍是原文前 clipHeadChars 个字符
		head := string([]rune([]string{huge1, huge2}[i])[:clipHeadChars])
		if !strings.HasPrefix(tools[i].Content, head) {
			t.Fatalf("第 %d 条应保留头部 %d 字符", i+1, clipHeadChars)
		}
	}
	if tools[2].Content != small {
		t.Fatalf("最近一条工具结果不该被截: %q", tools[2].Content)
	}
	// 日志原文一字不动(截断只发生在投影层)
	full := ""
	for _, ev := range l.Replay() {
		if p, ok := ev.Payload.(sdk.ToolResultEvent); ok && p.CallID == "c1" {
			full = p.Content
		}
	}
	if full != huge1 {
		t.Fatalf("日志应保留完整工具结果: %d", len(full))
	}
}

// TestLoadRestoresWatermarkFromSummary Load 后水位从最后一个摘要事件恢复:
// 投影 = 摘要 + 其后事件,不再出现"摘要 + 已被摘要覆盖的原文"重复。
func TestLoadRestoresWatermarkFromSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	l := newTestLog(t, path)
	appendTurn(l, 1)
	appendTurn(l, 2)
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventSummary, Payload: "摘甲"})
	appendTurn(l, 3)
	appendTurn(l, 4)
	l.Flush()
	l.Close()

	l2 := newTestLog(t, "")
	if err := l2.Load(path); err != nil {
		t.Fatal(err)
	}
	msgs := l2.DeriveMessages()
	if len(msgs) == 0 || msgs[0].Role != sdk.RoleSystem || !strings.Contains(msgs[0].Content, "摘甲") {
		t.Fatalf("摘要应置顶: %+v", msgs)
	}
	for _, m := range msgs[1:] {
		if strings.Contains(m.Content, "第1") || strings.Contains(m.Content, "第2") {
			t.Fatalf("摘要覆盖过的原文不该再进投影: %q", m.Content)
		}
	}
	if last := msgs[len(msgs)-1]; last.Role != sdk.RoleTool || last.ToolCallID != "c4" {
		t.Fatalf("水位后的最新轮应完整保留: %+v", last)
	}
}

// TestUsageWindowEventSetsWindow 窗口快照经 usage/window 事件进入会话日志
// (压缩阈值按窗口比例派生,而窗口只在 host-usage-stats 手里)。
func TestUsageWindowEventSetsWindow(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var sn sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sn); err != nil {
		t.Fatal(err)
	}
	lg, ok := sn.(*Log)
	if !ok {
		t.Fatalf("ctx.sessions 应是 *Log: %T", sn)
	}
	if _, err := c.Emit(context.Background(), sdk.EventUsageWindow, 12345, sdk.Emit); err != nil {
		t.Fatal(err)
	}
	if lg.window != 12345 {
		t.Fatalf("窗口快照未落地: %d", lg.window)
	}
}
