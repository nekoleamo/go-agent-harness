package sdk

// 规划(plan)的只读契约(D2 · 2026-10-21)。
//
// 为什么要有:规划模式(tool-auto-plan)把计划落盘在 `$GAH_HOME/plans/`,
// 但**界面上看不见** —— 人只能问模型"现在到第几步了"。
// 状态栏给一个「计划 2/5」+ 一条 `/plan` 命令,就把"agent 在想什么"从聊天记录
// 搬到了固定位置。
//
// 只读:写侧仍归 tool-auto-plan(它是工具);这里给界面一个**读**的入口。
// 契约放在 sdk(而不是让界面 import 工具包):插件只 import sdk/ 是硬红线。

// PlanView 规划摘要(精简字段;步骤明细用 PlanDetail 取)。
type PlanView struct {
	ID        string // 规划 id
	Objective string // 目标(一句话)
	Status    string // 状态(pending/confirmed/completed/…)
	Done      int    // 已完成步骤数
	Total     int    // 总步骤数
}

// PlanStep 检查清单一步(只读)。
type PlanStep struct {
	Index  int    // 序号(0 起;与落盘格式一致 —— 工具的 auto_plan.step 也用这个序号)
	Title  string // 步骤标题
	Status string // 步骤状态(pending/in_progress/completed/…)
}

// PlanDetail 规划详情(摘要 + 目标来源 + 约束 + 步骤清单)。
type PlanDetail struct {
	PlanView
	Request     string     // 原始用户请求(规划来源)
	Constraints []string   // 关键约束与风险
	Steps       []PlanStep // 线性检查清单
}

// PlanService 规划只读服务(tool-auto-plan 提供;ctx.plans)。
//
// 未装配(该插件未启用)⇒ 界面不显示计划项(隐身,不占位、不报错)。
type PlanService interface {
	// List 列规划摘要(status 空 = 全部;按最近变更倒序)。
	List(status string) []PlanView
	// Get 取规划详情(id 不存在 ⇒ ok=false)。
	Get(id string) (PlanDetail, bool)
}
