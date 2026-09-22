// host-agent-loop 单元测试:回合流程与取消/LLM 错误/工具错误分支(装配 + errLLM 注入)。
package hostagentloop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/adapter/llm-mock"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-llm"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-session-log"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// env 装配结果(同包可见字段,便于测试注入)。
type env struct {
	c        sdk.Ctx
	sessions sdk.SessionLog
	tools    sdk.ToolRegistry
	sp       sdk.SystemPromptService
	loop     *Loop
	log      *sessionlog.Log
}

// fakeTool 工具:echo 回显;fail 返回业务错误(结构化)。
type fakeTool struct{ def sdk.ToolDefinition }

func (f fakeTool) Definition() sdk.ToolDefinition { return f.def }
func (f fakeTool) Execute(_ context.Context, args string) (any, error) {
	switch f.def.Name {
	case "echo":
		return map[string]any{"echo": args}, nil
	case "fail":
		return map[string]any{"error": "业务失败"}, nil
	}
	return nil, nil
}

// errLLM 流错误注入(Complete 恒失败)。
type errLLM struct{}

func (e *errLLM) Complete(_ context.Context, _ *sdk.LLMRequest, _ func(sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	return nil, errors.New("llm 流中断")
}
func (e *errLLM) RegisterAdapter(_ sdk.LLMAdapter) sdk.Disposer { return func() {} }
func (e *errLLM) SetModel(_ string)                             {}
func (e *errLLM) Model() string                                 { return "err" }
func (e *errLLM) List() []string                                { return nil }
func (e *errLLM) SetProvider(_, _ string) error                 { return errors.New("unavailable") }
func (e *errLLM) UnsetProvider(_ string) error                  { return errors.New("unavailable") }
func (e *errLLM) ResetProvider() error                          { return errors.New("unavailable") }
func (e *errLLM) ProviderInfo() (string, string, bool)          { return "", "", false }
func (e *errLLM) ListModels() ([]sdk.ModelInfo, error)          { return nil, errors.New("unavailable") }
func (e *errLLM) SetThinking(_ sdk.ThinkingLevel)               {}
func (e *errLLM) Thinking() sdk.ThinkingLevel                   { return sdk.ThinkingOff }

// buildEnv 装配 sessions/tools/llm(mock)/systemPrompt + 本插件(llmScript 非法时走失败路径)。
func buildEnv(t *testing.T, llmScript string) *env {
	t.Helper()
	return buildEnvData(t, llmScript, nil)
}

// buildEnvData 同上,但给 host-agent-loop 插件传 data(如 max_steps)。
func buildEnvData(t *testing.T, llmScript string, loopData map[string]any) *env {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&sessionlog.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	tools.Register(fakeTool{def: sdk.ToolDefinition{Name: "echo", Description: "回显", InputSchema: map[string]any{"type": "object"}}})
	tools.Register(fakeTool{def: sdk.ToolDefinition{Name: "fail", Description: "失败", InputSchema: map[string]any{"type": "object"}}})
	if _, err := (&hostllm.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&llmmock.Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{"script": llmScript}}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: loopData}); err != nil {
		t.Fatal(err)
	}
	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		t.Fatal(err)
	}
	return &env{c: c, sessions: sessions, tools: tools, sp: sp, loop: loop.(*Loop), log: sessions.(*sessionlog.Log)}
}

// kinds 事件 Kind 序列。
func kinds(l *sessionlog.Log) string {
	var out []string
	for _, e := range l.Replay() {
		out = append(out, e.Kind)
	}
	return strings.Join(out, ",")
}

