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
	tok := c.register(cancel, &turn{})
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

// pokeTool 执行时注入一条转向消息(轮内注入测试的同步点:工具执行期间人按了 Enter)。
type pokeTool struct{ onExec func() }

func (p pokeTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "poke", Description: "注入", InputSchema: map[string]any{"type": "object"}}
}

func (p pokeTool) Execute(context.Context, string) (any, error) {
	if p.onExec != nil {
		p.onExec()
	}
	return map[string]any{"ok": true}, nil
}

// TestTurnControlSteer control.Steer 语义:无回合回落 false / 空消息报错 / 投出后进回合队列。
func TestTurnControlSteer(t *testing.T) {
	c := newTurnControl()
	if ok, err := c.Steer("插话"); ok || err != nil {
		t.Fatalf("无运行回合应返回 false/无错: ok=%v err=%v", ok, err)
	}
	if _, err := c.Steer("   "); err == nil {
		t.Fatal("空消息应报错")
	}
	tt := &turn{}
	tok := c.register(func() {}, tt)
	ok, err := c.Steer("插话")
	if !ok || err != nil {
		t.Fatalf("运行中应投出: ok=%v err=%v", ok, err)
	}
	if msgs := tt.takeSteers(); len(msgs) != 1 || msgs[0] != "插话" {
		t.Fatalf("消息应进回合队列: %#v", msgs)
	}
	c.unregister(tok)
	if ok, _ := c.Steer("再来"); ok {
		t.Fatal("注销后应回落 false")
	}
}

// TestTurnSteerInjectsInSameTurn 回合运行中 Enter 的插话在下一次模型请求组装之前落账并参与
// **本回合**(同一 turn/start..turn/end),模型因此能在本轮内响应 —— 而不是排到下一回合。
func TestTurnSteerInjectsInSameTurn(t *testing.T) {
	e := buildEnv(t, `[
		{"tool":{"name":"poke","args":"{}"}},
		{"text":"看到插话后的回答","finish":"stop"}
	]`)
	var tc sdk.TurnControl
	if err := e.c.Inject("ctx.turnControl", &tc); err != nil {
		t.Fatal(err)
	}
	sr, ok := tc.(sdk.TurnSteerer)
	if !ok {
		t.Fatal("ctx.turnControl 应实现 sdk.TurnSteerer")
	}
	e.tools.Register(pokeTool{onExec: func() {
		if ok, err := sr.Steer("别查了,直接改 B 方案"); !ok || err != nil {
			t.Errorf("工具执行期间应能注入: ok=%v err=%v", ok, err)
		}
	}})
	if err := e.loop.Run(context.Background(), "初始任务"); err != nil {
		t.Fatal(err)
	}
	k := kinds(e.log)
	if n := strings.Count(k, sdk.EventTurnStart); n != 1 {
		t.Fatalf("插话应留在本回合(1 个 turn/start): %d 次\n%s", n, k)
	}
	if n := strings.Count(k, sdk.EventUserMessage); n != 2 {
		t.Fatalf("应有 2 条 user/message(初始 + 插话): %d 次\n%s", n, k)
	}
	if n := strings.Count(k, sdk.EventAssistantMessage); n != 2 {
		t.Fatalf("模型应对插话再答一次(2 条 assistant/message): %d 次\n%s", n, k)
	}
	firstAssistant := strings.Index(k, sdk.EventAssistantMessage)
	if firstAssistant < 0 || !strings.Contains(k[firstAssistant:], sdk.EventUserMessage) {
		t.Fatalf("插话应排在首次 assistant/message 之后(中途插入,不是开头): %s", k)
	}
	var found bool
	for _, m := range e.sessions.DeriveMessages() {
		if strings.Contains(m.Content, "别查了") {
			found = true
		}
	}
	if !found {
		t.Fatal("插话文本应进派生历史(模型可见即已记录)")
	}
}

