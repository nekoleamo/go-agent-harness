// 滚动摘要的**模型摘要升级**端到端(批次 D4 的链路,2026-10-23)。
//
// 为什么需要这一条(code review 登记过的测试缺口):D4 落地时只有**插件级**集成用例
// (假摘要器 + 假 SessionLog)和真机验收(验的是**抽取式**压缩本身)。中间那段
// 「真实回合 → 折叠 → 回合结束触发 → 模型摘要 → **投影里真的换成模型摘要**」
// 没有一条用例穿过 —— 而它恰好是最容易断的:
//   - 触发点接错事件(host-session-summary 就是这么死的:订阅了全仓从未 emit 的总线
//     turn/end)⇒ 摘要永远是抽取式,但**没有任何报错**;
//   - 乐观锁对不上 / 摘要器解析失败 ⇒ 同样静默停留在抽取式。
//
// 所以本用例的判据必须是「后续回合的模型上下文里**出现了模型写的**那句话」,
// 而不是"摘要器被调用过"。
package tests

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeSummaryLLM 同时扮演"回合模型"与"摘要模型"。
//
// 区分靠 sdk.LLMSummaryMarker(后台摘要请求的首行标记)—— 与真实链路同一判据,
// 不用"第几次调用"这种脆弱的序号约定。
type fakeSummaryLLM struct {
	mu        sync.Mutex
	turns     int
	summaries int
	lastSumIn string   // 最近一次摘要请求的 user 输入(断言"模型看到了原文")
	turnHist  []string // 每个回合请求拼起来的上下文(断言"模型摘要真的发出去了")
}

// Name 实现 sdk.LLMAdapter(适配器名,host-llm 路由用)。
func (f *fakeSummaryLLM) Name() string { return "fake-summary" }

func (f *fakeSummaryLLM) Complete(_ context.Context, req *sdk.LLMRequest, _ func(sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	sys := ""
	for _, m := range req.Messages {
		if m.Role == sdk.RoleSystem {
			sys = m.Content
			break
		}
	}
	if strings.Contains(sys, sdk.LLMSummaryMarker) {
		f.mu.Lock()
		f.summaries++
		for _, m := range req.Messages {
			if m.Role == sdk.RoleUser {
				f.lastSumIn = m.Content
			}
		}
		n := f.summaries
		f.mu.Unlock()
		// 只有模型写得出来的话:后面要断言它**进了投影**(抽取式摘要不可能产生这句)
		body := fmt.Sprintf(`{"task":"模型摘要甲第%d版","changes":["改了 token-compress"],"decisions":[],"open":[],"errors":[]}`, n)
		return &sdk.LLMResponse{Message: sdk.LLMMessage{Role: sdk.RoleAssistant, Content: body}}, nil
	}
	f.mu.Lock()
	f.turns++
	n := f.turns
	var ctxText strings.Builder
	for _, m := range req.Messages {
		ctxText.WriteString(m.Content)
		ctxText.WriteString("\n")
	}
	f.turnHist = append(f.turnHist, ctxText.String())
	f.mu.Unlock()
	// 回合答复写长一点:几轮下来撑爆 80 字符的预算,逼出折叠
	return &sdk.LLMResponse{Message: sdk.LLMMessage{Role: sdk.RoleAssistant,
		Content: fmt.Sprintf("第%d轮答复:%s", n, strings.Repeat("细节。", 30))}}, nil
}

// lastTurnContext 最近一个回合请求的完整上下文。
func (f *fakeSummaryLLM) lastTurnContext() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.turnHist) == 0 {
		return ""
	}
	return f.turnHist[len(f.turnHist)-1]
}

func (f *fakeSummaryLLM) counts() (turns, summaries int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.turns, f.summaries
}