// TestTurnToolThenText 工具调用轮→文本收尾轮:完整回合 done。
func TestTurnToolThenText(t *testing.T) {
	e := buildEnv(t, `[
		{"tool":{"name":"echo","args":"{\"v\":1}"}},
		{"text":"完成","finish":"stop"}
	]`)
	if err := e.loop.Run(context.Background(), "任务"); err != nil {
		t.Fatal(err)
	}
	k := kinds(e.log)
	// turn/start 与 turn/end 成对(S-P0-1 轨迹视图依赖权威回合边界;此前该常量只声明未发出)
	for _, want := range []string{"turn/start", "user/message", "step/start", "step/end", "assistant/message", "tool/call", "tool/result", "turn/end"} {
		if !strings.Contains(k, want) {
			t.Fatalf("流程应记录 %s: %s", want, k)
		}
	}
	if !strings.HasPrefix(k, sdk.EventTurnStart+","+sdk.EventUserMessage) {
		t.Fatalf("回合应以 turn/start → user/message 起头: %s", k)
	}
	if !strings.HasSuffix(k, "turn/end") {
		t.Fatalf("回合应以 turn/end 收尾: %s", k)
	}
}

// blockLLM Complete 阻塞至 ctx 取消(回合取消测试的挂起点)。
type blockLLM struct{ errLLM }

