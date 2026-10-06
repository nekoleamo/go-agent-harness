// 控件态 ↔ cron 双向映射单测(cronexpr.go)。
//
// 验证的是两条纪律:
//  1. **round-trip 幂等**:控件能生成的表达式,一定能反解回同一个控件态(否则
//     「编辑时回填」会把用户已选的排期改掉 —— 比建错更糟)。
//  2. **不可表达的必须判 custom**:宁可只读展示,也不要把一条复杂表达式
//     悄悄简化成别的排期(用户看到的和实际触发的会不一致)。
package hostschedule

import (
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestStructToCronAllKinds(t *testing.T) {
	cases := []struct {
		s    cronStruct
		want string
	}{
		{cronStruct{Repeat: RepeatDaily, Minute: 0, Hour: 8}, "0 8 * * *"},
		{cronStruct{Repeat: RepeatWeekly, Minute: 30, Hour: 6, Dows: []int{1}}, "30 6 * * 1"},
		{cronStruct{Repeat: RepeatWeekly, Minute: 0, Hour: 9, Dows: []int{5, 3, 1}}, "0 9 * * 1,3,5"},
		{cronStruct{Repeat: RepeatWeekdays, Minute: 0, Hour: 9}, "0 9 * * 1-5"},
		{cronStruct{Repeat: RepeatMonthDay, Minute: 0, Hour: 0, Day: 5}, "0 0 5 * *"},
		{cronStruct{Repeat: RepeatMonthNth, Minute: 0, Hour: 9, Nth: 2, Dows: []int{2}}, "0 9 8-14 * 2"},
		{cronStruct{Repeat: RepeatMonthNth, Minute: 0, Hour: 9, Nth: 5, Dows: []int{3}}, "0 9 29-31 * 3"},
		{cronStruct{Repeat: RepeatMonthLast, Minute: 0, Hour: 9}, "0 9 L * *"},
		{cronStruct{Repeat: RepeatHourly, Minute: 0}, "0 * * * *"},
		{cronStruct{Repeat: RepeatHourly, Minute: 30}, "30 * * * *"},
		{cronStruct{Repeat: RepeatEveryNMin, Every: 15}, "*/15 * * * *"},
		{cronStruct{Repeat: RepeatEveryNHour, Every: 6, Minute: 0}, "0 */6 * * *"},
		{cronStruct{Repeat: RepeatEveryNHour, Every: 2, Minute: 30}, "30 */2 * * *"},
	}
	for _, tc := range cases {
		got, ok := structToCron(tc.s)
		if !ok || got != tc.want {
			t.Fatalf("%+v:期望 %q,得 %q(ok=%v)", tc.s, tc.want, got, ok)
		}
	}
}

func TestStructToCronRejects(t *testing.T) {
	// 每分钟不提供;区间越界;缺参数 —— 一律拒绝,不生成半成品表达式。
	cases := []cronStruct{
		{Repeat: RepeatEveryNMin, Every: 1},  // 每分钟
		{Repeat: RepeatEveryNMin, Every: 60}, // 越界
		{Repeat: RepeatEveryNHour, Every: 0},
		{Repeat: RepeatWeekly},                              // 没选周几
		{Repeat: RepeatMonthDay, Day: 0},                    // 没选几号
		{Repeat: RepeatMonthDay, Day: 32},                   // 越界
		{Repeat: RepeatMonthNth, Nth: 2},                    // 没选周几
		{Repeat: RepeatMonthNth, Nth: 6, Dows: []int{1}},    // 第 6 个不存在
		{Repeat: RepeatMonthNth, Nth: 2, Dows: []int{1, 2}}, // 目标周几必须单值
		{Repeat: RepeatDaily, Minute: 60},
		{Repeat: RepeatDaily, Hour: 24},
		{Repeat: RepeatCustom},
	}
	for _, s := range cases {
		if got, ok := structToCron(s); ok {
			t.Fatalf("%+v 应被拒,却生成了 %q", s, got)
		}
	}
}

// 核心不变量:控件能生成的每个档位,反解回来必须还是同一个控件态。
func TestCronStructRoundTrip(t *testing.T) {
	kinds := []cronStruct{
		{Repeat: RepeatDaily, Minute: 0, Hour: 8},
		{Repeat: RepeatDaily, Minute: 45, Hour: 23},
		{Repeat: RepeatWeekly, Minute: 30, Hour: 6, Dows: []int{1}},
		{Repeat: RepeatWeekly, Minute: 0, Hour: 9, Dows: []int{1, 3, 5}},
		{Repeat: RepeatWeekly, Minute: 0, Hour: 22, Dows: []int{0, 6}}, // 含周日
		{Repeat: RepeatWeekdays, Minute: 15, Hour: 10},
		{Repeat: RepeatMonthDay, Minute: 0, Hour: 0, Day: 1},
		{Repeat: RepeatMonthDay, Minute: 30, Hour: 12, Day: 31}, // 31 号:靠预览告知跳月
		{Repeat: RepeatMonthNth, Minute: 0, Hour: 9, Nth: 1, Dows: []int{1}},
		{Repeat: RepeatMonthNth, Minute: 0, Hour: 9, Nth: 2, Dows: []int{2}},
		{Repeat: RepeatMonthNth, Minute: 0, Hour: 9, Nth: 3, Dows: []int{3}},
		{Repeat: RepeatMonthNth, Minute: 0, Hour: 9, Nth: 4, Dows: []int{4}},
		{Repeat: RepeatMonthNth, Minute: 0, Hour: 9, Nth: 5, Dows: []int{5}},
		{Repeat: RepeatMonthLast, Minute: 0, Hour: 9},
		{Repeat: RepeatHourly, Minute: 0},
		{Repeat: RepeatHourly, Minute: 30},
		{Repeat: RepeatEveryNMin, Every: 5},
		{Repeat: RepeatEveryNMin, Every: 59},
		{Repeat: RepeatEveryNHour, Every: 2, Minute: 0},
		{Repeat: RepeatEveryNHour, Every: 23, Minute: 45},
	}
	for _, want := range kinds {
		expr, ok := structToCron(want)
		if !ok {
			t.Fatalf("%+v structToCron 失败", want)
		}
		got, ok := cronToStruct(expr)
		if !ok {
			t.Fatalf("%q(%+v)反解失败", expr, want)
		}
		if got.Repeat != want.Repeat || got.Minute != want.Minute || got.Hour != want.Hour ||
			got.Day != want.Day || got.Nth != want.Nth || got.Every != want.Every {
			t.Fatalf("%q:反解得 %+v,期望 %+v", expr, got, want)
		}
		if !sameInts(got.Dows, want.Dows) {
			t.Fatalf("%q:反解周几得 %v,期望 %v", expr, got.Dows, want.Dows)
		}
	}
}

// sameInts 顺序敏感的切片相等(nil 与空切片视为相等)。
func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 控件表达不了的表达式必须判 custom —— 绝不简化成「看起来像」的档位。
func TestCronToStructCustom(t *testing.T) {
	for _, expr := range []string{
		"5,35 8-10 * * 1,3", // 分/时/日/周都受限
		"0 8 1 * 1",         // 日+周**同时**受限 = OR 语义(真正的陷阱)
		"0 9 1-15 * *",      // 日区间
		"0 9 * jan *",       // 限定月份
		"0 9 L * 1",         // 月末 + 周(OR 语义)
		"0 8-18 * * *",      // 时区间
		"* * * * *",         // 每分钟(控件不提供)
		"0 0 30 2 *",        // 永不触发
		"不是 cron",
	} {
		if got, ok := cronToStruct(expr); ok {
			t.Fatalf("%q 应判 custom,却得 %+v", expr, got)
		}
	}
}

// 语义等价性:反解出来的档位必须**行为等价**,不只是长得像。
// 「每月 2 号或周一」简写成 monthly_day 会漏掉每周一那半边 —— 这类必须 custom。
func TestCronToStructKeepsOrSemantics(t *testing.T) {
	spec, err := parseCron("0 8 1 * 1")
	if err != nil {
		t.Fatal(err)
	}
	s1 := at(2026, 11, 2, 8, 0) // 每月 1 号
	s2 := at(2026, 11, 9, 8, 0) // 之后的周一
	if !spec.match(s1) || !spec.match(s2) {
		t.Fatal("测试前提不成立:该表达式应同时命中 1 号与周一")
	}
	if _, ok := cronToStruct("0 8 1 * 1"); ok {
		t.Fatal("日+周双受限的 OR 语义不得被简写成单一档位")
	}
}

// nthOfWindow:窗口识别的边界。
func TestNthOfWindow(t *testing.T) {
	cases := []struct {
		expr string
		nth  int
		ok   bool
	}{
		{"0 9 1-7 * 1", 1, true},
		{"0 9 8-14 * 2", 2, true},
		{"0 9 22-28 * 4", 4, true},
		{"0 9 29-31 * 5", 5, true},  // 钳位窗口
		{"0 9 29-35 * 5", 0, false}, // 越界:解析就失败
		{"0 9 2-8 * 1", 0, false},   // 窗口起点不对齐
		{"0 9 1-6 * 1", 0, false},   // 不足 7 天
		{"0 9 1,8 * 1", 0, false},   // 有空洞
		{"0 9 5 * 1", 0, false},     // 单日不是窗口
	}
	for _, tc := range cases {
		spec, err := parseCron(tc.expr)
		if err != nil {
			if tc.ok {
				t.Fatalf("%q 应可解析: %v", tc.expr, err)
			}
			continue
		}
		nth, ok := nthOfWindow(spec.dom)
		if ok != tc.ok || (ok && nth != tc.nth) {
			t.Fatalf("%q:期望 nth=%d,ok=%v;得 nth=%d,ok=%v", tc.expr, tc.nth, tc.ok, nth, ok)
		}
	}
}

func TestScheduleLabelOnceNotMonthly(t *testing.T) {
	// 一次性计划绝不能被说成「每月 20 号」—— 那在骗用户(它只会跑一次)。
	// 注:一次性的 cron 形如 `0 9 20 11 *`(限定月份),控件本就表达不了这种限定月份,
	// 所以它的标签只能来自 OnceDate,绝不来自 cronHuman。
	p := sdk.Schedule{Cron: "0 9 20 11 *", Once: true, OnceDate: "2026-11-20"}
	if got := scheduleLabel(p); got != "仅 2026-11-20 执行一次" {
		t.Fatalf("一次性计划标签应为其目标日期,得 %q", got)
	}
	// 非一次性的 `0 9 20 11 *` 就是「每年 11 月 20 日」,现在能说出来(此前判 custom)
	if got := scheduleLabel(sdk.Schedule{Cron: "0 9 20 11 *"}); got != "每年 11 月 20 日 09:00" {
		t.Fatalf("限定月份的循环排期应给年度文案,得 %q", got)
	}
	// 缺 OnceDate 的异常态(手改文件/旧数据)不得静默说成「每月」
	if got := scheduleLabel(sdk.Schedule{Cron: "0 9 20 11 *", Once: true}); got != "一次性(日期缺失)" {
		t.Fatalf("缺日期的一次性计划应明说异常,得 %q", got)
	}
	// 循环计划走 cronHuman
	if got := scheduleLabel(sdk.Schedule{Cron: "0 9 20 * *"}); got != "每月 20 号 09:00" {
		t.Fatalf("循环计划标签应来自 cronHuman,得 %q", got)
	}
	if got := scheduleLabel(sdk.Schedule{Cron: "5,35 8-10 * * 1,3"}); got != "" {
		t.Fatalf("无法简写的表达式应返回空串,得 %q", got)
	}
}

func TestNextRunsFrom(t *testing.T) {
	spec, err := parseCron("0 9 * * *")
	if err != nil {
		t.Fatal(err)
	}
	got := nextRunsFrom(spec, at(2026, 11, 14, 20, 0), 3)
	want := []time.Time{at(2026, 11, 15, 9, 0), at(2026, 11, 16, 9, 0), at(2026, 11, 17, 9, 0)}
	if len(got) != len(want) {
		t.Fatalf("应得 3 次触发,得 %d 次", len(got))
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("第 %d 次:期望 %v,得 %v", i, want[i], got[i])
		}
	}
	// 永不触发的表达式 → 空(UI 据此显示「不会触发」而不是空白)
	if n := nextRunsFrom(mustParse(t, "0 0 30 2 *"), at(2026, 11, 14, 20, 0), 3); len(n) != 0 {
		t.Fatalf("永不触发的表达式应无下次触发,得 %v", n)
	}
}

