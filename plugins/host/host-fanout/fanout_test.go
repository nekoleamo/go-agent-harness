// host-fanout 测试:装配 llm/mock 后直接调用 ctx.fanout 服务(独立上下文 ReAct)。
package hostfanout

import (
	"context"
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
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-shell"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildEnv 装配 ctx.tools/ctx.llm/ctx.systemPrompt + 本插件,返回 ctx.fanout。
// llm-mock 脚本:请求1 调 shell,请求2 文本收尾(与 workflow 测试同构)。
func buildEnv(t *testing.T) sdk.FanoutService {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	for _, pl := range []sdk.Plugin{
		&hosttools.Plugin{},
		&toolshell.Plugin{},
	} {
		if _, err := pl.Start(c, &sdk.Manifest{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&hostllm.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&llmmock.Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{
		"script": `[{"tool":{"name":"shell","args":"{\"command\":\"echo sub-ok\"}"}},{"text":"子代理完成","finish":"stop"}]`,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var svc sdk.FanoutService
	if err := c.Inject("ctx.fanout", &svc); err != nil {
		t.Fatal(err)
	}
	return svc
}

// TestAgent 单子代理:独立回合并返回最终文本。
func TestAgent(t *testing.T) {
	svc := buildEnv(t)
	res, err := svc.Agent(context.Background(), "任务A")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "子代理完成") {
		t.Fatalf("应返回子代理最终文本: %q", res)
	}
}

// TestParallel 并发扇出:多子代理聚合,顺序与输入对应。
func TestParallel(t *testing.T) {
	svc := buildEnv(t)
	results := svc.Parallel(context.Background(), []string{"甲", "乙"})
	if len(results) != 2 {
		t.Fatalf("应 2 个结果: %d", len(results))
	}
	for i, r := range results {
		if r.Input != []string{"甲", "乙"}[i] {
			t.Fatalf("顺序应对应输入: %+v", results)
		}
		if r.Error != "" || !strings.Contains(r.Result, "子代理完成") {
			t.Fatalf("每项应含结果: %+v", r)
		}
	}
}

// TestPipeline 串行链:上一步输出作为下一步输入。
func TestPipeline(t *testing.T) {
	svc := buildEnv(t)
	steps, final, err := svc.Pipeline(context.Background(), []string{"第一步", "第二步"})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 {
		t.Fatalf("应 2 步: %d", len(steps))
	}
	if !strings.Contains(final, "子代理完成") {
		t.Fatalf("最终应含子代理结果: %q", final)
	}
	for _, s := range steps {
		if s.Error != "" || !strings.Contains(s.Result, "子代理完成") {
			t.Fatalf("每步应含结果: %+v", steps)
		}
	}
	// 链式:第 2 步输入应为第 1 步输出
	if steps[1].Input != steps[0].Result {
		t.Fatalf("第 2 步输入应等于第 1 步输出: %q vs %q", steps[1].Input, steps[0].Result)
	}
}

// TestContextCancel 取消传播:ctx 取消后子代理退出。
func TestContextCancel(t *testing.T) {
	svc := buildEnv(t)
	ctx2, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.Agent(ctx2, "任务")
	if err == nil {
		t.Fatal("取消的 ctx 应报错")
	}
}

// TestSpawnAgentLifecycle M9.2:后台 spawn → 完成(done 含结果)→ 状态可查;再 kill 报错。
func TestSpawnAgentLifecycle(t *testing.T) {
	svc := buildEnv(t)
	id, err := svc.SpawnAgent(context.Background(), "后台任务")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("spawn 应返回句柄 id")
	}
	// 轮询完成(mock 两步 LLM,很快)
	var h sdk.AgentHandle
	deadline := time.Now().Add(5 * time.Second)
	for {
		var ok bool
		h, ok = svc.AgentStatus(id)
		if !ok {
			t.Fatalf("会话 %s 应存在", id)
		}
		if h.State == sdk.AgentDone || h.State == sdk.AgentFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待超时: %+v", h)
		}
		time.Sleep(30 * time.Millisecond)
	}
	if h.State != sdk.AgentDone || !strings.Contains(h.Result, "子代理完成") {
		t.Fatalf("后台子代理应 done 且含结果: %+v", h)
	}
	// ListAgents 应含该会话
	found := false
	for _, x := range svc.ListAgents() {
		if x.ID == id && x.State == sdk.AgentDone {
			found = true
		}
	}
	if !found {
		t.Fatalf("list 应含已完成会话 %s", id)
	}
	// 已完成 kill → 错误
	if err := svc.KillAgent(id); err == nil {
		t.Fatal("已完成会话 kill 应报错")
	}
}

// TestSpawnAgentKill M9.2:运行中 kill → killed 状态(不再产出结果)。
func TestSpawnAgentKill(t *testing.T) {
	svc := buildEnv(t)
	id, err := svc.SpawnAgent(context.Background(), "可终止任务")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.KillAgent(id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		h, ok := svc.AgentStatus(id)
		if !ok {
			t.Fatal("会话应存在")
		}
		if h.State == sdk.AgentKilled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("kill 后应转 killed: %+v", h)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 二次 kill → 错误
	if err := svc.KillAgent(id); err == nil {
		t.Fatal("二次 kill 应报错")
	}
}

// —— M9.3 send_message / fork ——

// captureLLM 记录型 LLM stub:记录每次请求消息;可选首请求门控(测试确定 send_message 时机)。
type captureLLM struct {
	mu        sync.Mutex
	lastMsg   []sdk.LLMMessage
	requests  int
	gate      chan struct{} // 非 nil:首请求到达后阻塞,直至关闭
	firstHit  chan struct{} // 非 nil:首请求到达时关闭(通知测试子代理已在运行)
	firstTool string        // 非空:首请求回复带工具调用(创造第 2 步,供 send_message 注入窗口)
}

func (l *captureLLM) RegisterAdapter(sdk.LLMAdapter) sdk.Disposer { return func() {} }
func (l *captureLLM) SetModel(string)                             {}
func (l *captureLLM) Model() string                               { return "capture" }
func (l *captureLLM) List() []string                              { return nil }
func (l *captureLLM) SetProvider(string, string) error            { return nil }
func (l *captureLLM) UnsetProvider(string) error                  { return nil }
func (l *captureLLM) ResetProvider() error                        { return nil }
func (l *captureLLM) ProviderInfo() (string, string, bool)        { return "", "", false }
func (l *captureLLM) ListModels() ([]sdk.ModelInfo, error)        { return nil, nil }
func (l *captureLLM) SetThinking(sdk.ThinkingLevel)               {}
func (l *captureLLM) Thinking() sdk.ThinkingLevel                 { return sdk.ThinkingOff }

func (l *captureLLM) Complete(_ context.Context, req *sdk.LLMRequest, onChunk func(sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	l.mu.Lock()
	l.lastMsg = append([]sdk.LLMMessage(nil), req.Messages...)
	l.requests++
	first := l.requests == 1
	l.mu.Unlock()
	if first && l.firstHit != nil {
		close(l.firstHit)
	}
	if first && l.gate != nil {
		<-l.gate
	}
	if first && l.firstTool != "" {
		msg := sdk.LLMMessage{Role: sdk.RoleAssistant,
			ToolCalls: []sdk.ToolCall{{ID: "call1", Name: l.firstTool, Arguments: `{"command":"echo x"}`}}}
		_ = onChunk(sdk.LLMStreamEvent{ToolCallID: "call1", ToolCallName: l.firstTool, ToolCallArgs: `{"command":"echo x"}`})
		_ = onChunk(sdk.LLMStreamEvent{Done: true, Message: msg, FinishReason: "tool_calls"})
		return &sdk.LLMResponse{Message: msg, FinishReason: "tool_calls"}, nil
	}
	l.mu.Lock()
	content := fmt.Sprintf("子代理回复%d", l.requests)
	l.mu.Unlock()
	msg := sdk.LLMMessage{Role: sdk.RoleAssistant, Content: content}
	_ = onChunk(sdk.LLMStreamEvent{Delta: content})
	_ = onChunk(sdk.LLMStreamEvent{Done: true, Message: msg, FinishReason: "stop"})
	return &sdk.LLMResponse{Message: msg, FinishReason: "stop"}, nil
}

// buildEnvSeeded 装配 ctx.sessions(真实 session-log)+ 记录型 llm;返回 fanout 与 sessions。
func buildEnvSeeded(t *testing.T, llm *captureLLM) (sdk.FanoutService, sdk.SessionLog) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	for _, pl := range []sdk.Plugin{
		&hosttools.Plugin{},
		&toolshell.Plugin{},
		&hostsystemprompt.Plugin{},
	} {
		if _, err := pl.Start(c, &sdk.Manifest{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Provide("ctx.llm", llm); err != nil {
		t.Fatal(err)
	}
	if _, err := (&sessionlog.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var svc sdk.FanoutService
	if err := c.Inject("ctx.fanout", &svc); err != nil {
		t.Fatal(err)
	}
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	return svc, sessions
}

// TestSendMessageInjectsAndRecords M9.3:运行中注入消息 → 子代理继续,对话经 Messages 可读。
func TestSendMessageInjectsAndRecords(t *testing.T) {
	llm := &captureLLM{gate: make(chan struct{}), firstHit: make(chan struct{}), firstTool: "shell"}
	svc, _ := buildEnvSeeded(t, llm)
	id, err := svc.SpawnAgent(context.Background(), "后台任务")
	if err != nil {
		t.Fatal(err)
	}
	<-llm.firstHit // 子代理已进入首请求并在 gate 等待(此刻必 running)
	if err := svc.SendMessage(id, "补充要求:注意验收口径"); err != nil {
		t.Fatal(err)
	}
	close(llm.gate) // 放行;
	deadline := time.Now().Add(5 * time.Second)
	var h sdk.AgentHandle
	var ok bool
	for {
		h, ok = svc.AgentStatus(id)
		if !ok {
			t.Fatal("会话应存在")
		}
		if h.State == sdk.AgentDone || h.State == sdk.AgentFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待超时: %+v", h)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.State != sdk.AgentDone {
		t.Fatalf("应 done: %+v", h)
	}
	var got []string
	for _, m := range h.Messages {
		got = append(got, m.From+":"+m.Content)
	}
	if !containsStr(got, "user:补充要求:注意验收口径") {
		t.Fatalf("应含注入消息: %v", got)
	}
	if !containsStr(got, "agent:子代理回复2") {
		t.Fatalf("应含注入后的回复: %v", got)
	}
}

// TestSendMessageNotRunning 已完成/未知会话注入显式报错。
func TestSendMessageNotRunning(t *testing.T) {
	svc, _ := buildEnvSeeded(t, &captureLLM{})
	id, err := svc.SpawnAgent(context.Background(), "快速任务")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		h, ok := svc.AgentStatus(id)
		if !ok {
			t.Fatal("会话应存在")
		}
		if h.State == sdk.AgentDone || h.State == sdk.AgentFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待超时: %+v", h)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := svc.SendMessage(id, "迟来的消息"); err == nil {
		t.Fatal("已完成会话 send_message 应报错")
	}
	if err := svc.SendMessage(id, "  "); err == nil {
		t.Fatal("空消息应报错")
	}
	if err := svc.SendMessage("ag999", "消息"); err == nil {
		t.Fatal("未知会话应报错")
	}
}

// TestForkInheritsParentContext M9.3:fork 种入父会话历史(父历史 + 子任务输入)。
func TestForkInheritsParentContext(t *testing.T) {
	llm := &captureLLM{}
	svc, sessions := buildEnvSeeded(t, llm)
	// 父会话已发生的历史(模型可见=已记录)
	if err := sessions.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "父问题"}}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Append(sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "父回答"}}); err != nil {
		t.Fatal(err)
	}
	id, err := svc.Fork(context.Background(), "子任务")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		h, ok := svc.AgentStatus(id)
		if !ok {
			t.Fatal("会话应存在")
		}
		if h.State == sdk.AgentDone || h.State == sdk.AgentFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待超时: %+v", h)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 首请求应含父历史(user 父问题/assistant 父回答),末条为子任务输入
	llm.mu.Lock()
	defer llm.mu.Unlock()
	var roles []string
	for _, m := range llm.lastMsg {
		roles = append(roles, string(m.Role)+":"+m.Content)
	}
	if !containsStr(roles, "user:父问题") || !containsStr(roles, "assistant:父回答") {
		t.Fatalf("应种入父历史: %v", roles)
	}
	last := llm.lastMsg[len(llm.lastMsg)-1]
	if last.Role != sdk.RoleUser || last.Content != "子任务" {
		t.Fatalf("末条应为子任务输入: %+v", last)
	}
}

// TestForkNoSessions 未装配 ctx.sessions → fork 显式报错。
func TestForkNoSessions(t *testing.T) {
	svc := buildEnv(t)
	if _, err := svc.Fork(context.Background(), "任务"); err == nil {
		t.Fatal("未装配 ctx.sessions 时 fork 应显式报错")
	}
}

// containsStr 字符串切片包含判定。
func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestParallelWidthCapped 并发宽度必须封顶(maxParallel):大量输入不得生成
// 等量并发子代理(内存/配额/上游限流雪崩)。
func TestParallelWidthCapped(t *testing.T) {
	svc := buildEnv(t)
	inputs := make([]string, maxParallel+5)
	for i := range inputs {
		inputs[i] = fmt.Sprintf("任务 %d", i)
	}
	results := svc.Parallel(context.Background(), inputs)
	if len(results) != len(inputs) {
		t.Fatalf("结果数应与输入一致: %d", len(results))
	}
	for i, r := range results {
		if r.Error == "" || !strings.Contains(r.Error, "超过上限") {
			t.Fatalf("超限项应显式报错而非静默执行: %d %+v", i, r)
		}
	}
}