func (b *blockLLM) Complete(ctx context.Context, _ *sdk.LLMRequest, _ func(sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestTurnControlCancel 回合取消走 ctx.turnControl:运行中 Running、Cancel 后回合
// 以 cancelled 结束、注销后非 Running(TUI Esc/Web cancel 共用入口)。
func TestTurnControlCancel(t *testing.T) {
	e := buildEnv(t, `[{"text":"x","finish":"stop"}]`)
	var tci sdk.TurnControl
	if err := e.c.Inject("ctx.turnControl", &tci); err != nil {
		t.Fatal(err)
	}
	if tci.Running() {
		t.Fatal("初始应无运行回合")
	}
	loop := &Loop{c: e.c, sessions: e.sessions, tools: e.tools, llm: &blockLLM{}, sp: e.sp, tc: tci.(*control)}
	done := make(chan error, 1)
	go func() { done <- loop.Run(context.Background(), "任务") }()
	time.Sleep(80 * time.Millisecond) // 等待进入 Complete 阻塞点
	if !tci.Running() {
		t.Fatal("回合挂起时应 Running=true")
	}
	tci.Cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消后应返回 context.Canceled,got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Cancel 后回合未结束(超时)")
	}
	if tci.Running() {
		t.Fatal("回合结束后应非 Running")
	}
	evts := e.log.Replay()
	last := evts[len(evts)-1]
	if last.Kind != sdk.EventTurnEnd || last.Payload != "cancelled" {
		t.Fatalf("应以 cancelled turn/end 收尾: %+v", last)
	}
}

// TestTurnControlRegistry control 注册表语义:注册/取消/注销/幂等。
func TestTurnControlRegistry(t *testing.T) {
	c := newTurnControl()
	if c.Running() {
		t.Fatal("空注册表应非 Running")
	}
	ctx0, cancel := context.WithCancel(context.Background())
	tok := c.register(cancel)
	if !c.Running() {
		t.Fatal("注册后应 Running")
	}
	c.Cancel()
	if ctx0.Err() == nil {
		t.Fatal("Cancel 应触发回合 ctx 取消")
	}
	c.Cancel() // 幂等:重复取消不 panic
	c.unregister(tok)
	if c.Running() {
		t.Fatal("注销后应非 Running")
	}
	c.unregister(tok) // 幂等注销
}

// TestTurnCancelled 上下文取消:回合以 cancelled 结束并返回错误。
func TestTurnCancelled(t *testing.T) {
	e := buildEnv(t, `[
		{"tool":{"name":"echo","args":"{}"}},
		{"text":"完成","finish":"stop"}
	]`)
	ctx2, cancel := context.WithCancel(context.Background())
	cancel() // 首步前取消
	if err := e.loop.Run(ctx2, "任务"); err == nil {
		t.Fatal("取消的回合应返回错误")
	}
	// cancelled 是 turn/end 的载荷(非 Kind),断言末尾事件
	evts := e.log.Replay()
	last := evts[len(evts)-1]
	if last.Kind != sdk.EventTurnEnd || last.Payload != "cancelled" {
		t.Fatalf("应以 cancelled turn/end 收尾: %+v", last)
	}
}

// TestTurnLLMError LLM 流错误:回合显式失败且无 done(不静默)。
func TestTurnLLMError(t *testing.T) {
	e := buildEnv(t, `[
		{"text":"完成","finish":"stop"}
	]`)
	bad := &Loop{c: e.c, sessions: e.sessions, tools: e.tools, llm: &errLLM{}, sp: e.sp}
	if err := bad.Run(context.Background(), "任务"); err == nil {
		t.Fatal("LLM 错误应使回合失败")
	}
	if strings.Contains(kinds(e.log), "done") {
		t.Fatalf("失败回合不应 done: %s", kinds(e.log))
	}
}

// TestToolErrorStructured 工具业务错误:结构化回传(模型可见 ERROR 前缀)。
func TestToolErrorStructured(t *testing.T) {
	e := buildEnv(t, `[
		{"tool":{"name":"fail","args":"{}"}},
		{"text":"收尾","finish":"stop"}
	]`)
	if err := e.loop.Run(context.Background(), "任务"); err != nil {
		t.Fatal(err)
	}
	var sawToolErr bool
	for _, m := range e.log.DeriveMessages() {
		if m.Role == sdk.RoleTool && strings.Contains(m.Content, "ERROR") {
			sawToolErr = true
			break
		}
	}
	if !sawToolErr {
		t.Fatalf("工具错误应结构化回传: %+v", e.log.DeriveMessages())
	}
}

// scriptedLLM 脚本化 LLM(capture):按请求序号依次返回固定文本,记录每次请求的完整消息。
type scriptedLLM struct {
	steps    []string
	calls    [][]sdk.ToolCall // 按步给"响应体内累积的 tool_calls"(模拟 anthropic 适配器)
	n        int
	requests [][]sdk.LLMMessage
	toolSets [][]sdk.ToolDefinition // 每次请求的 tools 下发(结构化调用依赖)
}

func (s *scriptedLLM) Name() string { return "scripted" }
func (s *scriptedLLM) Complete(_ context.Context, req *sdk.LLMRequest, _ func(sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	cp := append([]sdk.LLMMessage(nil), req.Messages...)
	s.requests = append(s.requests, cp)
	s.toolSets = append(s.toolSets, append([]sdk.ToolDefinition(nil), req.Tools...))
	i := s.n
	s.n++
	if i >= len(s.steps) {
		i = len(s.steps) - 1
	}
	var tc []sdk.ToolCall
	if i < len(s.calls) {
		tc = s.calls[i]
	}
	finish := sdk.FinishReasonStop
	if len(tc) > 0 {
		finish = sdk.FinishReasonToolCalls
	}
	return &sdk.LLMResponse{Message: sdk.LLMMessage{Role: sdk.RoleAssistant, Content: s.steps[i], ToolCalls: tc}, FinishReason: finish}, nil
}

// TestTurnExecutesResponseBodyToolCalls 工具调用在**响应体内**累积(anthropic 适配器的做法:
// 按 content block 累积,不发增量 ToolCallID 事件)时也必须执行。
// 此前 loop 只认增量累积的 calls → 这类响应被当成"无工具调用"直接收尾:正文照出、工具静默不跑。
func TestTurnExecutesResponseBodyToolCalls(t *testing.T) {
	e, llm := buildEnvScripted(t, []string{"", "收尾"})
	llm.calls = [][]sdk.ToolCall{{{ID: "toolu_1", Name: "echo", Arguments: `{"text":"甲"}`}}}
	if err := e.loop.Run(context.Background(), "任务"); err != nil {
		t.Fatal(err)
	}
	if len(llm.requests) != 2 {
		t.Fatalf("工具执行后应有第二趟请求(证明工具真跑了),实际 %d", len(llm.requests))
	}
	if n := countKind(e.log, sdk.EventToolResult); n != 1 {
		t.Errorf("应有 1 条 tool/result,实际 %d:%s", n, kinds(e.log))
	}
}

// buildEnvScripted 装配(host-llm + scripted adapter 注入;替换 mock),返回 env + adapter。
func buildEnvScripted(t *testing.T, steps []string) (*env, *scriptedLLM) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&sessionlog.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	tools.Register(fakeTool{def: sdk.ToolDefinition{Name: "echo", Description: "回显", InputSchema: map[string]any{"type": "object"}}})
	if _, err := (&hostllm.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var llm sdk.LLMService
	if err := c.Inject("ctx.llm", &llm); err != nil {
		t.Fatal(err)
	}
	sa := &scriptedLLM{steps: steps}
	llm.RegisterAdapter(sa)
	llm.SetModel("scripted") // host-llm Complete 需模型已设置(对齐 mock 适配器行为)
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		t.Fatal(err)
	}
	return &env{c: c, sessions: sessions, tools: tools, sp: sp, loop: loop.(*Loop), log: sessions.(*sessionlog.Log)}, sa
}

// countKind 统计事件流中某 Kind 出现次数。
func countKind(l *sessionlog.Log, kind string) int {
	n := 0
	for _, e := range l.Replay() {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// TestLLMRequestCarriesTools 结构化工具调用依赖 tools 下发到 API(修复:M6.14 后实测回合模型
// 只能正文伪调用——根因是 req.Tools 未赋值;此处断言每次请求都携带模型可见工具定义。
func TestLLMRequestCarriesTools(t *testing.T) {
	e, sa := buildEnvScripted(t, []string{"完成", "完成"})
	if err := e.loop.Run(context.Background(), "任务"); err != nil {
		t.Fatal(err)
	}
	var sawEcho bool
	for _, set := range sa.toolSets {
		for _, d := range set {
			if d.Name == "echo" {
				sawEcho = true
			}
		}
	}
	if !sawEcho {
		t.Fatal("LLM 请求应携带工具定义(echo);缺失则模型无法走结构化 tool_calls")
	}
}

// TestFakeToolCallGetsReminder 正文伪调用(无真实 tool_call):不被当作最终答案,注入提醒再给一轮;
// 提醒消息送达模型(第二请求末尾含"系统提醒")。
func TestFakeToolCallGetsReminder(t *testing.T) {
	e, sa := buildEnvScripted(t, []string{
		"我来帮你查天气。<DSML><tool_calls><invoke name=\"web_fetch\">https://wttr.in</invoke></tool_calls>",
		"我用工具查询天气。(正常文本,无调用)",
	})
	if err := e.loop.Run(context.Background(), "查宜兴天气"); err != nil {
		t.Fatal(err)
	}
	if got := countKind(e.log, sdk.EventAssistantMessage); got != 2 {
		t.Fatalf("伪调用不应作终答:应 2 轮 assistant(提醒后再答),got %d", got)
	}
	if len(sa.requests) != 2 {
		t.Fatalf("应发生 2 次请求,got %d", len(sa.requests))
	}
	// 提醒送达模型:第二请求末尾 user 消息含"系统提醒"
	last := sa.requests[1][len(sa.requests[1])-1]
	if last.Role != sdk.RoleUser || !strings.Contains(last.Content, "系统提醒") {
		t.Fatalf("第二请求应携带伪调用提醒: role=%s content=%q", last.Role, last.Content)
	}
}

// TestFakeToolCallReminderOnce 提醒每回合仅一次:二次伪调用不再无限修正,回合正常结束。
func TestFakeToolCallReminderOnce(t *testing.T) {
	e, sa := buildEnvScripted(t, []string{
		"伪调用一 <tool_calls>x</tool_calls>",
		"伪调用二 <antml:invoke>y</antml:invoke>", // 已提醒过 → 此轮直接结束
	})
	if err := e.loop.Run(context.Background(), "任务"); err != nil {
		t.Fatal(err)
	}
	if len(sa.requests) != 2 {
		t.Fatalf("提醒上限:应 2 次请求后结束(不无限),got %d", len(sa.requests))
	}
	if got := countKind(e.log, sdk.EventAssistantMessage); got != 2 {
		t.Fatalf("assistant 应为 2,got %d", got)
	}
	if !strings.HasSuffix(kinds(e.log), "turn/end") {
		t.Fatalf("应正常 turn/end 收尾: %s", kinds(e.log))
	}
}

// TestFakeToolCallReminderOnceMarkdown 误报防护:正文仅提一句格式但无调用标签 → 不提醒(一次即终答)。
func TestNoFakeMarkerPlainText(t *testing.T) {
	e, sa := buildEnvScripted(t, []string{"当前天气:宜兴小雨 27°C(直接回答,无工具调用)"})
	if err := e.loop.Run(context.Background(), "天气"); err != nil {
		t.Fatal(err)
	}
	if len(sa.requests) != 1 {
		t.Fatalf("无伪调用标记应一次请求收尾,got %d", len(sa.requests))
	}
	if got := countKind(e.log, sdk.EventAssistantMessage); got != 1 {
		t.Fatalf("assistant 应为 1,got %d", got)
	}
}

// TestContainsFakeToolCall 检测函数:真实伪调用标记命中;普通文本/大小写不敏感。
func TestContainsFakeToolCall(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"我来调用工具 <tool_calls>...", true},
		{"<DSML><invoke name=\"x\">", true},
		{"<antml:invoke name=\"tool\">", true},
		{"<function_calls>json</function_calls>", true},
		{"正常回答,无任何标记", false},
		{"", false},
		{"模型在文档里写 <tool_calls> 标签的含义", true}, // 复述格式也触发(温和提醒,可接受)
	}
	for _, c := range cases {
		if got := containsFakeToolCall(c.text); got != c.want {
			t.Errorf("containsFakeToolCall(%q) = %v,want %v", c.text, got, c.want)
		}
	}
}

// sleepTool 记录启动时刻并睡一会儿(并行/串行判定用)。
type sleepTool struct {
	name   string
	dur    time.Duration
	mu     *sync.Mutex
	starts *[]time.Time
}

func (s sleepTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: s.name, Description: s.name, InputSchema: map[string]any{"type": "object"}}
}

