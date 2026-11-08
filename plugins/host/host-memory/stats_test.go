package hostmemory

// 累计成本账 + 试查的宿主侧契约(B 批)。
//
// 钉的是三件"数字会悄悄错"的事:
//   ① 分母是**请求**、命中只算"真的追加了条目",回退(没查询/关闭)要与零命中分开;
//   ② 稳定块体积来自 section 的真实渲染(不是条目长度之和),否则每请求成本会低估;
//   ③ 试查与 Augment 必须走**同一份快照/同一个预算/同一个 Rank** —— 试查给出
//      "看起来对、实际不一致"的答案,比没有试查更坏。

import (
	"context"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/memory"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func augmentNoQuery(t *testing.T, c sdk.Ctx) {
	t.Helper()
	var aug sdk.MessageAugmenter
	if err := c.Inject("ctx.messageAugment", &aug); err != nil || aug == nil {
		t.Fatalf("ctx.messageAugment 未装配: %v", err)
	}
	aug.Augment(context.Background(), nil)
}

func TestInjectLedgerCountsRequestsNotTurns(t *testing.T) {
	// 稳定块只放得下最新一条(预算 = 壳 + 一条,且两条等长)⇒ 查旧那条时它只能靠
	// 尾部追加,这样"命中"才真的对应"追加了条目",而不是被稳定块吃掉。
	// (注意 collectWithin 是**新的优先、放不下就停**,所以两条必须等长才可控。)
	const older, newer = "部署统一走 make deploy", "发布前先跑 gate 与门"
	one := len("  · ") + len(older)
	c, svc := startServiceWith(t, map[string]any{"budget_bytes": int(memory.ShellOverhead) + one})
	if err := svc.Add(older, ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.Add(newer, ""); err != nil {
		t.Fatal(err)
	}
	// 一次命中(旧的那条不在稳定块里 ⇒ 必须靠尾部追加)
	augmentWithQuery(t, c, "部署")
	// 一次零命中:与库里毫无词面交集的句子
	augmentWithQuery(t, c, "量子色动力学胶子禁闭")
	// 一次回退:没有查询上下文
	augmentNoQuery(t, c)

	led := svc.InjectLedger()
	if led.Requests != 3 {
		t.Fatalf("分母应是请求数 3, got %d", led.Requests)
	}
	if led.Fallbacks != 1 {
		t.Fatalf("只有没查询那一次算回退, got %d", led.Fallbacks)
	}
	if led.Hits != 1 {
		t.Fatalf("只有真的追加了条目的那次算命中, got %d", led.Hits)
	}
	if led.StableBytes == 0 {
		t.Fatal("稳定块体积应来自 section 的真实渲染,不该是 0")
	}
	// 必须**等于真实渲染长度**(不是条目字节之和、也不是常量估算)——成本账的分母
	// 就在这里,估错一个字段整本账都是假数。
	if got := len(svc.section()); led.StableBytes != got {
		t.Fatalf("稳定块字节应为 section 的真实长度 %d, got %d", got, led.StableBytes)
	}
	if !strings.Contains(led.Line, "请求") {
		t.Fatalf("应有一行口径描述: %q", led.Line)
	}
}

// 节流落盘 + 卸载兜底:次数够了要落盘,卸载时必须落盘(否则这批计数全丢)。
func TestInjectLedgerPersistsOnFlushAndDispose(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	c, svc := startServiceWith(t, nil)
	if err := svc.Add("部署统一走 make deploy", ""); err != nil {
		t.Fatal(err)
	}
	augmentWithQuery(t, c, "部署")
	// 卸载:Disposer 里必须落盘
	var d sdk.Disposer
	svc.c = c
	d = func() { svc.flushStats(true) }
	d()

	st, err := memory.LoadInjectStats()
	if err != nil {
		t.Fatal(err)
	}
	if st.User.Requests == 0 && len(st.Projects) == 0 {
		t.Fatalf("卸载后账本应已落盘: %+v", st)
	}
}

func TestProbeMirrorsAugment(t *testing.T) {
	// 稳定块与尾部预算都故意压小:预算口径若与 Augment 不一致(比如用了稳定块预算
	// 而不是检索预算),试查"会注入"的条数就会多于尾部真的追加的条数 —— 这就是这里要钉的。
	c, svc := startServiceWith(t, map[string]any{
		"budget_bytes":    int(memory.ShellOverhead) + 40,
		"retrieve_budget": int(memory.ShellOverhead) + 70,
	})
	for _, s := range []string{"部署统一走 make deploy", "发布前先跑 gate 与门", "回收站落在数据根内", "提交前先跑门禁脚本"} {
		if err := svc.Add(s, ""); err != nil {
			t.Fatal(err)
		}
	}
	const q = "部署 发布 回收站 提交"
	_, tail := augmentWithQuery(t, c, q)
	res, err := svc.Probe(q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("至少应有一条命中(否则下面的一致性断言是空的)")
	}
	// 试查说"会注入"的条数,必须与 Augment 真的追加的条数一致(同一预算、同一 Rank)。
	injected := 0
	for _, h := range res.Hits {
		// 真正会追加 = 预算内选中 **且** 不在稳定块里(与 /memory probe 的 ✓ 同一判据)。
		if h.Selected && !h.Stable {
			injected++
		}
	}
	if got := strings.Count(tail, "  · "); got != injected {
		t.Fatalf("试查说注入 %d 条,实际尾部追加 %d 条(口径不一致)", injected, got)
	}
}

func TestProbeEmptyAndUnrelated(t *testing.T) {
	_, svc := startServiceWith(t, nil)
	if err := svc.Add("部署统一走 make deploy", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Probe("   "); err == nil {
		t.Fatal("空查询应显式报错(否则会给出一个'零命中'的假答案)")
	}
	res, err := svc.Probe("量子色动力学胶子禁闭")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 || res.Total != 0 {
		t.Fatalf("无关句子应零命中: %+v", res)
	}
}
