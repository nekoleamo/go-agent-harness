// IM 桥端到端(P0-2a):真实宿主装配(session-log/llm-mock/tools/commands/system-prompt/agent-loop/policy-guard)
// + im.Bridge + mock transport —— 入站消息 → 真实 agent 回合 → 输出聚合回推;
// smart 审批确认经 IM 文字回答闭环(批准/拒绝两分支)。
package tests

import (
	"context"
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
	"github.com/nekoleamo/go-agent-harness/im"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeShell 进程内假 shell 工具(触发 policy 危险命令审批;不真实执行)。
type fakeShell struct{}

func (f *fakeShell) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "shell", Description: "执行命令(fake,e2e)", InputSchema: map[string]any{"type": "object"}}
}
func (f *fakeShell) Execute(_ context.Context, _ string) (any, error) {
	return map[string]any{"ok": true, "out": "(fake)"}, nil
}

// e2eTransport 记录出站文本。
type e2eTransport struct {
	mu    sync.Mutex
	sends []string
}

func (t *e2eTransport) Name() string { return "mock" }
func (t *e2eTransport) SendText(_ context.Context, _ im.Route, text string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sends = append(t.sends, text)
	return nil
}
func (t *e2eTransport) sent() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.sends))
	copy(out, t.sends)
	return out
}
func (t *e2eTransport) waitContains(tb testing.TB, substr string, timeout time.Duration) string {
	tb.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, s := range t.sent() {
			if strings.Contains(s, substr) {
				return s
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	tb.Fatalf("超时未收到含 %q 的消息(已收: %v)", substr, t.sent())
	return ""
}

const dangerScript = `[
  {"tool":{"name":"shell","args":"rm -rf /tmp/im-e2e-x"}},
  {"text":"已清理临时文件","finish":"stop"}
]`

// buildImTestEnv 真实装配(除 ui 外同 web e2e 的最小服务集)+ im 桥(confirm 后注入,policy 每次现取)。
func buildImTestEnv(t *testing.T) (*ctx.Ctx, *im.Bridge, *e2eTransport, sdk.SessionLog) {
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
		{ID: "host-commands"},
		{ID: "host-system-prompt"},
		{ID: "llm-mock", Data: map[string]any{"script": dangerScript}},
		{ID: "policy-guard", Data: map[string]any{"approval": "smart", "sandbox": "workspace-write", "sync": true}},
		{ID: "host-agent-loop"},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := baseb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })

	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	// 假 shell(触发 policy 危险审批;危险确认后返回成功)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	tools.Register(&fakeShell{})

	tr := &e2eTransport{}
	b := im.New(nil, loop, sessions, tr, im.Options{
		Mode:  im.AccessAllowlist,
		Allow: []string{"mock\x00owner"},
	})
	var turn sdk.TurnControl
	_ = c.Inject("ctx.turnControl", &turn)
	if turn != nil {
		b.SetTurnControl(turn)
	}
	// ctx.confirm = im 桥(policy 每次 pre-execute 现取,后注入亦生效)
	if err := c.Provide("ctx.confirm", b); err != nil {
		t.Fatal(err)
	}
	return c, b, tr, sessions
}

func ownerIn(msgID, text string) im.Inbound {
	return im.Inbound{Route: im.Route{Channel: "mock", UserID: "owner", ChatID: "owner"}, MsgID: msgID, Text: text}
}

// TestImE2EApproveFlow smart 审批经 IM 批准:危险命令确认 → 回复 y → 工具执行 → 回合完成回推最终文本。
func TestImE2EApproveFlow(t *testing.T) {
	_, b, tr, _ := buildImTestEnv(t)
	done := make(chan error, 1)
	go func() { done <- b.HandleInbound(context.Background(), ownerIn("1", "帮我清理临时文件")) }()

	// 1. 审批确认消息推送
	tr.waitContains(t, "需要确认", 8*time.Second)
	// 2. 用户文字批准
	if err := b.HandleInbound(context.Background(), ownerIn("2", "y")); err != nil {
		t.Fatal(err)
	}
	// 3. 回合完成回推最终文本
	final := tr.waitContains(t, "已清理临时文件", 8*time.Second)
	if !strings.Contains(final, "已清理临时文件") {
		t.Fatalf("最终回复不符: %q", final)
	}
	if err := <-done; err != nil {
		t.Fatalf("回合应完成: %v", err)
	}
}

// TestImE2EDenyFlow smart 审批经 IM 拒绝:回复 n → 工具被 veto(tool/result Error 回模型)→
// 回合继续由模型处理(mock 静态文案收尾),会话日志留有拒绝证据。
func TestImE2EDenyFlow(t *testing.T) {
	_, b, tr, sessions := buildImTestEnv(t)
	done := make(chan error, 1)
	go func() { done <- b.HandleInbound(context.Background(), ownerIn("1", "帮我清理临时文件")) }()
	tr.waitContains(t, "需要确认", 8*time.Second)
	if err := b.HandleInbound(context.Background(), ownerIn("2", "n")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("拒绝为工具错误回模型,回合应正常结束: %v", err)
	}
	// 铁证:会话日志中该工具调用 tool/result 为拒绝错误(未执行)
	var denied bool
	for _, ev := range sessions.Replay() {
		if ev.Kind != sdk.EventToolResult {
			continue
		}
		tr2, ok := ev.Payload.(sdk.ToolResultEvent)
		if ok && strings.Contains(tr2.Error, "拒绝") {
			denied = true
		}
	}
	if !denied {
		t.Fatal("会话日志应留有拒绝的 tool/result 错误")
	}
}

// TestImE2EHostCommandsRegistered 桥命令注册进宿主 ctx.commands(/stop /im;disposer 撤销)。
func TestImE2EHostCommandsRegistered(t *testing.T) {
	c, b, _, _ := buildImTestEnv(t)
	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	unreg, err := b.RegisterCommands(cmds)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stop", "im"} {
		if _, ok := cmds.Get(name); !ok {
			t.Fatalf("命令 /%s 应已注册", name)
		}
	}
	// 执行 /im status(经宿主命令表)
	if spec, ok := cmds.Get("im"); !ok {
		t.Fatal("/im 命令缺失")
	} else if out, err := spec.Run([]string{"status"}); err != nil || !strings.Contains(out, "模式=allowlist") {
		t.Fatalf("/im status 执行失败: out=%q err=%v", out, err)
	}
	unreg()
	if _, ok := cmds.Get("stop"); ok {
		t.Fatal("撤销后 /stop 应不可见")
	}
}

// TestImE2EUnknownUserBlocked 未授权用户消息被静默丢弃(不进入回合、无任何回复)。
func TestImE2EUnknownUserBlocked(t *testing.T) {
	_, b, tr, _ := buildImTestEnv(t)
	if err := b.HandleInbound(context.Background(), im.Inbound{
		Route: im.Route{Channel: "mock", UserID: "stranger", ChatID: "stranger"},
		MsgID: "1", Text: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // 留窗口观察(应无出站)
	if n := len(tr.sent()); n != 0 {
		t.Fatalf("未授权消息应静默,收到 %d 条: %v", n, tr.sent())
	}
}
