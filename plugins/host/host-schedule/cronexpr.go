// cronexpr.go:cron 表达式 ↔ 「重复方式控件态」的双向映射(普通人层的解析层)。
//
// 为什么单独一份而不是让 UI 自己拼:5 字段 cron 的语义**只此一处**是权威
// (cron.go 的 parseCron/dayMatch)。若前端再做一次「反解」,同一批表达式就会有两套
// 判定,必然漂移 —— schedule.ts 现在只做形状校验、刻意不做 parser,就是这条纪律。
// 于是分工:Go 侧判定(本文件),前端只把控件状态原样搬过来、也只把本文件的结果原样画出去。
//
// 档位(RepeatKind)共 10 档,另含兜底 custom —— 控件表达不了的表达式(如模型 tool 侧
// 写歪的 `5,35 8-10 * * 1,3`)一律 custom,UI 只读展示,绝不静默改写成别的排期。
//
// 「每分钟」(`* * * * *`)**不在档位内**:它等于每分钟烧一轮模型,控件不给这个选项;
// 表达式的 cronHuman 仍会给它一句文案(CLI/存量文件仍要能读懂)。
package hostschedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// RepeatKind 排期档位(custom = 控件表达不了,只读展示)。
type RepeatKind string

const (
	RepeatDaily       RepeatKind = "daily"                // 每天
	RepeatWeekly      RepeatKind = "weekly"               // 每周(周几多选)
	RepeatWeekdays    RepeatKind = "weekdays"             // 每个工作日
	RepeatMonthDay    RepeatKind = "monthly_day"          // 每月几号
	RepeatMonthNth    RepeatKind = "monthly_nth"          // 每月第 N 个周几
	RepeatMonthLast   RepeatKind = "monthly_last"         // 每月最后一天(`L`)
	RepeatMonthLW     RepeatKind = "monthly_last_workday" // 每月最后一个工作日(`LW`;只排除周末)
	RepeatAnnualDate  RepeatKind = "annual_date"          // 每年某天(公历;
	RepeatLunarAnnual RepeatKind = "lunar_annual"         // 每年某天(农历;cron 表达不了)
	RepeatHourly      RepeatKind = "hourly"               // 每小时(第 M 分)
	RepeatEveryNMin   RepeatKind = "every_n_min"          // 每 N 分钟
	RepeatEveryNHour  RepeatKind = "every_n_hour"         // 每 N 小时(第 M 分)
	RepeatOnce        RepeatKind = "once"                 // 只跑一次(目标日期在 OnceDate)
	RepeatCustom      RepeatKind = "custom"               // 控件表达不了
)

// cronStruct 控件态(与 sdk.ScheduleView 的可写字段一一对应)。
type cronStruct struct {
	Repeat RepeatKind
	Minute int
	Hour   int
	Dows   []int // weekly:选中的周几(0=周日,与 cron 一致)
	Day    int   // monthly_day / annual_date / lunar_annual:日
	// Day == 0 是**「该月最后一天」的哨兵值**,只在年度档位用
	// (除夕:腊月可能 29 天也可能 30 天,写死 30 会在月小的年份算不出来)。
	Nth   int // monthly_nth:第几个(1-5)
	Every int // every_n_min / every_n_hour:间隔数
	// OnceDate 仅 RepeatOnce 用(YYYY-MM-DD)。一次性不靠 cron 表达日期 ——
	// 5 字段没有年字段,`0 9 20 11 *` 是「每年 11 月 20 日」,必须另记目标日期。
	OnceDate string
	// Month 供年度档位用(annual_date 与 lunar_annual)。
	Month int
	// Festival 是可选的节日名(中秋/春节…),**只影响文案**(「每年中秋节(农历八月十五)」
	// 比「每年农历八月十五」好读),不参与任何排期计算。
	Festival string
	// annual 内部标记:文本里说了「每年」—— 用来把随后解析出的日期从「一次性」
	// 改成「年度重复」(「每年10月1日」vs「10月1日」)。解析结束前会被清掉。
	annual bool
}

