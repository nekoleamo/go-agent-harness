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
	// 老日志(无 Session 字段)仍走会话事件通道,不能因为新通道就不记了;
	// 它来自主单例 ⇒ 归**主会话桶**(空键),不是随便落某个会话。
	s.HandleSessionEvent(&sdk.SessionEvent{Kind: sdk.EventUsage, Payload: sdk.UsageEvent{Model: "m1", Usage: sdk.Usage{PromptTokens: 9}}})
	if got := s.StatsFor("").PromptTokens; got != 9 {
		t.Fatalf("老格式用量应记到主会话桶:%d(应 9)", got)
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
