// Package tokencompress 提供 token-compress 插件(M6.5 拆分):会话 token 滚动摘要压缩。
// 从 host-session-log 拆出的独立能力:requires ctx.sessions,经 RegisterCompressor 注入;
// 投影超预算时压缩器把最旧块折叠为累计摘要(session/summary 事件),
// 原始事件保留(留盘完整),投影见"累计摘要 + 最近块"。
// 独立开关(配置 data.token_budget_chars,0 = 关闭)/独立测试/独立演进。
package tokencompress

import (
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 token-compress。
type Plugin struct{}

// Name 返回插件 id。
func (p *Plugin) Name() string { return "token-compress" }

// Start 读取 data.token_budget_chars 并注册压缩器到 ctx.sessions(host-session-log)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		return nil, err
	}
	budget := 0
	if m != nil && m.Data != nil {
		if b, ok := m.Data["token_budget_chars"].(int); ok && b > 0 {
			budget = b
		}
	}
	// 策略(阈值口径)与预算一起注册:压缩器实现了 sdk.BudgetPlanner,故 host-session-log
	// 每轮投影前会先问 Plan(见 planner.go);budget 仍作为"无实测用量/未启用"时的回落口径。
	sessions.RegisterCompressor(budget, newPlanner(m, budget))
	return func() {}, nil
}

// Engine 滚动摘要引擎(零状态:每次 Fold 从事件流重读累计摘要,幂等)。
// 估算口径与 host-session-log 的 approxChars 一致(中文 1 字 ≈ 1 token)。
type Engine struct {
	foldTarget float64 // 折叠深度:折到 预算×该比例(默认 0.5)
	minFold    float64 // 单次最小折叠量:预算×该比例(默认 1/3)
}

// maxCompressPerCall 单次 Fold 的最大折叠数(防极端场景死循环)。
const maxCompressPerCall = 20

// 折叠深度的缺省比例(F7:显式参数化 —— "少折、深折"的账写在配置里,不埋在代码里)。
//
// 为什么折到预算的一半而不是刚好折到预算:折到预算 ⇒ 下一轮立刻又撞线 ⇒ 每轮都折
// (每次折叠都改写历史前缀 ⇒ 缓存每轮失效)。折到一半 ⇒ 有滞回,通常隔几轮才折一次。
//
// 为什么单次折叠量有下限(预算的 1/3):折叠太小的块,摘要本身的开销可能比折掉的还多
// (摘要也要进上下文),纯亏。
const (
	defaultFoldTarget = 0.5
	defaultMinFold    = 1.0 / 3.0
)

// Fold 折叠 evs 中水位后的最旧块(不得越过最后一个用户轮,保留最新轮),
// 直至估算投影回预算内;每折一块经 summary 回调落盘累计摘要事件。
// 返回推进后的水位(无可折叠时返回原水位)。
func (e *Engine) Fold(evs []sdk.SessionEvent, watermark int, budget int, summary func(string)) int {
	oldSummary := summaryAt(evs, watermark)
	w := watermark
	foldTarget, minFold := e.foldTarget, e.minFold
	if foldTarget <= 0 {
		foldTarget = defaultFoldTarget
	}
	if minFold <= 0 {
		minFold = defaultMinFold
	}
	for iter := 0; iter < maxCompressPerCall; iter++ {
		if estimate(evs, w, oldSummary) <= budget {
			return w
		}
		need := estimate(evs, w, oldSummary) - int(float64(budget)*foldTarget) // 折到 预算×foldTarget
		if minNeed := int(float64(budget) * minFold); need < minNeed {
			need = minNeed // 单次最小折叠量(折太少不划算)
		}
		// 取水位后的原始事件块,累计到 need;块不得越过最后一个用户轮
		start := w + 1
		lastUser := lastUserIndex(evs)
		chars := 0
		end := start - 1
		for i := start; i < len(evs) && i < lastUser; i++ {
			ev := evs[i]
			if ev.Kind == sdk.EventSummary {
				continue
			}
			chars += approxEv(ev)
			end = i
			if chars >= need {
				break
			}
		}
		if end < start {
			return w // 无可压缩的普通消息
		}
		block := evs[start : end+1]
		newSummary := mergeSummary(oldSummary, summarizeEvents(block), budget)
		summary(newSummary) // 落盘 session/summary(host-session-log 锁内追加)
		oldSummary = newSummary
		w = end
	}
	return w
}