// structToCron 控件态 → cron 表达式。ok=false = 该状态非法(不生成半成品表达式)。
func structToCron(s cronStruct) (string, bool) {
	if s.Minute < 0 || s.Minute > 59 || s.Hour < 0 || s.Hour > 23 {
		return "", false
	}
	hm := fmt.Sprintf("%d %d", s.Minute, s.Hour)
	switch s.Repeat {
	case RepeatDaily:
		return hm + " * * *", true
	case RepeatWeekly:
		if len(s.Dows) == 0 {
			return "", false
		}
		return hm + " * * " + dowExpr(s.Dows), true
	case RepeatWeekdays:
		return hm + " * * 1-5", true
	case RepeatMonthDay:
		if s.Day < 1 || s.Day > 31 {
			return "", false
		}
		return fmt.Sprintf("%s %d * *", hm, s.Day), true
	case RepeatMonthNth:
		// 第 N 个周几 = 该 N 个 7 天窗口里的目标周几;连续 7 天必含每个周几各一次
		// ⇒ 窗口内唯一命中。N=5 时窗口 29-35 被钳到 31(该月若没有第 5 个就跳过,
		// 与「没有就跳过」的直觉一致)。
		if len(s.Dows) != 1 || s.Nth < 1 || s.Nth > 5 {
			return "", false
		}
		start := (s.Nth-1)*7 + 1
		end := s.Nth * 7
		if end > 31 {
			end = 31
		}
		return fmt.Sprintf("%s %d-%d * %d", hm, start, end, s.Dows[0]), true
	case RepeatMonthLast:
		return hm + " L * *", true
	case RepeatMonthLW:
		return hm + " LW * *", true
	case RepeatAnnualDate:
		// 公历年度排期 cron 本身就能表达(`M H D Mon *`),不需要额外的存储字段。
		//
		// **Day == 0(该月最后一天)在这里刻意不支持**:唯一会出现 0 的是除夕,
		// 而除夕走 RepeatLunarAnnual + `LunarDate`(它在公历上逐年不同,cron 表达不了);
		// 公历日期一律由 parseDate 产出,恒 ≥ 1。
		// 若将来真要做「每年某月最后一天」:用 `M H L Mon *` —— `cron.go` 的 `L`
		// 在 matchDay 里按**当天所在月**的 monthLen 现算,平年 28 / 闰年 29 自动正确。
		// **不要**写死某年的月末日号:那样平年 2 月 29 日会静默不跑,且与
		// `annualDate.dayIn`(按每年实际天数算)那侧分裂成「预览说 28 号、实际不跑」。
		if s.Month < 1 || s.Month > 12 || s.Day < 1 || s.Day > 31 {
			return "", false
		}
		return fmt.Sprintf("%s %d %d *", hm, s.Day, s.Month), true
	case RepeatLunarAnnual:
		// 农历年度**无法用 cron 表达**(公历上每年都在变):cron 只承载时分,
		// 真正的月日在 sdk.Schedule.LunarDate 里。日字段刻意留 `*` ——
		// 万一哪天农历逻辑失效,它退化成「每天跑」这个**明显错误**的行为,
		// 而不是悄悄变成某些日期上的错误排期。
		if s.Month < 1 || s.Month > 12 || s.Day < 0 || s.Day > 30 {
			return "", false
		}
		return hm + " * * *", true
	case RepeatHourly:
		return fmt.Sprintf("%d * * * *", s.Minute), true
	case RepeatEveryNMin:
		if s.Every < 2 || s.Every > 59 { // 下限 2:「每分钟」不进控件
			return "", false
		}
		return fmt.Sprintf("*/%d * * * *", s.Every), true
	case RepeatEveryNHour:
		if s.Every < 2 || s.Every > 23 {
			return "", false
		}
		return fmt.Sprintf("%d */%d * * *", s.Minute, s.Every), true
	case RepeatOnce:
		d, err := time.Parse("2006-01-02", s.OnceDate)
		if err != nil {
			return "", false
		}
		return fmt.Sprintf("%d %d %d %d *", s.Minute, s.Hour, d.Day(), int(d.Month())), true
	default:
		return "", false
	}
}

// dowExpr 周几集合 → cron 周字段(升序逗号列表;空/越界返回空串)。
func dowExpr(dows []int) string {
	if len(dows) == 0 {
		return ""
	}
	out := append([]int(nil), dows...)
	sort.Ints(out)
	parts := make([]string, 0, len(out))
	for _, d := range out {
		if d < 0 || d > 6 {
			return ""
		}
		parts = append(parts, strconv.Itoa(d))
	}
	return strings.Join(parts, ",")
}

