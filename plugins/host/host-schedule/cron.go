// cron.go:5 字段 cron 表达式解析与下次触发计算(零依赖手写,不引第三方库)。
//
// 支持:分 时 日 月 周,字段语法 `*` / `N` / `a-b` / `*/n` / `a-b/n` / 逗号列表;
// 月与周额外接受三字母英文名(JAN/MON,大小写不敏感),周 0 与 7 都是周日。
//
// 扩展语法(本项目自有,**日字段独占**,Quartz 同款写法):
//
//	`L`   = 当月最后一天(`0 9 L * *` = 每月最后一天 09:00)
//	`L-n` = 当月最后一天往前 n 天(`L-3` = 倒数第 4 天)
//	`LW`  = 当月最后一个工作日(`0 9 LW * *`;只排除周六周日)
//
// 「月末」与「月末工作日」在 5 字段 cron 里无处表达(Vixie 无 L),而月度对账/结算是
// 真实场景;相比「用 29-31 区间猜」或「宿主每月改写表达式」,扩展字段只在这一处动刀。
// 边界:`L`/`LW` 不能与其它取值并存(`L,15` 拒),不能带步长(`L/2` 拒),只在日字段可用 ——
// 混用会撞上日-周 OR 语义(`0 9 L * 1` = 月末**或**每周一),那与「月末跑一次」的意图不符,
// 不该被静默当成一句简单排期。
//
// **`LW` 的边界必须对用户说明**:只排除周六周日,不含法定节假日与调休 ——
// 「工作日」在日常语境里往往指「法定工作日」,不说清就会让人以为调休上班日照跑。
// 该文案在 UI 与文档里都要出现。
//
// 跨版本提示:`L`/`LW` 写进计划文件后,**旧版本 gah 读它会解析失败** → 该计划被保留但不再排期
// (launch 有对应分支,会打 warn 日志),不是静默损坏。
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
	// last/lastOff:日字段的「月末」语义(`L` 与 `L-n`)。
	// 月末不是固定数字,取值集合表达不了,故单列两个字段;last=true 时
	// dayMatch 只看它,vals 不参与日判定。
	last    bool
	lastOff int
	// lw = 当月最后一个工作日(`LW`;只排除周六周日,不含法定节假日与调休)。
	lw bool
}

func (f *cronField) has(v int) bool { return f.vals[v] }

// matchDay 日字段对某天是否命中(含 `L` 月末与 `LW` 月末工作日语义)。
func (f *cronField) matchDay(t time.Time) bool {
	switch {
	case f.lw:
		return t.Day() == lastWeekdayOfMonth(t)
	case f.last:
		return t.Day() == monthLen(t)-f.lastOff
	}
	return f.has(t.Day())
}

// monthLen 当月天数(2 月闰年 29、平年 28)。
// 用 time.Date 归一(「下月 0 号」即本月最后一天),不硬编码月份表 —— 闰年不会算错。
func monthLen(t time.Time) int {
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
}

