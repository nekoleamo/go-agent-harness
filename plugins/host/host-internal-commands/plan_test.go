package hostintcmd

// /plan 命令的用例(D2 · 2026-10-21)。
//
// 要钉的是:
//  1. 列规划:目标/进度/状态都在;
//  2. 看某一份:步骤清单(完成/进行中/待办各有标记);
//  3. **未装配** ⇒ 显式报错("规划功能未启用")—— 不静默说"暂无规划"
//     (那会把"没启用"说成"没规划",用户在找不存在的问题)。

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubPlans 规划只读桩。
type stubPlans struct {
	list []sdk.PlanView
	det  map[string]sdk.PlanDetail
}

func (s *stubPlans) List(status string) []sdk.PlanView {
	if status == "" {
		return s.list
	}
	var out []sdk.PlanView
	for _, p := range s.list {
		if p.Status == status {
			out = append(out, p)
		}
	}
	return out
}

func (s *stubPlans) Get(id string) (sdk.PlanDetail, bool) {
	d, ok := s.det[id]
	return d, ok
}

func TestCmdPlanNotAssembled(t *testing.T) {
	c, cmds := buildEnv(t)
	startCmds(t, c)
	_, err := run(t, cmds, "plan")
	if err == nil || !strings.Contains(err.Error(), "未启用") {
		t.Fatalf("未装配应显式报错,got %v", err)
	}
}

func TestCmdPlanList(t *testing.T) {
	c, cmds := buildEnv(t)
	if err := c.Provide("ctx.plans", sdk.PlanService(&stubPlans{
		list: []sdk.PlanView{
			{ID: "p1", Objective: "把导出做成流式", Status: "confirmed", Done: 2, Total: 5},
		},
	})); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	out, err := run(t, cmds, "plan")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"p1", "把导出做成流式", "2/5", "已确认"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
}

func TestCmdPlanDetail(t *testing.T) {
	c, cmds := buildEnv(t)
	if err := c.Provide("ctx.plans", sdk.PlanService(&stubPlans{
		list: []sdk.PlanView{{ID: "p1", Objective: "目标", Status: "confirmed", Done: 1, Total: 2}},
		det: map[string]sdk.PlanDetail{
			"p1": {
				PlanView:    sdk.PlanView{ID: "p1", Objective: "目标", Status: "confirmed", Done: 1, Total: 2},
				Request:     "用户的原话",
				Constraints: []string{"不许破坏缓存"},
				Steps: []sdk.PlanStep{
					{Index: 1, Title: "先量一遍", Status: "completed"},
					{Index: 2, Title: "再改", Status: "in_progress"},
				},
			},
		},
	})); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	out, err := run(t, cmds, "plan", "p1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"用户的原话", "不许破坏缓存", "先量一遍", "✓", "→"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
}

func TestCmdPlanUnknownID(t *testing.T) {
	c, cmds := buildEnv(t)
	if err := c.Provide("ctx.plans", sdk.PlanService(&stubPlans{})); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	if _, err := run(t, cmds, "plan", "nope"); err == nil {
		t.Fatal("不存在的规划应显式报错")
	}
}

// TestPlanTextHelpers 文本助手的边界(状态/标记/裁剪):别让"未知状态"显示成空白。
func TestPlanTextHelpers(t *testing.T) {
	cases := map[string]string{
		"proposed":  "待确认",
		"confirmed": "已确认",
		"completed": "已归档",
		"":          "未知",
		"weird":     "weird", // 未知状态:原样透出(不编)
	}
	for in, want := range cases {
		if got := planStatusText(in); got != want {
			t.Errorf("planStatusText(%q) = %q,期望 %q", in, got, want)
		}
	}
	if got := planStepMark("completed"); got != "✓" {
		t.Errorf("完成标记 = %q", got)
	}
	if got := planStepMark("in_progress"); got != "→" {
		t.Errorf("进行中标记 = %q", got)
	}
	if got := planStepMark("pending"); got != "·" {
		t.Errorf("待办标记 = %q", got)
	}
	// 裁剪:超长裁到 n 字并加省略号;短的不动
	long := strings.Repeat("甲", 50)
	if got := truncPlan(long, 10); len([]rune(got)) != 11 {
		t.Errorf("裁剪后应 10 字+省略号,got %d", len([]rune(got)))
	}
	if got := truncPlan("  短  ", 10); got != "短" {
		t.Errorf("应去空白,got %q", got)
	}
}