// TestRollingSummaryUpgradeEndToEnd 真实回合 → 折叠 → 回合结束触发 → 模型摘要进投影。
//
// 三步判据:
//  1. 第一轮之后**必须**发生过折叠(投影里出现滚动摘要),否则后面的断言是空断言;
//  2. 回合结束**必须**触发一次摘要模型请求(异步,给它时间);
//  3. 之后的投影里**必须**出现模型摘要的原话(而不是只剩抽取式的逐条截断)。
//
// **反向验证**:把 token-compress 里 `runner.dis` 的订阅改回总线事件 sdk.EventTurnEnd
// (即"全仓从未 emit 过"的那个)⇒ 第 2、3 步必红,已实跑验证。
func TestRollingSummaryUpgradeEndToEnd(t *testing.T) {
	ll := &fakeSummaryLLM{}
	c := buildSummaryUpgradeEnv(t)
	var llmSvc sdk.LLMService
	if err := c.Inject("ctx.llm", &llmSvc); err != nil {
		t.Fatalf("ctx.llm 未装配: %v", err)
	}
	if dis := llmSvc.RegisterAdapter(ll); dis != nil {
		t.Cleanup(dis)
	}
	// host-llm 未配置模型名时**显式报错**("不静默降级")⇒ 这里显式设一个
	llmSvc.SetModel("fake-summary-model")

	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatalf("ctx.sessions 未装配: %v", err)
	}
	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}

	turnText := strings.Repeat("这一轮说了很多细节内容。", 20)
	// 第 1、2 轮:折叠**不得越过最后一个用户轮**,所以首轮内容要等第二轮才折进去
	for i := 1; i <= 2; i++ {
		if err := loop.Run(context.Background(), turnText); err != nil {
			t.Fatalf("第 %d 轮失败: %v", i, err)
		}
	}

	// ① 折叠必须真的发生(否则本用例什么也没测)
	projected := derivedText(sessions)
	if !strings.Contains(projected, "滚动摘要") {
		t.Fatalf("两轮后未发生折叠(预算 80 字符应已触发),投影尾部:%q", tailOf(projected, 200))
	}
	// 且摘要里得有**内容**(不是一条空摘要:水位后的 turn/start 标记块曾被误折)
	if strings.Contains(projected, "滚动摘要:\n\n") || strings.HasSuffix(strings.TrimSpace(projected), "滚动摘要:") {
		t.Fatalf("摘要为空(标记块被当成了可折内容):%q", tailOf(projected, 300))
	}

	// ② 回合结束 → 摘要模型请求(异步:轮询到底,不靠固定 sleep)
	deadline := time.Now().Add(10 * time.Second)
	var summaries int
	for time.Now().Before(deadline) {
		_, summaries = ll.counts()
		if summaries > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if summaries == 0 {
		t.Fatal("回合结束后未触发摘要模型请求(turn/end 触发点没接上)")
	}

	// ③ 模型摘要必须真的进投影(等异步补写落盘)
	var got string
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got = derivedText(sessions)
		if strings.Contains(got, "模型摘要甲") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(got, "模型摘要甲") {
		t.Fatalf("投影里没有模型摘要(仍是抽取式):%q", tailOf(got, 300))
	}
	// 模型看到的是**原文片段**,不是抽取式截断版(否则"摘要升级"没有信息增量)
	if in := ll.lastSumIn; !strings.Contains(in, "这一轮说了很多细节内容") {
		t.Errorf("摘要请求里应含原文片段;实际:%q", tailOf(in, 300))
	}

	// ④ 第三轮:升级后的摘要要作为上下文**真的发给模型**(不只是日志里换了)
	if err := loop.Run(context.Background(), "第三轮问题"); err != nil {
		t.Fatalf("第三轮失败: %v", err)
	}
	if reqCtx := ll.lastTurnContext(); !strings.Contains(reqCtx, "模型摘要甲") {
		t.Errorf("第三轮请求的上下文里没有模型摘要(升级没生效):%q", tailOf(reqCtx, 400))
	}
	if turns, _ := ll.counts(); turns < 3 {
		t.Fatalf("应至少跑过 3 个回合,实际 %d", turns)
	}
}

// buildSummaryUpgradeEnv 装配「压缩 + 模型摘要」这条链路的最小环境。
//
// 刻意**不装** host-llm / llm-mock:模型由本用例直接提供(ctx.llm),这样
// "哪个请求是摘要请求"由 sdk.LLMSummaryMarker 判定,而不是靠脚本步序号。
// 预算压到 80 字符(与 pty 真机验收同款手法):首轮答复就撑爆,必然折叠。
func buildSummaryUpgradeEnv(t *testing.T) sdk.Ctx {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-system-prompt"},
		{ID: "host-agent-loop"},
		{ID: "host-llm-summary"},
		{ID: "token-compress", Data: map[string]any{"token_budget_chars": 80}},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	// 不用 host-llm/llm-mock 各自的模型来源:本用例注册一个假适配器
	// (判据是 sdk.LLMSummaryMarker,不是"第几次调用"这种脆弱的序号约定)。
	if err := baseb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c
}

// derivedText 把投影消息拼成一段文本(断言用)。
func derivedText(sessions sdk.SessionLog) string {
	var b strings.Builder
	for _, m := range sessions.DeriveMessages() {
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// tailOf 取尾部 n 字节(失败信息里够看)。
func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
