// lunar.go:农历 ↔ 公历换算与节日表(纯算法 + 静态表,零运行时依赖)。
//
// 为什么这件事值得做:对普通人来说「中秋提醒」「春节前提醒」是真实需求,而它们是
// **农历**日期 —— 公历上每年都在变,用 cron 表达不了。而农历换算本身是**纯算法**:
//
//	法定节假日与调休(「放假那几天」)**不做** —— 那需要每年国务院公告的数据,
//	只能联网取或内置年度数据集,违反「运行时依赖 0」的静态二进制定位;
//	但农历本身可以离线算,所以「农历八月十五」这类能支持,「国庆放假 7 天」这类不能。
//
// 数据来源与**验证方式**(这是本文件最要紧的部分 —— 农历差一天,用户就会在错的日子
// 给家人发祝福,比不支持更糟,所以表必须是被验证过的):
//
//	表取自 jjonline/calendar.js(1900-3000 的扩展 lunarInfo),本文件只取 **1900-2100**
//	(201 项,已由脚本从原始文件直接生成,无手工转录)。
//
//	三重交叉验证(2026-10-03,一次性离线核对):
//	 1. 与另一份独立实现 python-lunardate(lidaobing)的 200 项表逐项比对:
//	    1933/1954/1978 三年两项不同 —— **没有直接采用任一份**;
//	 2. 用**算法型**实现(6tail/lunar-javascript,不查表、自行算日月位置)裁决那三年:
//	    本表与算法实现**完全一致**,python-lunardate 在 1978 年中秋差一天(它错);
//	 3. 春节日期与 2020-2030 的公开已知值逐个吻合;相邻年跨度只出现
//	    353/354/355/383/384/385 天(农历年的合法集合);末项 0x0d520 与经典表一致。
//
// 对应的回归测试在 lunar_test.go 里钉住同一批锚点 —— 表被改坏必须立刻红灯。
package hostschedule

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// lunarBaseDate 农历 1900 年正月初一 = 公历 1900-01-31(整张表的基准点,仅做日差计算用)。
var lunarBaseDate = time.Date(1900, 1, 31, 0, 0, 0, 0, time.UTC)

// lunarMinYear/lunarMaxYear 表覆盖范围(超出显式报错,不猜)。
const (
	lunarMinYear = 1900
	lunarMaxYear = 2100
)

