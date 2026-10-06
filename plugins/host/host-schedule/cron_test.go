// cron 单测:解析边界 + 触发计算(含闰日/日周 OR/夏令时跳变)。
package hostschedule

import (
	"strings"
	"testing"
	"time"
)

// at 构造 UTC 时刻(确定性:多数用例不依赖本机时区)。
func at(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

func TestParseCronValid(t *testing.T) {
	for _, expr := range []string{
		"* * * * *", "0 8 * * *", "*/15 * * * *", "0 9-18 * * 1-5", "0 0 1,15 * *",
		"30 6 * * mon", "0 0 * * 7", "0 12 * JAN *", "5 0 * * SUN,WED", "0 */6 * * *",
		"0 0 29 2 *", "0 3 1-7 * 0", "0 0 1 */3 *",
	} {
		if _, err := parseCron(expr); err != nil {
			t.Fatalf("应可解析 %q: %v", expr, err)
		}
	}
}

func TestParseCronInvalid(t *testing.T) {
	cases := map[string]string{
		"* * * *":        "5 字段",
		"* * * * * *":    "5 字段",
		"":               "5 字段",
		"60 * * * *":     "分",
		"* 24 * * *":     "时",
		"0 0 0 * *":      "日",
		"0 0 32 * *":     "日",
		"0 0 * 0 *":      "月",
		"0 0 * 13 *":     "月",
		"0 0 * * 8":      "周",
		"*/0 * * * *":    "步长",
		"*/x * * * *":    "步长",
		"5-1 * * * *":    "倒序",
		"1,,2 * * * *":   "逗号",
		"? * * * *":      "扩展语法",
		"0 0 15W * *":    "取值非法", // W 仍未支持(「最近的工作日」)
		"0 0 L-31 * *":   "偏移非法",
		"abc * * * *":    "取值非法",
		"0 0 * FOO *":    "取值非法",
		"0 0 * * MONDAY": "取值非法",
		"0 0 1- * *":     "取值非法",
	}
	for expr, want := range cases {
		_, err := parseCron(expr)
		if err == nil {
			t.Fatalf("%q 应解析失败", expr)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%q 错误信息应含 %q,得 %q", expr, want, err.Error())
		}
	}
}

// TestCronWildcardSemantics 钉住有意差异:只有裸 `*` 算通配,`*/2` 算受限。
func TestCronWildcardSemantics(t *testing.T) {
	c, err := parseCron("0 0 */2 * 1")
	if err != nil {
		t.Fatal(err)
	}
	if c.dom.wild {
		t.Fatal("`*/2` 不应视为通配(否则与周字段的 OR 语义变得反直觉)")
	}
	if c.min.wild {
		t.Fatal("`0` 不是通配")
	}
	c2, err := parseCron("0 0 * * 1")
	if err != nil {
		t.Fatal(err)
	}
	if !c2.dom.wild {
		t.Fatal("裸 `*` 必须是通配")
	}
}

// TestCronDayOfMonthOrDayOfWeek 日与周同时受限 = OR(Vixie 传统语义,高频踩坑点)。
func TestCronDayOfMonthOrDayOfWeek(t *testing.T) {
	c, err := parseCron("0 8 1 * 1") // 每月 1 号 或 每周一
	if err != nil {
		t.Fatal(err)
	}
	if !c.match(at(2026, 2, 1, 8, 0)) {
		t.Fatal("1 号应命中")
	}
	if !c.match(at(2026, 2, 2, 8, 0)) { // 2026-02-02 是周一
		t.Fatal("周一应命中")
	}
	if c.match(at(2026, 2, 3, 8, 0)) {
		t.Fatal("周二且非 1 号不应命中")
	}
	// 周受限、日为 * → 只看周(AND 语义)
	c2, _ := parseCron("0 8 * * 1")
	if !c2.match(at(2026, 2, 2, 8, 0)) || c2.match(at(2026, 2, 1, 8, 0)) {
		t.Fatal("`* * 1` 形态应只按周判定")
	}
	// 周 = 7 等价周日
	c3, _ := parseCron("0 8 * * 7")
	if !c3.match(at(2026, 2, 1, 8, 0)) {
		t.Fatal("周 7 应等价周日(2026-02-01 是周日)")
	}
}

func TestCronNext(t *testing.T) {
	cases := []struct {
		expr string
		from time.Time
		want time.Time
	}{
		{"0 8 * * *", at(2026, 11, 14, 8, 0), at(2026, 11, 15, 8, 0)},  // 同一分钟不重复触发
		{"0 8 * * *", at(2026, 11, 14, 7, 59), at(2026, 11, 14, 8, 0)}, // 一分钟前
		{"*/15 * * * *", at(2026, 11, 14, 8, 7), at(2026, 11, 14, 8, 15)},
		{"0 0 1 * *", at(2026, 1, 15, 10, 0), at(2026, 2, 1, 0, 0)},
		{"0 0 29 2 *", at(2025, 3, 1, 0, 0), at(2028, 2, 29, 0, 0)},   // 闰日
		{"0 0 * * 1", at(2026, 2, 3, 0, 0), at(2026, 2, 9, 0, 0)},     // 下一个周一
		{"30 6 * * mon", at(2026, 2, 3, 0, 0), at(2026, 2, 9, 6, 30)}, // 英文名
		{"0 0 1-7 * 0", at(2026, 1, 31, 0, 0), at(2026, 2, 1, 0, 0)},  // 1 号且周日
		{"0 12 * JAN *", at(2026, 6, 1, 0, 0), at(2027, 1, 1, 12, 0)},
	}
	for _, tc := range cases {
		c, err := parseCron(tc.expr)
		if err != nil {
			t.Fatalf("%q: %v", tc.expr, err)
		}
		got, ok := c.next(tc.from)
		if !ok {
			t.Fatalf("%q 从 %s 应能算出下次触发", tc.expr, tc.from)
		}
		if !got.Equal(tc.want) {
			t.Fatalf("%q 从 %s:期望 %s,得 %s", tc.expr, tc.from, tc.want, got)
		}
	}
}

// TestCronNextNeverMatches 永不成立的表达式必须显式报错(不静默变成「不触发」)。
func TestCronNextNeverMatches(t *testing.T) {
	c, err := parseCron("0 0 30 2 *") // 2 月 30 日
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.next(at(2026, 1, 1, 0, 0)); ok {
		t.Fatal("2 月 30 日不该有下次触发")
	}
	if _, err := c.nextOrError(at(2026, 1, 1, 0, 0)); err == nil {
		t.Fatal("nextOrError 必须报错(Add 时据此拒绝)")
	} else if !strings.Contains(err.Error(), "4 年内无匹配") {
		t.Fatalf("错误信息应说明原因,得 %q", err.Error())
	}
}

// TestCronDST 夏令时跳变:春令时不存在的小时顺延,秋令时重复的小时取第一次。
func TestCronDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("本机无 tzdata,跳过夏令时用例: %v", err)
	}
	// 2026-03-08 02:00-03:00 EST→EDT 不存在:02:30 当天无匹配 → 顺延到次日
	c, _ := parseCron("30 2 * * *")
	from := time.Date(2026, 3, 8, 0, 0, 0, 0, ny)
	got, ok := c.next(from)
	if !ok {
		t.Fatal("应能算出下一次")
	}
	if got.Day() != 9 || got.Hour() != 2 || got.Minute() != 30 {
		t.Fatalf("春令时缺失的小时应顺延到次日 02:30,得 %s", got)
	}
	// 2026-11-01 01:00-02:00 出现两次:取第一次(EDT,UTC-4)
	c2, _ := parseCron("30 1 * * *")
	from2 := time.Date(2026, 11, 1, 0, 0, 0, 0, ny)
	got2, ok2 := c2.next(from2)
	if !ok2 {
		t.Fatal("应能算出下一次")
	}
	if got2.Day() != 1 || got2.Hour() != 1 || got2.Minute() != 30 {
		t.Fatalf("秋令时重复的小时应取第一次出现,得 %s", got2)
	}
	if _, off := got2.Zone(); off != -4*3600 {
		t.Fatalf("第一次出现应是夏令时(-04:00),得偏移 %ds", off)
	}
}