// lastWeekdayOfMonth 当月最后一个工作日(周一至周五)是几号。
// **不含法定节假日与调休** —— 那需要外部年度数据(违反「运行时依赖 0」),故不做;
// 它与「周末不顺延」这个最常见的诉求一致,且 UI 会明说这个边界。
func lastWeekdayOfMonth(t time.Time) int {
	last := monthLen(t)
	for i := 0; i < 7; i++ { // 连退 7 天必遇工作日
		d := last - i
		switch time.Date(t.Year(), t.Month(), d, 0, 0, 0, 0, t.Location()).Weekday() {
		case time.Saturday, time.Sunday:
			continue
		}
		return d
	}
	return last
}

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
	// `?`/`#` 是明确不支持的扩展语法(Quartz:nth-weekday 等);`W`(weekday)会落到
	// 下面的「取值非法」,错误信息更具体(指出是哪个字段)。`L` 已支持,见包注释。
	if strings.ContainsAny(expr, "?#") {
		return nil, fmt.Errorf("暂不支持 cron 扩展语法(? #),请用 5 字段标准写法: %q", expr)
	}
	min, err := parseCronField(fields[0], 0, 59, nil, "分", false)
	if err != nil {
		return nil, err
	}
	hour, err := parseCronField(fields[1], 0, 23, nil, "时", false)
	if err != nil {
		return nil, err
	}
	dom, err := parseCronField(fields[2], 1, 31, nil, "日", true) // 唯一允许 `L` 的字段
	if err != nil {
		return nil, err
	}
	mon, err := parseCronField(fields[3], 1, 12, monthNames, "月", false)
	if err != nil {
		return nil, err
	}
	dow, err := parseCronField(fields[4], 0, 7, dowNames, "周", false)
	if err != nil {
		return nil, err
	}
	if dow.vals[7] { // 7 = 周日。只在真为 true 时写 0 ——
		// 直接 `vals[0] = vals[0] || vals[7]` 即使算出 false 也会**建出一个 false 键**,
		// 而本文件外有多处按「遍历取值集合」统计的代码(控件态反解),会把那个假键当成周日。
		dow.vals[0] = true
	}
	delete(dow.vals, 7)
	delete(dow.vals, 8) // 防御:任何越界残留不得留在集合里
	return &cronSpec{expr: strings.Join(fields, " "), min: min, hour: hour, dom: dom, mon: mon, dow: dow}, nil
}

// parseCronField 解析单个字段(逗号列表;每项 `*`/`N`/`a-b` + 可选 `/n` 步长)。
// allowLast:是否允许 `L`/`L-n` 月末语义(只有日字段为 true)。
func parseCronField(field string, lo, hi int, names map[string]int, label string, allowLast bool) (*cronField, error) {
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
		if isLastTerm(body) {
			if !allowLast {
				return nil, fmt.Errorf("%s 字段不支持 %q:只有「日」字段支持 L(当月最后一天)", label, body)
			}
			if step != 1 {
				return nil, fmt.Errorf("%s 字段不支持步长:%q(L 是具体某天,不是等间隔)", label, term)
			}
			if f.last || f.lw {
				return nil, fmt.Errorf("%s 字段里重复出现 L/LW:%q", label, field)
			}
			off, err := lastOffset(body, label)
			if err != nil {
				return nil, err
			}
			f.last, f.lastOff = true, off
			continue
		}
		if isLastWeekdayTerm(body) {
			if !allowLast {
				return nil, fmt.Errorf("%s 字段不支持 %q:只有「日」字段支持 LW(当月最后一个工作日)", label, body)
			}
			if step != 1 {
				return nil, fmt.Errorf("%s 字段不支持步长:%q(LW 是具体某天,不是等间隔)", label, term)
			}
			if f.last || f.lw {
				return nil, fmt.Errorf("%s 字段里重复出现 L/LW:%q", label, field)
			}
			f.lw = true
			continue
		}
		if f.last || f.lw {
			// `L,15` 这类混用:L/LW 会让 vals 整段失效,静默忽略 15 属于骗人 —— 直接拒。
			return nil, fmt.Errorf("%s 字段里 L/LW 不能与其他取值并存:%q", label, field)
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

// isLastTerm 判断一段是否为月末语法 `L` 或 `L-n`(大小写不敏感)。
func isLastTerm(body string) bool {
	b := strings.ToUpper(strings.TrimSpace(body))
	return b == "L" || strings.HasPrefix(b, "L-")
}

// isLastWeekdayTerm 判断一段是否为「当月最后一个工作日」`LW`。
func isLastWeekdayTerm(body string) bool {
	return strings.ToUpper(strings.TrimSpace(body)) == "LW"
}

// lastOffset 解析月末偏移:`L`=0;`L-n`=n(往前 n 天)。n 越界显式报错。
func lastOffset(body, label string) (int, error) {
	b := strings.ToUpper(strings.TrimSpace(body))
	if b == "L" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(b[2:]))
	if err != nil || n < 0 || n > 30 {
		return 0, fmt.Errorf("%s 字段的 L-n 偏移非法(须为 0-30 的整数,0 等同 L): %q", label, body)
	}
	return n, nil
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
	domOK := c.dom.matchDay(t)
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
