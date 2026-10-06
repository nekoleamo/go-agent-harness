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
	Cron      string    `json:"cron" yaml:"cron"`     // 5 字段 cron:分 时 日 月 周(日字段可含 L = 当月最后一天)
	Prompt    string    `json:"prompt" yaml:"prompt"` // 到点提交给模型的输入
	Enabled   bool      `json:"enabled" yaml:"enabled"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`

	// —— 一次性(once:到点跑一轮就结束,自动停用) ——
	//
	// 为什么单独存 OnceDate 而不只靠 cron:5 字段 cron **没有年字段**,
	// `0 9 20 11 *` 表达的是「每年 11 月 20 日」,一次性必须另记目标日期。
	// 老计划文件缺这两个字段 → 零值 false/"" = 循环计划,无需迁移。
	Once     bool   `json:"once" yaml:"once"`
	OnceDate string `json:"once_date,omitempty" yaml:"once_date,omitempty"` // 本地日期 YYYY-MM-DD

	// —— 农历年度(每年某个农历日期,如中秋) ——
	//
	// 为什么必须另存:LunarDate 是**农历**月日,cron 表达不了它 ——
	// 「农历八月十五」在公历上每年都在变(2026 是 9/25,2027 是 9/15)。
	// 格式 "MM-DD";**"MM-0" 表示该月最后一天**(除夕要用:腊月有时 29 天有时 30 天)。
	// 设了它时,`Cron` 只承载时分(日/月/周全为 `*`),宿主按农历算排期。
	LunarDate string `json:"lunar_date,omitempty" yaml:"lunar_date,omitempty"`

	// 运行记录(只保留最近一次;每轮详细产出在会话记录里)。
	LastRunAt  time.Time        `json:"last_run_at,omitempty" yaml:"last_run_at,omitempty"`
	LastStatus ScheduleRunState `json:"last_status,omitempty" yaml:"last_status,omitempty"`
	LastError  string           `json:"last_error,omitempty" yaml:"last_error,omitempty"`

	// NextRun 宿主机算的下次触发时刻(只读派生值,**不落盘**:cron 变了旧值即失效)。
	// 零值 = 当前无下次触发(已停用;或 cron 永不匹配未来时刻)。
	// 注:time.Time 不支持 omitempty(结构体),故 JSON 里恒出现取零值 —— 与 Job.DoneAt 同例。
	NextRun time.Time `json:"next_run,omitempty" yaml:"-"`

	// —— 只读派生值(宿主算出,**不落盘**) ——

	// CronLabel 中文排期描述(如「每周一 06:30」);空串 = 控件表达不了,调用方回显原表达式。
	CronLabel string `json:"cron_label,omitempty" yaml:"-"`
	// NextRuns 接下来几次触发的时刻(给 UI 做「接下来三次」预览)。
	// 不会 cron 的用户没有别的验收手段 —— 看不见预览就等于盲存一条每分钟烧 token 的计划。
	NextRuns []time.Time `json:"next_runs,omitempty" yaml:"-"`
}

// ScheduleView 排期解释视图(解析层唯一输出;UI 据此渲染控件与预览)。
type ScheduleView struct {
	Cron     string      `json:"cron"`
	Label    string      `json:"label"`            // 中文排期描述
	NextRuns []time.Time `json:"next_runs"`        // 接下来 1-3 次(once 只 1 次;无匹配则空)
	Repeat   string      `json:"repeat"`           // 档位:daily/weekly/…/custom
	Minute   int         `json:"minute,omitempty"` // 时刻(分)
	Hour     int         `json:"hour,omitempty"`   // 时刻(时)
	Dows     []int       `json:"dows,omitempty"`   // weekly 选中的周几(0=周日)
	Month    int         `json:"month,omitempty"`  // annual_date / lunar_annual 的月
	Day      int         `json:"day,omitempty"`    // monthly_day 几号;annual/lunar 的日(0 = 该月最后一天)
	// Festival 节日名(中秋/春节…),仅用于文案。
	Festival  string `json:"festival,omitempty"`
	Nth       int    `json:"nth,omitempty"`   // monthly_nth 第几个(1-5)
	Every     int    `json:"every,omitempty"` // every_n_min/every_n_hour 的间隔数
	Once      bool   `json:"once,omitempty"`
	OnceDate  string `json:"once_date,omitempty"`
	LunarDate string `json:"lunar_date,omitempty"`
	// AssumedTime=true 表示用户那句话里没写时刻,当前时刻是补的默认值 ——
	// UI 必须回显告知(如「已按 09:00 设好」),否则用户会以为是自己写的那个时间。
	AssumedTime bool `json:"assumed_time,omitempty"`
	// Converged 非空 = 用户说的是区间/模糊说法,被收敛成了确定排期(如「月底前」→「月底」)。
	// UI 必须说出来(「按「月底」理解为…」):收敛是可以的,但猜了不说就成了静默错误。
	Converged string `json:"converged,omitempty"`
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

// ScheduleResolver 排期解析服务(**ScheduleService 的可选扩展**,由 host-schedule 实现)。
//
// 为什么是可选扩展而不是独立 ctx key:注入点能少一个是一个 ——
// ScheduleService 的实现者本来就该同时提供解析能力,web 侧一次类型断言即可拿到,
// 未实现时 resolve 端点显式报「不支持」(不静默降级成假的解析结果)。
//
// 存在的原因:不会 cron 的用户是默认前提,排期解析(含自然语言)必须**只有一处权威** ——
// 两套解析必然漂移,而漂移的后果是「用户看到的排期和实际触发的排期不一样」。
type ScheduleResolver interface {
	// ResolveCron 解释一条 cron 表达式:反解成控件档位 + 中文描述 + 接下来几次触发时刻。
	// ok=false = 该表达式合法但控件表达不了(repeat=custom),调用方按只读展示。
	ResolveCron(expr string) (ScheduleView, error)
	// ResolveText 解析一句中文排期(「每周一三五 早上八点半」)→ 同上。
	// 未命中返回可读错误,调用方**不得**当作致命错误(回退到控件即可)。
	ResolveText(text string) (ScheduleView, error)
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
