// crontxt.go:一句中文 → 排期(规则解析,不引 NLU 依赖、不调模型)。
//
// **边界是刻意的**:只做到「每个输入都能落到一个确定时刻」为止。
//
//	覆盖:今天/明天/后天/大后天、下周X、本周X、12月20日、每天、每周一三五、工作日、
//	     每月5号、每月最后一个周二、月底/月末、每小时、每半小时、每10分钟、每2小时、只跑一次,
//	     早上/上午/中午/下午/晚上/半夜 + 8点/8点半/8点30/8:30,
//	     节日与农历(中秋/春节/除夕/「农历八月十五」—— 换算见 lunar.go)
//	不覆盖:区间型(「月底前」)、模糊型(「有空的时候」)、语气词,
//	     以及**法定节假日与调休**(放假日历要联网取每年的国务院公告,违反「运行时依赖 0」的
//	     静态二进制定位;农历本身是纯算法所以能做,放假安排不是 —— 见 lunar.go 头注)。
//
// 不做区间/模糊,不是能力不足而是**产品判断**:解析不出就回退到控件,而控件里本来就有
// 日号与周几可选 —— 让用户「说一句话 → 系统回一句问话 → 再选」比直接选更绕。
// 真上 NLU 还有两条硬约束:节假日必须联网取数据(违反「运行时依赖 0」的静态二进制定位)、
// 或改调云端模型(每次建计划烧 token + 出错不可预期)。
//
// 三条不可让步的纪律:
//  1. **只填控件,不直接执行** —— 结果必须回填到控件里让人看见(「晚上8点」被当成 20:00
//     还是 08:00 是高代价猜错,要求用户信任「已识别」是不可接受的)。
//  2. **解析不出就明说,不猜** —— 返回中文原因,调用方按「回退话术」提示,不是致命错误。
//  3. **缺时刻用回退值并告知** —— 沿用表单当前时刻,再缺才用 09:00,且标记 assumed,
//     UI 必须回显「已按 09:00 设好」。
package hostschedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// parsedText 中文解析结果。
type parsedText struct {
	Struct cronStruct
	// AssumedTime=true 表示时刻是**补的默认值**(用户没说),调用方必须回显告知,
	// 否则用户会以为是自己写的那个时间。
	AssumedTime bool
	// Converged 非空 = 用户说的是区间/模糊说法,被**收敛**成了确定排期
	// (如「月底前」→「月底」)。界面必须把它说出来(「按「月底」理解为…」)——
	// 收敛是可接受的猜,但**猜了不说**就变成了静默错误。
	Converged string
}

