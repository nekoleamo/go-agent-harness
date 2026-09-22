// planner_test.go:压缩策略(阈值口径/长单轮截断阈值/字符-token 自校准)的单测。
// 表驱动,不依赖装机配置。
package tokencompress

import (
	"math"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestPlannerThreshold 阈值口径:min(窗口 × ratio, max_tokens);窗口未知 → 只用上限。
func TestPlannerThreshold(t *testing.T) {
	cases := []struct {
		name   string
		ratio  float64
		maxTok int
		window int
		want   int
	}{
		{"128K 窗口 × 0.8", 0.8, 200000, 128000, 102400},
		{"200K 窗口 × 0.8", 0.8, 200000, 200000, 160000},
		{"1M 窗口被上限截住", 0.8, 200000, 1000000, 200000},
		{"窗口未知 → 绝对上限", 0.8, 200000, 0, 200000},
		{"小窗口不被上限抬高", 0.8, 200000, 32000, 25600},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &Planner{triggerRatio: c.ratio, maxTokens: c.maxTok, cpt: defaultCharsPerTok}
			if got := p.thresholdTokens(c.window); got != c.want {
				t.Fatalf("阈值 = %d,期望 %d", got, c.want)
			}
		})
	}
}

// TestPlannerPlan 触发判定:未到阈值不压;到阈值折回阈值(预算 < 当前投影)、并给尾巴截断阈值。
func TestPlannerPlan(t *testing.T) {
	p := &Planner{triggerRatio: 0.8, maxTokens: 200000, fallbackChars: 40960, cpt: 2}
	// 窗口 128K → 阈值 102400;上次实测 60K token、投影 120K 字符(固定开销已含在实测里)
	in := sdk.CompressInput{LastPromptTokens: 60000, LastProjectChars: 120000, Window: 128000, ProjectChars: 130000}
	if d := p.Plan(in); d.BudgetChars != 0 || d.TrimChars != 0 {
		t.Fatalf("未到阈值不应压: %+v", d)
	}
	// 再涨:投影 +300K 字符(= +150K token)⇒ 预测 210K > 阈值
	in.ProjectChars = 420000
	d := p.Plan(in)
	if d.BudgetChars <= 0 || d.BudgetChars >= in.ProjectChars {
		t.Fatalf("到阈值应给出收敛预算: %+v", d)
	}
	if d.TrimChars != d.BudgetChars {
		t.Fatalf("截断阈值应与预算同源: %+v", d)
	}
	// 预算 = 投影 - (预测 - 阈值)×cpt:预测 210K、阈值 102.4K ⇒ 折掉 107.6K token = 215.2K 字符
	want := in.ProjectChars - int(math.Ceil((210000-102400)*2))
	if d.BudgetChars != want {
		t.Fatalf("预算 = %d,期望 %d", d.BudgetChars, want)
	}
}

// TestPlannerFallback 无实测用量(新会话首轮)时退回字符口径,不猜 token。
func TestPlannerFallback(t *testing.T) {
	p := &Planner{triggerRatio: 0.8, maxTokens: 200000, fallbackChars: 40960, cpt: 2}
	if d := p.Plan(sdk.CompressInput{Window: 128000, ProjectChars: 999999}); d.BudgetChars != 40960 {
		t.Fatalf("无实测用量应退回字符预算: %+v", d)
	}
	// 窗口未知但有用量:仍按上限阈值判定(用户设的绝对线)
	p2 := &Planner{triggerRatio: 0.8, maxTokens: 200000, fallbackChars: 40960, cpt: 2}
	in := sdk.CompressInput{LastPromptTokens: 150000, LastProjectChars: 100000, Window: 0, ProjectChars: 100000}
	if d := p2.Plan(in); d.BudgetChars != 0 {
		t.Fatalf("窗口未知时按上限阈值,150K 未到 200K 不应压: %+v", d)
	}
}