// lunarInfo 农历年信息表(1900-2100,一项一年)。编码:
//
//	bit 16   闰月大小(1 = 30 天,0 = 29 天;仅当有闰月时有效)
//	bit 15-4 十二个常规月的大小(1 = 30 天,0 = 29 天),从正月开始
//	bit 3-0  闰月月份(0 = 本年无闰月,1-12 = 闰几月)
//
// 生成方式:一次性从原始文件抽取(见文件头「数据来源与验证方式」),不是手抄。
var lunarInfo = [lunarMaxYear - lunarMinYear + 1]int{
	0x04bd8, 0x04ae0, 0x0a570, 0x054d5, 0x0d260, 0x0d950, 0x16554, 0x056a0, 0x09ad0, 0x055d2, 0x04ae0, 0x0a5b6, 0x0a4d0, 0x0d250, 0x1d255, 0x0b540, // 1900-1915
	0x0d6a0, 0x0ada2, 0x095b0, 0x14977, 0x04970, 0x0a4b0, 0x0b4b5, 0x06a50, 0x06d40, 0x1ab54, 0x02b60, 0x09570, 0x052f2, 0x04970, 0x06566, 0x0d4a0, // 1916-1931
	0x0ea50, 0x16a95, 0x05ad0, 0x02b60, 0x186e3, 0x092e0, 0x1c8d7, 0x0c950, 0x0d4a0, 0x1d8a6, 0x0b550, 0x056a0, 0x1a5b4, 0x025d0, 0x092d0, 0x0d2b2, // 1932-1947
	0x0a950, 0x0b557, 0x06ca0, 0x0b550, 0x15355, 0x04da0, 0x0a5b0, 0x14573, 0x052b0, 0x0a9a8, 0x0e950, 0x06aa0, 0x0aea6, 0x0ab50, 0x04b60, 0x0aae4, // 1948-1963
	0x0a570, 0x05260, 0x0f263, 0x0d950, 0x05b57, 0x056a0, 0x096d0, 0x04dd5, 0x04ad0, 0x0a4d0, 0x0d4d4, 0x0d250, 0x0d558, 0x0b540, 0x0b6a0, 0x195a6, // 1964-1979
	0x095b0, 0x049b0, 0x0a974, 0x0a4b0, 0x0b27a, 0x06a50, 0x06d40, 0x0af46, 0x0ab60, 0x09570, 0x04af5, 0x04970, 0x064b0, 0x074a3, 0x0ea50, 0x06b58, // 1980-1995
	0x05ac0, 0x0ab60, 0x096d5, 0x092e0, 0x0c960, 0x0d954, 0x0d4a0, 0x0da50, 0x07552, 0x056a0, 0x0abb7, 0x025d0, 0x092d0, 0x0cab5, 0x0a950, 0x0b4a0, // 1996-2011
	0x0baa4, 0x0ad50, 0x055d9, 0x04ba0, 0x0a5b0, 0x15176, 0x052b0, 0x0a930, 0x07954, 0x06aa0, 0x0ad50, 0x05b52, 0x04b60, 0x0a6e6, 0x0a4e0, 0x0d260, // 2012-2027
	0x0ea65, 0x0d530, 0x05aa0, 0x076a3, 0x096d0, 0x04afb, 0x04ad0, 0x0a4d0, 0x1d0b6, 0x0d250, 0x0d520, 0x0dd45, 0x0b5a0, 0x056d0, 0x055b2, 0x049b0, // 2028-2043
	0x0a577, 0x0a4b0, 0x0aa50, 0x1b255, 0x06d20, 0x0ada0, 0x14b63, 0x09370, 0x049f8, 0x04970, 0x064b0, 0x168a6, 0x0ea50, 0x06aa0, 0x1a6c4, 0x0aae0, // 2044-2059
	0x092e0, 0x0d2e3, 0x0c960, 0x0d557, 0x0d4a0, 0x0da50, 0x05d55, 0x056a0, 0x0a6d0, 0x055d4, 0x052d0, 0x0a9b8, 0x0a950, 0x0b4a0, 0x0b6a6, 0x0ad50, // 2060-2075
	0x055a0, 0x0aba4, 0x0a5b0, 0x052b0, 0x0b273, 0x06930, 0x07337, 0x06aa0, 0x0ad50, 0x14b55, 0x04b60, 0x0a570, 0x054e4, 0x0d160, 0x0e968, 0x0d520, // 2076-2091
	0x0daa0, 0x16aa6, 0x056d0, 0x04ae0, 0x0a9d4, 0x0a2d0, 0x0d150, 0x0f252, 0x0d520, // 2092-2100
}

// lunarLeapMonth 某农历年的闰月号(0 = 无闰月)。
func lunarLeapMonth(y int) int { return lunarInfo[y-lunarMinYear] & 0xf }

// lunarLeapDays 某农历年闰月的天数(无闰月返回 0)。
func lunarLeapDays(y int) int {
	if lunarLeapMonth(y) == 0 {
		return 0
	}
	if lunarInfo[y-lunarMinYear]&0x10000 != 0 {
		return 30
	}
	return 29
}

// lunarYearDays 某农历年的总天数(约 353-385)。
func lunarYearDays(y int) int {
	sum := 348
	for i := 0x8000; i > 0x8; i >>= 1 {
		if lunarInfo[y-lunarMinYear]&i != 0 {
			sum++
		}
	}
	return sum + lunarLeapDays(y)
}

// lunarMonthDays 某农历年第 m 个**常规月**的天数(m: 1-12;不含闰月)。
func lunarMonthDays(y, m int) int {
	if lunarInfo[y-lunarMinYear]&(0x10000>>uint(m)) != 0 {
		return 30
	}
	return 29
}

