// 中文排期解析单测(crontxt.go)。
//
// 三类用例各有用例,因为三类错了的代价不同:
//  1. 正例 —— 解析对了(改错了用户会拿到错的排期);
//  2. **回退值** —— 用户没说的东西(时刻)如何补:补错 = 静默排错时刻;
//  3. 拒绝 —— 解析不出必须明说,绝不能猜(猜错比不排更坏:用户以为它会跑)。
package hostschedule

import (
	"strings"
	"testing"
	"time"
)

// 基准时刻:2026-11-14 是周六,故「下周二」落在 11-17(而不是本周二)。
func txtNow() time.Time { return time.Date(2026, 11, 14, 10, 0, 0, 0, time.Local) }

func mustText(t *testing.T, in string) parsedText {
	t.Helper()
	got, err := parseCronText(in, txtNow(), cronStruct{Minute: 0, Hour: 9})
	if err != nil {
		t.Fatalf("%q 解析失败: %v", in, err)
	}
	return got
}

func TestParseCronTextRepeat(t *testing.T) {
	cases := []struct {
		in     string
		repeat RepeatKind
		min    int
		hour   int
		dows   []int
		day    int
		nth    int
		every  int
	}{
		{in: "每天早上8点", repeat: RepeatDaily, hour: 8},
		{in: "每日9点", repeat: RepeatDaily, hour: 9},
		{in: "每天下午3点半", repeat: RepeatDaily, hour: 15, min: 30},
		{in: "每周一9点", repeat: RepeatWeekly, hour: 9, dows: []int{1}},
		{in: "每周一三五8点", repeat: RepeatWeekly, hour: 8, dows: []int{1, 3, 5}},
		{in: "每周一、三、五8点", repeat: RepeatWeekly, hour: 8, dows: []int{1, 3, 5}},
		{in: "每周日10点", repeat: RepeatWeekly, hour: 10, dows: []int{0}},
		// 数字写法:中文为空才启用数字分支,所以「每周日10点」的 10 不会被吃成周一
		{in: "每周1,3,5 9点", repeat: RepeatWeekly, hour: 9, dows: []int{1, 3, 5}},
		{in: "每周1 3 5 9点", repeat: RepeatWeekly, hour: 9, dows: []int{1, 3, 5}},
		{in: "每周1、3、5 9点", repeat: RepeatWeekly, hour: 9, dows: []int{1, 3, 5}},
		{in: "每周7 9点", repeat: RepeatWeekly, hour: 9, dows: []int{0}},
		{in: "每周2,4 8点", repeat: RepeatWeekly, hour: 8, dows: []int{2, 4}},
		{in: "每周12点", repeat: RepeatWeekly, hour: 12, dows: []int{1}}, // 12 是时刻,不是周一+周二
		{in: "每个工作日8点30", repeat: RepeatWeekdays, hour: 8, min: 30},
		{in: "工作日9点", repeat: RepeatWeekdays, hour: 9},
		{in: "每月5号9点", repeat: RepeatMonthDay, day: 5, hour: 9},
		{in: "每月十五号晚上7点", repeat: RepeatMonthDay, day: 15, hour: 19},
		{in: "每月第二个周二9点", repeat: RepeatMonthNth, nth: 2, dows: []int{2}, hour: 9},
		{in: "每月最后一天17点", repeat: RepeatMonthLast, hour: 17},
		{in: "月底最后一天18点", repeat: RepeatMonthLast, hour: 18},
		{in: "月末晚上8点", repeat: RepeatMonthLast, hour: 20},
		{in: "每小时", repeat: RepeatHourly},
		{in: "每半小时", repeat: RepeatEveryNMin, every: 30},
		{in: "每10分钟", repeat: RepeatEveryNMin, every: 10},
		{in: "每2小时", repeat: RepeatEveryNHour, every: 2},
	}
	for _, tc := range cases {
		got := mustText(t, tc.in)
		s := got.Struct
		if s.Repeat != tc.repeat || s.Minute != tc.min || s.Hour != tc.hour ||
			s.Day != tc.day || s.Nth != tc.nth || s.Every != tc.every {
			t.Fatalf("%q 得 %+v,期望 repeat=%s min=%d hour=%d day=%d nth=%d every=%d",
				tc.in, s, tc.repeat, tc.min, tc.hour, tc.day, tc.nth, tc.every)
		}
		if !sameInts(s.Dows, tc.dows) {
			t.Fatalf("%q 周几得 %v,期望 %v", tc.in, s.Dows, tc.dows)
		}
	}
}