// TestCronNextLocalTZ 本机时区语义:同一表达式在 UTC 与固定东八区应给出各自墙上时间。
func TestCronNextLocalTZ(t *testing.T) {
	sh := time.FixedZone("CST", 8*3600)
	c, _ := parseCron("0 8 * * *")
	from := time.Date(2026, 11, 14, 7, 0, 0, 0, sh)
	got, ok := c.next(from)
	if !ok {
		t.Fatal("应能算出下一次")
	}
	if got.Hour() != 8 || got.Location() != sh {
		t.Fatalf("应按时区墙上时间 08:00 触发,得 %s", got)
	}
	if got.UTC().Hour() != 0 {
		t.Fatalf("东八区 08:00 应为 UTC 00:00,得 %s", got.UTC())
	}
}

func TestCronHuman(t *testing.T) {
	cases := map[string]string{
		"0 8 * * *":     "每天 08:00",
		"30 6 * * 1":    "每周一 06:30",
		"0 0 5 * *":     "每月 5 号 00:00",
		"* * * * *":     "每分钟",
		"0 * * * *":     "每小时整点",
		"*/15 * * * *":  "每 15 分钟",
		"0 */6 * * *":   "每 6 小时", // 分是 0 时不提「第 0 分」(这串直接给人看)
		"30 */6 * * *":  "每 6 小时的第 30 分",
		"0 0 * * 0":     "每周日 00:00",
		"0 9 * * 1-5":   "每个工作日 09:00",
		"0 9 * * 1,3,5": "每周一、三、五 09:00",
		"0 9 8-14 * 2":  "每月第二个周二 09:00",
		"0 9 15-21 * 3": "每月第三个周三 09:00",
		"0 9 29-31 * 3": "每月第五个周三 09:00", // 第 5 个窗口被钳到 29-31
		"0 9 L * *":     "每月最后一天 09:00",
		"30 * * * *":    "每小时第 30 分",
	}
	for expr, want := range cases {
		if got := cronHuman(expr); got != want {
			t.Fatalf("%q:期望 %q,得 %q", expr, want, got)
		}
	}
	// 每年某天能被 cron 表达,所以现在有文案(此前一律返回空串:看得见却改不了)。
	if got, want := cronHuman("0 9 1 10 *"), "每年 10 月 1 日 09:00"; got != want {
		t.Fatalf("限定月份的年度排期应有文案:期望 %q,得 %q", want, got)
	}

	// 无法简写的形态返回空串(调用方回显表达式本身)。
	// 红线:`0 8 1 * 1` = 「每月 1 号**或**每周一」(日与周**同时受限**),简写成任何一种都在说谎。
	// 注意别把 `0 0 1 1 *` 也归进来 —— 它是「1 月 1 日」(周字段为 `*`),不是 OR 陷阱。
	for _, expr := range []string{
		"5,35 8-10 * * 1,3", "0 8 1 * 1", "0 9 1-15 * *", "0 9 L * 1", "0 8-18 * * *",
	} {
		if got := cronHuman(expr); got != "" {
			t.Fatalf("%q 应无法简写(得 %q),实得 %q", expr, "空串", got)
		}
	}
}

