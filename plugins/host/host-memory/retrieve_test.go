package hostmemory

// 自动调取的宿主侧契约(P0)。
//
// 钉的是五件会"悄悄错"的事:
//   ① 记忆超过稳定块预算时,**旧但相关**的条目能被调取(这正是自动调取要解决的事);
//   ② 不相关的不进尾部(自动调取不是"把最近几条再发一遍");
//   ③ 稳定块里已有的条目不在尾部重复(同一句出现两遍会被当成两条事实);
//   ④ 没查询 / 零命中 / 命中但放不进预算,三者在观测值上可区分;
//   ⑤ 未知的 data.retrieve 显式失败,不静默降级。

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/internal/memory"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// startServiceWith 用给定 data 装配(host-system-prompt + host-memory)。
func startServiceWith(t *testing.T, data map[string]any) (sdk.Ctx, *Service) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	man := &sdk.Manifest{}
	if data != nil {
		man.Data = data
	}
	if _, err := (&Plugin{}).Start(c, man); err != nil {
		t.Fatal(err)
	}
	var ms sdk.MemoryService
	if err := c.Inject("ctx.memory", &ms); err != nil {
		t.Fatal(err)
	}
	return c, ms.(*Service)
}

// augmentWithQuery 模拟 agent-loop 的组装链:系统提示(稳定块)+ 尾部追加(本轮相关)。
// 返回 (系统提示全文, 尾部追加部分)。
func augmentWithQuery(t *testing.T, c sdk.Ctx, q string) (prompt string, tail string) {
	t.Helper()
	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		t.Fatal(err)
	}
	cp, ok := sp.(sdk.ContextualSystemPrompt)
	if !ok {
		t.Fatal("host-system-prompt 应实现 ContextualSystemPrompt")
	}
	qctx := sdk.WithQuery(context.Background(), q)
	msgs := cp.AssembleFor(qctx, nil, nil)
	var aug sdk.MessageAugmenter
	if err := c.Inject("ctx.messageAugment", &aug); err != nil || aug == nil {
		t.Fatalf("ctx.messageAugment 未装配: %v", err)
	}
	all := aug.Augment(qctx, msgs)
	return concat(msgs), concat(all[len(msgs):])
}

func injectStats(t *testing.T, svc *Service) sdk.MemoryInjectStats {
	t.Helper()
	rs, ok := any(svc).(sdk.MemoryRetrieveStats)
	if !ok {
		t.Fatal("host-memory 应实现 sdk.MemoryRetrieveStats")
	}
	return rs.LastInject()
}

// TestAugmentRetrievesOldButRelevant:
// 稳定块(mini 预算)只放最新一条;查询命中的是**更旧**那条 —— 必须能被调取。
//
// 这条是自动调取的存在理由:原先只能注入"最新的 N 条",旧的偏好永远排不上队。
func TestAugmentRetrievesOldButRelevant(t *testing.T) {
	// 稳定块预算 380 字节(扣掉壳开销 320 后只剩 60)⇒ 只放得下最新一条。
	c, svc := startServiceWith(t, map[string]any{"budget_bytes": 380})
	for _, s := range []string{
		"部署统一走 make deploy,不要手动 scp", // 旧:稳定块装不下,靠检索
		"报告配色用蓝灰,不要渐变",               // 无关
		"提交前跑 gate.sh",               // 最新:占住稳定块
	} {
		if err := svc.Add(s, ""); err != nil {
			t.Fatal(err)
		}
	}
	prompt, tail := augmentWithQuery(t, c, "怎么部署")
	if !strings.Contains(prompt, "gate.sh") {
		t.Fatalf("稳定块应带最新那条:\n%s", prompt)
	}
	if !strings.Contains(tail, "make deploy") {
		t.Fatalf("旧但相关的记忆应被尾调取:\n%s", tail)
	}
	if strings.Contains(tail, "报告配色") {
		t.Fatalf("无关条目不该占尾部预算:\n%s", tail)
	}
	if !strings.Contains(tail, "相关记忆") || !strings.Contains(tail, "不是用户发言") {
		t.Fatalf("尾部块应带抬头与「非用户发言」标注:\n%s", tail)
	}
	if st := injectStats(t, svc); st.Mode != retrievePerStep || st.Items != 1 || st.Stable != 1 {
		t.Fatalf("观测值应记 稳定块=1 尾部=1,got %+v", st)
	}
}

// TestAugmentSkipsEntriesAlreadyInStableBlock 默认预算下记忆全在稳定块里 ⇒ 尾部不再重复。
func TestAugmentSkipsEntriesAlreadyInStableBlock(t *testing.T) {
	c, svc := startServiceWith(t, nil)
	if err := svc.Add("部署统一走 make deploy", ""); err != nil {
		t.Fatal(err)
	}
	_, tail := augmentWithQuery(t, c, "怎么部署")
	if strings.Contains(tail, "<memory>") {
		t.Fatalf("稳定块里已有的条目不该在尾部重复注入:\n%s", tail)
	}
	if st := injectStats(t, svc); st.Stable != 1 || st.Items != 0 || st.Total != 1 {
		t.Fatalf("观测值应能说明「命中但已在稳定块」,got %+v", st)
	}
}