// TestPlannerCalibrate 字符/token 自校准:增量口径(固定开销被抵消)。
func TestPlannerCalibrate(t *testing.T) {
	p := &Planner{triggerRatio: 0.8, maxTokens: 200000, fallbackChars: 40960, cpt: 2}
	// 第一次:只记基线
	p.Plan(sdk.CompressInput{LastPromptTokens: 10000, LastProjectChars: 20000, Window: 128000, ProjectChars: 20000})
	// 第二次:增量 = +10000 token / +28000 字符 ⇒ 实测 2.8 ⇒ EMA(2, 2.8) = 2.4
	p.Plan(sdk.CompressInput{LastPromptTokens: 20000, LastProjectChars: 48000, Window: 128000, ProjectChars: 48000})
	if math.Abs(p.cpt-2.4) > 0.001 {
		t.Fatalf("cpt 应校准到 2.4: %v", p.cpt)
	}
	// 离群样本(1 字符/token)不应被采信到离谱:夹紧后仍在 0.4~8
	p2 := &Planner{triggerRatio: 0.8, maxTokens: 200000, cpt: 2}
	p2.Plan(sdk.CompressInput{LastPromptTokens: 10000, LastProjectChars: 50000, Window: 128000, ProjectChars: 50000})
	p2.Plan(sdk.CompressInput{LastPromptTokens: 600000, LastProjectChars: 50001, Window: 128000, ProjectChars: 50001})
	if p2.cpt < 0.4 || p2.cpt > 8 {
		t.Fatalf("校准值应夹紧: %v", p2.cpt)
	}
}

// TestNewPlannerConfig manifest 取值(含 YAML 整数/小数两种解型)+ 非法值按缺省。
func TestNewPlannerConfig(t *testing.T) {
	m := &sdk.Manifest{Data: map[string]any{
		"trigger_ratio":   0.6,
		"max_tokens":      150000, // int 解型
		"chars_per_token": 3,      // int 解型
	}}
	p := newPlanner(m, 40960)
	if p.triggerRatio != 0.6 || p.maxTokens != 150000 || p.cpt != 3 || p.fallbackChars != 40960 {
		t.Fatalf("配置未生效: %+v", p)
	}
	// 非法/缺省:比例 0、tokens -1 不生效
	p2 := newPlanner(&sdk.Manifest{Data: map[string]any{"trigger_ratio": 0.0, "max_tokens": -1}}, 0)
	if p2.triggerRatio != defaultTriggerRatio || p2.maxTokens != defaultMaxTokens || p2.cpt != defaultCharsPerTok {
		t.Fatalf("非法值应按缺省: %+v", p2)
	}
	if p3 := newPlanner(nil, 100); p3.triggerRatio != defaultTriggerRatio {
		t.Fatalf("无 manifest 应按缺省: %+v", p3)
	}
}

// TestPlannerFoldDelegates 策略对象仍满足 SessionCompressor(委托抽取式引擎)。
func TestPlannerFoldDelegates(t *testing.T) {
	p := newPlanner(nil, 40960)
	evs := []sdk.SessionEvent{
		{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "一"}},
		{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "二"}},
		{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "三"}},
	}
	var summaries []string
	if w := p.Fold(evs, -1, 1, func(s string) { summaries = append(summaries, s) }); w != 1 {
		t.Fatalf("应折叠到最后一个用户轮之前: %d", w)
	}
	if len(summaries) == 0 {
		t.Fatal("应有摘要回调")
	}
}

// TestPlannerOverflowSubmit 溢出兜底:端点已报超窗 ⇒ 必须给出压缩量(哪怕常规估算说"没到阈值"),
// 且目标取阈值的一半(折到恰好阈值会下一轮立刻再撞线)。
func TestPlannerOverflowSubmit(t *testing.T) {
	p := &Planner{triggerRatio: 0.8, maxTokens: 200000, fallbackChars: 40960, cpt: 2}
	// 窗口 128K → 阈值 102400(半阈值 51200);实测 60K token、投影 120K 字符
	in := sdk.CompressInput{LastPromptTokens: 60000, LastProjectChars: 120000, Window: 128000, ProjectChars: 200000}
	// 常规口径:预测 100000 < 阈值 102400 ⇒ 不压
	if d := p.Plan(in); d.BudgetChars != 0 || d.TrimChars != 0 {
		t.Fatalf("常规路径未到阈值不应压: %+v", d)
	}
	// 同一输入 + Overflow:折到半阈值 ⇒ 腾出 (100000-51200)×2 = 97600 字符
	in.Overflow = true
	d := p.Plan(in)
	want := 200000 - int(math.Ceil((100000-51200)*2))
	if d.BudgetChars != want || d.TrimChars != want {
		t.Fatalf("溢出兜底应折到 %d(得 %+v)", want, d)
	}
	// 无实测用量/窗口未知:保守折掉一半投影
	if d := p.Plan(sdk.CompressInput{ProjectChars: 100000, Overflow: true}); d.BudgetChars != 50000 {
		t.Fatalf("无用量应折一半投影: %+v", d)
	}
	// 极短投影也保底 minBudgetChars(折完还得能装下系统提示)
	if d := p.Plan(sdk.CompressInput{ProjectChars: 100, Overflow: true}); d.BudgetChars != minBudgetChars {
		t.Fatalf("应落到预算下限 %d: %+v", minBudgetChars, d)
	}
}