// parseCronText 解析一句中文排期。now 提供「今天/明天/下周X」的基准;now=零值时按今天。
// err 非 nil 时给出中文原因(调用方回退到控件,不当作致命错误)。
func parseCronText(text string, now time.Time, fallbackHM cronStruct) (parsedText, error) {
	s := squeezeSpace(normalizeText(text))
	if s == "" {
		return parsedText{}, fmt.Errorf("还没写排期:试试「每天早上8点」「每周一三五 9 点」「月底最后一天 17:00」")
	}
	if now.IsZero() {
		now = time.Now()
	}
	res := parsedText{Struct: fallbackHM}

	// ⓪ 区间型口语先**收敛**成确定说法(见 convergeRange)。
	// 放在最前是为了让它与后面的时刻/日期解析自然组合:「月底前 17点」→「月底 17点」。
	s, res.Converged = convergeRange(s)

	// ① 节日与农历日期:它们本身就是具体日期,直接定位到年度排期,不走「多久一次」那套。
	// 节日名必须**长名优先**(见 festivalOf),否则「中秋节」会被「中秋」吃掉再留下「节」。
	annualDone := false
	if name, mo, day, lunar, r, okFest := festivalOf(s); okFest {
		res.Struct.Month, res.Struct.Day, res.Struct.Festival = mo, day, name
		res.Struct.Repeat = RepeatAnnualDate
		if lunar {
			res.Struct.Repeat = RepeatLunarAnnual
		}
		s, annualDone = r, true
	} else if mo, day, r, okLunar := parseLunarMD(s); okLunar {
		res.Struct.Repeat, res.Struct.Month, res.Struct.Day = RepeatLunarAnnual, mo, day
		s, annualDone = r, true
	}

	// ② 频率(先于日期:「每周一」的「周一」属于频率,不能被日期解析吃掉)
	repeatOK := false
	if !annualDone {
		// 「每年」只是标记重复,具体月日交给后面的日期解析(「每年10月1日」)。
		if hit, r := cutAny(s, []string{"每一年", "每年"}); hit {
			s, res.Struct.annual = r, true
		}
		rest, ok, err := parseRepeat(s, &res.Struct)
		if err != nil {
			return parsedText{}, err
		}
		s, repeatOK = rest, ok
	}

	// ③ 日期(公历具体日期:配合「每年」= 年度重复,否则 = 一次性)
	d, rest2, foundDate := parseDate(s, now)
	s = rest2

	// ④ 时刻
	h, m, rest3, foundTime := parseTimeOfDay(s)
	s = rest3

	switch {
	case res.Struct.annual && foundDate:
		// 「每年 10 月 1 日」= 年度重复(不是一次性)
		res.Struct.Repeat = RepeatAnnualDate
		res.Struct.Month, res.Struct.Day = int(d.Month()), d.Day()
		res.Struct.OnceDate = ""
	case foundDate:
		res.Struct.Repeat = RepeatOnce
		res.Struct.OnceDate = d.Format("2006-01-02")
	case annualDone: // 节日/农历日期已定,只等时刻
	case repeatOK: // 频率已识别
	case foundTime:
		// 只说了时刻(如「9点」)→ 默认每天。控件里可见可改,不是猜。
		res.Struct.Repeat = RepeatDaily
	default:
		return parsedText{}, fmt.Errorf("没看懂「%s」的重复方式:试试「每天」「每周一」「工作日」「每月 5 号」「每月最后一天」「中秋」「每 2 小时」,或直接点下面的常用预设", text)
	}
	res.Struct.annual = false // 内部标记不往外传
	if foundTime {
		if h > 23 || m > 59 {
			return parsedText{}, fmt.Errorf("时刻 %02d:%02d 不存在(小时 0-23、分钟 0-59)", h, m)
		}
		res.Struct.Hour, res.Struct.Minute = h, m
	} else {
		res.AssumedTime = true // 用户没说时刻 → 用表单当前值(仍是确定值,回显即可)
	}

	if leftover := strings.Trim(s, " 的。,、-~到至"); leftover != "" {
		// 残留说明有没覆盖的说法:明说,不静默忽略(否则用户以为自己写对了)。
		return parsedText{}, fmt.Errorf("「%s」里的「%s」没看懂,请换个说法或直接用下方选择器", text, leftover)
	}
	return res, nil
}

// normalizeText 全角数字/冒号转半角(不动空格:空格是「日期 时刻」的分隔,归一阶段无法前瞻)。
func normalizeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '０' && r <= '９': // 全角 ０-９
			b.WriteRune(r - '０' + '0')
		case r == '：':
			b.WriteByte(':')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// squeezeSpace 删掉无意义的空格(「每周一 三五」=「每周一三五」),