// TestTurnSteerKeepsTurnAlive 模型已无工具调用但插话还在等 → 本回合不收尾,下个 step
// 注入后继续(否则用户的插话石沉大海)。
func TestTurnSteerKeepsTurnAlive(t *testing.T) {
	e := buildEnv(t, `[
		{"text":"第一版回答","finish":"stop"},
		{"text":"照插话改过的回答","finish":"stop"}
	]`)
	var tc sdk.TurnControl
	if err := e.c.Inject("ctx.turnControl", &tc); err != nil {
		t.Fatal(err)
	}
	sr := tc.(sdk.TurnSteerer)
	// 在首次流式正文期间插话:此时本 step 开头的注入点已过 → 收尾时必须发现它还等着。
	steered := false
	unsub := e.c.Subscribe(sdk.EventSession, func(_ context.Context, ev *sdk.Event) error {
		sev, ok := ev.Payload.(*sdk.SessionEvent)
		if !ok || sev.Kind != sdk.EventAssistantChunk || steered {
			return nil
		}
		steered = true
		if ok, err := sr.Steer("换个说法"); !ok || err != nil {
			t.Errorf("流式期间应能注入: ok=%v err=%v", ok, err)
		}
		return nil
	})
	defer unsub()
	if err := e.loop.Run(context.Background(), "初始任务"); err != nil {
		t.Fatal(err)
	}
	if !steered {
		t.Fatal("未观察到 assistant/chunk(测试同步点失效)")
	}
	k := kinds(e.log)
	if n := strings.Count(k, sdk.EventAssistantMessage); n != 2 {
		t.Fatalf("有插话待注入时不得收尾(应 2 条 assistant/message): %d 次\n%s", n, k)
	}
	if n := strings.Count(k, sdk.EventTurnStart); n != 1 {
		t.Fatalf("仍是同一回合(1 个 turn/start): %d 次\n%s", n, k)
	}
}

// TestTurnSteerDroppedOnCancel 回合取消时未注入的插话不丢也不冒充历史:
// 经 agent/steer-dropped 交回发起端(TUI 回待发队列 / Web 推回输入框)。
func TestTurnSteerDroppedOnCancel(t *testing.T) {
	e := buildEnv(t, `[
		{"tool":{"name":"poke","args":"{}"}},
		{"text":"不该到达","finish":"stop"}
	]`)
	var tc sdk.TurnControl
	if err := e.c.Inject("ctx.turnControl", &tc); err != nil {
		t.Fatal(err)
	}
	var got []string
	unsub := e.c.Subscribe("agent/steer-dropped", func(_ context.Context, ev *sdk.Event) error {
		if msgs, ok := ev.Payload.([]string); ok {
			got = append(got, msgs...)
		}
		return nil
	})
	defer unsub()
	e.tools.Register(pokeTool{onExec: func() {
		tc.Cancel() // 人在工具跑的时候按了 Esc
		if ok, err := tc.(sdk.TurnSteerer).Steer("这条没赶上"); !ok || err != nil {
			t.Errorf("取消瞬间注入应仍被受理: ok=%v err=%v", ok, err)
		}
	}})
	err := e.loop.Run(context.Background(), "初始任务")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应返回 context.Canceled: %v", err)
	}
	if len(got) != 1 || got[0] != "这条没赶上" {
		t.Fatalf("应回吐未注入的插话: %#v", got)
	}
	if n := strings.Count(kinds(e.log), sdk.EventUserMessage); n != 1 {
		t.Fatalf("未注入的插话不得进历史(1 条 user/message): %d 条\n%s", n, kinds(e.log))
	}
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

// ---------- 溢出兜底压缩(第五十六批)----------

// overflowLLM 首次 Complete 报端点超窗(用适配层真实错误串形状),之后成功;记录每次请求投影,
// 供断言"重试发出去的是压缩后的历史"。嵌入 errLLM 复用其余接口方法。
type overflowLLM struct {
	errLLM
	calls  int
	always bool
	reqs   [][]sdk.LLMMessage
}