func TestAugmentNoHitRecordsZero(t *testing.T) {
	c, svc := startServiceWith(t, nil)
	if err := svc.Add("部署统一走 make deploy", ""); err != nil {
		t.Fatal(err)
	}
	_, tail := augmentWithQuery(t, c, "数据库连接池怎么配")
	if strings.Contains(tail, "相关记忆") || strings.Contains(tail, "<memory>") {
		t.Fatalf("零命中不该追加任何记忆块(不摆空壳):\n%s", tail)
	}
	st := injectStats(t, svc)
	if st.Items != 0 || st.Total != 0 || st.Query == "" {
		t.Fatalf("零命中也要在观测值里可见(否则分不清没记忆还是没命中),got %+v", st)
	}
}

func TestNoQueryFallsBackToStableBlockOnly(t *testing.T) {
	c, svc := startServiceWith(t, nil)
	if err := svc.Add("报告配色用蓝灰", ""); err != nil {
		t.Fatal(err)
	}
	// 无上下文路径(Assemble):稳定块仍在。
	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		t.Fatal(err)
	}
	if txt := concat(sp.Assemble(nil, nil)); !strings.Contains(txt, "报告配色用蓝灰") {
		t.Fatalf("无上下文路径应仍由稳定块承载记忆:\n%s", txt)
	}
	// 有 ctx 但没有查询(斜杠命令):不追加尾部。
	_, tail := augmentWithQuery(t, c, "")
	if strings.Contains(tail, "<memory>") {
		t.Fatalf("没有查询时不该追加尾部块:\n%s", tail)
	}
	if st := injectStats(t, svc); st.Mode != retrieveFallback {
		t.Fatalf("观测值应标明回退模式,got %+v", st)
	}
}

func TestRetrieveOffKeepsStableBlockOnly(t *testing.T) {
	c, svc := startServiceWith(t, map[string]any{"retrieve": "off"})
	if err := svc.Add("报告配色用蓝灰", ""); err != nil {
		t.Fatal(err)
	}
	prompt, tail := augmentWithQuery(t, c, "怎么部署") // off 模式忽略查询
	if !strings.Contains(prompt, "报告配色用蓝灰") {
		t.Fatalf("off 模式仍由稳定块承载记忆:\n%s", prompt)
	}
	if strings.Contains(tail, "<memory>") {
		t.Fatalf("off 模式不该追加尾部块:\n%s", tail)
	}
	if st := injectStats(t, svc); st.Mode != retrieveOff {
		t.Fatalf("观测值应标明 off,got %+v", st)
	}
}

func TestUnknownRetrieveModeFailsLoudly(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	_, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{"retrieve": "per_turn"}})
	if err == nil {
		t.Fatal("未支持的调取模式必须显式失败(per_turn 需要回合标识,本批未实现)")
	}
	if !strings.Contains(err.Error(), "per_turn") {
		t.Fatalf("错误信息应点出具体值,got %v", err)
	}
}

func TestAugmentBudgetTruncationIsVisible(t *testing.T) {
	// 稳定块极小(只放最新一条);检索预算 520(扣壳后可放约一条)。
	c, svc := startServiceWith(t, map[string]any{"budget_bytes": int(memory.ShellOverhead) + 40, "retrieve_budget": int(memory.ShellOverhead) + 200})
	for i := 0; i < 4; i++ {
		if err := svc.Add(fmt.Sprintf("部署注意事项 %d:%s", i, strings.Repeat("细节", 12)), ""); err != nil {
			t.Fatal(err)
		}
	}
	_, tail := augmentWithQuery(t, c, "部署注意事项")
	if !strings.Contains(tail, "其余超出预算") {
		t.Fatalf("截断必须如实说明:\n%s", tail)
	}
	if st := injectStats(t, svc); !st.Truncated || st.Total <= st.Items {
		t.Fatalf("观测值应反映截断,got %+v", st)
	}
}

func TestAugmentBudgetTooSmallToFitAny(t *testing.T) {
	// 检索预算只够壳、不够任何一条:那一条必须显示成「有命中但放不下」。
	c, svc := startServiceWith(t, map[string]any{"budget_bytes": int(memory.ShellOverhead) + 40, "retrieve_budget": int(memory.ShellOverhead) + 5})
	if err := svc.Add("部署统一走 make deploy", ""); err != nil {
		t.Fatal(err)
	}
	_, tail := augmentWithQuery(t, c, "部署")
	if strings.Contains(tail, "<memory>") {
		t.Fatalf("预算放不下任何一条时不该追加半块:\n%s", tail)
	}
	// 但"有命中却放不下"必须与"没命中"区分:观测值里 Total>0、Items==0。
	if st := injectStats(t, svc); st.Items != 0 || st.Total != 1 {
		t.Fatalf("观测值应区分「有命中但放不下」,got %+v", st)
	}
}
