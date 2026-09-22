// planner.go:压缩策略(M6.5+/2026-09-22)—— 阈值按**真实 token 占用**与模型窗口比例定,
// 不再用"固定字符预算"当唯一口径。
//
// 为什么改:字符与 token 没有固定关系(本机实测语料中位 **2.8 字符/token**,中文近 1),
// 固定 40960 字符在 200K 窗口上约 7% 使用率就开始丢细节;反过来,"长单轮"里几十次工具调用
// 的尾巴(水位不得越过最后一个用户轮)又能靠字符口径藏住 ~100K token 的占用。
package tokencompress

import (
	"math"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Planner 压缩策略 + 折叠机制(实现 sdk.SessionCompressor 与 sdk.BudgetPlanner)。
// 口径:阈值 = min(窗口 × trigger_ratio, max_tokens)(token)。
//   - 真实占用取 session/usage 的 PromptTokens(host-session-log 每次投影前喂进来);
//     两次请求之间只有投影在长,故 Δtoken ≈ Δ投影字符 / chars_per_token —— 增量口径自动
//     抵消系统提示 + 工具 schema 的固定开销,不必单独估算前缀。
//   - 窗口未知(usage 事件没带窗口快照)时:仍用 max_tokens 作绝对阈值;连实测用量都没有时
//     退回 data.token_budget_chars 的字符口径(与历史行为一致)。
type Planner struct {
	eng Engine // 策略与机制分开:折叠仍用抽取式引擎

	triggerRatio  float64 // data.trigger_ratio:窗口占比阈值(默认 0.8)
	maxTokens     int     // data.max_tokens:阈值上限(token;默认 200000)
	fallbackChars int     // data.token_budget_chars:无实测用量/未启用时的字符预算(0 = 关闭)
	cpt           float64 // 字符/token 起算值(data.chars_per_token,默认 2)

	prevTokens, prevChars int // 上次观测(增量校准用)
}

const (
	defaultTriggerRatio = 0.8
	defaultMaxTokens    = 200000
	defaultCharsPerTok  = 2.0
	minBudgetChars      = 4096 // 预算下限:前缀独自超阈值时尽力而为(折到下限 + 截尾)
)

// Fold 实现 sdk.SessionCompressor(委托抽取式引擎,行为不变)。
func (p *Planner) Fold(evs []sdk.SessionEvent, watermark, budget int, summary func(string)) int {
	return p.eng.Fold(evs, watermark, budget, summary)
}

// Plan 实现 sdk.BudgetPlanner:给出"本轮折到多少字符"与"尾巴截断阈值"。
func (p *Planner) Plan(in sdk.CompressInput) sdk.CompressDecision {
	p.calibrate(in)
	// 溢出兜底(第五十六批):端点已报超窗 ⇒ 估算准不准不再重要,**必须**给出压缩量。
	// 目标取阈值的一半(而不是恰好阈值):折到阈值会下一轮立刻再撞线 —— 估算本来就偏乐观,
	// 而且折完还有固定的系统提示 + 工具 schema 开销没算进去。
	if in.Overflow {
		target := in.ProjectChars / 2 // 无实测用量/窗口未知:保守折一半
		if in.LastPromptTokens > 0 && p.maxTokens > 0 {
			if threshold := p.thresholdTokens(in.Window); threshold > 0 {
				half := threshold / 2
				growth := float64(in.ProjectChars-in.LastProjectChars) / p.cpt
				if growth < 0 {
					growth = 0
				}
				predicted := float64(in.LastPromptTokens) + growth
				target = in.ProjectChars - int(math.Ceil((predicted-float64(half))*p.cpt))
			}
		}
		if target < minBudgetChars {
			target = minBudgetChars
		}
		return sdk.CompressDecision{BudgetChars: target, TrimChars: target}
	}
	if in.LastPromptTokens <= 0 {
		// 尚无实测用量(新会话/首轮):退回字符口径,不猜。
		return sdk.CompressDecision{BudgetChars: p.fallbackChars}
	}
	threshold := p.thresholdTokens(in.Window)
	if threshold <= 0 {
		return sdk.CompressDecision{BudgetChars: p.fallbackChars}
	}
	// 预测本轮 prompt token = 上次实测 + 投影增量换算(折叠过的轮次增长为负,按 0 计)。
	growth := float64(in.ProjectChars-in.LastProjectChars) / p.cpt
	if growth < 0 {
		growth = 0
	}
	predicted := float64(in.LastPromptTokens) + growth
	if predicted < float64(threshold) {
		return sdk.CompressDecision{} // 未到阈值:本轮不压
	}
	// 折到阈值(引擎内部再折到预算一半 ⇒ 实际落到约阈值一半,天然滞回,不会每轮都折)。
	budget := in.ProjectChars - int(math.Ceil((predicted-float64(threshold))*p.cpt))
	if budget < minBudgetChars {
		budget = minBudgetChars
	}
	// 同一个字符预算兼作尾巴截断阈值:折叠够不着当前轮,截断是长单轮的唯一杠杆。
	return sdk.CompressDecision{BudgetChars: budget, TrimChars: budget}
}

// thresholdTokens 阈值 = min(窗口 × 比例, 上限);窗口未知 → 只用上限(用户设的绝对线)。
func (p *Planner) thresholdTokens(window int) int {
	t := p.maxTokens
	if window > 0 && p.triggerRatio > 0 {
		if w := int(float64(window) * p.triggerRatio); w > 0 && (t <= 0 || w < t) {
			t = w
		}
	}
	return t
}

// calibrate 用相邻两次"真实用量 + 投影字符"的增量校准字符/token:语料差异大(中文近 1、
// 代码/英文约 2.8),固定换算值必然偏;夹紧到 [0.4, 8] 防离群样本带偏。
func (p *Planner) calibrate(in sdk.CompressInput) {
	if p.prevTokens > 0 && in.LastPromptTokens > p.prevTokens && in.LastProjectChars > p.prevChars {
		dt := in.LastPromptTokens - p.prevTokens
		dc := in.LastProjectChars - p.prevChars
		if dt >= 256 {
			if r := float64(dc) / float64(dt); r >= 0.4 && r <= 8 {
				p.cpt = 0.5*p.cpt + 0.5*r
			}
		}
	}
	p.prevTokens, p.prevChars = in.LastPromptTokens, in.LastProjectChars
}

// newPlanner 从 manifest data 构造策略(缺省见常量;值非法按缺省,不静默当 0)。
func newPlanner(m *sdk.Manifest, fallbackChars int) *Planner {
	p := &Planner{
		triggerRatio:  defaultTriggerRatio,
		maxTokens:     defaultMaxTokens,
		fallbackChars: fallbackChars,
		cpt:           defaultCharsPerTok,
	}
	if m == nil || m.Data == nil {
		return p
	}
	if v := numData(m, "trigger_ratio", 0); v > 0 {
		p.triggerRatio = v
	}
	if v := numData(m, "chars_per_token", 0); v > 0 {
		p.cpt = v
	}
	if v := int(numData(m, "max_tokens", 0)); v > 0 {
		p.maxTokens = v
	}
	return p
}

// numData 读数值型配置(YAML 整数解成 int、小数解成 float64,两种都收)。
func numData(m *sdk.Manifest, key string, def float64) float64 {
	switch v := m.Data[key].(type) {
	case int:
		return float64(v)
	case float64:
		return v
	}
	return def
}
