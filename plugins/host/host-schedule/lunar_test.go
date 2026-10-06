// 农历换算单测:钉住**跨源验证过的锚点**。
//
// 为什么这批锚点值得单列:农历差一天,用户就会在错的日子给家人发祝福 —— 比不支持更糟。
// 表来自 jjonline/calendar.js(1900-3000 扩展表,本文件只取 1900-2100),它的正确性是这样
// 确认的(2026-10-03,一次性离线核对):
//  1. 与另一份独立实现 python-lunardate 逐项比对,1933/1954/1978 三年不同;
//  2. 用**算法型**实现(6tail/lunar-javascript,不查表)裁决那三年:本表完全一致,
//     python-lunardate 在 1978 年中秋差一天(即它错);
//  3. 春节与 2020-2030 公开已知值逐个吻合;相邻年跨度只落在农历年的合法集合里。
//
// 下面每一条都在守上述结论的一角:表被改坏必须立刻红灯。
package hostschedule

import (
	"testing"
	"time"
)

// loc 测试统一用 UTC(与现有 at() 一致),避免机器时区影响断言。
var loc = time.UTC

func TestLunarTableEndpoints(t *testing.T) {
	if len(lunarInfo) != lunarMaxYear-lunarMinYear+1 {
		t.Fatalf("表长应为 %d,得 %d", lunarMaxYear-lunarMinYear+1, len(lunarInfo))
	}
	// 首末项跨源一致(经典表首项 1900=0x04bd8、末项 2100=0xd520)
	if lunarInfo[0] != 0x04bd8 {
		t.Fatalf("1900 年表项应为 0x04bd8,得 0x%x", lunarInfo[0])
	}
	if lunarInfo[len(lunarInfo)-1] != 0xd520 {
		t.Fatalf("2100 年表项应为 0xd520,得 0x%x", lunarInfo[len(lunarInfo)-1])
	}
}

// 春节(农历正月初一)的公开已知日期 —— 与表算出的必须逐个吻合。
func TestLunarSpringFestival(t *testing.T) {
	cases := map[int]string{
		2020: "2020-01-25", 2021: "2021-02-12", 2022: "2022-02-01", 2023: "2023-01-22",
		2024: "2024-02-10", 2025: "2025-01-29", 2026: "2026-02-17", 2027: "2027-02-06",
		2028: "2028-01-26", 2029: "2029-02-13", 2030: "2030-02-03", 2031: "2031-01-23",
		2032: "2032-02-11",
	}
	for y, want := range cases {
		got, err := solarFromLunar(y, 1, 1, loc)
		if err != nil {
			t.Fatalf("%d 年春节换算失败: %v", y, err)
		}
		if s := got.Format("2006-01-02"); s != want {
			t.Fatalf("%d 年春节应为 %s,得 %s", y, want, s)
		}
	}
}

// 跨源分歧的三年:本表与算法实现一致,python-lunardate 在 1978 年中秋差一天。
// 这条是 1978 那个 bit 的守卫 —— 改动 lunarInfo 前先看这里。
func TestLunarDisputedYears(t *testing.T) {
	cases := []struct {
		y          int
		midAutumn  string
		springFest string
	}{
		{1933, "1933-10-04", "1933-01-26"},
		{1954, "1954-09-11", "1954-02-03"},
		// python-lunardate 在这里给 1978-09-16(错一天);算法实现与本表都是 09-17
		{1978, "1978-09-17", "1978-02-07"},
	}
	for _, tc := range cases {
		ma, err := solarFromLunar(tc.y, 8, 15, loc)
		if err != nil {
			t.Fatalf("%d 年中秋换算失败: %v", tc.y, err)
		}
		if s := ma.Format("2006-01-02"); s != tc.midAutumn {
			t.Fatalf("%d 年中秋应为 %s,得 %s", tc.y, tc.midAutumn, s)
		}
		sf, err := solarFromLunar(tc.y, 1, 1, loc)
		if err != nil {
			t.Fatalf("%d 年春节换算失败: %v", tc.y, err)
		}
		if s := sf.Format("2006-01-02"); s != tc.springFest {
			t.Fatalf("%d 年春节应为 %s,得 %s", tc.y, tc.springFest, s)
		}
	}
}

