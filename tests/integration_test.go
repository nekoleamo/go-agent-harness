// Package tests 集成测试:完整装配(base bundle)→ 跑一轮 → 验证事件流与不变量。
package tests

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/plugins/catalogue"
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
		{ID: "policy-sandbox", Data: map[string]any{"mode": "workspace-write"}},
		{ID: "host-agent-loop"},
	})
	// 内部服务(boot 同款,见 cmd/gah)
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := base.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c, reg
}

func catalogueInfoForTest() map[string]sdk.PluginInfo {
	out := make(map[string]sdk.PluginInfo)
	for id, d := range catalogue.All {
		out[id] = sdk.PluginInfo{ID: id, Type: d.Manifest.Type, Bundle: d.Bundle}
	}
	return out
}

func enabledSetForTest(tree *config.Tree) map[string]bool {
	set := make(map[string]bool)
	for _, id := range tree.List() {
		if tree.Enabled(id) {
			set[id] = true
		}
	}
	return set
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

func TestTurnCancelled(t *testing.T) {
	c, _ := buildTestEnv(t)
	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消:取消链应在第一步生效
	err := loop.Run(ctx, "hi")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应传播到 turn,got %v", err)
	}
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range sessions.Replay() {
		if ev.Kind == sdk.EventTurnEnd && fmt.Sprint(ev.Payload) == "cancelled" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("会话应记录 cancelled 的 turn/end: %v", sessions.Replay())
	}
}

func TestSandboxReadOnlyBlocksShell(t *testing.T) {
	c, _ := buildTestEnv(t)
	var sb sdk.Sandbox
	if err := c.Inject("ctx.sandbox", &sb); err != nil {
		t.Fatal(err)
	}
	// 默认 workspace-write:shell 放行
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "shell", `{"command":"echo ok"}`)
	if err != nil || res.Error != "" {
		t.Fatalf("workspace-write 下 shell 应放行,err=%v res=%+v", err, res)
	}
	// read-only:shell 被 veto(structured 错误,不中断)
	sb.SetMode(sdk.SandboxReadOnly)
	res, err = tools.Execute(context.Background(), "shell", `{"command":"echo blocked"}`)
	if err != nil {
		t.Fatalf("veto 应转为结构化结果,got err: %v", err)
	}
	if res.Error == "" {
		t.Fatalf("read-only 下 shell 应被拦截: %+v", res)
	}
	// 切回 full-access:放行
	sb.SetMode(sdk.SandboxFullAccess)
	res, err = tools.Execute(context.Background(), "shell", `{"command":"echo free"}`)
	if err != nil || res.Error != "" {
		t.Fatalf("full-access 下 shell 应放行,err=%v res=%+v", err, res)
	}
}

// TestPluginUnloadMatrix 插拔解耦矩阵:核心宿主卸载被依赖保护拒卸(提示含原因);
// 叶子插件逐个卸载后宿主存活、回合可继续;卸载后调用已卸工具给结构化可操作提示;
// LLM 适配器全卸后回合显式失败且提示明确(不静默)。
func TestPluginUnloadMatrix(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir()) // host-bridge 默认目录隔离
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	tree := config.NewTree()
	b, err := config.ReadBundle("../config/bundle-base.yaml") // tests 包 cwd
	if err != nil {
		t.Fatal(err)
	}
	tree.Apply(b.Entries)
	// 与 headless 默认一致:禁真实适配器,仅 llm-mock(避免矩阵测试打外网 401)
	off, on := false, true
	tree.Apply([]config.Entry{
		{ID: "llm-openai-compat", Enabled: &off},
		{ID: "llm-mock", Enabled: &on},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := base.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.DisposeAll)

	var mgr sdk.PluginManager
	if err := c.Inject("ctx.pluginManager", &mgr); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}

	// 1) 被依赖者:卸载必须被依赖保护拒绝,提示含依赖者与可操作路径
	// (依赖保护只作用被依赖者;无依赖者的宿主服务可卸,卸后注入引用仍可用)
	for _, id := range []string{"host-tools", "host-llm", "host-session-log", "host-system-prompt"} {
		err := mgr.Unload(id)
		if err == nil {
			t.Fatalf("%s 应被依赖保护拒绝卸载", id)
		}
		if !strings.Contains(err.Error(), "依赖") {
			t.Fatalf("%s 拒绝提示应含原因与操作: %v", id, err)
		}
	}

	// 2) 叶子/无依赖者逐个卸载:宿主存活 + 回合可继续(不 panic)
	// 含无依赖者的宿主服务(agent-loop/plugin-manager/cwd-sessions 卸载无副作用,引用仍可用)
	leaves := []string{"policy-approval", "tool-shell", "host-skills", "host-jobs", "host-bridge", "mcp-bridge", "policy-sandbox", "host-agent-loop", "host-plugin-manager", "host-cwd-sessions"}
	for _, id := range leaves {
		if err := mgr.Unload(id); err != nil {
			t.Fatalf("卸载 %s 失败: %v", id, err)
		}
		if err := loop.Run(context.Background(), "卸载 "+id+" 后的回合"); err != nil {
			t.Fatalf("卸载 %s 后回合应可继续: %v", id, err)
		}
	}

	// 3) 卸载后调用已卸工具:结构化错误 + 可操作提示(指向排查入口)
	res, err := tools.Execute(context.Background(), "shell", `{"command":"echo x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "不存在") || !strings.Contains(res.Error, "排查") {
		t.Fatalf("卸载工具提示应明确可操作: %s", res.Error)
	}

	// 4) LLM 适配器全卸:回合显式失败,提示说明原因(不再静默)
	if err := mgr.Unload("llm-openai-compat"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Unload("llm-mock"); err != nil {
		t.Fatal(err)
	}
	err = loop.Run(context.Background(), "无适配器回合")
	if err == nil {
		t.Fatal("无 LLM 适配器时回合应显式失败")
	}
	if !strings.Contains(err.Error(), "无可用 LLM 适配器") {
		t.Fatalf("提示应明确: %v", err)
	}

	// 5) 大量卸载后宿主仍可查询(plugin manager 存活,不 panic)
	if len(mgr.List()) == 0 {
		t.Fatal("宿主应仍可列出插件")
	}
}

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
