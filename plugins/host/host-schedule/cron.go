// cron.go:5 字段 cron 表达式解析与下次触发计算(零依赖手写,不引第三方库)。
//
// 支持:分 时 日 月 周,字段语法 `*` / `N` / `a-b` / `*/n` / `a-b/n` / 逗号列表;
// 月与周额外接受三字母英文名(JAN/MON,大小写不敏感),周 0 与 7 都是周日。
//
// 与 Vixie cron 的两处**有意**差异(均更保守/更直观,已在文档与测试里钉住):
//  1. 只有**裸 `*`** 才算通配;`*/2` 视为「受限字段」(每天 ≠ 每 2 天,Vixie 把
//     `*/2` 也算通配,会让配对字段的 OR 语义变得反直觉)。
//  2. 日与周**同时受限**时按 OR(任一命中即触发)—— 这点与 Vixie 一致,但属高频
//     踩坑点:`0 8 1 * 1` = 「每月 1 号**或**每周一」,不是「1 号且周一」。
//
// 时间语义:全部按**本机本地时区**;夏令时跳变交给 time.Date 归一 ——
// 春令时被跳过的小时不存在(归一后墙上时间不再匹配 → 顺延到下一个合法时刻),
// 秋令时重复的小时取第一次出现。最大搜索 4 年(覆盖 2 月 29 日等闰年组合),
// 超出即判「无下次触发」(如 `0 0 30 2 *` = 2 月 30 日,永远不成立)。
package hostschedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// maxSearchDays 下次触发的最远搜索天数(4 年 + 1 天:覆盖闰日组合)。
const maxSearchDays = 366*4 + 1

// cronField 一个已解析的字段(取值集合 + 是否裸通配)。
type cronField struct {
	wild bool // 裸 `*`(影响日/周的 AND-OR 语义)
	vals map[int]bool
}

func (f *cronField) has(v int) bool { return f.vals[v] }

// cronSpec 一条已解析的 5 字段表达式。
type cronSpec struct {
	expr string
	min  *cronField
	hour *cronField
	dom  *cronField
	mon  *cronField
	dow  *cronField
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// parseCron 解析 5 字段 cron 表达式(非法输入返回中文可读错误,不静默兜底)。
func parseCron(expr string) (*cronSpec, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron 需 5 字段「分 时 日 月 周」,收到 %d 个: %q", len(fields), expr)
	}
	// `?`/`#` 是明确不支持的扩展语法(Quartz:nth-weekday 等);`L`/`W` 会落到
	// 下面的「取值非法」,错误信息更具体(指出是哪个字段)。
	if strings.ContainsAny(expr, "?#") {
		return nil, fmt.Errorf("暂不支持 cron 扩展语法(? #),请用 5 字段标准写法: %q", expr)
	}
	min, err := parseCronField(fields[0], 0, 59, nil, "分")
	if err != nil {
		return nil, err
	}
	hour, err := parseCronField(fields[1], 0, 23, nil, "时")
	if err != nil {
		return nil, err
	}
	dom, err := parseCronField(fields[2], 1, 31, nil, "日")
	if err != nil {
		return nil, err
	}
	mon, err := parseCronField(fields[3], 1, 12, monthNames, "月")
	if err != nil {
		return nil, err
	}
	dow, err := parseCronField(fields[4], 0, 7, dowNames, "周")
	if err != nil {
		return nil, err
	}
	dow.vals[0] = dow.vals[0] || dow.vals[7] // 7 = 周日
	delete(dow.vals, 7)
	delete(dow.vals, 8) // 防御:任何越界残留不得留在集合里
	return &cronSpec{expr: strings.Join(fields, " "), min: min, hour: hour, dom: dom, mon: mon, dow: dow}, nil
}

// parseCronField 解析单个字段(逗号列表;每项 `*`/`N`/`a-b` + 可选 `/n` 步长)。
func parseCronField(field string, lo, hi int, names map[string]int, label string) (*cronField, error) {
	f := &cronField{vals: map[int]bool{}}
	if strings.TrimSpace(field) == "" {
		return nil, fmt.Errorf("%s 字段为空: 检查是否漏了一个字段", label)
	}
	for _, term := range strings.Split(field, ",") {
		term = strings.TrimSpace(term)
		if term == "" {
			return nil, fmt.Errorf("%s 字段有多余逗号: %q", label, field)
		}
		step := 1
		body := term
		if i := strings.Index(term, "/"); i >= 0 {
			n, err := strconv.Atoi(strings.TrimSpace(term[i+1:]))
			if err != nil || n < 1 {
				return nil, fmt.Errorf("%s 字段步长非法(须为 ≥1 的整数): %q", label, term)
			}
			step = n
			body = strings.TrimSpace(term[:i])
		}
		if body == "*" {
			if step == 1 {
				f.wild = true
			}
			for v := lo; v <= hi; v += step {
				f.vals[v] = true
			}
			continue
		}
		a, b, err := parseCronRange(body, lo, hi, names, label)
		if err != nil {
			return nil, err
		}
		if a > b {
			return nil, fmt.Errorf("%s 字段区间倒序: %q(应为 %d-%d 形式)", label, term, lo, hi)
		}
		for v := a; v <= b; v += step {
			f.vals[v] = true
		}
	}
	return f, nil
}