func (s sleepTool) Execute(_ context.Context, _ string) (any, error) {
	s.mu.Lock()
	*s.starts = append(*s.starts, time.Now())
	s.mu.Unlock()
	time.Sleep(s.dur)
	return map[string]any{"ok": s.name}, nil
}

// TestTurnParallelToolCalls 一轮多个 tool_calls 必须**并发**执行,且事件落序仍按调用序
// (串行时 ask_user_question 这类等作答的工具会互等阻塞 → 问题栈/「待答 N」永不成立)。
func TestTurnParallelToolCalls(t *testing.T) {
	e := buildEnv(t, `[{"tools":[{"name":"slow1","args":"{}"},{"name":"slow2","args":"{}"}]},{"text":"完成"}]`)
	var mu sync.Mutex
	var starts []time.Time
	e.tools.Register(sleepTool{name: "slow1", dur: 300 * time.Millisecond, mu: &mu, starts: &starts})
	e.tools.Register(sleepTool{name: "slow2", dur: 300 * time.Millisecond, mu: &mu, starts: &starts})

	t0 := time.Now()
	if err := e.loop.Run(context.Background(), "任务"); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(t0)

	var seq []string
	for _, ev := range e.log.Replay() {
		switch ev.Kind {
		case sdk.EventToolCall:
			if c, ok := ev.Payload.(sdk.ToolCallEvent); ok {
				seq = append(seq, "call:"+c.Name)
			}
		case sdk.EventToolResult:
			if r, ok := ev.Payload.(sdk.ToolResultEvent); ok {
				seq = append(seq, "result:"+r.Name)
			}
		}
	}
	if got, want := strings.Join(seq, ","), "call:slow1,call:slow2,result:slow1,result:slow2"; got != want {
		t.Errorf("并行调用的会话日志顺序应固定为调用序:\n got %s\nwant %s", got, want)
	}
	mu.Lock()
	n, gap := len(starts), time.Duration(0)
	if n == 2 {
		gap = starts[1].Sub(starts[0])
	}
	mu.Unlock()
	if n != 2 {
		t.Fatalf("两个工具都应执行: %d", n)
	}
	if gap > 250*time.Millisecond || elapsed > 550*time.Millisecond {
		t.Errorf("工具调用未并行:启动间隔 %v 总耗时 %v(串行约 600ms)", gap, elapsed)
	}
}

