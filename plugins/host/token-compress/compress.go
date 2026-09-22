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
type Engine struct{}

// maxCompressPerCall 单次 Fold 的最大折叠数(防极端场景死循环)。
const maxCompressPerCall = 20

// Fold 折叠 evs 中水位后的最旧块(不得越过最后一个用户轮,保留最新轮),
// 直至估算投影回预算内;每折一块经 summary 回调落盘累计摘要事件。
// 返回推进后的水位(无可折叠时返回原水位)。
func (e *Engine) Fold(evs []sdk.SessionEvent, watermark int, budget int, summary func(string)) int {
	oldSummary := summaryAt(evs, watermark)
	w := watermark
	for iter := 0; iter < maxCompressPerCall; iter++ {
		if estimate(evs, w, oldSummary) <= budget {
			return w
		}
		need := estimate(evs, w, oldSummary) - budget/2 // 需腾出的字符量(压到约一半预算)
		if need < budget/3 {
			need = budget / 3
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

// mergeSummary 累计摘要:旧 + 新段,限长(预算一半,防摘要自身膨胀)。
func mergeSummary(old, seg string, budget int) string {
	merged := old + seg
	if budget > 0 && len([]rune(merged)) > budget/2 {
		merged = clip(merged, budget/2)
	}
	return merged
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