// 但**两侧都是数字的空格保留** ——「2027-01-05 10点」里它是日期与时刻的分隔,
// 删掉会粘成「2027-01-0510点」而整句解析失败。
func squeezeSpace(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		if r != ' ' && r != '\u3000' && r != '\t' && r != '\n' {
			b.WriteRune(r)
			continue
		}
		// 只对**ASCII 数字**保留(「2027-01-05 10点」的日期-时刻分隔);
		// 中文数字不算:否则「腊八 7点」的空格会被留下,时刻解析直接失败。
		if i > 0 && i+1 < len(rs) && isASCIIDigit(rs[i-1]) && isASCIIDigit(rs[i+1]) {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// convergeRange 把区间型/模糊口语**收敛**成确定说法。
//
// 为什么是收敛而不是拒绝、也不是澄清轮:「月底前」「月初」「月中」在本项目语境下
// 意图高度一致(就是要一个具体的日号),拒绝会把用户挡在门外,澄清轮又让他多绕一圈;
// 而收敛的代价(猜的时间点)由 Converged 一路传到界面说出来,不静默吞下。
//
// 真正模糊到无法收敛的表述(「有空的时候」)**不在这里处理** —— 它们没有确定解,
// 硬猜一个时刻就是在编。那些走调用方的「没看懂」+ 预设引导。
func convergeRange(s string) (string, string) {
	for _, m := range []struct {
		keys []string
		to   string
		want string
	}{
		{[]string{"月底之前", "月底以前", "月底前", "月末之前", "月末以前", "月末前",
			"每月月底前", "每月月底之前", "每月月末前", "每个月月底前"}, "月底", "月底"},
		{[]string{"月头", "月初", "每月月初", "每个月月初"}, "每月1号", "月初"},
		{[]string{"月半", "月中", "每月月中"}, "每月15号", "月中"},
	} {
		if hit, r := cutAny(s, m.keys); hit {
			return m.to + r, m.want
		}
	}
	return s, ""
}

// parseRepeat 解析重复方式,返回「剩余文本 + 是否识别」。识别到即写入 out。
func parseRepeat(s string, out *cronStruct) (rest string, ok bool, err error) {
	t := s
	// 每月第 N 个周几 —— 必须最先判:它也以「每月」开头。
	if nth, dow, r, found := parseMonthNth(t); found {
		if len(dow) != 1 {
			return "", false, fmt.Errorf("「%s」里同时说了好几个周几,一次只挑一个", s)
		}
		out.Repeat, out.Nth, out.Dows = RepeatMonthNth, nth, dow
		return r, true, nil
	}
	for _, m := range []struct {
		keys  []string
		apply func()
	}{
		// 每月最后一次 / 月底 / 月末 —— 5 字段 cron 没有「月末」,靠 `L` 扩展表达。
		{[]string{"每月最后一天", "每月最后一日", "每月最后", "每个月的最后一天", "月底最后一天"}, func() { out.Repeat = RepeatMonthLast }},
		{[]string{"月底", "月末", "每月月底", "每月月末"}, func() { out.Repeat = RepeatMonthLast }},
	} {
		if hit, r := cutAny(t, m.keys); hit {
			m.apply()
			return r, true, nil
		}
	}
	// 每月 N 号 / N 号
	if _, d, r, found := parseMonthDay(t); found {
		if d < 1 || d > 31 {
			return "", false, fmt.Errorf("每月没有 %d 号(1-31)", d)
		}
		out.Repeat, out.Day = RepeatMonthDay, d
		return r, true, nil
	}
	// 工作日 / 平日
	if hit, r := cutAny(t, []string{"每个工作日", "工作日", "平日", "上班日"}); hit {
		out.Repeat = RepeatWeekdays
		return r, true, nil
	}
	// 每周/每星期 + 周几列表(无周几时默认周一)
	if hit, r := cutAny(t, []string{"每周", "每星期", "每礼拜", "每个星期", "每个礼拜"}); hit {
		dows, rest := parseDows(r)
		if len(dows) == 0 {
			dows = []int{1}
		}
		out.Repeat, out.Dows = RepeatWeekly, dows
		return rest, true, nil
	}
	// 每天
	if hit, r := cutAny(t, []string{"每天", "每日", "天天", "每回"}); hit {
		out.Repeat = RepeatDaily
		return r, true, nil
	}
	// 每小时 / 每 N 小时 / 每 N 分钟 / 每半小时
	if hit, r := cutAny(t, []string{"每小时", "每钟头"}); hit {
		// 每小时固定在整点:小时位由 cron 的 `*` 承担,不是用户选的值,清掉免得回显错。
		out.Repeat, out.Minute, out.Hour = RepeatHourly, 0, 0
		return r, true, nil
	}
	if n, r, found := parseEveryUnit(t, "小时"); found {
		if n < 1 || n > 23 {
			return "", false, fmt.Errorf("每 %d 小时不在 1-23 之间", n)
		}
		if n < 2 {
			return "", false, fmt.Errorf("每小时请直接说「每小时」")
		}
		// 「每 2 小时」只给间隔:小时位由 `*/N` 承担(清零),分钟位是「第几分钟」——
		// 用户没说就沿用表单当前分钟(那是真实可用的信息,不该丢)。
		out.Repeat, out.Every, out.Hour = RepeatEveryNHour, n, 0
		return r, true, nil
	}
	if hit, r := cutAny(t, []string{"每半小时"}); hit {
		// 同上:间隔类档位不带具体时刻,清掉回退值免得混进控件显示。
		out.Repeat, out.Every, out.Minute, out.Hour = RepeatEveryNMin, 30, 0, 0
		return r, true, nil
	}
	if n, r, found := parseEveryUnit(t, "分钟"); found {
		if n < 1 || n > 59 {
			return "", false, fmt.Errorf("每 %d 分钟不在 1-59 之间", n)
		}
		if n < 2 {
			return "", false, fmt.Errorf("每分钟请直接说「每小时」或改用「每 5 分钟」:每分钟等于每分钟烧一轮模型")
		}
		out.Repeat, out.Every, out.Minute, out.Hour = RepeatEveryNMin, n, 0, 0
		return r, true, nil
	}
	// 只跑一次
	if hit, r := cutAny(t, []string{"只跑一次", "只执行一次", "仅一次", "就一次", "一次"}); hit {
		out.Repeat = RepeatOnce
		return strings.TrimPrefix(r, ":"), true, nil
	}
	return s, false, nil
}

// parseMonthNth 「每月第二个周二」「每月第2个周三」。
func parseMonthNth(s string) (nth int, dows []int, rest string, found bool) {
	rest = s
	if hit, r := cutAny(rest, []string{"每月", "每个月", "每個月"}); hit {
		rest = r
	} else {
		return 0, nil, s, false
	}
	rest = strings.TrimPrefix(rest, "的")
	if !strings.HasPrefix(rest, "第") {
		return 0, nil, s, false
	}
	const ordPrefix = "第" // 中文是 3 字节:数字从 len(ordPrefix) 起,不是 +1
	i := digitEnd(rest, len(ordPrefix))
	if i <= len(ordPrefix) { // 「第」后面没有数字
		return 0, nil, s, false
	}
	n, ok := cnNum(rest[len(ordPrefix):i])
	if !ok || i+len("个") > len(rest) || rest[i:i+len("个")] != "个" {
		return 0, nil, s, false
	}
	rest = rest[i+len("个"):]
	dows, rest = parseDows(rest)
	if n < 1 || n > 5 {
		n = 0 // 第 6 个不存在 → 让调用方报错
	}
	return n, dows, rest, true
}

// parseMonthDay 「每月5号」。**必须带「每月」前缀才生效**:没有前缀的「12月20日」
// 是一次性目标日期(走 parseDate),若这里也认,「12月20日」会被误读成「每月 20 号」——
// 那是在说一件完全不同的事。扫描式找「数字+号/日」,因为时刻通常跟在后面(「每月5号9点」)。
func parseMonthDay(s string) (hit bool, day int, rest string, found bool) {
	t := s
	if h, r := cutAny(t, []string{"每月", "每个月"}); h {
		t = r
	} else {
		return false, 0, s, false
	}
	for _, suf := range []string{"号", "日"} {
		i := strings.Index(t, suf)
		if i <= 0 {
			continue
		}
		j := i
		for j > 0 {
			r, size := lastRuneBefore(t, j)
			if !isNumRune(r) {
				break
			}
			j -= size
		}
		if j == i {
			continue // 「号/日」前面没有数字
		}
		n, ok := cnNum(t[j:i])
		if !ok {
			continue
		}
		return true, n, t[:j] + t[i+len(suf):], true
	}
	return true, 0, s, false
}

// parseEveryUnit 「每5分钟」「每 2 小时」。
func parseEveryUnit(s, unit string) (n int, rest string, found bool) {
	i := strings.Index(s, "每")
	if i < 0 {
		return 0, s, false
	}
	rest = s[i+len("每"):]
	j := digitEnd(rest, 0)
	if j == 0 {
		return 0, s, false
	}
	n, ok := cnNum(rest[:j])
	if !ok || !strings.HasPrefix(rest[j:], unit) {
		return 0, s, false
	}
	return n, rest[j+len(unit):], true
}

// parseDows 从串首吃连续周几字符(「一三五」或「1,3,5」)。
// 数字是**中文为空时的回退** —— 两者共存时以中文为准,见 parseDowsArabic 里的歧义规则。
func parseDows(s string) (dows []int, rest string) {
	// 先剥「星期/礼拜/周」前缀:「每月第二个周二」里的周几带着「周」字。
	for _, p := range []string{"星期", "礼拜", "周"} {
		if strings.HasPrefix(s, p) {
			s = s[len(p):]
			break
		}
	}
	seen := map[int]bool{}
	i := 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		d := dowOf(r)
		if d < 0 {
			break
		}
		// 阿拉伯数字不在这里吃:交给 parseDowsArabic(它有一条「必须跟分隔符」的规则,
		// 否则无法与时刻区分)。这里一旦遇到数字就停。
		if r >= '0' && r <= '9' {
			break
		}
		seen[d] = true
		i += size
		// 跳过周几之间的分隔符
		for i < len(s) {
			switch {
			case strings.HasPrefix(s[i:], "、"):
				i += len("、")
			case s[i] == ',' || s[i] == '.' || s[i] == ' ':
				i++
			default:
				goto sepDone
			}
		}
	sepDone:
	}
	rest = s[i:]
	for d := 0; d <= 6; d++ {
		if seen[d] {
			dows = append(dows, d)
		}
	}
	// 一个中文字都没读到 → 试试「1,3,5」这种写法。「每周日10点」这种
	// 中文已命中(日)的情形不会走到这里,10 不会被当成周一。
	if len(dows) == 0 {
		if ad, arest, ok := parseDowsArabic(rest); ok {
			return ad, arest
		}
	}
	return dows, rest
}

// parseDowsArabic 解析「1,3,5」「1 3 5」「1、3、5」形式的周几(0 与 7 都是周日)。
//
// 歧义规则(这是它能存在的前提):每个周几必须是**单个数字且后面跟分隔符或结束**。
// 于是「12点」里的 12 不会被拆成两个周几、「1,3,5 9点」里的 9 因为超出 1-7 而
// 自然停下并留作时刻。代价:不支持「每周1 3 5」以外的紧凑写法 —— 但那本来就没人写。
func parseDowsArabic(s string) (dows []int, rest string, ok bool) {
	seen := map[int]bool{}
	i := 0
	for i < len(s) {
		c := s[i]
		if c < '0' || c > '9' {
			break
		}
		d := int(c - '0')
		if d > 7 {
			break // 8/9 不是周几
		}
		// 下一位还是数字 ⇒ 这是个多位数(时刻或日期,如「12点」),不是周几列表
		if i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
			break
		}
		if d == 7 {
			d = 0 // 7 = 周日
		}
		seen[d] = true
		i++
		if i < len(s) {
			switch {
			case s[i] == ',' || s[i] == '.' || s[i] == ' ':
				i++
			case strings.HasPrefix(s[i:], "、"):
				i += len("、")
			default:
				// 后面既不是分隔符也不是结尾 ⇒ 这一串不是周几列表,整个回退失败
				return nil, s, false
			}
		}
	}
	if len(seen) == 0 {
		return nil, s, false
	}
	for d := 0; d <= 6; d++ {
		if seen[d] {
			dows = append(dows, d)
		}
	}
	return dows, s[i:], true
}