// lunarSupported 年份是否在表覆盖范围内。
func lunarSupported(y int) bool { return y >= lunarMinYear && y <= lunarMaxYear }

// solarFromLunar 农历(年, 月, 日) → 公历日期(当天零点,按 loc 构造)。
// 只支持**常规月** —— 节日都在常规月;闰月要额外区分,本功能不需要,也不假装支持。
func solarFromLunar(y, month, day int, loc *time.Location) (time.Time, error) {
	if !lunarSupported(y) {
		return time.Time{}, fmt.Errorf("农历只支持 %d-%d 年: %d 超出范围", lunarMinYear, lunarMaxYear, y)
	}
	if month < 1 || month > 12 {
		return time.Time{}, fmt.Errorf("农历月份须为 1-12: %d", month)
	}
	if day < 1 || day > lunarMonthDays(y, month) {
		return time.Time{}, fmt.Errorf("农历%d年%d月没有 %d 日(该月 %d 天)", y, month, day, lunarMonthDays(y, month))
	}
	offset := 0
	for k := lunarMinYear; k < y; k++ {
		offset += lunarYearDays(k)
	}
	leap := lunarLeapMonth(y)
	for m := 1; m < month; m++ {
		offset += lunarMonthDays(y, m)
		if leap == m { // 闰月排在该月之后
			offset += lunarLeapDays(y)
		}
	}
	offset += day - 1
	d := lunarBaseDate.AddDate(0, 0, offset)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc), nil
}

// —— 年度排期(公历/农历各一支) ——

// annualDate 一个「每年某天」的日期:公历或农历。
//
// 公历年度排期本来就能用 cron 表达(`0 9 1 10 *` = 每年 10 月 1 日),加这一支是为了
// **让界面能反解与编辑**它(此前一律判 custom:用户看得见却改不了)。
// 农历则必须走这里 —— 公历上每年都在变,cron 表达不了。
type annualDate struct {
	Month int
	Day   int
	Lunar bool
	// LastDayOfMonth:取该月最后一天(除夕要用 —— 腊月可能 29 天也可能 30 天,
	// 写死 30 会在月小的年份直接算不出来)。
	LastDayOfMonth bool
}

// String 人类写法(农历带前缀,免得与公历混淆)。
func (a annualDate) String() string {
	pre := ""
	if a.Lunar {
		pre = "农历"
	}
	if a.LastDayOfMonth {
		return fmt.Sprintf("%s%d月最后一天", pre, a.Month)
	}
	return fmt.Sprintf("%s%d月%d日", pre, a.Month, a.Day)
}

// solarMaxDay 公历各月的最大天数(2 月按闰年给 29 —— 静态合法性只问「存在不存在」,
// 具体某年有没有由 annualDate.dayIn 判定)。
var solarMaxDay = [13]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

// solarDayValid 公历该月有没有这一天(2 月 30 日这类永远不存在,不能当成年度排期)。
func solarDayValid(month, day int) bool {
	if month < 1 || month > 12 || day < 1 {
		return false
	}
	return day <= solarMaxDay[month]
}

// parseAnnual 解析 "MM-DD"(公历与农历都存这个形状;Lunar 决定怎么解释)。
func parseAnnual(md string, lunar bool) (annualDate, error) {
	var m, d int
	if _, err := fmt.Sscanf(md, "%d-%d", &m, &d); err != nil {
		return annualDate{}, fmt.Errorf("日期格式须为 MM-DD: %q", md)
	}
	if m < 1 || m > 12 {
		return annualDate{}, fmt.Errorf("月份须为 1-12: %d", m)
	}
	// day == 0 = 该月最后一天(除夕:腊月可能 29 天也可能 30 天,不能写死)
	if d < 0 || d > 31 {
		return annualDate{}, fmt.Errorf("日期须为 0-31(0 = 该月最后一天): %d", d)
	}
	// 农历月只有 29/30 天:31 号这种永远不会存在,当场拒掉比「算不出下一次」好懂
	if lunar && d > 30 {
		return annualDate{}, fmt.Errorf("农历%s没有 %d 日(农历月最多 30 天)", lunarMonthZh[m], d)
	}
	return annualDate{Month: m, Day: d, Lunar: lunar, LastDayOfMonth: d == 0}, nil
}

