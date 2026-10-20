package tui

// 状态栏的规划项(D2 · 2026-10-21)。
//
// 为什么要有:规划落盘了却**没人看得见** —— 人只能问模型"现在到第几步了"。
// 状态栏给一个常驻信号(「计划 2/5」),细节走 /plan。
//
// 口径:无规划 ⇒ **隐身**(不占位):规划模式不是默认路径,不该给普通会话加噪声。

import (
	"strings"
	"testing"
)

// TestStatuslinePlanItem 规划进度渲染。
func TestStatuslinePlanItem(t *testing.T) {
	s := &State{PlanDone: 2, PlanTotal: 5, PlanTitle: "把导出做成流式"}
	out := stripANSI(statuslineItem(s, "plan"))
	if !strings.Contains(out, "2/5") {
		t.Fatalf("规划项应显示进度,got %q", out)
	}
	if !strings.Contains(out, "把导出做成流式") {
		t.Fatalf("规划项应带目标摘要,got %q", out)
	}
}

// TestStatuslinePlanHiddenWithoutPlan 无规划 ⇒ 隐身(不占位)。
func TestStatuslinePlanHiddenWithoutPlan(t *testing.T) {
	s := &State{}
	if out := statuslineItem(s, "plan"); out != "" {
		t.Fatalf("无规划时该项应隐身,got %q", out)
	}
}

// TestStatuslinePlanTokenRegistered 该项要在合法项清单里(/statusline 校验用)。
func TestStatuslinePlanTokenRegistered(t *testing.T) {
	found := false
	for _, tok := range statuslineTokens {
		if tok == "plan" {
			found = true
		}
	}
	if !found {
		t.Fatal("plan 应在 statuslineTokens 里")
	}
	inDefault := false
	for _, tok := range defaultStatusline {
		if tok == "plan" {
			inDefault = true
		}
	}
	if !inDefault {
		t.Fatal("plan 应在默认状态栏项里")
	}
}