// dowOf 单个周几字符 → 0-6(-1 = 不是周几)。0 与 7 都是周日。
func dowOf(r rune) int {
	switch r {
	case '日', '天':
		return 0
	case '一':
		return 1
	case '二':
		return 2
	case '三':
		return 3
	case '四':
		return 4
	case '五':
		return 5
	case '六':
		return 6
	}
	return -1
}

// parseDate 解析一次性目标日期(在频率已剥离之后调用 —— 否则「每周一」的周一会被吃掉)。
func parseDate(s string, now time.Time) (date time.Time, rest string, found bool) {
	t := s
	// 「今年10月1日」:今年只是限定年份(绝对日期缺省年就是今年),剥掉继续解析后面的月日。
	for _, p := range []string{"今年", "本年"} {
		if strings.HasPrefix(t, p) {
			t = t[len(p):]
			break
		}
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	// 今天/明天/后天/大后天
	for _, kv := range []struct {
		keys []string
		off  int
	}{
		{[]string{"大后天"}, 3},
		{[]string{"后天"}, 2},
		{[]string{"明天", "明日"}, 1},
		{[]string{"今天", "今日", "今晚"}, 0},
	} {
		if hit, r := cutAny(t, kv.keys); hit {
			return today.AddDate(0, 0, kv.off), r, true
		}
	}
	// YYYY年M月D日 / YYYY-M-D / M月D日 / M-D
	if d, r, ok := parseAbsoluteDate(t, now); ok {
		return d, r, true
	}
	// (下|本|这)?周X
	rest2 := t
	for _, p := range []string{"下个", "下", "本", "这个", "这"} {
		if strings.HasPrefix(rest2, p) {
			rest2 = rest2[len(p):]
			break
		}
	}
	trimmed := false
	for _, p := range []string{"星期", "礼拜", "周"} {
		if strings.HasPrefix(rest2, p) {
			rest2, trimmed = rest2[len(p):], true
			break
		}
	}
	if trimmed {
		if dows, r := parseDows(rest2); len(dows) == 1 {
			return nthWeekday(today, dows[0]), r, true
		}
	}
	return time.Time{}, s, false
}

// parseAbsoluteDate 「2026年12月20日」「2026-12-20」「12月20日」「12/20」。
// 年份缺省时取 now 的年(传 now 而非 time.Now(),测试才可确定)。
func parseAbsoluteDate(s string, now time.Time) (time.Time, string, bool) {
	rest, year := s, now.Year()
	if i := strings.Index(rest, "年"); i > 0 {
		if y, ok := cnNum(rest[:i]); ok {
			year, rest = y, rest[i+len("年"):]
		}
	}
	i := strings.Index(rest, "月")
	if i <= 0 {
		// 「2026-12-20」式:三段数字
		for _, sep := range []string{"-", "/"} {
			parts := strings.Split(rest, sep)
			if len(parts) != 3 {
				continue
			}
			nums := [3]int{}
			tail := ""
			bad := false
			for k, p := range parts {
				p = strings.TrimSuffix(p, "日")
				p = strings.TrimSuffix(p, "号")
				// 只取前导数字:「05 10点」里的日是 5,后面「 10点」是时刻(该段残留)
				e := digitEnd(p, 0)
				n, ok := cnNum(p[:e])
				if !ok {
					bad = true
					break
				}
				nums[k] = n
				if k == len(parts)-1 {
					tail = strings.TrimSpace(p[e:])
				}
			}
			if bad || nums[1] < 1 || nums[1] > 12 || nums[2] < 1 || nums[2] > 31 {
				continue
			}
			// 三段式第 1 段是年份(2027);「12/20」这种则沿用当前年。
			y := year
			if nums[0] >= 1000 && nums[0] < 10000 {
				y = nums[0]
			}
			return time.Date(y, time.Month(nums[1]), nums[2], 0, 0, 0, 0, now.Location()), tail, true
		}
		return time.Time{}, s, false
	}
	mo, ok := cnNum(rest[:i])
	if !ok {
		return time.Time{}, s, false
	}
	rest = rest[i+len("月"):]
	j := digitEnd(rest, 0)
	if j == 0 {
		return time.Time{}, s, false
	}
	d, ok := cnNum(rest[:j])
	if !ok || mo < 1 || mo > 12 || d < 1 || d > 31 {
		return time.Time{}, s, false
	}
	tail := rest[j:]
	tail = strings.TrimPrefix(tail, "日")
	tail = strings.TrimPrefix(tail, "号")
	return time.Date(year, time.Month(mo), d, 0, 0, 0, 0, now.Location()), tail, true
}

// nthWeekday 从 today 起找**最近**的目标周几(含今天)。
//
// 「下周二」与「周二」在这里是同一个意思,刻意如此:今天周六时,用户说「下周二」要的是
// 后天那个周二,不是 8 天后的。强制「下」= 7-13 天外会明显反直觉(要 8 天后的事却说下周)。
// 代价是「下周二」在口语里可能指下下周 —— 这条歧义交给 UI 回显解决:解析结果必须填进
// 控件让人看见具体日期,而不是显示一句「已识别:下周二」。
func nthWeekday(today time.Time, dow int) time.Time {
	for off := 0; off <= 6; off++ {
		d := today.AddDate(0, 0, off)
		if int(d.Weekday()) == dow {
			return d
		}
	}
	return today
}

// parseTimeOfDay 「早上8点」「下午3点半」「8:30」「9点」「十点」。
func parseTimeOfDay(s string) (h, m int, rest string, found bool) {
	// 8:30 / 08:30
	if i := strings.Index(s, ":"); i > 0 {
		hh, ok1 := cnNum(s[:i])
		tail := s[i+1:]
		j := digitEnd(tail, 0)
		mm, ok2 := cnNum(tail[:j])
		if ok1 && ok2 {
			return hh, mm, tail[j:], true
		}
	}
	// 时段前缀
	shift := 0
	rest = s
	for _, kv := range []struct {
		keys []string
		sh   int
	}{
		{[]string{"凌晨", "半夜"}, 0},
		{[]string{"早上", "早晨", "上午", "清晨"}, 0},
		{[]string{"中午"}, 12}, // 中午1点 = 13:00;中午12点 靠 hh<12 条件不加(仍是 12:00)
		{[]string{"下午", "傍晚"}, 12},
		{[]string{"晚上", "夜里", "深夜", "晚间"}, 12},
	} {
		if hit, r := cutAny(rest, kv.keys); hit {
			shift, rest = kv.sh, r
			break
		}
	}
	// 数字 + 点/时
	i := digitEnd(rest, 0)
	if i == 0 {
		return 0, 0, s, false
	}
	hh, ok := cnNum(rest[:i])
	if !ok {
		return 0, 0, s, false
	}
	rest2 := rest[i:]
	unit := ""
	for _, u := range []string{"点", "时", "："} {
		if strings.HasPrefix(rest2, u) {
			unit = u
			break
		}
	}
	if unit == "" {
		return 0, 0, s, false // 数字后面不是时刻单位(比如「每月2个」不该被读成时刻)
	}
	rest2 = rest2[len(unit):] // 注意:中文单位是 3 字节,按 len(单位) 前进而不是 +1
	// 半 → 30 分
	if strings.HasPrefix(rest2, "半") {
		if hh < 12 && shift == 12 {
			hh += 12
		}
		return hh, 30, rest2[len("半"):], true
	}
	// 8点30 / 8点30分
	j := digitEnd(rest2, 0)
	if j > 0 {
		mm, ok := cnNum(rest2[:j])
		if !ok {
			return 0, 0, s, false
		}
		tail := rest2[j:]
		tail = strings.TrimPrefix(tail, "分")
		tail = strings.TrimPrefix(tail, "秒钟")
		if hh < 12 && shift == 12 {
			hh += 12
		}
		return hh, mm, tail, true
	}
	if hh < 12 && shift == 12 {
		hh += 12
	}
	return hh, 0, rest2, true
}

// cutAny 若串首命中任一关键词,则剥掉它并返回剩余(否则返回原串与 false)。
func cutAny(s string, keys []string) (bool, string) {
	for _, k := range keys {
		if strings.HasPrefix(s, k) {
			return true, strings.TrimPrefix(s, k)
		}
	}
	return false, s
}

// cnDigits 中文数字查表。
//
// 必须查表、**不能用范围判断**:「一二三四五六七八九」的码点并不连续
// (二=U+4E8C > 九=U+4E5D、四=U+56DB > 九),写成 r>='一' && r<='九' 会漏掉二四六八。
var cnDigits = map[rune]int{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4,
	'五': 5, '六': 6, '七': 7, '八': 8, '九': 9}

func isNumRune(r rune) bool {
	if r >= '0' && r <= '9' {
		return true
	}
	if _, ok := cnDigits[r]; ok {
		return true
	}
	return r == '十'
}

// isASCIIDigit 半角数字 0-9(与 isNumRune 区分:后者含中文数字)。
func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }

