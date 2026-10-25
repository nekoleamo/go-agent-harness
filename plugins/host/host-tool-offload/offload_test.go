package hosttooloffload

// 分流插件的测试(第一百六十五批)。
//
// 钉三件会悄悄错的事:
//  ① 超预算时原文进 Data、Content 变瘦 —— 而**小结果一个字节都不许动**;
//  ② data.limit=0 真的关掉(不订阅);
//  ③ skip_tools / 工具自带 Data 时**不重复分流**(尊重工具自己的判断);
// 外加配置写错必须显式报错(预算写错会让分流静默失效),预算过小同样必须报错
// (把结果切成碎片 = 让模型去猜被省略的部分)。

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func newCtx(t *testing.T) sdk.Ctx {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	return ctx.New(logger, event.New(logger))
}

// emit 走真实总线(waterfall),返回被改写后的结果。
func emit(t *testing.T, c sdk.Ctx, tool, content string) *sdk.ToolResult {
	t.Helper()
	res := &sdk.ToolResult{Content: content}
	callCtx := sdk.WithToolName(t.Context(), tool)
	if _, err := c.Emit(callCtx, "tools/post-execute", res, sdk.Waterfall); err != nil {
		t.Fatalf("post-execute 返回错误: %v", err)
	}
	return res
}

// TestOffloadsOversizedResult 超预算 ⇒ Content 变瘦、Data 是完整原文、且瘦身版**明说**省略了多少。
func TestOffloadsOversizedResult(t *testing.T) {
	c := newCtx(t)
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{"limit": minLimit}}); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 5000)
	res := emit(t, c, "shell", big)
	if res.Data != big {
		t.Fatal("Data 必须是完整原文")
	}
	if res.Content == big {
		t.Fatal("Content 应该被瘦身")
	}
	if len([]rune(res.Content)) > minLimit {
		t.Fatalf("瘦身后 %d 字符,超预算", len([]rune(res.Content)))
	}
	if !strings.Contains(res.Content, "省略") {
		t.Fatalf("必须明说省略(不静默截断): %q", res.Content)
	}
}

// TestUnderBudgetUntouched 不超预算 ⇒ Data 保持空、Content 逐字不变。
//
// 反向验证过:把 Split 的 `orig <= limit` 早退删掉(改成永远走裁剪路径)⇒ 本用例红。
func TestUnderBudgetUntouched(t *testing.T) {
	c := newCtx(t)
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{"limit": minLimit}}); err != nil {
		t.Fatal(err)
	}
	small := `{"output":"ok"}`
	res := emit(t, c, "shell", small)
	if res.Data != "" {
		t.Fatalf("小结果不该存两份: Data=%q", res.Data)
	}
	if res.Content != small {
		t.Fatalf("小结果被改动: %q", res.Content)
	}
}

// TestLimitZeroDisables data.limit=0 ⇒ 完全关闭(不订阅、不碰结果)。
func TestLimitZeroDisables(t *testing.T) {
	c := newCtx(t)
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{"limit": 0}}); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("y", 5000)
	res := emit(t, c, "shell", big)
	if res.Data != "" || res.Content != big {
		t.Fatal("limit=0 时不该分流")
	}
}

// TestSkipTools 豁免清单里的工具不碰。
func TestSkipTools(t *testing.T) {
	c := newCtx(t)
	m := &sdk.Manifest{Data: map[string]any{"limit": minLimit, "skip_tools": []any{"file_read"}}}
	if _, err := (&Plugin{}).Start(c, m); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("z", 5000)
	if res := emit(t, c, "file_read", big); res.Data != "" || res.Content != big {
		t.Fatal("豁免工具不该被分流")
	}
	if res := emit(t, c, "shell", big); res.Data == "" {
		t.Fatal("未豁免的工具该被分流")
	}
}

// TestToolProvidedDataWins 工具自己带了 Data(它更懂自己的数据)⇒ 宿主不再覆盖。
func TestToolProvidedDataWins(t *testing.T) {
	c := newCtx(t)
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{"limit": minLimit}}); err != nil {
		t.Fatal(err)
	}
	res := &sdk.ToolResult{Content: "已摘要", Data: strings.Repeat("w", 5000)}
	if _, err := c.Emit(t.Context(), "tools/post-execute", res, sdk.Waterfall); err != nil {
		t.Fatal(err)
	}
	if res.Content != "已摘要" {
		t.Fatalf("工具自己的分流被宿主覆盖: %q", res.Content)
	}
}

// TestBadConfigFailsLoudly 配置类型写错 ⇒ Start 报错(不静默失效)。
func TestBadConfigFailsLoudly(t *testing.T) {
	c := newCtx(t)
	for _, tc := range []map[string]any{
		{"limit": "20000"},
		{"skip_tools": "shell"},
		{"skip_tools": []any{1}},
	} {
		if _, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: tc}); err == nil {
			t.Fatalf("配置 %v 应报错", tc)
		}
	}
}

// TestLimitBelowFloorFailsLoudly 预算低于下限 ⇒ Start 报错,且文案给出两条合法选择。
//
// 为什么要报错而不是抬到下限:静默改用户设的数等于静默改意图;而 0 已经是「关闭」的
// 逃逸口,文案直接写出来,用户不需要读源码就知道该改哪个数。
// 反向验证过:把 parseConfig 里的下限判断整段删掉 ⇒ 本用例红。
func TestLimitBelowFloorFailsLoudly(t *testing.T) {
	c := newCtx(t)
	for _, n := range []int{1, 500, minLimit - 1} {
		_, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{"limit": n}})
		if err == nil {
			t.Fatalf("limit=%d 低于下限 %d,应报错", n, minLimit)
		}
		msg := err.Error()
		if !strings.Contains(msg, "0(") || !strings.Contains(msg, fmt.Sprint(minLimit)) {
			t.Fatalf("报错必须给出两条合法选择(0 关闭 / >= %d): %q", minLimit, msg)
		}
	}
	// 边界:正好等于下限与 0 都必须放行
	for _, n := range []int{minLimit, 0} {
		if _, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{"limit": n}}); err != nil {
			t.Fatalf("limit=%d 应放行: %v", n, err)
		}
	}
}

// TestDefaultLimit 缺省配置也能起来,且缺省预算就是 20000(不是 0 = 意外关闭)。
func TestDefaultLimit(t *testing.T) {
	c := newCtx(t)
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if res := emit(t, c, "shell", strings.Repeat("a", defaultLimit-10)); res.Data != "" {
		t.Fatal("未超缺省预算不该分流")
	}
	if res := emit(t, c, "shell", strings.Repeat("a", defaultLimit+10)); res.Data == "" {
		t.Fatal("超缺省预算应分流")
	}
}
