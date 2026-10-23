// Package tokencompress 提供 token-compress 插件(M6.5 拆分):会话 token 滚动摘要压缩。
// 从 host-session-log 拆出的独立能力:requires ctx.sessions,经 RegisterCompressor 注入;
// 投影超预算时压缩器把最旧块折叠为累计摘要(session/summary 事件),
// 原始事件保留(留盘完整),投影见"累计摘要 + 最近块"。
// 独立开关(配置 data.token_budget_chars,0 = 关闭)/独立测试/独立演进。
package tokencompress

import (
	"context"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 token-compress。
type Plugin struct{}

// Name 返回插件 id。
func (p *Plugin) Name() string { return "token-compress" }

// Start 读取 data.token_budget_chars 并注册压缩器到 ctx.sessions(host-session-log)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	runner, err := p.start(c, m)
	if err != nil {
		return nil, err
	}
	return func() { runner.close() }, nil
}

// start 装配(与 Start 分开:测试要拿到 runner 做断言)。
func (p *Plugin) start(c sdk.Ctx, m *sdk.Manifest) (*upgradeRunner, error) {
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
	planner := newPlanner(m, budget)
	// 模型摘要升级(可选,批次 D4):折叠当场只落抽取式摘要(确定性、零延迟),
	// 模型摘要在**回合结束后**异步补写(折叠跑在会话日志锁内,不能就地调模型);
	// 补写失败/被抢先 ⇒ 保持抽取式(确定性回退)。详见 upgrade.go。
	opts := upgradeOptions(m)
	runner := newUpgradeRunner(c, sessions, opts.timeout, opts.maxChars(budget))
	runner.enabled = opts.enabled
	if opts.enabled {
		planner.eng.onFold = runner.onFold
	}
	sessions.RegisterCompressor(budget, planner)
	// 回合结束 → 触发升级(不在折叠当场调模型:那会按住会话日志锁)。
	//
	// 订阅的是**会话事件通道**而不是总线事件 "turn/end":后者全仓**从未被 emit 过**
	// (host-session-summary 的「回合后自动概述」就死在这同一处 —— 见交付记录)。
	// 会话事件通道按会话归位(多会话并行时不会把 A 会话的回合结束当成 B 的)。
	runner.dis = c.Subscribe(sdk.EventSession, func(_ context.Context, ev *sdk.Event) error {
		if sev, ok := ev.Payload.(*sdk.SessionEvent); ok && sev.Kind == sdk.EventTurnEnd {
			runner.trigger()
		}
		return nil
	})
	return runner, nil
}

