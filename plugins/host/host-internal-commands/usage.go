package hostintcmd

// usage.go:/usage 跨会话 token 用量(2026-10-23)。
//
// 要回答的问题:「这个项目这段时间一共烧了多少 token / 哪几个会话最贵」。
// 既有能力答不了:看板的「用量」卡与 `/context` 都是**当前会话**的账,而 `ctx.usageStats`
// 是进程内累计(重启清零)。权威数据在盘上 —— 每个 `session/usage` 事件一条 ——
// 所以这里做的事就一件:扫会话文件按会话求和(read side = internal/usageindex)。
//
// 三条纪律:只读、不调模型、不说假话(口径必须写在输出里,见 scanScopeNote)。

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/usageindex"
)

// usageDefaultDays 默认时间窗。
//
// 为什么有默认窗而不是"全部":会话目录只增不减,一年后全量扫描是明显的等待
// (本机 146 文件 18MB 约百毫秒,十倍之后就是"卡一下")。默认 30 天覆盖"这个月花了多少"
// 这个最常见的问法,要全部就显式写 `/usage all`。
const usageDefaultDays = 30

// usageTopN 榜单长度(前 N 个会话)。剩下的只在汇总里体现。
const usageTopN = 8

func (h *Host) cmdUsage(args []string) (string, error) {
	days, all, err := parseUsageScope(args)
	if err != nil {
		return "", err
	}
	var since time.Time
	if !all {
		since = time.Now().AddDate(0, 0, -days)
	}
	res, serr := usageindex.Scan("", since)
	if serr != nil {
		return "", errString("扫描会话目录失败: " + serr.Error())
	}
	return usageReport(res), nil
}

// parseUsageScope 解析 `/usage [N|all]`。
func parseUsageScope(args []string) (days int, all bool, err error) {
	days = usageDefaultDays
	if len(args) == 0 {
		return days, false, nil
	}
	a := strings.ToLower(strings.TrimSpace(args[0]))
	if a == "" {
		return days, false, nil
	}
	if a == "all" {
		return days, true, nil
	}
	n, cerr := strconv.Atoi(a)
	if cerr != nil || n <= 0 {
		return 0, false, errString("用法: /usage [天数|all] —— 天数为正整数,all = 不限时间窗")
	}
	return n, false, nil
}

// usageReport 渲染报告(纯函数,便于单测)。
func usageReport(res usageindex.Result) string {
	var sb strings.Builder
	sb.WriteString("跨会话 token 用量" + scanScopeNote(res) + ":\n")
	fmt.Fprintf(&sb, "  请求 %d 次 · 输入 %s · 输出 %s · 合计 %s\n",
		res.Requests, fmtK(res.Prompt), fmtK(res.Complete), fmtK(res.Sum()))
	if res.Prompt > 0 {
		// 缓存命中率只对**输入**有意义(输出 token 从不走缓存)。
		fmt.Fprintf(&sb, "  其中缓存命中输入 %s(%.1f%%)\n",
			fmtK(res.Cached), float64(res.Cached)*100/float64(res.Prompt))
	}
	if len(res.Sessions) == 0 {
		sb.WriteString("\n这段时间没有用量记录(没有发过模型请求的会话不计)。")
		return sb.String()
	}
	sb.WriteString("\n最花 token 的会话:\n")
	top := res.Sessions
	if len(top) > usageTopN {
		top = top[:usageTopN]
	}
	for i, s := range top {
		// 会话名形如 `<项目键>-20261023-101500`:项目键人读起来太长,只留时间戳段,
		// 但**不能只留时间戳** —— 不同项目同一分钟会撞名,而这份榜单可能跨项目。
		fmt.Fprintf(&sb, "  %d. %s  %s(%d 次 · 输入 %s / 输出 %s)\n",
			i+1, usageSessionLabel(s.Session), fmtK(s.Total()), s.Requests,
			fmtK(s.Prompt), fmtK(s.Complete))
	}
	if rest := len(res.Sessions) - len(top); rest > 0 {
		fmt.Fprintf(&sb, "  …另有 %d 个会话(合计见上)\n", rest)
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// usageSessionLabel 会话名 → 一眼能读的标签:「时间戳 · 项目尾」。
//
// 会话名是 `<项目键>-<YYYYMMDD>-<HHMMSS>[-序号]`(项目键由 cwd 逐段连字符拼成,
// 可能很长:`Users-nekoleamo-Documents-Working-go-agent-harness-20261023-101500`)。
// 直接摆原串会把榜单挤成一列看不出差别的长串(裁中间还会切断单词,实测
// `Users-neko…cuments-Working-go-agent-harness` 谁也认不出是哪个项目)。
// 所以:**时间戳拆出来格式化**(榜单靠它辨先后),**项目键只留尾部**(区别性在尾部)。
func usageSessionLabel(name string) string {
	proj, stamp := splitSessionName(name)
	const projKeep = 18
	tail := func(s string) string {
		r := []rune(s)
		if len(r) > projKeep {
			return "…" + string(r[len(r)-projKeep:])
		}
		return s
	}
	if stamp == "" {
		// 没有时间戳段 = 该项目的**主会话**(文件名就是项目键;派生会话才带 `-YYYYMMDD-HHMMSS`)。
		// 标出来,否则榜单里一行长串与别的行看不出区别。
		return "主会话 · " + tail(name)
	}
	if proj == "" {
		return stamp
	}
	return stamp + " · " + tail(proj)
}

// splitSessionName 拆 `<项目键>-<YYYYMMDD>-<HHMMSS>[-序号]`。
func splitSessionName(name string) (proj, stamp string) {
	parts := strings.Split(name, "-")
	n := len(parts)
	// 可选的尾部序号(同分钟撞名时追加,1~2 位数字)
	if n >= 4 && isAllDigits(parts[n-1]) && len(parts[n-1]) <= 2 &&
		isAllDigits(parts[n-2]) && len(parts[n-2]) == 6 &&
		isAllDigits(parts[n-3]) && len(parts[n-3]) == 8 {
		n--
	}
	if n < 3 || !isAllDigits(parts[n-1]) || len(parts[n-1]) != 6 ||
		!isAllDigits(parts[n-2]) || len(parts[n-2]) != 8 {
		return name, ""
	}
	day, clock := parts[n-2], parts[n-1]
	stamp = day[0:4] + "-" + day[4:6] + "-" + day[6:8] + " " + clock[0:2] + ":" + clock[2:4]
	return strings.Join(parts[:n-2], "-"), stamp
}

// isAllDigits 纯数字(空串为 false —— 名字里出现空段就是别的形状了)。
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// scanScopeNote 口径说明。
//
// 为什么必须写出来:时间窗筛的是**会话文件**(按文件最后修改时间),不是单条请求 ——
// 一个最近还在用的会话里含上个月的请求,也会整份计入。不写清就是给出了一个错答案,
// 而"我到底花了多少"这种问题错得越静默越糟。
func scanScopeNote(res usageindex.Result) string {
	if res.Since.IsZero() {
		return "(全部会话文件 · 按会话文件统计)"
	}
	return fmt.Sprintf("(近 %d 天用过/改过的会话文件 · 按会话文件统计,非逐请求时间窗)",
		int(time.Since(res.Since).Hours()/24+0.5))
}