// runeAt 从下标 i 起解码一个 rune(中文是 3 字节,按字节取会把汉字读成乱码)。
func runeAt(s string, i int) rune {
	if i >= len(s) {
		return -1
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return r
}

// lastRuneBefore 取下标 i 之前那个 rune 与其字节长度。
// 调用方回退时必须用返回的 size 前进 —— 中文是 3 字节,按 1 退会把汉字切成半个字符,
// 下一轮 cnNum 拿到半截字节就失败(症状:「每月十五号」解析不出号数)。
func lastRuneBefore(s string, i int) (rune, int) {
	if i <= 0 {
		return -1, 0
	}
	return utf8.DecodeLastRuneInString(s[:i])
}

// digitEnd 从 i 起连续数字/中文数字字符结束后的下标(按 rune 解码)。
func digitEnd(s string, i int) int {
	for i < len(s) && isNumRune(runeAt(s, i)) {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return i
}

// cnNum 中文/阿拉伯数字 → int。支持 0-99 常用写法(一二三…十、十五、二十三)。
func cnNum(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(s); err == nil { // 纯阿拉伯数字(已含全角归一)
		return n, true
	}
	r := []rune(s)
	// 十 / 十五 / 二十 / 二十三 / 二十五
	if r[0] == '十' {
		v := 10
		if len(r) > 1 {
			d, ok := cnDigits[r[1]]
			if !ok || len(r) > 2 {
				return 0, false
			}
			v += d
		}
		return v, true
	}
	if len(r) == 1 {
		d, ok := cnDigits[r[0]]
		return d, ok
	}
	if len(r) == 2 && r[1] == '十' {
		// 「二十」= 20、「三十」= 30(不带个位);「二十一」才 +1,在下面的三分支里。
		d, ok := cnDigits[r[0]]
		if !ok {
			return 0, false
		}
		return d * 10, true
	}
	if len(r) == 3 && r[1] == '十' {
		// 「二十一」= 21:十位 + 个位,十本身不再加。
		d1, ok1 := cnDigits[r[0]]
		d2, ok2 := cnDigits[r[2]]
		if !ok1 || !ok2 {
			return 0, false
		}
		return d1*10 + d2, true
	}
	return 0, false
}
