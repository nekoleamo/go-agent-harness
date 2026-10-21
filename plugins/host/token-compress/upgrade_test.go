// 模型摘要升级通道的用例(2026-10-21 批次 D4)。
//
// 要验的三条纪律:
//  1. **折叠当场不调模型** —— 折叠只落抽取式摘要(确定性、零延迟);
//  2. **补写原子** —— 期间又折叠了 ⇒ 放弃补写(不回退到更旧的摘要);
//  3. **失败回退** —— 摘要器报错/不可用 ⇒ 保持抽取式摘要,回合不受影响。
package tokencompress

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-session-log"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeSummarizer 假摘要器(可指定返回/报错)。
type fakeSummarizer struct {
	text string
	err  error
	got  sdk.SummaryRequest
}

func (f *fakeSummarizer) Summarize(_ context.Context, in sdk.SummaryRequest) (string, error) {
	f.got = in
	if f.err != nil {
		return "", f.err
	}
	return f.text, nil
}

// buildUpgradeEnv 装配 host-session-log + token-compress(带摘要器),返回日志、runner 与 ctx。
func buildUpgradeEnv(t *testing.T, sum sdk.SessionSummarizer) (sdk.SessionLog, *upgradeRunner, sdk.Ctx) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&sessionlog.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if sum != nil {
		if err := c.Provide("ctx.sessionSummarizer", sum); err != nil {
			t.Fatal(err)
		}
	}
	runner, err := (&Plugin{}).start(c, &sdk.Manifest{Data: map[string]any{
		"token_budget_chars": 400,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runner.close)
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	return sessions, runner, c
}

// latestSummaryOf 取日志里最新一条摘要(文本 + 来源)。
func latestSummaryOf(l sdk.SessionLog) (string, string) {
	text, source := "", ""
	for _, ev := range l.Replay() {
		if ev.Kind != sdk.EventSummary {
			continue
		}
		text = sdk.SummaryText(ev.Payload)
		if rs, ok := ev.Payload.(sdk.RollingSummary); ok {
			source = rs.Source
		}
	}
	return text, source
}

// waitFor 轮询等待条件成立(上限 3s;不靠固定 sleep 猜时序)。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

// TestUpgradeReplacesExtractSummary 折叠 → 回合后补写 → 投影里的摘要变成模型摘要。
//
// **反向验证**:① 不触发补写(trigger 不跑)⇒ 本用例红;② 补写不原子(直接写,不看乐观锁)
// ⇒ TestUpgradeSkipsWhenSummaryChanged 红;③ 摘要器报错 ⇒ 摘要应仍是抽取式(见下一条)。
func TestUpgradeReplacesExtractSummary(t *testing.T) {
	sum := &fakeSummarizer{text: "任务: 改登录\n改动: auth.go"}
	l, _, c := buildUpgradeEnv(t, sum)
	for i := 1; i <= 12; i++ {
		appendTurn(l, i)
		l.DeriveMessages()
	}
	// 折叠当场:摘要已落盘,但仍是**抽取式**(没调模型)
	text, source := latestSummaryOf(l)
	if text == "" {
		t.Fatal("应有摘要")
	}
	if source != sdk.SummarySourceExtract {
		t.Fatalf("折叠当场应只落抽取式摘要,来源=%q", source)
	}
	if sum.got.Prev != "" || sum.got.Seg != "" {
		t.Fatal("折叠当场不应调模型")
	}
	// 回合结束(经会话事件通道)→ 触发补写
	if err := l.Append(sdk.SessionEvent{Kind: sdk.EventTurnEnd, Payload: "done"}); err != nil {
		t.Fatal(err)
	}
	_ = c
	waitFor(t, "模型摘要补写", func() bool {
		_, src := latestSummaryOf(l)
		return src == sdk.SummarySourceLLM
	})
	text, source = latestSummaryOf(l)
	if source != sdk.SummarySourceLLM {
		t.Fatalf("补写后来源应为 llm: %q", source)
	}
	if !strings.Contains(text, "改登录") {
		t.Fatalf("补写后应是模型摘要: %q", text)
	}
	// 投影里看到的也是模型摘要
	var proj string
	for _, m := range l.DeriveMessages() {
		if m.Role == sdk.RoleSystem && strings.Contains(m.Content, "滚动摘要") {
			proj = m.Content
		}
	}
	if !strings.Contains(proj, "改登录") {
		t.Fatalf("投影应含模型摘要: %q", proj)
	}
	// 摘要器拿到的输入:旧摘要 + 新片段(原始内容,不是抽取式截断)
	if sum.got.Seg == "" {
		t.Fatal("摘要器应收到新片段")
	}
	if !strings.Contains(sum.got.Seg, "用户") {
		t.Fatalf("新片段应是原文转述: %q", sum.got.Seg)
	}
}

// TestUpgradeFallsBackWhenSummarizerFails 摘要器报错 ⇒ 保持抽取式摘要(确定性回退)。
func TestUpgradeFallsBackWhenSummarizerFails(t *testing.T) {
	sum := &fakeSummarizer{err: errors.New("模型不可用")}
	l, runner, _ := buildUpgradeEnv(t, sum)
	for i := 1; i <= 12; i++ {
		appendTurn(l, i)
		l.DeriveMessages()
	}
	before, _ := latestSummaryOf(l)
	runner.trigger()
	// 跑过一次(等摘要器被调用过)
	waitFor(t, "摘要器被调用", func() bool { return sum.got.Seg != "" })
	time.Sleep(50 * time.Millisecond) // 给"写坏摘要"留出机会
	after, source := latestSummaryOf(l)
	if source != sdk.SummarySourceExtract {
		t.Fatalf("失败时应保持抽取式摘要,来源=%q", source)
	}
	if after != before {
		t.Fatalf("失败时摘要不应被改写:\nbefore=%q\nafter=%q", before, after)
	}
}

// TestUpgradeDisabledByConfig data.llm_summary=false ⇒ 整条通道不工作。
func TestUpgradeDisabledByConfig(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&sessionlog.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	sum := &fakeSummarizer{text: "不该被调用"}
	if err := c.Provide("ctx.sessionSummarizer", sum); err != nil {
		t.Fatal(err)
	}
	runner, err := (&Plugin{}).start(c, &sdk.Manifest{Data: map[string]any{
		"token_budget_chars": 400,
		"llm_summary":        false,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.close()
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 12; i++ {
		appendTurn(sessions, i)
		sessions.DeriveMessages()
	}
	runner.trigger()
	time.Sleep(100 * time.Millisecond)
	if sum.got.Seg != "" {
		t.Fatal("关闭后不应调模型")
	}
	_, source := latestSummaryOf(sessions)
	if source != sdk.SummarySourceExtract {
		t.Fatalf("关闭时应只有抽取式摘要,来源=%q", source)
	}
}

// TestEngineReportsFoldScope 折叠时把范围交给 onFold(旧摘要 + 新片段 + 合并结果)。
func TestEngineReportsFoldScope(t *testing.T) {
	var scopes []FoldScope
	e := &Engine{onFold: func(s FoldScope) { scopes = append(scopes, s) }}
	var evs []sdk.SessionEvent
	for i := 1; i <= 6; i++ {
		evs = append(evs,
			sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "用户问题 " + strings.Repeat("行", 40)}},
			sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "助手回答 " + strings.Repeat("果", 40)}},
			sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "c" + itoa(i), Name: "shell", Content: "输出 " + strings.Repeat("o", 30)}})
	}
	e.Fold(evs, -1, 300, func(sdk.RollingSummary) {})
	if len(scopes) == 0 {
		t.Fatal("折叠应触发 onFold")
	}
	last := scopes[len(scopes)-1]
	if last.Prev != "" {
		t.Fatalf("首次折叠前无旧摘要: %q", last.Prev)
	}
	if !strings.Contains(last.Seg, "用户问题") {
		t.Fatalf("新片段应是原文: %q", last.Seg)
	}
	if !strings.Contains(last.Merged, "用户") {
		t.Fatalf("合并结果应是抽取式摘要: %q", last.Merged)
	}
}
