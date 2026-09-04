// host-agent-loop 单元测试:回合流程与取消/LLM 错误/工具错误分支(装配 + errLLM 注入)。
package hostagentloop

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host-llm"
	"github.com/nekoleamo/go-agent-harness/plugins/host-session-log"
	"github.com/nekoleamo/go-agent-harness/plugins/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/plugins/host-tools"
	"github.com/nekoleamo/go-agent-harness/plugins/llm-mock"
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
func (e *errLLM) ProviderInfo() (string, string, bool)          { return "", "", false }

// buildEnv 装配 sessions/tools/llm(mock)/systemPrompt + 本插件(llmScript 非法时走失败路径)。
func buildEnv(t *testing.T, llmScript string) *env {
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
	for _, want := range []string{"user/message", "step/start", "step/end", "assistant/message", "tool/call", "tool/result", "turn/end"} {
		if !strings.Contains(k, want) {
			t.Fatalf("流程应记录 %s: %s", want, k)
		}
	}
	if !strings.HasSuffix(k, "turn/end") {
		t.Fatalf("回合应以 turn/end 收尾: %s", k)
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
