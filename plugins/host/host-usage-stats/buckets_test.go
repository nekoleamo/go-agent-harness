package hostusagestats

// 用量按会话分桶(第一百一十六批)。
//
// 这组测试钉的是一次**既有缺陷**的修复:原先是一个全局累加器 + 切会话 Reset,
// 且只订阅主会话事件名(非主会话的用量压根收不到)。多会话并行(页签)下表现为
// 「页签 B 切一下,页签 A 的 token 数当场归零 / 根本不累计」。

import (
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// newSvc 造一个空统计服务(不装配插件 —— 这些测的是纯逻辑)。
func newSvc(t *testing.T) *Service {
	t.Helper()
	return &Service{}
}

// RecordUsage 测试入口:等价于订阅 usage/recorded 时的处理。
func (s *Service) RecordUsage(ue sdk.UsageEvent) { s.add(ue.Session, ue.Model, ue.Usage) }

func TestUsageBucketedPerSession(t *testing.T) {
	s := newSvc(t)
	s.RecordUsage(sdk.UsageEvent{Session: "A", Model: "m1", Usage: sdk.Usage{PromptTokens: 100, CompletionTokens: 10}})
	s.RecordUsage(sdk.UsageEvent{Session: "A", Model: "m1", Usage: sdk.Usage{PromptTokens: 50, CompletionTokens: 5}})
	s.RecordUsage(sdk.UsageEvent{Session: "B", Model: "m2", Usage: sdk.Usage{PromptTokens: 7}})

	a := s.StatsFor("A")
	if a.PromptTokens != 150 || a.CompletionTokens != 15 || a.Requests != 2 {
		t.Fatalf("会话 A 应累计 150/15/2,得 %+v", a)
	}
	if a.LastPromptTokens != 50 {
		t.Fatalf("上下文占用应是最近一次 prompt(50),得 %d", a.LastPromptTokens)
	}
	b := s.StatsFor("B")
	if b.PromptTokens != 7 || b.Requests != 1 {
		t.Fatalf("会话 B 应是 7/1,得 %+v", b)
	}
	// 空会话 = 零值(不是别的会话的数字)
	if z := s.StatsFor("Z"); z.PromptTokens != 0 || z.Requests != 0 {
		t.Fatalf("未花过费的会话应为零值,得 %+v", z)
	}
}

// 窗口按模型解析,且各会话用自己那次请求的模型。
func TestUsageWindowFollowsSessionModel(t *testing.T) {
	s := newSvc(t)
	s.RecordUsage(sdk.UsageEvent{Session: "A", Model: "gpt-x", Usage: sdk.Usage{PromptTokens: 10}})
	s.RecordUsage(sdk.UsageEvent{Session: "B", Model: "gpt-y", Usage: sdk.Usage{PromptTokens: 10}})
	// 用**配置层的两个必然不同**的窗口,把"窗口按哪个会话解析"钉死(内置表可能两个模型同窗口,
	// 那种情况下旧实现也能过 —— 去掉 skip 才能反向验证)。
	s2 := &Service{extraWindows: map[string]int{"gpt-x": 100_000, "gpt-y": 200_000}}
	s2.RecordUsage(sdk.UsageEvent{Session: "A", Model: "gpt-x", Usage: sdk.Usage{PromptTokens: 10}})
	s2.RecordUsage(sdk.UsageEvent{Session: "B", Model: "gpt-y", Usage: sdk.Usage{PromptTokens: 10}})
	if got := s2.StatsFor("A").Window; got != 100_000 {
		t.Fatalf("A 的窗口应取 A 自己的模型(gpt-x=100000),得 %d —— 拿别的会话的模型算占用率会差几倍", got)
	}
	if got := s2.StatsFor("B").Window; got != 200_000 {
		t.Fatalf("B 的窗口应取 B 自己的模型(gpt-y=200000),得 %d", got)
	}
	wa, wb := s.StatsFor("A").Window, s.StatsFor("B").Window
	if wa == wb {
		t.Skip("两个模型的解析窗口相同(内置表里一致),本条只保证不因会话串味")
	}
	if wb == 0 {
		t.Fatalf("B 用 gpt-y,窗口不该是未知,得 %d", wb)
	}
}

// 会话事件通道(主单例)与实例级通道**不重复计数**。
func TestUsageNotDoubleCounted(t *testing.T) {
	s := newSvc(t)
	ue := sdk.UsageEvent{Session: "A", Model: "m1", Usage: sdk.Usage{PromptTokens: 30}}
	// agent-loop 会同时:追加会话事件(Log 广播) + Emit usage/recorded
	s.HandleSessionEvent(&sdk.SessionEvent{Kind: sdk.EventUsage, Payload: ue})
	s.RecordUsage(ue)
	if got := s.StatsFor("A").PromptTokens; got != 30 {
		t.Fatalf("同一笔用量被计了两次:%d(应 30)", got)
	}
	// 极老日志的载荷是**裸 sdk.Usage**(没有模型名):那条仍走会话事件通道,
	// 归**主会话桶**(空键)。注意:现代写入方永远发 UsageEvent(且同时发 usage/recorded),
	// 所以这个分支不会与实例通道重叠。
	s.HandleSessionEvent(&sdk.SessionEvent{Kind: sdk.EventUsage, Payload: sdk.Usage{PromptTokens: 9}})
	if got := s.StatsFor("").PromptTokens; got != 9 {
		t.Fatalf("老格式(裸 Usage)应记到主会话桶:%d(应 9)", got)
	}
}

// Reset 只清指定的会话(清主会话不影响别的会话的桶)。
func TestResetForIsScoped(t *testing.T) {
	s := newSvc(t)
	s.RecordUsage(sdk.UsageEvent{Session: "", Usage: sdk.Usage{PromptTokens: 5}})
	s.RecordUsage(sdk.UsageEvent{Session: "A", Usage: sdk.Usage{PromptTokens: 8}})
	s.ResetFor("")
	if s.StatsFor("").PromptTokens != 0 {
		t.Fatal("主会话桶应被清零")
	}
	if s.StatsFor("A").PromptTokens != 8 {
		t.Fatal("A 的桶不该被主会话的 Reset 清掉")
	}
}

// 主会话的用量**不得双计**(2026-10-24 review 的 P1)。
//
// 主会话的 SessionFromContext 返回 ""，而老实现只在 `ue.Session != ""` 时跳过 session 事件
// ⇒ 主会话的每笔用量被"会话事件 + usage/recorded"两条通道各记一次(累计 ×2)。
// **反向验证**:把 HandleSessionEvent 的 return 改回"仅 Session 非空才 return" ⇒ 本用例红。
func TestMainSessionUsageNotDoubleCounted(t *testing.T) {
	s := &Service{}
	// agent-loop 对**同一笔**用量做的事:append 一条 session/usage(带 UsageEvent)+ 发一条 usage/recorded
	ue := sdk.UsageEvent{Session: "", Usage: sdk.Usage{PromptTokens: 30, CompletionTokens: 3}}
	s.HandleSessionEvent(&sdk.SessionEvent{Kind: sdk.EventUsage, Payload: ue})
	s.add(ue.Session, ue.Model, ue.Usage) // 等价于 usage/recorded 通道
	if got := s.StatsFor("").PromptTokens; got != 30 {
		t.Fatalf("主会话用量被双计:PromptTokens=%d,应为 30", got)
	}
}
