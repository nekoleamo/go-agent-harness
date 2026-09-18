// Package toolschedule 提供 tool-schedule 插件(NOND-W4b):模型侧定时计划工具。
//
// 为什么单独一个插件、且**默认停用**:定时计划 = 模型给自己排未来的活,是「把决定权
// 交给模型」的一档能力。默认空 = 行为零变化(既有装配完全不受影响),要用的人显式打开
// (bundle 条目 enabled: true 或 profile patch)。
//
// 语义红线(与 sdk.Schedule 一致,本插件**不新增执行路径**):到点后经 ctx.agentLoop
// 走既有回合入口 —— 工具执行仍只经 ctx.tools、仍受 policy-guard 路径/审批裁决、仍落
// 会话记录;触发时无人值守(需审批的动作一律拒绝,绝不弹确认)。
//
// 单工具多 action(与 todo/jobs 同风格:省 schema 成本,而不是 add/list/remove 三个工具)。
package toolschedule

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin tool-schedule:装配时经 ctx.tools 注册(依赖 ctx.tools 与 ctx.schedule)。
type Plugin struct{}

// Name 插件名(与 catalogue 条目同名)。
func (p *Plugin) Name() string { return "tool-schedule" }

// Start 注册 schedule 工具;ctx.schedule 缺失 = 显式失败(不静默降级成空工具)。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	var sc sdk.ScheduleService
	if err := c.Inject("ctx.schedule", &sc); err != nil {
		return nil, fmt.Errorf("tool-schedule: 需要 host-schedule(ctx.schedule): %w", err)
	}
	return tools.Register(&Tool{svc: sc}), nil
}

// Tool 定时计划工具。
type Tool struct {
	svc sdk.ScheduleService
}

// Definition 工具描述(模型可见文案:不用 markdown 加粗,短句说清禁区)。
func (t *Tool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: "schedule",
		Description: "定时计划:到点自动提交一段输入、跑一轮(经既有回合入口 —— 同样受沙箱/审批裁决、同样落会话记录)。" +
			"action:list 看全部计划;add(name,cron,prompt)新建;update(id 加要改的字段)改配置;remove(id)删除;run(id)立即跑一次(不等下个时点,会真消耗 token 并产生副作用)。" +
			"cron 是 5 字段(分 时 日 月 周),按本地时区。" +
			"注意:触发时无人值守 —— 需要审批的动作会被直接拒绝(没人回答确认),所以计划里别放需要人点头的事。" +
			"面向人的计划管理命令是 /schedule(用户侧),本工具是给模型自己排活用。",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"action"},
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"list", "add", "update", "remove", "run"}},
				"id":     map[string]any{"type": "string", "description": "update/remove/run 的目标计划 id"},
				"name":   map[string]any{"type": "string", "description": "add 必填(短名);update 可改"},
				"cron":   map[string]any{"type": "string", "description": "5 字段 cron:分 时 日 月 周(如 '0 9 * * 1' = 每周一 9:00;add 必填)"},
				"prompt": map[string]any{"type": "string", "description": "到点提交给模型的输入(add 必填);写自包含的一句话,别依赖当前会话上下文"},
				"enabled": map[string]any{
					"type": "boolean", "description": "update 可改(停用 = 保留配置但不触发);add 默认启用"},
			},
		},
	}
}