// 「晚上8点」= 20:00 还是 08:00 是高代价猜错 —— 这组用例钉住 12 小时制的换算。
func TestParseCronTextPeriods(t *testing.T) {
	cases := map[string][2]int{
		"早上8点":  {8, 0},
		"早晨8点":  {8, 0},
		"上午8点":  {8, 0},
		"凌晨2点":  {2, 0},
		"中午12点": {12, 0},
		"中午1点":  {13, 0},
		"下午3点":  {15, 0},
		"傍晚5点":  {17, 0},
		"晚上8点":  {20, 0},
		"夜里11点": {23, 0},
		"8点半":   {8, 30},
		"晚上8点半": {20, 30},
		"8点30":  {8, 30},
		"8点30分": {8, 30},
		"8:30":  {8, 30},
		"08:05": {8, 5},
		"十点半":   {10, 30},
		"二十点":   {20, 0},
		"二十三点":  {23, 0},
		"下午2点半": {14, 30},
	}
	for in, want := range cases {
		s := mustText(t, in).Struct
		if s.Hour != want[0] || s.Minute != want[1] {
			t.Fatalf("%q 得 %02d:%02d,期望 %02d:%02d", in, s.Hour, s.Minute, want[0], want[1])
		}
	}
}

// 一次性:有明确日期、没给重复方式 → 解释成「就那天跑一次」。
func TestParseCronTextOnce(t *testing.T) {
	cases := []struct{ in, date string }{
		{"下周二上午9点", "2026-11-17"}, // 基准是周六 → 下周二 = 11-17
		{"周二上午9点", "2026-11-17"},  // 本周二已过(今天周六)→ 下一个周二
		{"明天早上8点", "2026-11-15"},
		{"后天9点", "2026-11-16"},
		{"大后天9点", "2026-11-17"},
		{"今天晚上8点", "2026-11-14"},
		{"12月20日上午9点", "2026-12-20"},
		{"2026年12月20日9点", "2026-12-20"},
		{"2027-01-05 10点", "2027-01-05"},
		{"12月20号下午2点", "2026-12-20"},
		{"只跑一次:11月30日8点", "2026-11-30"},
	}
	for _, tc := range cases {
		got := mustText(t, tc.in)
		if got.Struct.Repeat != RepeatOnce {
			t.Fatalf("%q 应是一次性,得 %s", tc.in, got.Struct.Repeat)
		}
		if got.Struct.OnceDate != tc.date {
			t.Fatalf("%q 日期得 %q,期望 %q", tc.in, got.Struct.OnceDate, tc.date)
		}
	}
}

// 周几一律取「从今天起最近的那个」:今天周六说「下周二」要的是后天那个周二,
// 不是 8 天后的。这条选择刻意如此(反直觉的强制「下周」更糟),歧义由 UI 回显具体日期解决。
func TestParseCronTextWeekdayNearest(t *testing.T) {
	tuesday := time.Date(2026, 11, 17, 8, 0, 0, 0, time.Local) // 当天正好是周二、时刻未过
	got, err := parseCronText("下周二9点", tuesday, cronStruct{Minute: 0, Hour: 9})
	if err != nil {
		t.Fatal(err)
	}
	if got.Struct.OnceDate != "2026-11-17" {
		t.Fatalf("今天周二时说「下周二」应取最近的周二 11-17,得 %s", got.Struct.OnceDate)
	}
	got2, err := parseCronText("周二9点", tuesday, cronStruct{Minute: 0, Hour: 9})
	if err != nil {
		t.Fatal(err)
	}
	if got2.Struct.OnceDate != "2026-11-17" {
		t.Fatalf("「周二」在今天就是周二时应取今天,得 %s", got2.Struct.OnceDate)
	}
}

// 用户没说时刻 → 用回退值并**标记 assumed**,UI 必须回显告知。
func TestParseCronTextAssumedTime(t *testing.T) {
	got := mustText(t, "每天")
	if !got.AssumedTime {
		t.Fatal("没说时刻时应标记 AssumedTime(UI 据此回显「已按 09:00 设好」)")
	}
	if got.Struct.Hour != 9 || got.Struct.Minute != 0 {
		t.Fatalf("应回退到表单当前值 09:00,得 %02d:%02d", got.Struct.Hour, got.Struct.Minute)
	}
	// 说了时刻 → 不该标记
	if mustText(t, "每天8点").AssumedTime {
		t.Fatal("说了时刻却仍标记 AssumedTime")
	}
	// 回退值来自调用方(表单当前值),不是硬编码 09:00
	got2, err := parseCronText("工作日", txtNow(), cronStruct{Minute: 30, Hour: 17})
	if err != nil {
		t.Fatal(err)
	}
	if got2.Struct.Hour != 17 || got2.Struct.Minute != 30 {
		t.Fatalf("应沿用调用方给的 17:30,得 %02d:%02d", got2.Struct.Hour, got2.Struct.Minute)
	}
}