// Engine 滚动摘要引擎(零状态:每次 Fold 从事件流重读累计摘要,幂等)。
// 估算口径与 host-session-log 的 approxChars 一致(中文 1 字 ≈ 1 token)。
type Engine struct {
	foldTarget float64 // 折叠深度:折到 预算×该比例(默认 0.5)
	minFold    float64 // 单次最小折叠量:预算×该比例(默认 1/3)
	// onFold 折叠后的可选回调(把本次折叠的范围交给可替换的摘要器做**事后升级**;
	// nil = 不做模型摘要,只用抽取式)。见 upgrade.go。
	onFold func(FoldScope)
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
func (e *Engine) Fold(evs []sdk.SessionEvent, watermark int, budget int, summary func(sdk.RollingSummary)) int {
	oldSummary := summaryAt(evs)
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
		seg := summarizeEvents(block)
		if seg == "" {
			// 这块里**没有任何可摘要的内容**(只有 turn/start、step/start 这类无载荷标记)。
			//
			// 为什么单独判(2026-10-23,真机 e2e 逮到):水位后的第一个事件常常正是
			// turn/start —— 它 `approxEv` 为 0,`chars >= need` 一次都到不了,于是
			// 整块只有标记 ⇒ 落一条**空摘要**并推进水位。代价有三:
			//   ① 投影里多一条空摘要事件(噪声,且 `recoverWatermark` 会认它);
			//   ② 模型摘要那条通道会拿到 `Merged: ""` 的范围,可能把模型摘要写到
			//      "什么都没覆盖"的水位上(乐观锁的 expected 也对不上语义);
			//   ③ 一次折叠迭代白跑(每轮最多折 maxCompressPerCall 次)。
			// 正确做法:跳过标记(推进水位但不落摘要),继续下一次迭代。
			w = end
			continue
		}
		newSummary := mergeSummary(oldSummary, seg, budget)
		// 摘要事件带**覆盖水位**(本次真正折到的事件下标)与来源:
		// 宿主靠它精确恢复水位(否则重载后水位取"摘要事件自己的下标",会连带跳过
		// 折叠点之后那段没被折叠的事件 —— 实测重启后最后几条消息凭空消失)。
		summary(sdk.RollingSummary{Text: newSummary, Until: end, Source: sdk.SummarySourceExtract})
		// 通知可替换的摘要器:本次折叠的范围(旧摘要 + 新片段)——
		// 它可以**事后**用模型把摘要升级成结构化版本(不在此处调模型:折叠跑在
		// 会话日志锁内)。没有回调 = 纯抽取式(默认行为不变)。
		//
		// 回调为空时**不构造** FoldScope:rawSegment 要把整块原文拼一遍(最多 6KB),
		// 纯抽取式模式下白算(折叠虽少,但没必要)。
		if e.onFold != nil {
			e.onFoldHook(FoldScope{Prev: oldSummary, Seg: rawSegment(block), Merged: newSummary})
		}
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

// summaryAt 最新累计摘要(全流扫描取**最后**一条非空摘要事件)。
//
// **为什么不是"水位内"的最后一条**(2026-10-21 批次 E 修的真缺陷):
// 摘要事件是**追加在日志末尾**的(它的下标 > 水位),所以"只扫水位以内"永远扫不到
// 任何摘要 ⇒ 每次折叠拿到的 oldSummary 都是空 ⇒ 新摘要**不累计**:第二次折叠把
// 第一次折叠的内容整段顶掉,模型视野里只剩最近折进去的那一小块,更早的历史**静默消失**。
//
// 折叠是单调的(水位只前进、摘要按折叠顺序追加),所以"全流最后一条摘要"就是
// 当前水位处的累计摘要 —— 与 host-session-log 的投影口径(projectLocked)一致。
//
// 反向验证:把本函数改回"只扫 watermark 以内",TestRollingSummaryAccumulatesAcrossTurns 必须红。
func summaryAt(evs []sdk.SessionEvent) string {
	s := ""
	for _, ev := range evs {
		if ev.Kind != sdk.EventSummary {
			continue
		}
		if x := sdk.SummaryText(ev.Payload); x != "" {
			s = x
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

// rawSegment 块 → 原始内容转述(每条截断,总量有上限)。
//
// 与 summarizeEvents 的区别:这里给的是**原文**(只截长度,不抽取)——
// 模型摘要的价值就在于它能看到原文;抽取式已经把细节丢了,再拿它喂模型没有意义。
func rawSegment(evs []sdk.SessionEvent) string {
	const (
		perEvent = 800  // 单条上限
		total    = 6000 // 整段上限(与模型摘要的输入预算同量级)
	)
	var b strings.Builder
	for _, ev := range evs {
		if b.Len() >= total {
			break
		}
		var text, who string
		switch p := ev.Payload.(type) {
		case sdk.UserMessage:
			who, text = "用户", p.Content
		case sdk.AssistantMessage:
			who, text = "助手", p.Content
		case sdk.ToolResultEvent:
			who, text = "工具 "+p.Name, p.Content
		default:
			continue
		}
		if text == "" {
			continue
		}
		b.WriteString(who + ": " + clip(text, perEvent) + "\n")
	}
	s := b.String()
	if len([]rune(s)) > total {
		s = clip(s, total)
	}
	return s
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