// parseCronRange 解析 `N` 或 `a-b`(允许三字母英文名)。
func parseCronRange(body string, lo, hi int, names map[string]int, label string) (int, int, error) {
	parseOne := func(s string) (int, error) {
		s = strings.TrimSpace(s)
		if names != nil {
			if v, ok := names[strings.ToLower(s)]; ok {
				return v, nil
			}
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("%s 字段取值非法: %q(应为数字或 %s 范围内的值)", label, s, fmt.Sprintf("%d-%d", lo, hi))
		}
		if v < lo || v > hi {
			return 0, fmt.Errorf("%s 字段越界: %d 不在 %d-%d 内", label, v, lo, hi)
		}
		return v, nil
	}
	if i := strings.Index(body, "-"); i >= 0 {
		a, err := parseOne(body[:i])
		if err != nil {
			return 0, 0, err
		}
		b, err := parseOne(body[i+1:])
		if err != nil {
			return 0, 0, err
		}
		return a, b, nil
	}
	v, err := parseOne(body)
	if err != nil {
		return 0, 0, err
	}
	return v, v, nil
}

// match 判断给定时刻是否命中本表达式(**整分钟**;秒与纳秒忽略)。
func (c *cronSpec) match(t time.Time) bool {
	if c == nil {
		return false
	}
	return c.min.has(t.Minute()) && c.hour.has(t.Hour()) && c.dayMatch(t)
}

// dayMatch 日/月/周判定(含日-周 OR 语义)。
func (c *cronSpec) dayMatch(t time.Time) bool {
	if !c.mon.has(int(t.Month())) {
		return false
	}
	domOK := c.dom.has(t.Day())
	dowOK := c.dow.has(int(t.Weekday())) // Go: Sunday = 0
	switch {
	case c.dom.wild && c.dow.wild:
		return true
	case c.dom.wild:
		return dowOK
	case c.dow.wild:
		return domOK
	default:
		return domOK || dowOK // 两者都受限 → 任一命中(Vixie 语义)
	}
}

// next 返回 after 之后第一个命中时刻(不含 after 本身所在分钟)。
// ok=false = 未来 4 年内无匹配(表达式不会触发)。
func (c *cronSpec) next(after time.Time) (time.Time, bool) {
	if c == nil {
		return time.Time{}, false
	}
	start := after.Truncate(time.Minute).Add(time.Minute)
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, start.Location())
	for i := 0; i < maxSearchDays; i++ {
		if c.dayMatch(day) {
			for h := 0; h < 24; h++ {
				if !c.hour.has(h) {
					continue
				}
				for m := 0; m < 60; m++ {
					if !c.min.has(m) {
						continue
					}
					// time.Date 归一夏令时:春令时缺失的小时会被顺延,
					// 归一后墙上时间不再匹配 → 该次触发不存在,继续找。
					cand := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, day.Location())
					if cand.Before(start) || !c.match(cand) {
						continue
					}
					return cand, true
				}
			}
		}
		// 用 time.Date 加一天(不用 Add(24h):夏令时切换日不是 24 小时)
		day = time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, start.Location())
	}
	return time.Time{}, false
}

// nextOrError 计算下次触发;无匹配返回可读错误(Add/Update 时用它拒掉永不触发的表达式)。
func (c *cronSpec) nextOrError(after time.Time) (time.Time, error) {
	t, ok := c.next(after)
	if !ok {
		return time.Time{}, fmt.Errorf("cron 表达式在未来 4 年内无匹配时刻(如 2 月 30 日),不会触发: %q", c.expr)
	}
	return t, nil
}

// cronHuman 给一句中文人话描述(只覆盖常见形态;无法简写返回 "" —— 调用方回显表达式本身)。
func cronHuman(expr string) string {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return ""
	}
	dowZh := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	m, errM := strconv.Atoi(f[0])
	h, errH := strconv.Atoi(f[1])
	switch {
	case f[0] == "*" && f[1] == "*" && f[2] == "*" && f[3] == "*" && f[4] == "*":
		return "每分钟"
	case f[0] == "0" && f[1] == "*" && f[2] == "*" && f[3] == "*" && f[4] == "*":
		return "每小时整点"
	case errM == nil && errH == nil:
		hm := fmt.Sprintf("%02d:%02d", h, m)
		switch {
		case f[2] == "*" && f[3] == "*" && f[4] == "*":
			return "每天 " + hm
		case f[2] == "*" && f[3] == "*":
			if v, err := strconv.Atoi(f[4]); err == nil && v >= 0 && v <= 7 {
				return "每" + dowZh[v%7] + " " + hm
			}
		case f[3] == "*" && f[4] == "*":
			if d, err := strconv.Atoi(f[2]); err == nil {
				return fmt.Sprintf("每月 %d 号 %s", d, hm)
			}
		}
	}
	if strings.HasPrefix(f[0], "*/") && f[1] == "*" && f[2] == "*" && f[3] == "*" && f[4] == "*" {
		if n, err := strconv.Atoi(f[0][2:]); err == nil && n > 0 {
			return fmt.Sprintf("每 %d 分钟", n)
		}
	}
	if strings.HasPrefix(f[1], "*/") && f[2] == "*" && f[3] == "*" && f[4] == "*" {
		if n, err := strconv.Atoi(f[1][2:]); err == nil && n > 0 {
			if f[0] == "*" {
				return fmt.Sprintf("每 %d 小时", n)
			}
			if m2, err := strconv.Atoi(f[0]); err == nil {
				return fmt.Sprintf("每 %d 小时的第 %d 分", n, m2)
			}
		}
	}
	return ""
}