// 解析不出 / 有害输入必须**明说**,不猜。
func TestParseCronTextRejects(t *testing.T) {
	cases := []string{
		"随便什么时候都行", // 无频率无时刻
		"",         // 空
		"   ",      // 空白
		"每周第6个周二",  // 没有第 6 个
		"每月32号",    // 没有 32 号
		"每60分钟",    // 越界
		"每分钟",      // 代价太高,要求换说法
		"每天25点",    // 时刻不存在
		"每天8点75分",  // 分钟不存在
		"有空的时候",    // 模糊型:没有确定解,硬猜就是在编
		"每天早上随便几点", // 残留未覆盖词
	}
	for _, in := range cases {
		if got, err := parseCronText(in, txtNow(), cronStruct{}); err == nil {
			t.Fatalf("%q 应被拒,却解析出 %+v", in, got.Struct)
		}
	}
}

// 错误文案必须指出**缺什么/哪不对**,让用户知道下一步怎么改。
func TestParseCronTextErrorReason(t *testing.T) {
	_, err := parseCronText("随便什么时候都行", txtNow(), cronStruct{})
	if err == nil {
		t.Fatal("应报错")
	}
	if !contains(err.Error(), "重复方式") {
		t.Fatalf("应指出缺重复方式,得 %q", err.Error())
	}
	// 区间词现在会**收敛**(见 TestParseCronTextConvergesRanges),仍被拒的是真正模糊的表述。
	_, err = parseCronText("有空的时候", txtNow(), cronStruct{})
	if err == nil {
		t.Fatal("「有空的时候」应被拒(没有确定解)")
	}
	if !contains(err.Error(), "选择器") && !contains(err.Error(), "预设") {
		t.Fatalf("拒绝时应指向控件/预设兜底,得 %q", err.Error())
	}
}