// estimate 估算投影字符:累计摘要 + 水位后的未压缩块。
func estimate(evs []sdk.SessionEvent, watermark int, summary string) int {
	n := len([]rune(summary))
	for i := watermark + 1; i < len(evs); i++ {
		n += approxEv(evs[i])
	}
	return n
}

// summaryAt 水位内最新累计摘要(EventSummary 载荷为字符串)。
func summaryAt(evs []sdk.SessionEvent, watermark int) string {
	s := ""
	for i := 0; i <= watermark; i++ {
		if evs[i].Kind == sdk.EventSummary {
			if x, ok := evs[i].Payload.(string); ok && x != "" {
				s = x
			}
		}
	}
	return s
}

// approxEv 单个事件的字符估算。
func approxEv(ev sdk.SessionEvent) int {
	switch p := ev.Payload.(type) {
	case sdk.UserMessage:
		return len([]rune(p.Content))
	case sdk.AssistantMessage:
		return len([]rune(p.Content))
	case sdk.ToolResultEvent:
		return len([]rune(p.Content))
	default:
		return 0
	}
}

// summarizeEvents 块 → 抽取式摘要(用户问/助手答/工具结果,逐条截断)。
func summarizeEvents(evs []sdk.SessionEvent) string {
	var b strings.Builder
	for _, ev := range evs {
		switch p := ev.Payload.(type) {
		case sdk.UserMessage:
			b.WriteString("- 用户: " + clip(p.Content, 120) + "\n")
		case sdk.AssistantMessage:
			if p.Content != "" {
				b.WriteString("- 助手: " + clip(p.Content, 200) + "\n")
			}
		case sdk.ToolResultEvent:
			if p.Name != "" {
				b.WriteString("- 工具 " + p.Name + ": " + clip(p.Content, 120) + "\n")
			}
		}
	}
	return b.String()
}

// lastUserIndex 最后一个用户消息索引(最新轮起点,压缩块不得越过;无则 -1)。
func lastUserIndex(evs []sdk.SessionEvent) int {
	last := -1
	for i, ev := range evs {
		if ev.Kind == sdk.EventUserMessage {
			last = i
		}
	}
	return last
}

// mergeSummary 累计摘要:老段 + 新段,限长(预算一半,防摘要自身膨胀)。
//
// **新段必留、老段让位**(2026-10-20 优化批次 A2 修的真缺陷)。
// 原实现是 `clip(merged, budget/2)` —— clip 取**头部**,于是摘要一旦长到预算一半,
// 此后每次折叠**新折进来的那段直接消失**,摘要永久停在最老的内容上;会话越长,
// 「模型看到的历史摘要」与「实际历史」偏离越大,而且是**静默**的。
//
// 现在的口径(两端各留一头,中间丢):
//
//	头部  = 累计摘要最早的 limit/3 字符(**钉住**最初的任务陈述,别让它被滑掉);
//	尾部  = 累计摘要最新的剩余部分(**必含**新折进来的那段);
//	中间  = 省略,并标 [早期内容已省略] —— 免得模型误以为摘要是完整的。
//
// 之所以两端都留而不是只留尾部:只留尾部等于把「最初要做什么」滑出窗口(老),
// 只留头部等于把「刚做完什么」滑掉(新,原实现就是这个坑);两端各留是折中。
//
// 反向验证:把本函数改回 `clip(merged, budget/2)`,TestMergeSummaryKeepsNewSegment 必须红。
func mergeSummary(old, seg string, budget int) string {
	merged := old + seg
	limit := budget / 2
	if limit <= 0 {
		return merged
	}
	r := []rune(merged)
	if len(r) <= limit {
		return merged
	}
	const mark = "[早期内容已省略]"
	headKeep := limit / 3
	tailKeep := limit - headKeep - len([]rune(mark))
	if tailKeep < 0 {
		// 预算太小(极小 budget)⇒ 退化为只留尾部,保证新段一定在。
		tailKeep = limit
		headKeep = 0
	}
	head := ""
	if headKeep > 0 {
		head = string(r[:headKeep])
	}
	tail := string(r[len(r)-tailKeep:])
	if headKeep > 0 {
		return head + mark + tail
	}
	return tail
}

// clip 按 rune 截断并加省略号。
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 3 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
