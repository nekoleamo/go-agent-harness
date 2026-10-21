package tokencompress

// F7(2026-10-21)的用例:压缩深度参数化 + 阈值上限与窗口关系的核对。
//
// 两条要钉住的:
//  1. 折叠深度**可配**(fold_target_ratio / min_fold_ratio),缺省行为与从前逐字一致
//     (折到预算一半、单次不少于预算的 1/3);
//  2. 阈值上限(max_tokens)与窗口的关系是**有意的**:常规路径按上限(成本阀),
//     溢出路径按窗口比例(已经超窗了,再按更小的线折才是白扔空间)——
//     两条路径的差别是故意的,别把它们抹平(抹平 ⇒ 512K 窗口的用户白扔可用空间)。

import (
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// mkEvents 造 n 条用户消息事件(每条 100 字符)。
func mkEvents(n int) []sdk.SessionEvent {
	evs := make([]sdk.SessionEvent, 0, n)
	for i := 0; i < n; i++ {
		evs = append(evs, sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: repeat("x", 100)}})
	}
	return evs
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

// TestFoldDepthDefaults 缺省折叠深度 = 折到预算一半(滞回:不会每轮都折)。
func TestFoldDepthDefaults(t *testing.T) {
	e := Engine{}
	evs := mkEvents(40)
	var summaries []string
	w := e.Fold(evs, -1, 1000, func(s sdk.RollingSummary) { summaries = append(summaries, s.Text) })
	if w < 0 {
		t.Fatalf("应推进水位,got %d", w)
	}
	if len(summaries) == 0 {
		t.Fatal("应产出摘要")
	}
	// 折到一半:剩余投影(摘要 + 未折部分)应明显低于原投影,但不低于最小折叠量
	est := estimate(evs, w, summaryAt(evs))
	if est > 1000 {
		t.Fatalf("折后投影应回预算内,got %d", est)
	}
	if est < 1000/3 {
		t.Fatalf("不该折得比单次最小折叠量还狠,got %d", est)
	}
}

// TestFoldDepthConfigurable 折叠深度可配:目标比例越小 ⇒ 折得越多(水位推进越靠后)。
func TestFoldDepthConfigurable(t *testing.T) {
	evs := mkEvents(60)
	shallow := Engine{foldTarget: 0.9, minFold: 0.1} // 折到 预算×0.9 = 折得浅
	deep := Engine{foldTarget: 0.2, minFold: 0.05}   // 折到 预算×0.2 = 折得深
	wShallow := shallow.Fold(evs, -1, 1000, func(sdk.RollingSummary) {})
	wDeep := deep.Fold(evs, -1, 1000, func(sdk.RollingSummary) {})
	if wShallow >= wDeep {
		t.Fatalf("折得深(0.2)应折掉更多 ⇒ 水位更靠后:浅 %d 深 %d", wShallow, wDeep)
	}
}

// TestPlannerFoldDepthFromData 配置项生效(非法值按缺省,不静默当 0)。
func TestPlannerFoldDepthFromData(t *testing.T) {
	p := newPlanner(&sdk.Manifest{Data: map[string]any{
		"fold_target_ratio": 0.25,
		"min_fold_ratio":    0.1,
	}}, 40960)
	if p.foldTarget != 0.25 || p.minFold != 0.1 {
		t.Fatalf("折叠深度应取配置值,got %v/%v", p.foldTarget, p.minFold)
	}
	if p.eng.foldTarget != 0.25 || p.eng.minFold != 0.1 {
		t.Fatalf("引擎应拿到折叠深度,got %v/%v", p.eng.foldTarget, p.eng.minFold)
	}
	// 非法值(≥1 / ≤0)按缺省
	p2 := newPlanner(&sdk.Manifest{Data: map[string]any{
		"fold_target_ratio": 1.5,
		"min_fold_ratio":    0,
	}}, 40960)
	if p2.foldTarget != defaultFoldTarget || p2.minFold != defaultMinFold {
		t.Fatalf("非法值应回落缺省,got %v/%v", p2.foldTarget, p2.minFold)
	}
	// 无 data ⇒ 缺省
	p3 := newPlanner(nil, 40960)
	if p3.foldTarget != defaultFoldTarget || p3.minFold != defaultMinFold {
		t.Fatalf("缺省应为 %v/%v,got %v/%v", defaultFoldTarget, defaultMinFold, p3.foldTarget, p3.minFold)
	}
}

// TestWindowVsMaxTokensContract 阈值上限与窗口的关系(两条路径的差别是故意的)。
func TestWindowVsMaxTokensContract(t *testing.T) {
	p := &Planner{triggerRatio: 0.8, maxTokens: 200000, cpt: 2, foldTarget: defaultFoldTarget, minFold: defaultMinFold}
	// 常规路径:512K 窗口也只按 200K 上限(成本阀:历史每轮重发,窗口越大越贵)
	if got := p.thresholdTokens(512000); got != 200000 {
		t.Fatalf("常规路径阈值应被上限截住,got %d", got)
	}
	// 溢出路径:已经超窗 ⇒ 按窗口×比例(不按上限),否则白扔可用空间
	dec := p.Plan(sdk.CompressInput{
		Overflow: true, Window: 512000, ProjectChars: 400000,
		LastPromptTokens: 700000, LastProjectChars: 400000,
	})
	if dec.BudgetChars <= 0 {
		t.Fatal("溢出路径应给出压缩预算")
	}
	// 溢出预算按 512K×0.8=409K 折一半 ⇒ 目标远低于 200K 上限 ⇒ 证明没被上限压住
	regular := p.thresholdTokens(512000)
	if dec.BudgetChars >= regular {
		t.Fatalf("溢出路径应无视 %d 上限(按窗口比例),got %d", regular, dec.BudgetChars)
	}
}