// TestTurnThinkingChunkIsLogged 思维增量必须落流:此前 onChunk 只在 Delta 非空时落
// assistant/chunk,推理模型的 thinking 增量整段丢失(TUI 思维块/HTML 思考块/ACP 拿不到内容)。
func TestTurnThinkingChunkIsLogged(t *testing.T) {
	e := buildEnv(t, `[{"thinking":"先推理甲。","text":"结论乙。"}]`)
	if err := e.loop.Run(context.Background(), "任务"); err != nil {
		t.Fatal(err)
	}
	var think string
	for _, ev := range e.log.Replay() {
		if ev.Kind != sdk.EventAssistantChunk {
			continue
		}
		if c, ok := ev.Payload.(sdk.LLMStreamEvent); ok {
			think += c.Thinking
		}
	}
	if think != "先推理甲。" {
		t.Fatalf("思维增量未落流: %q", think)
	}
}

// TestMaxStepsFromManifest data.max_steps 解析:只认数值;缺省/类型不符 = 不限(0)。
func TestMaxStepsFromManifest(t *testing.T) {
	cases := []struct {
		name string
		m    *sdk.Manifest
		want int
	}{
		{"nil manifest", nil, 0},
		{"未配置", &sdk.Manifest{Data: map[string]any{}}, 0},
		{"int", &sdk.Manifest{Data: map[string]any{"max_steps": 7}}, 7},
		{"float64(yaml 数字)", &sdk.Manifest{Data: map[string]any{"max_steps": 12.0}}, 12},
		{"0 = 不限", &sdk.Manifest{Data: map[string]any{"max_steps": 0}}, 0},
		{"负数 = 不限", &sdk.Manifest{Data: map[string]any{"max_steps": -1}}, -1},
		{"类型不符按缺省", &sdk.Manifest{Data: map[string]any{"max_steps": "10"}}, 0},
	}
	for _, c := range cases {
		if got := maxStepsFromManifest(c.m); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

// mockSteps 造 n 步工具调用 + 一步文本收尾的 mock 脚本。
func mockSteps(n int) string {
	steps := make([]string, 0, n+1)
	for i := 0; i < n; i++ {
		steps = append(steps, fmt.Sprintf(`{"tool":{"name":"echo","args":"{\"i\":%d}"}}`, i))
	}
	steps = append(steps, `{"text":"完成","finish":"stop"}`)
	return "[" + strings.Join(steps, ",") + "]"
}

// TestTurnDefaultHasNoStepCap 缺省不再有 10 步硬上限:12 步工具调用应跑完而不是报"达到最大步数"。
// 老口径(const maxSteps = 10)下这条必然失败,是本次真机反馈(长任务撞线)的回归锁。
func TestTurnDefaultHasNoStepCap(t *testing.T) {
	e := buildEnv(t, mockSteps(12))
	if err := e.loop.Run(context.Background(), "长任务"); err != nil {
		t.Fatalf("缺省不应有步数上限: %v", err)
	}
	if calls := strings.Count(kinds(e.log), "tool/call"); calls != 12 {
		t.Fatalf("应执行 12 次工具调用: %d", calls)
	}
	if k := kinds(e.log); !strings.HasSuffix(k, "turn/end") || strings.Contains(k, "max_steps") {
		t.Fatalf("回合应以 done 收尾: %s", k)
	}
}

// TestTurnMaxStepsConfigured 配了 data.max_steps 仍显式失败(阈值生效,且事实不被掩盖)。
func TestTurnMaxStepsConfigured(t *testing.T) {
	e := buildEnvData(t, mockSteps(5), map[string]any{"max_steps": 2})
	err := e.loop.Run(context.Background(), "会撞线")
	if err == nil || !strings.Contains(err.Error(), "达到最大步数 2") {
		t.Fatalf("应报步数上限错误: %v", err)
	}
	if calls := strings.Count(kinds(e.log), "tool/call"); calls != 2 {
		t.Fatalf("应在第 2 步停下: %d", calls)
	}
	if k := kinds(e.log); !strings.Contains(k, sdk.EventTurnEnd) || !strings.HasSuffix(k, sdk.EventTurnEnd) {
		t.Fatalf("撞线也必须有 turn/end 收尾: %s", k)
	}
}
