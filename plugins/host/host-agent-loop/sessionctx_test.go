package hostagentloop

// 「回合 → 审批/提问」的会话归属链路测试。
//
// 要钉的是**对外 id**:弹层要落在会话列表里的那一个会话上。
// 内部锁键是归一过的(空 = 主会话),直接吐它前端无处可对应 —— 与
// running_sessions 当初踩的同一个坑(所以那次把空键换成了 CurrentSession)。

import (
	"context"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubCS 只为 sessionIDOf 提供「当前打开的会话」。
type stubCS struct {
	sdk.CwdSessions
	cur string
}

func (s *stubCS) CurrentSession() string { return s.cur }

func TestSessionIDOfMapping(t *testing.T) {
	e := buildEnv(t, `[{"text":"ok","finish":"stop"}]`)
	// 无会话服务 ⇒ 只有非归一的 sid 能给出 id
	if got := e.loop.sessionIDOf("sA"); got != "sA" {
		t.Fatalf("显式 sid 应原样返回,得 %q", got)
	}
	if got := e.loop.sessionIDOf(""); got != "" {
		t.Fatalf("无会话服务时主会话应为空,得 %q", got)
	}
	e.loop.cs = &stubCS{cur: "cur-1"}
	if got := e.loop.sessionIDOf(""); got != "cur-1" {
		t.Fatalf("空 sid 应映射到当前打开的会话,得 %q", got)
	}
	if got := e.loop.sessionIDOf("cur-1"); got != "cur-1" {
		t.Fatalf("显式传当前会话 id 应归一后仍给出该 id,得 %q", got)
	}
	if got := e.loop.sessionIDOf("sB"); got != "sB" {
		t.Fatalf("非当前会话应原样,得 %q", got)
	}
}

// TestSessionIDReachesToolCtx 归属要能沿调用链走到工具里:审批/提问正是工具执行时
// 发起的(context 一路派生),工具都看不到会话 id 就说明链路断了。
func TestSessionIDReachesToolCtx(t *testing.T) {
	e, llm := buildEnvScripted(t, []string{"", "收尾"})
	rec := &probeTool{}
	e.tools.Register(rec)
	llm.calls = [][]sdk.ToolCall{{{ID: "toolu_1", Name: "probe", Arguments: `{}`}}}
	e.loop.cs = &stubCS{cur: "cur-9"}

	if err := e.loop.Run(context.Background(), "任务"); err != nil {
		t.Fatal(err)
	}
	if rec.seen != "cur-9" {
		t.Fatalf("工具执行时应能从 ctx 读到会话 id,得 %q", rec.seen)
	}
}

// TestSessionIDOnOtherSessionRun 跑在非当前会话上时,归属是那个会话自己的 id。
func TestSessionIDOnOtherSessionRun(t *testing.T) {
	e, llm := buildEnvScripted(t, []string{"", "收尾"})
	rec := &probeTool{}
	e.tools.Register(rec)
	llm.calls = [][]sdk.ToolCall{{{ID: "toolu_1", Name: "probe", Arguments: `{}`}}}
	e.loop.sdir = newMemDir("cur-1")
	e.loop.cs = &stubCS{cur: "cur-1"}

	if err := e.loop.RunInSession(context.Background(), "sX", "任务"); err != nil {
		t.Fatal(err)
	}
	if rec.seen != "sX" {
		t.Fatalf("非当前会话回合应带自己的 id,得 %q", rec.seen)
	}
}

// probeTool 记录执行时的会话归属。
type probeTool struct{ seen string }

func (p *probeTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "probe", Description: "探针", InputSchema: map[string]any{"type": "object"}}
}

func (p *probeTool) Execute(ctx context.Context, _ string) (any, error) {
	p.seen = sdk.SessionFromContext(ctx)
	return map[string]any{"ok": true}, nil
}

