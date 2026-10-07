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