// 相邻两个春节的间隔只可能是农历年的合法天数(353/354/355 平年,383/384/385 闰年)。
// 这条能抓住整片区域性的表损坏(单点比特错会破坏跨度集合)。
func TestLunarYearSpanSet(t *testing.T) {
	legal := map[int]bool{353: true, 354: true, 355: true, 383: true, 384: true, 385: true}
	prev, err := solarFromLunar(lunarMinYear, 1, 1, loc)
	if err != nil {
		t.Fatal(err)
	}
	for y := lunarMinYear + 1; y <= lunarMaxYear; y++ {
		cur, err := solarFromLunar(y, 1, 1, loc)
		if err != nil {
			t.Fatalf("%d 年春节换算失败: %v", y, err)
		}
		span := int(cur.Sub(prev).Hours() / 24)
		if !legal[span] {
			t.Fatalf("%d → %d 跨度 %d 天不在合法集合内(表可能被改坏)", y-1, y, span)
		}
		prev = cur
	}
}

// 除夕 = 腊月最后一天 = 春节前一天(腊月可能 29 天,写死 30 会算不出来)。
func TestLunarNewYearEve(t *testing.T) {
	eve := annualDate{Month: 12, LastDayOfMonth: true, Lunar: true}
	for _, y := range []int{2025, 2026, 2027, 2028, 2030} {
		got, err := eve.dayIn(y, loc)
		if err != nil {
			t.Fatalf("%d 年除夕算不出: %v", y, err)
		}
		// 与春节比:公历年 y 的除夕就是**同年**春节的前一天
		sf, err := solarFromLunar(y, 1, 1, loc)
		if err != nil {
			t.Fatal(err)
		}
		want := sf.AddDate(0, 0, -1)
		if !got.Equal(want) {
			t.Fatalf("%d 年除夕应为 %s(同年春节前一天),得 %s", y, want.Format("2006-01-02"), got.Format("2006-01-02"))
		}
	}
}

// 年末月份跨公历年的对齐:腊八(农历 12 月初八)落在公历 1 月,属**上一个农历年** ——
// 按公历年取必须拿到当年 1 月那一次,不能跑到次年,也不能算不出来。
func TestLunarMonthCrossingSolarYear(t *testing.T) {
	lab := annualDate{Month: 12, Day: 8, Lunar: true}
	for _, y := range []int{2024, 2025, 2026, 2027, 2030} {
		got, err := lab.dayIn(y, loc)
		if err != nil {
			t.Fatalf("公历 %d 年的腊八算不出: %v", y, err)
		}
		if got.Year() != y {
			t.Fatalf("公历 %d 年的腊八应落回 %d 年,得 %s", y, y, got.Format("2006-01-02"))
		}
		want, err := solarFromLunar(y-1, 12, 8, loc)
		if err != nil {
			t.Fatalf("公历 %d 年: %v", y, err)
		}
		if !got.Equal(want) {
			t.Fatalf("公历 %d 年的腊八应取农历 %d 年腊月初八(%s),得 %s",
				y, y-1, want.Format("2006-01-02"), got.Format("2006-01-02"))
		}
	}
}

func TestAnnualDateSolar(t *testing.T) {
	gq := annualDate{Month: 10, Day: 1}
	got, err := gq.dayIn(2026, loc)
	if err != nil || got.Format("2006-01-02") != "2026-10-01" {
		t.Fatalf("国庆应落 2026-10-01,得 %v(%v)", got.Format("2006-01-02"), err)
	}
	// 公历没有的一天必须拒:time.Date 会把 2 月 30 日归一成 3 月 2 日,
	// 那是**静默改语义**(用户说 2/30,系统跑去 3/2),绝不能放过。
	if _, err := (annualDate{Month: 2, Day: 30}).dayIn(2026, loc); err == nil {
		t.Fatal("公历 2 月 30 日应被拒")
	}
}

// 2 月 29 日:非闰年没有这一天 → 顺延到下一个闰年(而不是报错卡死,也不是悄悄变成 3/1)。
func TestAnnualDateLeapDaySkipsNonLeapYears(t *testing.T) {
	a := annualDate{Month: 2, Day: 29}
	got, ok := a.nextFrom(time.Date(2026, 1, 1, 0, 0, 0, 0, loc), 9, 0)
	if !ok || got.Format("2006-01-02 15:04") != "2028-02-29 09:00" {
		t.Fatalf("应从 2026 顺延到 2028-02-29 09:00,得 %v(ok=%v)", got.Format("2006-01-02 15:04"), ok)
	}
}