// dayIn 该年度日期在公历年 year 里落在哪一天(当天零点,loc 时区)。
//
// 农历的年末月份(冬月/腊月)落在**公历次年** 1-2 月,所以「公历年 year 的腊八」其实属于
// 农历 year-1 年 —— 这里按「算出来的公历年份是否等于 year」对齐,两种情形一套逻辑。
func (a annualDate) dayIn(year int, loc *time.Location) (time.Time, error) {
	if !a.Lunar {
		d := a.Day
		if a.LastDayOfMonth {
			d = time.Date(year, time.Month(a.Month)+1, 0, 0, 0, 0, 0, loc).Day()
		}
		t := time.Date(year, time.Month(a.Month), d, 0, 0, 0, 0, loc)
		// time.Date 会把 2 月 30 日归一成 3 月 2 日 —— 那是静默改语义,必须拒
		if int(t.Month()) != a.Month || t.Day() != d {
			return time.Time{}, fmt.Errorf("公历 %d 年 %d 月没有 %d 号", year, a.Month, d)
		}
		return t, nil
	}
	for _, ly := range []int{year, year - 1} { // 年末月份取 year-1,年初月份取 year
		if !lunarSupported(ly) {
			continue
		}
		d := a.Day
		if a.LastDayOfMonth {
			d = lunarMonthDays(ly, a.Month)
		}
		t, err := solarFromLunar(ly, a.Month, d, loc)
		if err != nil {
			continue
		}
		if t.Year() == year {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("算不出 %d 年的%s(农历表范围 %d-%d)", year, a, lunarMinYear, lunarMaxYear)
}

// nextFrom 从 now 之后(严格)找下一次发生时刻(hh:mm 由调用方给)。
func (a annualDate) nextFrom(now time.Time, hh, mm int) (time.Time, bool) {
	for y := now.Year(); y <= lunarMaxYear; y++ {
		day, err := a.dayIn(y, now.Location())
		if err != nil {
			continue
		}
		at := time.Date(day.Year(), day.Month(), day.Day(), hh, mm, 0, 0, now.Location())
		if at.After(now) {
			return at, true
		}
	}
	return time.Time{}, false
}

// occurrences 从 now 之后连取 n 次(界面「接下来几次」预览用)。
func (a annualDate) occurrences(now time.Time, hh, mm, n int) []time.Time {
	var out []time.Time
	cur := now
	for i := 0; i < n; i++ {
		at, ok := a.nextFrom(cur, hh, mm)
		if !ok {
			break
		}
		out = append(out, at)
		cur = at
	}
	return out
}

// —— 节日表 ——

// lunarFestivals 农历节日(名称 → 农历月日)。除夕单独处理(腊月最后一天,29 或 30)。
var lunarFestivals = map[string][2]int{
	"春节": {1, 1}, "大年初一": {1, 1}, "正月初一": {1, 1},
	"元宵": {1, 15}, "元宵节": {1, 15}, "正月十五": {1, 15},
	"龙抬头": {2, 2},
	"端午":  {5, 5}, "端午节": {5, 5},
	"七夕": {7, 7}, "七夕节": {7, 7},
	"中元": {7, 15}, "中元节": {7, 15},
	"中秋": {8, 15}, "中秋节": {8, 15}, "八月十五": {8, 15},
	"重阳": {9, 9}, "重阳节": {9, 9},
	"腊八": {12, 8}, "腊八节": {12, 8},
}

// solarFestivals 公历固定节日(名称 → 公历月日)。这些用 cron 就能表达,
// 收在这里是为了让「元旦」「国庆」这类口语也能直接说。
var solarFestivals = map[string][2]int{
	"元旦": {1, 1}, "劳动节": {5, 1}, "五一": {5, 1}, "国庆": {10, 1}, "国庆节": {10, 1},
	"儿童节": {6, 1}, "教师节": {9, 10}, "圣诞": {12, 25}, "圣诞节": {12, 25}, "情人节": {2, 14},
}

// isEveName 是否「除夕」类说法(腊月最后一天,可能 29 或 30)。
func isEveName(s string) bool {
	switch s {
	case "除夕", "大年三十", "年三十", "大年夜":
		return true
	}
	return false
}

// —— 农历日期的中文写法(给文案用) ——

// 月份用传统名(正月/冬月/腊月),日期用初十/廿一/三十 那套 —— 「农历八月初十五」
// 这种半吊子写法读起来最别扭。
var lunarMonthZh = []string{"", "正月", "二月", "三月", "四月", "五月", "六月",
	"七月", "八月", "九月", "十月", "冬月", "腊月"}

var lunarDayZh = []string{"", "初一", "初二", "初三", "初四", "初五", "初六", "初七", "初八", "初九", "初十",
	"十一", "十二", "十三", "十四", "十五", "十六", "十七", "十八", "十九", "二十",
	"廿一", "廿二", "廿三", "廿四", "廿五", "廿六", "廿七", "廿八", "廿九", "三十"}

// lunarMDText 农历月日的中文写法;day == 0 表示该月最后一天(除夕)。
func lunarMDText(m, d int) string {
	mon := fmt.Sprintf("%d月", m)
	if m >= 1 && m <= 12 {
		mon = lunarMonthZh[m]
	}
	switch {
	case d == 0:
		return "农历" + mon + "最后一天"
	case d >= 1 && d <= 30:
		return "农历" + mon + lunarDayZh[d]
	}
	return fmt.Sprintf("农历%d月%d日", m, d)
}

// festivalOf 从串首匹配节日名。**长名优先** —— 否则「中秋节」会先被「中秋」吃掉再留下「节」。
// 返回节日名与其对应月日(lunar 决定解释方式;除夕的 day = 0 表示「该月最后一天」)。
func festivalOf(s string) (name string, month, day int, lunar bool, rest string, ok bool) {
	// 除夕类先判(它不是某个固定月日)
	for _, n := range []string{"大年三十", "大年夜", "年三十", "除夕"} {
		if strings.HasPrefix(s, n) {
			return n, 12, 0, true, s[len(n):], true
		}
	}
	type hit struct {
		name string
		md   [2]int
		lu   bool
	}
	var all []hit
	for k, v := range lunarFestivals {
		all = append(all, hit{k, v, true})
	}
	for k, v := range solarFestivals {
		all = append(all, hit{k, v, false})
	}
	// 长名优先(稳定性:同长度按名字排序,保证映射确定)
	sort.Slice(all, func(i, j int) bool {
		if len(all[i].name) != len(all[j].name) {
			return len(all[i].name) > len(all[j].name)
		}
		return all[i].name < all[j].name
	})
	for _, h := range all {
		if strings.HasPrefix(s, h.name) {
			return h.name, h.md[0], h.md[1], h.lu, s[len(h.name):], true
		}
	}
	return "", 0, 0, false, s, false
}

// parseLunarMD 解析「农历八月十五」「农历12月30」这类写法。
// 日支持 初一…初十、十一…十九、二十、廿一…廿九、三十 与阿拉伯数字。
func parseLunarMD(s string) (month, day int, rest string, ok bool) {
	t := strings.TrimPrefix(strings.TrimSpace(s), "农历")
	if t == s {
		return 0, 0, s, false // 必须显式带「农历」,否则与公历日期混淆
	}
	idx := strings.Index(t, "月")
	if idx <= 0 {
		return 0, 0, s, false
	}
	mo, okMO := parseLunarMonth(strings.TrimSpace(t[:idx]))
	if !okMO || mo < 1 || mo > 12 {
		return 0, 0, s, false
	}
	tail := t[idx+len("月"):]
	d, n := cnLunarDay(tail)
	if d == 0 {
		return 0, 0, s, false
	}
	tail = tail[n:]
	for _, suf := range []string{"日", "号"} {
		if strings.HasPrefix(tail, suf) {
			tail = tail[len(suf):]
			break
		}
	}
	return mo, d, tail, true
}

// lunarMonthAlias 农历月份的别名(cnNum 认不了这几个字:正/元=正月,冬=冬月,腊=腊月)。
var lunarMonthAlias = map[string]int{"正": 1, "元": 1, "冬": 11, "腊": 12}

// parseLunarMonth 解析农历月份:数字(八/8/十二)或别名(正/冬/腊)。
func parseLunarMonth(s string) (int, bool) {
	if m, ok := cnNum(s); ok {
		return m, true
	}
	if m, ok := lunarMonthAlias[strings.TrimSuffix(s, "月")]; ok {
		return m, true
	}
	return 0, false
}

// lunarFestivalCanon 节日名的**规范写法**(用于把农历月日反解成节日名)。
// 反查而不是让调用方传名字:少一个需要落盘、需要保持一致的字段。
var lunarFestivalCanon = []struct {
	Name string
	M, D int
}{
	{"春节", 1, 1}, {"元宵节", 1, 15}, {"龙抬头", 2, 2}, {"端午节", 5, 5},
	{"七夕", 7, 7}, {"中元节", 7, 15}, {"中秋节", 8, 15}, {"重阳节", 9, 9},
	{"腊八", 12, 8}, {"除夕", 12, 0},
}

// lunarMDKey 农历月日 → 落盘键 "MM-DD"(day=0 = 该月最后一天,写成 "MM-00")。
func lunarMDKey(m, d int) string { return fmt.Sprintf("%02d-%02d", m, d) }

// festivalNameOfLunar 农历月日 → 规范节日名(不是节日返回空串)。
func festivalNameOfLunar(m, d int) string {
	for _, f := range lunarFestivalCanon {
		if f.M == m && f.D == d {
			return f.Name
		}
	}
	return ""
}

// cnLunarDay 从串首解析农历日(返回日与消耗的字节数)。
//
// **中文数字与阿拉伯数字不混读**:「十五20点」里的 20 是时刻,不是日的一部分 ——
// 贪心地把两者连起来读会得到「十五20」这种既不是数字也不是日期的东西。
func cnLunarDay(s string) (int, int) {
	// 初一…初十
	if strings.HasPrefix(s, "初") {
		r, sz := utf8.DecodeRuneInString(s[len("初"):])
		if d, ok := cnNum(string(r)); ok && d >= 1 && d <= 10 {
			return d, len("初") + sz
		}
	}
	// 廿一…廿九
	if strings.HasPrefix(s, "廿") {
		r, sz := utf8.DecodeRuneInString(s[len("廿"):])
		if d, ok := cnNum(string(r)); ok && d >= 1 && d <= 9 {
			return 20 + d, len("廿") + sz
		}
	}
	if strings.HasPrefix(s, "三十") {
		return 30, len("三十")
	}
	// 十一…三十(只吃中文数字)或阿拉伯数字(只吃半角数字)—— 两者不混读
	i := 0
	if isASCIIDigit(runeAt(s, 0)) {
		for i < len(s) && isASCIIDigit(runeAt(s, i)) {
			i++
		}
	} else {
		for i < len(s) {
			r := runeAt(s, i)
			if !isNumRune(r) || isASCIIDigit(r) {
				break
			}
			_, sz := utf8.DecodeRuneInString(s[i:])
			i += sz
		}
	}
	if i == 0 {
		return 0, 0
	}
	d, ok := cnNum(s[:i])
	if !ok || d < 1 || d > 30 {
		return 0, 0
	}
	return d, i
}
