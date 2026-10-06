// resolver.go:ScheduleResolver 实现 —— 排期解析层的对外入口(cron 解释 / 中文解析)。
//
// 为什么单独一个文件:解析层有两副面孔(机器表达式、自然语言),但**出口只有一个**。
// UI 拿到 ScheduleView 后既不解析 cron 也不自己判断时刻,只按 repeat 渲染控件、
// 按 next_runs 渲染预览 —— 于是「用户看到的」和「实际触发的」不可能分叉。
package hostschedule

import (
	"fmt"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// previewRuns 循环计划给几次预览。3 次是够用的最小值:足够看出「2 月会跳过」
// 与「比我想的频繁」这两类最贵的错。
const previewRuns = 3

// defaultHour 未指定时刻时的兜底时(表单一般会带上当前值,这条只兜底裸调用)。
const defaultHour = 9

// ResolveCron 解释一条 cron 表达式:反解成控件档位 + 中文描述 + 接下来三次触发。
// 表达式合法但控件表达不了时不算错误:返回 repeat=custom、label 空串,调用方只读展示。
func (s *Scheduler) ResolveCron(expr string) (sdk.ScheduleView, error) {
	return s.resolveCronAt(expr, time.Now())
}

func (s *Scheduler) resolveCronAt(expr string, now time.Time) (sdk.ScheduleView, error) {
	spec, err := parseCron(expr)
	if err != nil {
		return sdk.ScheduleView{}, err // 已是中文可读原因
	}
	st, ok := cronToStruct(expr)
	view := sdk.ScheduleView{
		Cron:     spec.expr,
		Repeat:   string(RepeatCustom),
		NextRuns: nextRunsFrom(spec, now, previewRuns),
	}
	if !ok {
		// custom:留空 label 而不是编一句 —— UI 会显式说明「这条排期较特殊」。
		return view, nil
	}
	view.Repeat = string(st.Repeat)
	view.Label = labelOf(st)
	view.Minute, view.Hour = st.Minute, st.Hour
	view.Dows, view.Day, view.Nth, view.Every = st.Dows, st.Day, st.Nth, st.Every
	view.Month, view.Festival = st.Month, st.Festival
	return view, nil
}

// ResolveText 解析一句中文排期(如「每周一三五 早上八点半」)。
//
// 失败返回中文原因 —— 调用方**不得**当作致命错误:不会 cron 的用户看到红字就放弃了,
// 正确做法是保留他原来的选择并提示一句「没看懂,可以直接用下面的选择器」。
func (s *Scheduler) ResolveText(text string) (sdk.ScheduleView, error) {
	return s.resolveTextAt(text, time.Now())
}

func (s *Scheduler) resolveTextAt(text string, now time.Time) (sdk.ScheduleView, error) {
	fallback := cronStruct{Minute: 0, Hour: defaultHour}
	parsed, err := parseCronText(text, now, fallback)
	if err != nil {
		return sdk.ScheduleView{}, err
	}
	expr, ok := structToCron(parsed.Struct)
	if !ok {
		return sdk.ScheduleView{}, fmt.Errorf("解析出的排期无法表达:%s", text)
	}
	// 农历年度:cron 里只有时分(日期在 LunarDate),拿它去算预览会得到「每天」这种错话 ——
	// 这一支自己算预览与标签,不复用 cron 那条路。
	if parsed.Struct.Repeat == RepeatLunarAnnual {
		a := annualDate{Month: parsed.Struct.Month, Day: parsed.Struct.Day, Lunar: true,
			LastDayOfMonth: parsed.Struct.Day == 0}
		return sdk.ScheduleView{
			Cron: expr, Label: labelOf(parsed.Struct), Repeat: string(RepeatLunarAnnual),
			Minute: parsed.Struct.Minute, Hour: parsed.Struct.Hour,
			Month: parsed.Struct.Month, Day: parsed.Struct.Day, Festival: parsed.Struct.Festival,
			LunarDate:   lunarMDKey(parsed.Struct.Month, parsed.Struct.Day),
			NextRuns:    a.occurrences(now, parsed.Struct.Hour, parsed.Struct.Minute, previewRuns),
			AssumedTime: parsed.AssumedTime, Converged: parsed.Converged,
		}, nil
	}
	view, err := s.resolveCronAt(expr, now)
	if err != nil {
		return sdk.ScheduleView{}, err
	}
	// 一次性:预览只有一次,且 OnceDate 才是权威。
	// 必须覆盖 NextRuns —— 上面按 cron 算出来的「每年该月该日」对一次性计划是骗人的
	// (它明年不会跑),所以这里换成那一个具体时刻。
	if parsed.Struct.Repeat == RepeatOnce {
		view.Once, view.OnceDate = true, parsed.Struct.OnceDate
		view.NextRuns = nil
		if t, ok := onceNext(view.OnceDate, cronSpecOf(parsed.Struct), now.Location()); ok && t.After(now) {
			view.NextRuns = []time.Time{t}
		}
	}
	view.AssumedTime = parsed.AssumedTime
	view.Converged = parsed.Converged
	return view, nil
}

// cronSpecOf 控件态 → 解析后的 cronSpec(仅用于 onceNext 取时分)。
// structToCron 刚生成过合法表达式,这里再解析一次不会失败;失败则返回 nil,
// onceNext 会当作「无排期」处理。
func cronSpecOf(st cronStruct) *cronSpec {
	expr, ok := structToCron(st)
	if !ok {
		return nil
	}
	spec, err := parseCron(expr)
	if err != nil {
		return nil
	}
	return spec
}

// onceNext 一次性计划的下次触发时刻 = OnceDate + cron 里的时/分。
// spec 为 nil 或日期非法时 ok=false。**不按 cron 搜索** —— cron 无年字段。
func onceNext(onceDate string, spec *cronSpec, loc *time.Location) (time.Time, bool) {
	if spec == nil || loc == nil {
		return time.Time{}, false
	}
	d, err := time.ParseInLocation("2006-01-02", onceDate, loc)
	if err != nil {
		return time.Time{}, false
	}
	h, m := 0, 0
	if isSingle(spec.hour) {
		h = spec.hour.only()
	}
	if isSingle(spec.min) {
		m = spec.min.only()
	}
	return time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, loc), true
}