func mustParse(t *testing.T, expr string) *cronSpec {
	t.Helper()
	spec, err := parseCron(expr)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

// LW:当月最后一个工作日(**只排除周六周日**,不含法定节假日与调休)。
// 边界写进文案是硬要求 —— 「工作日」在日常语境里常指法定工作日,不说清会让人误判。
func TestCronLastWeekday(t *testing.T) {
	spec, err := parseCron("0 9 LW * *")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		at      time.Time
		matches bool
	}{
		{at(2026, 1, 30, 9, 0), true}, // 1/31 是周六 → 回退到 1/30 周五
		{at(2026, 1, 31, 9, 0), false},
		{at(2026, 2, 27, 9, 0), true}, // 2/28 是周六(平年)→ 回退到 2/27 周五
		{at(2026, 2, 28, 9, 0), false},
		{at(2026, 3, 31, 9, 0), true}, // 3/31 是周二
		{at(2026, 5, 29, 9, 0), true}, // 5/31 是周日 → 回退到 5/29 周五
		{at(2026, 5, 31, 9, 0), false},
		{at(2026, 11, 30, 9, 0), true}, // 11/30 是周一
	} {
		if got := spec.match(tc.at); got != tc.matches {
			t.Fatalf("%s 命中=%v,期望 %v", tc.at.Format("2006-01-02"), got, tc.matches)
		}
	}
}