// cronToStruct cron 表达式 → 控件态。ok=false = 解析失败或控件表达不了(一律 custom)。
//
// 判定顺序 = **从最特殊到最一般**:步长类形态必须先判,否则 `*/30 * * * *`
// 会被当成普通「每天」而丢掉步长信息(那是每 30 分钟,不是每天)。
func cronToStruct(expr string) (cronStruct, bool) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return cronStruct{}, false
	}
	spec, err := parseCron(expr)
	if err != nil {
		return cronStruct{}, false
	}
	// 日/月/周必须全通配才可能落在步长类档位上。
	plain := f[2] == "*" && f[3] == "*" && f[4] == "*"
	switch {
	case plain && strings.HasPrefix(f[0], "*/"):
		if n, err := strconv.Atoi(f[0][2:]); err == nil && n >= 2 && n <= 59 {
			return cronStruct{Repeat: RepeatEveryNMin, Every: n}, true
		}
	case plain && strings.HasPrefix(f[1], "*/"):
		if n, err := strconv.Atoi(f[1][2:]); err == nil && n >= 2 && n <= 23 && isSingle(spec.min) {
			return cronStruct{Repeat: RepeatEveryNHour, Every: n, Minute: spec.min.only()}, true
		}
	}
	// 「每小时第 M 分」(`M * * * *`)必须先于「分、时均单值」的判定:
	// 时字段是通配(24 个取值),若等到后面就会被当成非单值而误拒。
	if f[1] == "*" && plain && isSingle(spec.min) {
		return cronStruct{Repeat: RepeatHourly, Minute: spec.min.only()}, true
	}
	// 以下档位都要求「分、时各为单值」(每分钟 `* *` 不在任何档位内)。
	if !isSingle(spec.min) || !isSingle(spec.hour) {
		return cronStruct{}, false
	}
	s := cronStruct{Minute: spec.min.only(), Hour: spec.hour.only()}
	if f[3] != "*" {
		// 限定月份的两种情形:每年某天(可用 cron 表达,反解得出来),
		// 或其它复杂形态(控件表达不了)。
		if f[4] == "*" && isSingle(spec.dom) {
			mo, day := int(spec.mon.only()), spec.dom.only()
			// 2 月 30 日这类**永远不存在**的日期不能给自信的年度文案(它一次也不会跑)
			if !solarDayValid(mo, day) {
				return cronStruct{}, false
			}
			return cronStruct{Repeat: RepeatAnnualDate, Minute: s.Minute, Hour: s.Hour,
				Month: mo, Day: day}, true
		}
		return cronStruct{}, false
	}
	switch {
	case spec.dom.lw:
		// 周未限定才干净;与周并用会走日-周 OR 语义,不是「月末工作日」本意。
		if f[4] == "*" {
			return cronStruct{Repeat: RepeatMonthLW, Minute: s.Minute, Hour: s.Hour}, true
		}
		return cronStruct{}, false
	case spec.dom.last:
		if f[4] == "*" { // 周未限定才干净;与周并用会走日-周 OR 语义,不是「月末」本意
			return cronStruct{Repeat: RepeatMonthLast, Minute: s.Minute, Hour: s.Hour}, true
		}
		return cronStruct{}, false
	case f[2] == "*":
		dows := sortedSet(spec.dow)
		if len(dows) == 7 { // 周全限定 = 每天(等价 `* *`)
			return cronStruct{Repeat: RepeatDaily, Minute: s.Minute, Hour: s.Hour}, true
		}
		if isSet(dows, weekdays1to5) {
			return cronStruct{Repeat: RepeatWeekdays, Minute: s.Minute, Hour: s.Hour}, true
		}
		if f[4] == "*" && len(dows) == 0 {
			return cronStruct{Repeat: RepeatDaily, Minute: s.Minute, Hour: s.Hour}, true
		}
		if len(dows) > 0 {
			return cronStruct{Repeat: RepeatWeekly, Minute: s.Minute, Hour: s.Hour, Dows: dows}, true
		}
		return cronStruct{}, false
	default:
		// 日被限定。两种可表达形态:
		//  ① 每月第 N 个周几 = `M H <7 天窗口> * <单值周几>` —— 周字段是单值(不是 `*`),
		//     靠「连续 7 天窗口必含每个周几各一次」保证窗口内唯一命中。
		//  ② 每月几号 = `M H <单值日> * *` —— 需周字段全通配。
		if spec.dow.count() == 1 {
			if nth, ok := nthOfWindow(spec.dom); ok {
				return cronStruct{Repeat: RepeatMonthNth, Minute: s.Minute, Hour: s.Hour,
					Nth: nth, Dows: sortedSet(spec.dow)}, true
			}
		}
		if f[4] == "*" && isSingle(spec.dom) {
			return cronStruct{Repeat: RepeatMonthDay, Minute: s.Minute, Hour: s.Hour,
				Day: spec.dom.only()}, true
		}
		// 日与周同时受限(如 `0 8 1 * 1`)= 「1 号或周一」的 OR 语义,
		// 以及限定月份(`* jan`)、日区间(`1-15`)、每分钟等 —— 控件都没有对应档位。
		// 简写成任何一种都会说谎,故判 custom。
		return cronStruct{}, false
	}
}

