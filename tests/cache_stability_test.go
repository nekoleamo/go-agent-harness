// 缓存稳定性门(A4):一段正常的多轮会话里,模型请求的**前缀**必须只增不改。
//
// 为什么要有这条门:提示缓存按「与上一次请求完全相同的最长前缀」给折扣。前缀一旦在
// 中段被改写(而不是尾部追加),那之后的全部内容都按全价计 —— 这是**看不见的钱**:
// 命中率掉到 0%,而代码里没有任何一行报错。
//
// gah 早就有前缀指纹探针(host-agent-loop 的 prefixProbe),但它只活在插件单测里,
// **没有任何回归门**:把某个系统提示片段改成「每轮重算」(比如塞进当前时间)不会让任何
// 测试变红,只会在真机上悄悄把缓存打穿。本文件把这件事变成 CI 会拦的东西。
//
// 两条纪律(本仓「修完必须反向验证」):
//  1. **门必须有牙**:TestCacheStabilityGateDetectsVolatilePrompt 证明这条门**能**抓到
//     变动的前缀(灵敏度验证)—— 否则它可能只是一条恒绿的摆设。
//  2. **反向验证**:把系统提示里塞进 time.Now() 后本文件必须红(已实跑验证,见交付记录)。
package tests

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// cacheStabilityScript 3 轮 × 2 步(工具调用 + 收尾)的 mock 脚本。
func cacheStabilityScript() []any {
	var s []any
	for i := 0; i < 3; i++ {
		s = append(s,
			map[string]any{"tool": map[string]any{"name": "shell", "args": `{"command":"echo mock-ok"}`}},
			map[string]any{"text": fmt.Sprintf("第 %d 轮收尾", i+1), "finish": "stop"},
		)
	}
	return s
}

// prefixSamples 取探针样本(未装配 ⇒ 显式失败,不能静默跳过)。
func prefixSamples(t *testing.T, c sdk.Ctx) []sdk.PrefixSample {
	t.Helper()
	var probe sdk.PrefixProbe
	if err := c.Inject("ctx.prefixProbe", &probe); err != nil || probe == nil {
		t.Fatalf("ctx.prefixProbe 未装配(host-agent-loop 未加载?): %v", err)
	}
	return probe.PrefixSamples(0)
}

// TestCacheStabilityGate 正常多轮会话:前缀只能「追加」,不能「改写」。
//
// 断言两件事:
//  1. 探针确实采到了样本(≥6 次请求)—— 否则「没有 changed」是**空断言**,恒绿;
//  2. 没有任何样本的 DiffToPrev 是 "changed"(尾部追加 "appended" 与完全相同 "same" 都正常)。
func TestCacheStabilityGate(t *testing.T) {
	c := buildScriptEnv(t, cacheStabilityScript())

	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if err := loop.Run(context.Background(), fmt.Sprintf("第 %d 轮请求", i)); err != nil {
			t.Fatalf("第 %d 轮失败: %v", i, err)
		}
	}

	samples := prefixSamples(t, c)
	if len(samples) < 6 {
		t.Fatalf("样本数应 ≥6(3 轮 × 2 步),实际 %d —— 门会变成空断言", len(samples))
	}
	for _, s := range samples {
		switch s.DiffToPrev {
		case "first", "same", "appended":
		case "changed":
			t.Fatalf("前缀被改写(第 %d 次请求):%s\n  三段: sys %s · tools %s · hist %s\n"+
				"  提示缓存从这一点起全部按全价计 —— 查「哪一段在动」:%s",
				s.Seq, s.At, s.SysHash, s.ToolsHash, s.HistHash, s.DiffWhere)
		default:
			t.Fatalf("未知的前缀关系 %q(第 %d 次请求)", s.DiffToPrev, s.Seq)
		}
	}
}

// TestCacheStabilityGateDetectsVolatilePrompt **灵敏度验证**:门必须能抓到变动的前缀。
//
// 这里故意注册一个「每次求值都变」的系统提示片段(模拟「片段里塞了当前时间」这类
// 真实事故),断言探针**必须**报出 changed。若这条用例变绿,说明上面的门是摆设 ——
// 它对真正的失配也看不见。
func TestCacheStabilityGateDetectsVolatilePrompt(t *testing.T) {
	c := buildScriptEnv(t, cacheStabilityScript())

	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		t.Fatal(err)
	}
	n := 0
	sp.AddSection(sdk.SystemPromptSection{
		Name:    "每轮重算的片段",
		Content: func() string { n++; return fmt.Sprintf("当前时间戳 %d", n) },
	})

	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if err := loop.Run(context.Background(), fmt.Sprintf("第 %d 轮请求", i)); err != nil {
			t.Fatalf("第 %d 轮失败: %v", i, err)
		}
	}

	samples := prefixSamples(t, c)
	if len(samples) < 2 {
		t.Fatalf("样本数应 ≥2,实际 %d", len(samples))
	}
	var changed []sdk.PrefixSample
	for _, s := range samples {
		if s.DiffToPrev == "changed" {
			changed = append(changed, s)
		}
	}
	if len(changed) == 0 {
		t.Fatalf("变动的前缀必须被探针抓到(没抓到 ⇒ 稳定性门是摆设):\n%s", formatSamples(samples))
	}
	// 且要指名 system 段(否则「变了」不可行动 —— 这是探针的既有义务)。
	if !strings.Contains(changed[0].DiffWhere, "system") {
		t.Fatalf("应指名 system 段变了,实际:%q", changed[0].DiffWhere)
	}
}

// formatSamples 样本列表的可读输出(失败信息用)。
func formatSamples(samples []sdk.PrefixSample) string {
	var sb strings.Builder
	for _, s := range samples {
		fmt.Fprintf(&sb, "  #%d %s %s sys %s tools %s hist %s %s\n",
			s.Seq, s.At, s.DiffToPrev, s.SysHash, s.ToolsHash, s.HistHash, s.DiffWhere)
	}
	return sb.String()
}