func TestCronLastWeekdayHumanAndStruct(t *testing.T) {
	if got, want := cronHuman("0 18 LW * *"), "每月最后一个工作日 18:00(遇周末提前到周五,不含法定节假日)"; got != want {
		t.Fatalf("人话得 %q,期望 %q", got, want)
	}
	s, ok := cronToStruct("0 18 LW * *")
	if !ok || s.Repeat != RepeatMonthLW || s.Hour != 18 {
		t.Fatalf("反解得 %+v(ok=%v)", s, ok)
	}
	expr, ok := structToCron(cronStruct{Repeat: RepeatMonthLW, Minute: 0, Hour: 18})
	if !ok || expr != "0 18 LW * *" {
		t.Fatalf("拼得 %q(ok=%v)", expr, ok)
	}
	// 与限制周几并用 = 日-周 OR 语义,控件表达不了 → custom
	if _, ok := cronToStruct("0 18 LW * 1"); ok {
		t.Fatal("LW 与限制周几并用应判 custom")
	}
}

// TestLWBoundarySentenceLocked:「不含法定节假日」这句**边界声明**不许被改掉或从文案里删掉。
//
// 为什么钉一句字符串就算一条测试:LW 只排除周六周日(见 cron.go 的 lastWeekdayOfMonth),
// 而日常语境里的「工作日」几乎总是指「法定工作日」—— 文案不说清,用户会以为调休上班日照跑。
// 同一句话在两处出现(Go 的 labelOf / Web 下拉项 REPEATS 的 hint),哪边被改都会漂移;
// 前端那份由 web-src/src/schedule.test.ts 钉住,此处钉 Go 这份。
func TestLWBoundarySentenceLocked(t *testing.T) {
	for _, expr := range []string{"0 18 LW * *", "30 9 LW * *"} {
		got := cronHuman(expr)
		if !strings.Contains(got, "不含法定节假日") {
			t.Fatalf("%q 的人话 %q 丢了边界声明「不含法定节假日」", expr, got)
		}
		if !strings.Contains(got, "遇周末提前到周五") {
			t.Fatalf("%q 的人话 %q 应说明周末如何处理", expr, got)
		}
	}
}