func (o *overflowLLM) Complete(_ context.Context, req *sdk.LLMRequest, _ func(sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	o.calls++
	o.reqs = append(o.reqs, req.Messages)
	if o.always || o.calls == 1 {
		return nil, errors.New(`llm-openai: HTTP 400: {"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens."}}`)
	}
	return &sdk.LLMResponse{
		Message:      sdk.LLMMessage{Role: sdk.RoleAssistant, Content: "压缩后完成"},
		FinishReason: sdk.FinishReasonStop,
	}, nil
}

// foldAllCompressor 测试用压缩引擎:把水位后、最后一条用户消息之前的全部事件折成摘要
// (与真实引擎"水位不得越过最后一个用户轮"一致)。不消费预算 —— 溢出兜底路径本来就不看预算。
type foldAllCompressor struct{ calls int }

func (c *foldAllCompressor) Fold(evs []sdk.SessionEvent, watermark, _ int, summary func(string)) int {
	c.calls++
	last := -1
	for i, ev := range evs {
		if ev.Kind == sdk.EventUserMessage {
			last = i
		}
	}
	end := last - 1
	if end <= watermark {
		return watermark
	}
	summary("【累计摘要】" + strings.Repeat("旧", 200))
	return end
}

// recNotices 记录发布过的提示(不依赖 host-notices 插件)。
type recNotices struct{ got []sdk.Notice }

func (r *recNotices) Publish(n sdk.Notice) uint64 {
	r.got = append(r.got, n)
	return uint64(len(r.got))
}
func (r *recNotices) List(uint64) sdk.NoticePage { return sdk.NoticePage{} }

// projChars 投影总字符(断言"重试更短"用)。
func projChars(msgs []sdk.LLMMessage) int {
	n := 0
	for _, m := range msgs {
		n += len([]rune(m.Content))
	}
	return n
}

// countUser 投影里某条用户消息出现的次数(断言"重试不重复计入用户消息")。
func countUser(msgs []sdk.LLMMessage, text string) int {
	n := 0
	for _, m := range msgs {
		if m.Role == sdk.RoleUser && m.Content == text {
			n++
		}
	}
	return n
}

// TestTurnOverflowCompactsAndRetries 端点报超窗 ⇒ 压缩历史后重试同一回合:
// 只重试一次、重试发出去的是压缩后的历史、提示通道可见、用户消息不重复。
func TestTurnOverflowCompactsAndRetries(t *testing.T) {
	e := buildEnv(t, `[{"text":"unused"}]`)
	// 先造一段可折叠的历史(长输入,让"压缩后更短"有可观测差异)
	if err := e.loop.Run(context.Background(), "第一轮很长的问题"+strings.Repeat("长", 2000)); err != nil {
		t.Fatal(err)
	}
	comp := &foldAllCompressor{}
	e.log.RegisterCompressor(1_000_000, comp) // 预算给足:确保不是自动路径折的,而是溢出兜底折的
	llm := &overflowLLM{}
	notices := &recNotices{}
	loop := &Loop{c: e.c, sessions: e.sessions, tools: e.tools, llm: llm, sp: e.sp, notices: notices}
	if err := loop.Run(context.Background(), "第二轮问题"); err != nil {
		t.Fatalf("压缩后应能完成: %v", err)
	}
	if llm.calls != 2 {
		t.Fatalf("应请求两次(原请求 + 压缩后重试一次): %d", llm.calls)
	}
	if comp.calls != 1 {
		t.Fatalf("重试前必须压缩一次: %d", comp.calls)
	}
	if len(notices.got) != 1 {
		t.Fatalf("提示通道应恰好一条(压缩是用户看不见的输入改写,至少露面一次): %+v", notices.got)
	}
	if n := notices.got[0]; n.Level != sdk.NoticeInfo || n.Title != "已自动压缩上下文" || !strings.Contains(n.Body, "折叠 ") {
		t.Fatalf("提示内容不对(需 info 级 + 标题 + 折叠帧数): %+v", n)
	}
	first, second := llm.reqs[0], llm.reqs[1]
	if projChars(second) >= projChars(first) {
		t.Fatalf("重试投影应更短: %d → %d", projChars(first), projChars(second))
	}
	if !strings.Contains(projCharsAll(second), "累计摘要") {
		t.Fatalf("重试投影应带压缩摘要: %s", projCharsAll(second))
	}
	if n := countUser(second, "第二轮问题"); n != 1 {
		t.Fatalf("用户消息应恰好 1 条(重试不得重复计入): %d", n)
	}
	if k := kinds(e.log); !strings.HasSuffix(k, sdk.EventTurnEnd) {
		t.Fatalf("应正常收尾: %s", k)
	}
}

// projCharsAll 投影全文(断言摘要出现用)。
func projCharsAll(msgs []sdk.LLMMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Content)
	}
	return b.String()
}