// TestOtherSessionRequestUsesItsOwnHistory 回归护栏:非当前会话跑回合时,模型请求里的历史
// 必须是**那个会话自己的**,不是主会话的。
//
// 这条钉的是一次真缺陷(第一百一十五批修):`step` 里组装请求时写的是 `l.sessions`
// (主单例)而不是本回合的日志 —— 单会话下两者是同一个对象,看不出问题;多会话并行
// (页签/多窗口)时,每个会话的模型看到的都是主会话的对话,而它自己的输入与工具结果
// 根本不在上下文里(实测 4 个会话的请求内容会一模一样)。
func TestOtherSessionRequestUsesItsOwnHistory(t *testing.T) {
	e, llm := buildEnvScripted(t, []string{"A1", "A2"})
	dir := newMemDir("cur-1")
	e.loop.sdir = dir
	e.loop.cs = &stubCS{cur: "cur-1"}

	if err := e.loop.RunInSession(context.Background(), "sA", "A 的第一个问题"); err != nil {
		t.Fatal(err)
	}
	if err := e.loop.RunInSession(context.Background(), "sA", "A 的第二个问题"); err != nil {
		t.Fatal(err)
	}
	// 另一个会话的内容不能出现在 sA 的请求里
	if err := e.loop.RunInSession(context.Background(), "sB", "B 的问题"); err != nil {
		t.Fatal(err)
	}
	if len(llm.requests) < 3 {
		t.Fatalf("应至少 3 次请求,实得 %d", len(llm.requests))
	}
	last := llm.requests[len(llm.requests)-1] // B 的第一次请求
	var text []string
	for _, m := range last {
		text = append(text, m.Content)
	}
	joined := strings.Join(text, "\n")
	if !strings.Contains(joined, "B 的问题") {
		t.Fatalf("B 的请求应含自己的输入,实得 %v", text)
	}
	if strings.Contains(joined, "A 的问题") {
		t.Fatalf("B 的请求混进了别的会话历史(跨会话串上下文):%v", text)
	}
}

// TestAssembleUsesSessionScopedExtensions 组装请求时优先用**按会话**的可选扩展:
// 工具清单(ListFor)与系统提示(AssembleFor)都要按那次调用所属会话解析 —— 否则角色
// 在别的页签排除的工具/技能/指令,在本页签仍然可见(角色的收窄静默失效)。
func TestAssembleUsesSessionScopedExtensions(t *testing.T) {
	e, llm := buildEnvScripted(t, []string{"ok"})
	// 假扩展:把"本会话 id"塞进工具名与系统提示,便于断言确实走了它们
	tools := &ctxTools{inner: e.tools}
	sp := &ctxSP{inner: e.sp}
	e.loop.tools = tools
	e.loop.sp = sp
	dir := newMemDir("cur-1")
	e.loop.sdir = dir
	e.loop.cs = &stubCS{cur: "cur-1"}

	if err := e.loop.RunInSession(context.Background(), "sA", "任务"); err != nil {
		t.Fatal(err)
	}
	if len(llm.requests) != 1 {
		t.Fatalf("一次请求,实得 %d", len(llm.requests))
	}
	// scriptedLLM 分别记录每趟请求的消息与工具清单
	if len(llm.toolSets) != 1 || len(llm.toolSets[0]) == 0 || llm.toolSets[0][0].Name != "tools@sA" {
		t.Fatalf("工具清单应来自 ListFor(本会话 sA),得 %+v", llm.toolSets)
	}
	msgs := llm.requests[0]
	found := false
	for _, m := range msgs {
		if m.Role == sdk.RoleSystem && strings.Contains(m.Content, "assemble@sA") {
			found = true
		}
	}
	if !found {
		t.Fatal("系统提示应来自 AssembleFor(本会话 sA)")
	}
}

// ctxTools 实现 sdk.ContextualToolCatalogue(工具名后缀带会话 id)。
type ctxTools struct {
	sdk.ToolRegistry
	inner sdk.ToolRegistry
}

// 其余方法转发到 inner(内嵌接口是 nil,直接调会 panic —— 替身必须显式转发)。
func (c *ctxTools) List() []sdk.ToolDefinition { return c.inner.List() }

func (c *ctxTools) SetContextFilter(func(context.Context, sdk.ToolDefinition) bool) sdk.Disposer {
	return func() {}
}
func (c *ctxTools) ListFor(ctx context.Context) []sdk.ToolDefinition {
	defs := c.inner.List()
	out := make([]sdk.ToolDefinition, 0, len(defs))
	for _, d := range defs {
		d.Name = "tools@" + sdk.SessionFromContext(ctx)
		out = append(out, d)
	}
	return out
}

// ctxSP 实现 sdk.ContextualSystemPrompt(系统提示里塞入会话 id)。
type ctxSP struct {
	sdk.SystemPromptService
	inner sdk.SystemPromptService
}

// Assemble 转发(inner 是真实实现)。
func (c *ctxSP) Assemble(h []sdk.LLMMessage, tools []sdk.ToolDefinition) []sdk.LLMMessage {
	return c.inner.Assemble(h, tools)
}

func (c *ctxSP) AssembleFor(ctx context.Context, h []sdk.LLMMessage, tools []sdk.ToolDefinition) []sdk.LLMMessage {
	out := c.inner.Assemble(h, tools)
	return append([]sdk.LLMMessage{{Role: sdk.RoleSystem, Content: "assemble@" + sdk.SessionFromContext(ctx)}}, out...)
}