// 农历年度排期:中秋 —— 公历上每年都在变,cron 表达不了,必须走农历换算。
func TestAnnualDateLunarNextAndOccurrences(t *testing.T) {
	ma := annualDate{Month: 8, Day: 15, Lunar: true}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
	got, ok := ma.nextFrom(now, 20, 0)
	if !ok {
		t.Fatal("中秋应能算出下次")
	}
	want, err := solarFromLunar(2026, 8, 15, loc)
	if err != nil {
		t.Fatal(err)
	}
	want = time.Date(want.Year(), want.Month(), want.Day(), 20, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("2026 年中秋应为 %s,得 %s", want.Format("2006-01-02 15:04"), got.Format("2006-01-02 15:04"))
	}
	// 连取三次:必须是三个**不同年份**里的中秋,且严格递增
	occ := ma.occurrences(now, 20, 0, 3)
	if len(occ) != 3 {
		t.Fatalf("应取到 3 次,得 %d", len(occ))
	}
	seen := map[int]bool{}
	for i, o := range occ {
		if seen[o.Year()] {
			t.Fatalf("第 %d 次与前面同一年: %v", i, o)
		}
		seen[o.Year()] = true
		if i > 0 && !o.After(occ[i-1]) {
			t.Fatalf("第 %d 次没有晚于上一次: %v vs %v", i, o, occ[i-1])
		}
	}
}

// nextFrom 是**严格**之后:正好等于 now 的那一刻不能算「下一次」,否则会重复触发。
func TestAnnualDateNextFromIsStrict(t *testing.T) {
	a := annualDate{Month: 10, Day: 1}
	exact := time.Date(2026, 10, 1, 9, 0, 0, 0, loc)
	got, ok := a.nextFrom(exact, 9, 0)
	if !ok {
		t.Fatal("应能算出下一次")
	}
	if !got.After(exact) {
		t.Fatalf("应严格晚于 %s,得 %s", exact.Format("2006-01-02 15:04"), got.Format("2006-01-02 15:04"))
	}
	if got.Year() != 2027 {
		t.Fatalf("正好到点时应取次年 2027,得 %d", got.Year())
	}
}

func TestLunarRangeAndValidity(t *testing.T) {
	if _, err := solarFromLunar(1899, 1, 1, loc); err == nil {
		t.Fatal("超出表范围应报错(不猜)")
	}
	if _, err := solarFromLunar(2101, 1, 1, loc); err == nil {
		t.Fatal("超出表范围应报错(不猜)")
	}
	if _, err := solarFromLunar(2026, 13, 1, loc); err == nil {
		t.Fatal("农历 13 月应报错")
	}
	// 农历某月只有 29 天时,第 30 日必须报错而不是顺延
	for y := 2020; y < 2030; y++ {
		if lunarMonthDays(y, 1) == 29 {
			if _, err := solarFromLunar(y, 1, 30, loc); err == nil {
				t.Fatalf("农历 %d 年正月只有 29 天,第 30 日应报错", y)
			}
			break
		}
	}
}

// 节日表:常用节日名都在,且指向的农历月日在合法范围。
func TestFestivalTables(t *testing.T) {
	if len(lunarFestivals) == 0 || len(solarFestivals) == 0 {
		t.Fatal("节日表不该为空")
	}
	for name, md := range lunarFestivals {
		if md[0] < 1 || md[0] > 12 || md[1] < 1 || md[1] > 30 {
			t.Fatalf("农历节日 %s 的月日非法: %v", name, md)
		}
	}
	for name, md := range solarFestivals {
		if md[0] < 1 || md[0] > 12 || md[1] < 1 || md[1] > 31 {
			t.Fatalf("公历节日 %s 的月日非法: %v", name, md)
		}
	}
	for _, want := range []string{"春节", "中秋", "端午", "重阳", "腊八", "元宵", "七夕"} {
		if _, ok := lunarFestivals[want]; !ok {
			t.Fatalf("农历节日表缺 %s", want)
		}
	}
	if !isEveName("除夕") || isEveName("中秋") {
		t.Fatal("除夕识别不对")
	}
}