// TestTurnOverflowRetriesAtMostOnce 端点持续报超窗:只重试一次,失败文案如实说明"已试过自动压缩"
// 并给出人话出路 —— 不静默把原始报错丢给用户,也不形成重试环/重复计费。
func TestTurnOverflowRetriesAtMostOnce(t *testing.T) {
	e := buildEnv(t, `[{"text":"unused"}]`)
	e.log.RegisterCompressor(1_000_000, &foldAllCompressor{})
	llm := &overflowLLM{always: true}
	loop := &Loop{c: e.c, sessions: e.sessions, tools: e.tools, llm: llm, sp: e.sp}
	err := loop.Run(context.Background(), "问题")
	if err == nil {
		t.Fatal("端点持续超窗应显式失败")
	}
	if llm.calls != 2 {
		t.Fatalf("只应重试一次(硬上限): %d", llm.calls)
	}
	for _, want := range []string{"已自动压缩上下文后重试仍超窗", "/compact"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("失败文案应含 %q,得: %v", want, err)
		}
	}
}

// TestTurnOverflowWithoutCompactor 没有压缩能力(未装配 token-compress)时:不盲目重试,
// 如实说明"自动压缩不可用"。
func TestTurnOverflowWithoutCompactor(t *testing.T) {
	e := buildEnv(t, `[{"text":"unused"}]`)
	llm := &overflowLLM{}
	loop := &Loop{c: e.c, sessions: e.sessions, tools: e.tools, llm: llm, sp: e.sp}
	err := loop.Run(context.Background(), "问题")
	if err == nil {
		t.Fatal("应失败")
	}
	if llm.calls != 1 {
		t.Fatalf("压缩不可用时不该原样重试: %d", llm.calls)
	}
	if !strings.Contains(err.Error(), "自动压缩不可用") {
		t.Fatalf("文案应说明原因: %v", err)
	}
}

// TestTurnPlainErrorDoesNotCompress 普通错误(非超窗)不得触发压缩:用户取消/网络断流/限额
// 与上下文无关,压了反而白丢上下文。
func TestTurnPlainErrorDoesNotCompress(t *testing.T) {
	e := buildEnv(t, `[{"text":"unused"}]`)
	comp := &foldAllCompressor{}
	e.log.RegisterCompressor(1_000_000, comp)
	loop := &Loop{c: e.c, sessions: e.sessions, tools: e.tools, llm: &errLLM{}, sp: e.sp}
	err := loop.Run(context.Background(), "问题")
	if err == nil || !strings.Contains(err.Error(), "llm 流中断") {
		t.Fatalf("应原样失败: %v", err)
	}
	if comp.calls != 0 {
		t.Fatalf("普通错误不得触发压缩: %d", comp.calls)
	}
}