// TestAnnualDateRejectsDayZero:公历年度档位的 Day==0(该月最后一天)刻意不支持。
//
// 钉的是**语义**,不是现状:写死某年的月末日号会让平年 2 月 29 日静默不跑,
// 而 annualDate.dayIn 那侧按每年实际天数算 —— 两者会分裂成「预览说 28 号、实际不跑」。
// 若将来真要做这个档位,正确形态是 `M H L Mon *`(见 structToCron 里的路标注释),
// 到那时把这条测试改成断言新形态,而不是把 0 又放回来。
func TestAnnualDateRejectsDayZero(t *testing.T) {
	if _, ok := structToCron(cronStruct{Repeat: RepeatAnnualDate, Minute: 0, Hour: 9, Month: 2, Day: 0}); ok {
		t.Fatal("公历年度档位不该接受 Day==0(该月最后一天)")
	}
	// 农历那条路(除夕)不受影响:Day==0 落在 RepeatLunarAnnual,日期走 LunarDate。
	expr, ok := structToCron(cronStruct{Repeat: RepeatLunarAnnual, Minute: 0, Hour: 9, Month: 12, Day: 0})
	if !ok || expr != "0 9 * * *" {
		t.Fatalf("农历除夕应仍能拼出纯时分表达式,得 %q(ok=%v)", expr, ok)
	}
}

func TestParseCronLastWeekdayInvalid(t *testing.T) {
	cases := map[string]string{
		"0 18 LW,15 * *": "不能与其他取值并存",
		"0 18 LW/2 * *":  "不支持步长",
		"0 18 L,LW * *":  "重复出现",
		"0 LW * * *":     "只有「日」字段支持 LW",
		"0 18 * * LW":    "只有「日」字段支持 LW",
	}
	for expr, want := range cases {
		_, err := parseCron(expr)
		if err == nil {
			t.Fatalf("%q 应被拒", expr)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%q 报错应含 %q,得 %q", expr, want, err.Error())
		}
	}
}