// args 统一入参(enabled 用指针:区分「没给」与「显式 false」)。
type args struct {
	Action  string `json:"action"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Cron    string `json:"cron"`
	Prompt  string `json:"prompt"`
	Enabled *bool  `json:"enabled"`
}

// Execute 执行一个 action;业务失败以 map{"error":...} 回传模型(不中断 turn)。
func (t *Tool) Execute(_ context.Context, raw string) (any, error) {
	var a args
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return map[string]any{"error": "参数不是合法 JSON: " + err.Error()}, nil
	}
	switch strings.ToLower(strings.TrimSpace(a.Action)) {
	case "list":
		return map[string]any{"plans": viewsOf(t.svc.List())}, nil
	case "add":
		return t.add(a)
	case "update":
		return t.update(a)
	case "remove":
		return t.remove(a)
	case "run":
		return t.run(a)
	case "":
		return map[string]any{"error": "缺少 action(list/add/update/remove/run)"}, nil
	default:
		return map[string]any{"error": "未知 action: " + a.Action + "(仅 list/add/update/remove/run)"}, nil
	}
}

func (t *Tool) add(a args) (any, error) {
	if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Cron) == "" || strings.TrimSpace(a.Prompt) == "" {
		return map[string]any{"error": "add 需要 name/cron/prompt 三项(缺一不可)"}, nil
	}
	enabled := true // 模型新建的计划默认启用(默认停用是**插件级**开关,不是每条计划)
	if a.Enabled != nil {
		enabled = *a.Enabled
	}
	p, err := t.svc.Add(sdk.Schedule{Name: a.Name, Cron: a.Cron, Prompt: a.Prompt, Enabled: enabled})
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	return map[string]any{"created": viewOf(p), "note": "已落盘,跨会话保留"}, nil
}

// update 部分更新:服务层 Update 是整组覆盖(Name/Cron/Prompt/Enabled),所以这里
// 先读回当前值再只覆盖用户给到的字段 —— 否则「改 cron」会把 name/prompt 抹空。
func (t *Tool) update(a args) (any, error) {
	id := strings.TrimSpace(a.ID)
	if id == "" {
		return map[string]any{"error": "update 需要 id"}, nil
	}
	cur, ok := t.find(id)
	if !ok {
		return map[string]any{"error": "计划不存在: " + id}, nil
	}
	merged := sdk.Schedule{ID: cur.ID, Name: cur.Name, Cron: cur.Cron, Prompt: cur.Prompt, Enabled: cur.Enabled}
	if strings.TrimSpace(a.Name) != "" {
		merged.Name = a.Name
	}
	if strings.TrimSpace(a.Cron) != "" {
		merged.Cron = a.Cron
	}
	if strings.TrimSpace(a.Prompt) != "" {
		merged.Prompt = a.Prompt
	}
	if a.Enabled != nil {
		merged.Enabled = *a.Enabled
	}
	p, err := t.svc.Update(merged)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	return map[string]any{"updated": viewOf(p)}, nil
}

func (t *Tool) remove(a args) (any, error) {
	id := strings.TrimSpace(a.ID)
	if id == "" {
		return map[string]any{"error": "remove 需要 id"}, nil
	}
	if err := t.svc.Remove(id); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	return map[string]any{"removed": id}, nil
}

// run 立即跑一次:不等下个时点、不改动排期;异步执行,结果看 last_status 与 /schedule。
func (t *Tool) run(a args) (any, error) {
	id := strings.TrimSpace(a.ID)
	if id == "" {
		return map[string]any{"error": "run 需要 id"}, nil
	}
	if err := t.svc.RunNow(id); err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	return map[string]any{"started": id, "note": "已在后台触发一轮;状态用 list 看 last_status(/schedule 或 Web 计划面亦有)"}, nil
}

// find 按 id 取当前计划。
func (t *Tool) find(id string) (sdk.Schedule, bool) {
	for _, p := range t.svc.List() {
		if p.ID == id {
			return p, true
		}
	}
	return sdk.Schedule{}, false
}

// view 模型可见的计划摘要(不吐完整 prompt 之外的东西:字段少而稳定,省 token)。
type view struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Cron       string `json:"cron"`
	Prompt     string `json:"prompt"`
	Enabled    bool   `json:"enabled"`
	NextRun    string `json:"next_run,omitempty"`
	LastRunAt  string `json:"last_run_at,omitempty"`
	LastStatus string `json:"last_status,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

func viewOf(p sdk.Schedule) view {
	v := view{ID: p.ID, Name: p.Name, Cron: p.Cron, Prompt: p.Prompt, Enabled: p.Enabled,
		LastStatus: string(p.LastStatus), LastError: p.LastError}
	if !p.NextRun.IsZero() {
		v.NextRun = p.NextRun.Format("2006-01-02 15:04")
	}
	if !p.LastRunAt.IsZero() {
		v.LastRunAt = p.LastRunAt.Format("2006-01-02 15:04")
	}
	return v
}

// viewsOf 列表视图(顺序沿用服务层:创建时间升序,同刻按 id;此处不重排)。
func viewsOf(ps []sdk.Schedule) []view {
	out := make([]view, 0, len(ps))
	for _, p := range ps {
		out = append(out, viewOf(p))
	}
	return out
}
