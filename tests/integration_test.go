// Package tests 集成测试:完整装配(base bundle)→ 跑一轮 → 验证事件流与不变量。
package tests

import (
	"context"
	"log/slog"
	"testing"

	"github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildTestEnv 装配 base(启用 mock LLM + shell 工具),返回宿主组件。
func buildTestEnv(t *testing.T) (*ctx.Ctx, *plugin.Registry) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-system-prompt"},
		{ID: "llm-mock"},
		{ID: "tool-shell"},
		{ID: "host-agent-loop"},
	})
	if err := base.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartAll(c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c, reg
}

func TestEndToEndTurn(t *testing.T) {
	c, _ := buildTestEnv(t)

	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	if err := loop.Run(context.Background(), "请运行 echo 集成测试"); err != nil {
		t.Fatalf("turn 运行失败: %v", err)
	}

	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	evs := sessions.Replay()
	kinds := []string{}
	for _, ev := range evs {
		kinds = append(kinds, ev.Kind)
	}

	// 关键事件序列断言:输入→两步(工具调用+收尾)→结束
	requireKind(t, kinds, sdk.EventUserMessage)
	requireKind(t, kinds, sdk.EventAssistantMessage)
	requireKind(t, kinds, sdk.EventToolCall)
	requireKind(t, kinds, sdk.EventToolResult)
	requireKind(t, kinds, sdk.EventTurnEnd)
	// 单轮至少 2 步(工具调用请求 + 收尾请求)
	if count(kinds, sdk.EventStepStart) < 2 {
		t.Fatalf("单轮应含 ≥2 步,got %v", kinds)
	}

	// 模型可见即已记录:derive 消息可从日志重建
	msgs := sessions.DeriveMessages()
	if len(msgs) == 0 {
		t.Fatal("derive 历史为空(不变量破坏)")
	}
	// 最后一条 assistant 消息应含工具调用后的文本
	var last string
	for _, m := range msgs {
		if m.Role == sdk.RoleAssistant {
			last = m.Content
		}
	}
	if last == "" {
		t.Fatal("最终 assistant 回复为空")
	}
	// tool result 应作为 RoleTool 消息进入历史
	foundToolMsg := false
	for _, m := range msgs {
		if m.Role == sdk.RoleTool {
			foundToolMsg = true
			break
		}
	}
	if !foundToolMsg {
		t.Fatal("tool 结果未投影为 RoleTool 消息(模型将看不到工具结果)")
	}
}

func TestToolResultVisibleInDerive(t *testing.T) {
	c, _ := buildTestEnv(t)

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "shell", `{"command":"echo hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("shell 执行应成功,got error: %s", res.Error)
	}
	if res.Content == "" {
		t.Fatal("shell 结果为空")
	}
}

func TestVetoViaPreExecute(t *testing.T) {
	c, _ := buildTestEnv(t)
	// 注册一个策略:veto 所有 shell(pre-execute waterfall 拦截)
	vetoed := true
	c.Subscribe("tools/pre-execute", func(ctx context.Context, ev *sdk.Event) error {
		if call, ok := ev.Payload.(*sdk.ToolCallEvent); ok && call.Name == "shell" {
			return &policyErr{msg: "sandbox read-only"}
		}
		return nil
	})
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "shell", `{"command":"echo blocked"}`)
	if err != nil {
		t.Fatalf("流水线应吞掉 veto 为结构化结果,got err: %v", err)
	}
	if res.Error == "" || vetoed == false {
		t.Fatalf("veto 应转为结构化错误回传: %+v", res)
	}
}

type policyErr struct{ msg string }

func (e *policyErr) Error() string { return e.msg }

func requireKind(t *testing.T, kinds []string, kind string) {
	t.Helper()
	for _, k := range kinds {
		if k == kind {
			return
		}
	}
	t.Fatalf("事件流缺少 %s,got %v", kind, kinds)
}

func count(kinds []string, kind string) int {
	n := 0
	for _, k := range kinds {
		if k == kind {
			n++
		}
	}
	return n
}
