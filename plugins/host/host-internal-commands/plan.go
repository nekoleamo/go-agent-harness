package hostintcmd

// /plan 命令(D2 · 2026-10-21):把"agent 在想什么"从聊天记录里搬到看得见的地方。
//
// 现状:规划模式(tool-auto_plan)把计划落盘在 `$GAH_HOME/plans/`,但**界面上没有入口**
// —— 人只能问模型"现在到第几步了"。
//
// 三条口径:
//   - **只读**:写侧仍是工具(auto_plan.create/step/…);这里只读不改;
//   - **服务注入**:经 ctx.plans(tool-auto-plan 提供)—— 未装配 ⇒ 显式报错,
//     不静默显示"暂无规划"(那会把"没启用"说成"没规划");
//   - **不发模型请求**:纯本地读文件(与 /context /recap 同款)。

import (
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// cmdPlan /plan [规划id]:列规划(带步骤进度),或看某一份规划的明细。
func (h *Host) cmdPlan(args []string) (string, error) {
	var plans sdk.PlanService
	if err := h.c.Inject("ctx.plans", &plans); err != nil {
		return "", errString("规划功能未启用(tool-auto-plan 未装配): " + err.Error())
	}
	if len(args) > 0 {
		d, ok := plans.Get(args[0])
		if !ok {
			return "", errString("规划 " + args[0] + " 不存在")
		}
		return planDetailText(d), nil
	}
	list := plans.List("")
	if len(list) == 0 {
		return "暂无规划(模型进入规划模式时会落一份计划)", nil
	}
	var b strings.Builder
	for i, p := range list {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s · %s · %d/%d 步 · %s", p.ID, planObjective(p.Objective), p.Done, p.Total, planStatusText(p.Status))
	}
	return b.String(), nil
}

// planDetailText 一份规划的明细(目标/来源/约束/步骤清单)。
func planDetailText(d sdk.PlanDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "规划 %s · %s\n", d.ID, planObjective(d.Objective))
	fmt.Fprintf(&b, "状态:%s · 进度:%d/%d 步", planStatusText(d.Status), d.Done, d.Total)
	if d.Request != "" {
		fmt.Fprintf(&b, "\n来源:%s", truncPlan(d.Request, 120))
	}
	if len(d.Constraints) > 0 {
		b.WriteString("\n约束:")
		for _, c := range d.Constraints {
			b.WriteString("\n  · " + truncPlan(c, 100))
		}
	}
	if len(d.Steps) > 0 {
		b.WriteString("\n步骤:")
		for _, s := range d.Steps {
			// 显示序号从 1 起(人看的编号;落盘/工具用的是 0 起)
			fmt.Fprintf(&b, "\n  %d. [%s] %s", s.Index+1, planStepMark(s.Status), truncPlan(s.Title, 80))
		}
	}
	return b.String()
}

// planObjective 目标摘要(裁到 40 字)。
func planObjective(s string) string { return truncPlan(s, 40) }

// planStatusText 规划状态的人话。
func planStatusText(s string) string {
	switch s {
	case "proposed":
		return "待确认"
	case "confirmed":
		return "已确认"
	case "completed":
		return "已归档"
	case "":
		return "未知"
	default:
		return s
	}
}

// planStepMark 步骤状态标记(完成/进行中/待办)。
func planStepMark(s string) string {
	switch s {
	case "completed":
		return "✓"
	case "in_progress":
		return "→"
	default:
		return "·"
	}
}

// truncPlan 裁到 n 字(不切多字节)。
func truncPlan(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
