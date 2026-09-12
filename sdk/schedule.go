// schedule.go:ctx.schedule 定时任务域模型(NOND-W4 host-schedule)。
//
// 触发语义(设计红线,勿绕):到点后**经 ctx.agentLoop 走既有回合入口** ——
// 工具执行仍只经 ctx.tools、仍受 policy-guard 路径/审批裁决、仍落会话记录
// (不变量:模型可见即已记录)。定时任务因此不是「第二条执行路径」。
package sdk

import (
	"context"
	"time"
)

// Schedule 一条定时计划(落盘 $GAH_HOME/schedules/<id>.yaml)。
// 字段名同时用于 YAML(落盘)与 JSON(Web/命令输出)。
type Schedule struct {
	ID        string    `json:"id" yaml:"id"`
	Name      string    `json:"name" yaml:"name"`
	Cron      string    `json:"cron" yaml:"cron"`     // 5 字段 cron:分 时 日 月 周
	Prompt    string    `json:"prompt" yaml:"prompt"` // 到点提交给模型的输入
	Enabled   bool      `json:"enabled" yaml:"enabled"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`

	// 运行记录(只保留最近一次;每轮详细产出在会话记录里)。
	LastRunAt  time.Time        `json:"last_run_at,omitempty" yaml:"last_run_at,omitempty"`
	LastStatus ScheduleRunState `json:"last_status,omitempty" yaml:"last_status,omitempty"`
	LastError  string           `json:"last_error,omitempty" yaml:"last_error,omitempty"`

	// NextRun 宿主机算的下次触发时刻(只读派生值,**不落盘**:cron 变了旧值即失效)。
	// 零值 = 当前无下次触发(已停用;或 cron 永不匹配未来时刻)。
	// 注:time.Time 不支持 omitempty(结构体),故 JSON 里恒出现取零值 —— 与 Job.DoneAt 同例。
	NextRun time.Time `json:"next_run,omitempty" yaml:"-"`
}

// ScheduleRunState 一次触发的终态。
type ScheduleRunState string

const (
	ScheduleRunOK      ScheduleRunState = "ok"      // 回合正常结束
	ScheduleRunFailed  ScheduleRunState = "failed"  // 回合报错(详情见 LastError + 会话记录)
	ScheduleRunSkipped ScheduleRunState = "skipped" // 未执行(如当时有回合在跑)
)

// EventScheduleRun 定时任务一次触发的终态通知(载荷 *ScheduleRunEvent)。
// 订阅方(Web/TUI)据此刷新计划列表;无订阅方时纯广播,无副作用。
const EventScheduleRun = "schedule/run"

// ScheduleRunEvent schedule/run 事件载荷。
type ScheduleRunEvent struct {
	ID    string           `json:"id"`
	State ScheduleRunState `json:"state"`
	Error string           `json:"error,omitempty"`
	RunAt time.Time        `json:"run_at"`
}

// ScheduleService 定时任务服务(ctx.schedule;host-schedule 提供)。
type ScheduleService interface {
	// List 全部计划(稳定顺序:创建时间升序,同刻按 id)。含宿主机算的 NextRun。
	List() []Schedule
	// Add 新增计划(校验 cron 与必填字段;ID 空则自动生成),返回落盘结果(含 NextRun)。
	Add(s Schedule) (Schedule, error)
	// Update 按 ID 覆盖可变字段(Name/Cron/Prompt/Enabled),保留 CreatedAt 与运行记录。
	Update(s Schedule) (Schedule, error)
	// Remove 删除计划(不存在 = 显式错误,不静默成功)。
	Remove(id string) error
	// RunNow 立即触发一次(不等下一个时点;不改动既有排期)。
	RunNow(id string) error
}

// —— 无人值守标记(NOND-W4 安全决策) ——

// unattendedKey 无人值守标记的 ctx key(私有类型防误用)。
type unattendedKey struct{}

// WithUnattended 把 ctx 标记为「无人值守运行」(host-schedule 触发时施加)。
//
// 语义:policy-guard 对需审批的动作**一律拒绝、绝不弹确认** —— 定时任务没有
// 在场的人来回答确认弹窗,「没人问」不等于「默认同意」。该标记只影响审批档
// (危险动作),沙箱档位照旧由用户配置决定。
func WithUnattended(ctx context.Context) context.Context {
	return context.WithValue(ctx, unattendedKey{}, true)
}

// UnattendedOf 读取无人值守标记(未标记时 false = 正常交互回合)。
func UnattendedOf(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, ok := ctx.Value(unattendedKey{}).(bool)
	return ok && v
}