var weekdays1to5 = []int{1, 2, 3, 4, 5}

// nthOfWindow 识别「第 N 个周几」的日窗口:7 天连续窗口(1-7/8-14/…/22-28),
// 或被钳位的 29-31(第 5 个)。返回 false = 不是这种形态。
func nthOfWindow(f *cronField) (int, bool) {
	lo, hi, contiguous := intRangeOf(f)
	if !contiguous {
		return 0, false
	}
	if lo < 1 || (lo-1)%7 != 0 {
		return 0, false // 窗口起点不是 (N-1)*7+1
	}
	if hi-lo+1 != 7 && !(lo == 29 && hi == 31) {
		return 0, false // 既不是完整 7 天窗口,也不是钳位窗口
	}
	return (lo-1)/7 + 1, true
}

// intRangeOf 取字段取值集合的 [lo,hi];集合有空洞则 ok=false。
// 只统计**为 true** 的值:map 里理论上不应有 false 键(parseCron 写入时已避开),
// 但遍历统计一旦把它算进来就会凭空多出一个取值。
func intRangeOf(f *cronField) (lo, hi int, ok bool) {
	lo, hi = 1<<30, -1
	for v, on := range f.vals {
		if !on {
			continue
		}
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	if hi < lo {
		return 0, 0, false
	}
	if f.count() != hi-lo+1 {
		return lo, hi, false
	}
	return lo, hi, true
}

// isSingle 字段恰有一个取值。
func isSingle(f *cronField) bool { return f.count() == 1 }

// count 字段真值取值的个数。
func (f *cronField) count() int {
	n := 0
	for _, on := range f.vals {
		if on {
			n++
		}
	}
	return n
}

// only 单取值字段的取值(调用方须先用 isSingle 判定)。
func (f *cronField) only() int {
	for v := range f.vals {
		return v
	}
	return 0
}

// sortedSet 字段取值集合 → 升序切片(只收真值)。
func sortedSet(f *cronField) []int {
	out := make([]int, 0, f.count())
	for v, on := range f.vals {
		if on {
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

func isSet(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// —— 中文文案 ——

var dowZh = []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

var nthZh = []string{"", "第一个", "第二个", "第三个", "第四个", "第五个"}

// dowZhList 周几集合 → 中文(多日用「一、三、五」序数简写)。
// 序数必须查表:一二三四五六 的 Unicode 码点并不连续(+1 会算出「丂丄」)。
func dowZhList(dows []int) string {
	if len(dows) == 0 {
		return ""
	}
	orderZh := []string{"日", "一", "二", "三", "四", "五", "六"}
	parts := make([]string, 0, len(dows))
	for _, d := range dows {
		if d < 0 || d > 6 {
			return ""
		}
		parts = append(parts, orderZh[d])
	}
	return strings.Join(parts, "、")
}

// cronHuman 给一句中文人话描述;无法简写返回 ""(调用方回显表达式本身)。
//
// 实现上**委托 cronToStruct**:能落到档位的形态一律由 labelOf 出文案,避免
// 「中文描述」与「控件档位」两套判定漂移(它们必须始终一致 —— 列表显示的
// 就是用户在编辑时看到的那个下拉项)。
func cronHuman(expr string) string {
	f := strings.Fields(expr)
	// 每分钟不在任何档位内(控件不提供),单独给文案:它仍可能是手写/存量表达式。
	if len(f) == 5 && f[0] == "*" && f[1] == "*" && f[2] == "*" && f[3] == "*" && f[4] == "*" {
		return "每分钟"
	}
	s, ok := cronToStruct(expr)
	if !ok {
		return ""
	}
	return labelOf(s)
}

// labelOf 控件态 → 中文排期描述(与 RepeatKind 一一对应)。
func labelOf(s cronStruct) string {
	hm := fmt.Sprintf("%02d:%02d", s.Hour, s.Minute)
	switch s.Repeat {
	case RepeatDaily:
		return "每天 " + hm
	case RepeatWeekly:
		if len(s.Dows) == 1 {
			return "每" + dowZh[s.Dows[0]] + " " + hm
		}
		return "每周" + dowZhList(s.Dows) + " " + hm
	case RepeatWeekdays:
		return "每个工作日 " + hm
	case RepeatMonthDay:
		return fmt.Sprintf("每月 %d 号 %s", s.Day, hm)
	case RepeatMonthNth:
		return fmt.Sprintf("每月%s%s %s", nthZh[s.Nth], dowZh[s.Dows[0]], hm)
	case RepeatMonthLast:
		return "每月最后一天 " + hm
	case RepeatMonthLW:
		// 文案必须带「不含法定节假日」这个边界 —— 不说清用户会以为调休上班日照跑。
		// 措辞与 Web 下拉项的 hint(web-src/src/schedule.ts 的 REPEATS)用同一套词,
		// 两处各有一道测试钉住这句边界声明(漂移即红),不追求字面同串。
		return "每月最后一个工作日 " + hm + "(遇周末提前到周五,不含法定节假日)"
	case RepeatAnnualDate:
		d := s.Day
		if d == 0 {
			return fmt.Sprintf("每年 %d 月最后一天 %s", s.Month, hm)
		}
		return fmt.Sprintf("每年 %d 月 %d 日 %s", s.Month, d, hm)
	case RepeatLunarAnnual:
		desc := lunarMDText(s.Month, s.Day)
		if s.Festival != "" {
			return fmt.Sprintf("每年%s(%s)%s", s.Festival, desc, hm)
		}
		return "每年" + desc + " " + hm
	case RepeatHourly:
		if s.Minute == 0 {
			return "每小时整点"
		}
		return fmt.Sprintf("每小时第 %d 分", s.Minute)
	case RepeatEveryNMin:
		return fmt.Sprintf("每 %d 分钟", s.Every)
	case RepeatEveryNHour:
		if s.Minute == 0 {
			return fmt.Sprintf("每 %d 小时", s.Every)
		}
		return fmt.Sprintf("每 %d 小时的第 %d 分", s.Every, s.Minute)
	default:
		return ""
	}
}

// scheduleLabel 一条计划的中文排期描述。
//
// 与 cronHuman 的差别只在一处:一次性计划**绝不能**显示成「每月 20 号」——
// 那会让用户以为它会一直重复。once 由 OnceDate 决定,与 cron 无关,故在此单独处理。
func scheduleLabel(p sdk.Schedule) string {
	if p.Once {
		if p.OnceDate == "" {
			return "一次性(日期缺失)"
		}
		return fmt.Sprintf("仅 %s 执行一次", p.OnceDate)
	}
	if p.LunarDate != "" {
		// 农历计划的 cron 只承载时分 —— 直接读 cron 会得到「每天 20:00」,
		// 那是彻底的错话(它一年才跑一次)。
		if lbl := lunarPlanLabel(p); lbl != "" {
			return lbl
		}
	}
	return cronHuman(p.Cron)
}

// lunarPlanLabel 农历年度计划的中文描述(时分取自 cron;月日取自 LunarDate)。
// 拿不到时分就返回空串,让调用方回退 —— 不编一个看似合理的排期。
func lunarPlanLabel(p sdk.Schedule) string {
	a, err := parseAnnual(p.LunarDate, true)
	if err != nil {
		return ""
	}
	spec, err := parseCron(p.Cron)
	if err != nil || !isSingle(spec.min) || !isSingle(spec.hour) {
		return ""
	}
	return labelOf(cronStruct{
		Repeat: RepeatLunarAnnual, Minute: spec.min.only(), Hour: spec.hour.only(),
		Month: a.Month, Day: a.Day, Festival: festivalNameOfLunar(a.Month, a.Day),
	})
}

// nextRunsFrom 从 after 起连续取 n 个命中时刻(给 UI 做「接下来几次」预览)。
func nextRunsFrom(spec *cronSpec, after time.Time, n int) []time.Time {
	var out []time.Time
	cur := after
	for i := 0; i < n; i++ {
		t, ok := spec.next(cur)
		if !ok {
			break
		}
		out = append(out, t)
		cur = t
	}
	return out
}