func TestCnNum(t *testing.T) {
	cases := map[string]int{"0": 0, "8": 8, "十二": 12, "十五": 15, "二十": 20, "二十三": 23, "三十一": 31, "两": 2}
	for in, want := range cases {
		got, ok := cnNum(in)
		if !ok || got != want {
			t.Fatalf("cnNum(%q)=(%d,%v),期望 %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "abc", "五九"} {
		if _, ok := cnNum(in); ok {
			t.Fatalf("cnNum(%q) 应失败", in)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// 区间型口语的收敛:给确定值 + 把「按 X 理解」说出来(不静默猜)。
func TestParseCronTextConvergesRanges(t *testing.T) {
	cases := []struct {
		in        string
		repeat    RepeatKind
		day       int
		converged string
	}{
		{"月底前", RepeatMonthLast, 0, "月底"},
		{"月底之前", RepeatMonthLast, 0, "月底"},
		{"月末前 17点", RepeatMonthLast, 0, "月底"},
		{"每月月底前", RepeatMonthLast, 0, "月底"},
		{"月初", RepeatMonthDay, 1, "月初"},
		{"月初 9点", RepeatMonthDay, 1, "月初"},
		{"月中", RepeatMonthDay, 15, "月中"},
		{"月中 14:30", RepeatMonthDay, 15, "月中"},
	}
	for _, tc := range cases {
		got := mustText(t, tc.in)
		if got.Struct.Repeat != tc.repeat || got.Struct.Day != tc.day {
			t.Fatalf("%q 得 %+v,期望 repeat=%s day=%d", tc.in, got.Struct, tc.repeat, tc.day)
		}
		if got.Converged != tc.converged {
			t.Fatalf("%q 收敛标记得 %q,期望 %q(必须说出来)", tc.in, got.Converged, tc.converged)
		}
	}
	// 收敛后的时刻跟着走
	if got := mustText(t, "月底前 17点"); got.Struct.Hour != 17 {
		t.Fatalf("「月底前 17点」的时刻应被采纳,得 %d", got.Struct.Hour)
	}
	// 不带收敛的说法不该有标记
	if got := mustText(t, "每天8点"); got.Converged != "" {
		t.Fatalf("「每天8点」不该有收敛标记,得 %q", got.Converged)
	}
}

// 真正模糊到没有确定解的表述仍然拒绝(硬猜一个时刻就是在编)。
func TestParseCronTextStillRejectsVague(t *testing.T) {
	for _, in := range []string{"有空的时候", "过两天吧", "什么时候都行"} {
		if got, err := parseCronText(in, txtNow(), cronStruct{Minute: 0, Hour: 9}); err == nil {
			t.Fatalf("%q 应被拒,却解析出 %+v", in, got.Struct)
		}
	}
	// 拒绝文案要指向预设,不能只说「没看懂」
	_, err := parseCronText("有空的时候", txtNow(), cronStruct{})
	if err == nil || !strings.Contains(err.Error(), "预设") {
		t.Fatalf("拒绝文案应指向预设,得 %v", err)
	}
}

// 节日与农历日期:直接定位到年度排期(不落成一次性 —— 农历节日明年还要再提醒)。
func TestParseCronTextFestivals(t *testing.T) {
	cases := []struct {
		in     string
		repeat RepeatKind
		month  int
		day    int
		hour   int
		min    int
		fest   string
	}{
		{"中秋", RepeatLunarAnnual, 8, 15, 9, 0, "中秋"}, // 时刻缺失 → 回退值 09:00
		{"中秋节 20:00", RepeatLunarAnnual, 8, 15, 20, 0, "中秋节"},
		{"春节上午10点", RepeatLunarAnnual, 1, 1, 10, 0, "春节"},
		{"除夕 18点", RepeatLunarAnnual, 12, 0, 18, 0, "除夕"}, // day=0 → 腊月最后一天
		{"大年三十 18:30", RepeatLunarAnnual, 12, 0, 18, 30, "大年三十"},
		{"元宵节 8点", RepeatLunarAnnual, 1, 15, 8, 0, "元宵节"},
		{"端午 9点", RepeatLunarAnnual, 5, 5, 9, 0, "端午"},
		{"重阳节 9点", RepeatLunarAnnual, 9, 9, 9, 0, "重阳节"},
		{"腊八 7点", RepeatLunarAnnual, 12, 8, 7, 0, "腊八"},
		{"七夕 20点", RepeatLunarAnnual, 7, 7, 20, 0, "七夕"},
		{"国庆 9点", RepeatAnnualDate, 10, 1, 9, 0, "国庆"}, // 公历固定节日走年度档
		{"元旦 0点", RepeatAnnualDate, 1, 1, 0, 0, "元旦"},
		{"圣诞 20点", RepeatAnnualDate, 12, 25, 20, 0, "圣诞"},
	}
	for _, tc := range cases {
		got := mustText(t, tc.in).Struct
		if got.Repeat != tc.repeat || got.Month != tc.month || got.Day != tc.day ||
			got.Hour != tc.hour || got.Minute != tc.min || got.Festival != tc.fest {
			t.Fatalf("%q 得 %+v,期望 repeat=%s %d-%d %02d:%02d festival=%s",
				tc.in, got, tc.repeat, tc.month, tc.day, tc.hour, tc.min, tc.fest)
		}
	}
}

// 「中秋」/「中秋节」必须长名优先,不能被短名吃掉再留个「节」字。
func TestParseCronTextFestivalLongNameFirst(t *testing.T) {
	if got := mustText(t, "中秋节"); got.Struct.Festival != "中秋节" {
		t.Fatalf("「中秋节」应匹配长名,得 %q", got.Struct.Festival)
	}
	if got := mustText(t, "重阳节"); got.Struct.Festival != "重阳节" {
		t.Fatalf("「重阳节」应匹配长名,得 %q", got.Struct.Festival)
	}
}

// 「农历八月十五」这种写法不带节日名,也要能排。
func TestParseCronTextLunarMD(t *testing.T) {
	cases := []struct {
		in    string
		month int
		day   int
	}{
		{"农历八月十五 20点", 8, 15},
		{"农历正月初一 9点", 1, 1},
		{"农历十二月初八 7点", 12, 8},
		{"农历腊月廿三 9点", 12, 23},
		{"农历九月初九 9点", 9, 9},
		{"农历一月初十 9点", 1, 10},
		{"农历8月15日 20点", 8, 15},
	}
	for _, tc := range cases {
		got := mustText(t, tc.in).Struct
		if got.Repeat != RepeatLunarAnnual || got.Month != tc.month || got.Day != tc.day {
			t.Fatalf("%q 得 %+v,期望 lunar %d-%d", tc.in, got, tc.month, tc.day)
		}
	}
	// 不带「农历」前缀的月日是一次性/公历,不能误判成农历
	if got := mustText(t, "12月20日 9点").Struct; got.Repeat != RepeatOnce {
		t.Fatalf("不带农历前缀的日期应是一次性,得 %s", got.Repeat)
	}
}

// 「每年10月1日」是年度重复;「10月1日」是一次性 —— 同一个月日,语义完全不同。
func TestParseCronTextYearlyVsOnce(t *testing.T) {
	yearly := mustText(t, "每年10月1日 9点").Struct
	if yearly.Repeat != RepeatAnnualDate || yearly.Month != 10 || yearly.Day != 1 {
		t.Fatalf("「每年10月1日」应是年度重复,得 %+v", yearly)
	}
	once := mustText(t, "今年10月1日 9点").Struct
	if once.Repeat != RepeatOnce {
		t.Fatalf("「今年10月1日」应是一次性,得 %s", once.Repeat)
	}
}