// —— 月末扩展语法 `L` / `L-n` ——

func TestParseCronLastValid(t *testing.T) {
	for _, expr := range []string{"0 9 L * *", "30 6 l * *", "0 9 L-3 * *", "0 0 L * 1"} {
		if _, err := parseCron(expr); err != nil {
			t.Fatalf("应可解析 %q: %v", expr, err)
		}
	}
}

func TestParseCronLastInvalid(t *testing.T) {
	cases := map[string]string{
		"0 9 L,15 * *": "不能与其他取值并存", // 混用会让 vals 整段失效
		"0 9 L * * ,":  "5 字段",
		"0 9 L/2 * *":  "不支持步长",
		"0 9 L,L * *":  "重复出现 L",
		"0 9 L-x * *":  "偏移非法",
		"0 9 L-99 * *": "偏移非法",
		"0 L * * *":    "只有「日」字段支持 L", // 小时字段不支持
		"0 9 * L *":    "只有「日」字段支持 L", // 月份字段不支持
		"0 9 * * L":    "只有「日」字段支持 L", // 周字段不支持
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

func TestCronLastMatchesMonthEnd(t *testing.T) {
	spec, err := parseCron("0 9 L * *")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		at      time.Time
		matches bool
	}{
		{at(2026, 1, 31, 9, 0), true},  // 31 天月
		{at(2026, 1, 30, 9, 0), false}, // 前一天不算
		{at(2026, 2, 28, 9, 0), true},  // 平年 2 月
		{at(2026, 4, 30, 9, 0), true},  // 30 天月
		{at(2028, 2, 29, 9, 0), true},  // 闰年 2 月
		{at(2028, 2, 28, 9, 0), false}, // 闰年 2 月不是 28 号
	} {
		if got := spec.match(tc.at); got != tc.matches {
			t.Fatalf("%s 命中=%v,期望 %v", tc.at, got, tc.matches)
		}
	}
}

func TestCronLastNextAcrossMonths(t *testing.T) {
	spec, err := parseCron("0 9 L * *")
	if err != nil {
		t.Fatal(err)
	}
	// 下次触发恒为「当月最后一天 09:00」,跨月/跨闰年都要准。
	for _, tc := range []struct{ from, want time.Time }{
		{at(2026, 1, 15, 0, 0), at(2026, 1, 31, 9, 0)},
		{at(2026, 2, 28, 9, 0), at(2026, 3, 31, 9, 0)}, // 当天 9:00 已过 → 下个月末
		{at(2028, 2, 29, 10, 0), at(2028, 3, 31, 9, 0)},
	} {
		got, ok := spec.next(tc.from)
		if !ok || !got.Equal(tc.want) {
			t.Fatalf("next(%s)=%v(%v),期望 %v", tc.from, got, ok, tc.want)
		}
	}
}

func TestCronLastOffset(t *testing.T) {
	spec, err := parseCron("0 9 L-2 * *")
	if err != nil {
		t.Fatal(err)
	}
	// 2 月平年 28 天:最后一天往前 2 天 = 26 号。
	if !spec.match(at(2026, 2, 26, 9, 0)) {
		t.Fatal("L-2 应命中 2 月 26 号")
	}
	if spec.match(at(2026, 2, 27, 9, 0)) {
		t.Fatal("L-2 不该命中 27 号")
	}
}

// L 是受限字段(非裸 *):与受限周几并存时走日-周 OR,与 Vixie 语义一致。
func TestCronLastWithDayOfWeekOr(t *testing.T) {
	spec, err := parseCron("0 9 L * 1")
	if err != nil {
		t.Fatal(err)
	}
	if !spec.match(at(2026, 1, 31, 9, 0)) { // 1/31 = 月末
		t.Fatal("月末应命中")
	}
	if !spec.match(at(2026, 1, 26, 9, 0)) { // 1/26 = 周一
		t.Fatal("每周一应命中")
	}
	if spec.match(at(2026, 1, 28, 9, 0)) { // 既非月末也非周一
		t.Fatal("1/28 不该命中")
	}
}
