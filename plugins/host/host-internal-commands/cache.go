package hostintcmd

// cache.go:/cache 缓存命中率诊断(只读;回答「命中率为什么低」)。
//
// 为什么要它:/context 会报「缓存命中 X(%)」,但那是两个数字相除的**结果**,分不出两种
// 根因,而它们的处置完全相反:
//
//	前缀被改写  ⇒ 改代码(哪一段在动,输出里会指名)
//	前缀稳定但没命中 ⇒ 大概率前缀太短(不足厂商的最小命中块)或缓存过期 ⇒ 改代码没用
//
// 所以这里看的是**前缀指纹**:每次请求取 system + 工具 + 历史的哈希,指纹变了就指出
// 「第几条消息开始不一样」。跑一轮真实会话就能定性 —— 先测再改,别猜。
//
// 零模型请求、零写盘。

import (
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// cmdCache /cache [n] —— n 为显示的最近请求条数(默认 8)。
func (h *Host) cmdCache(args []string) (string, error) {
	limit := 8
	if len(args) > 0 {
		n := 0
		if _, err := fmt.Sscanf(strings.TrimSpace(args[0]), "%d", &n); err == nil && n > 0 {
			if n > 50 {
				n = 50 // 样本上限就是 20,给大了也只是截断;写死上限免得看着像"显示全了"
			}
			limit = n
		}
	}

	var sb strings.Builder
	sb.WriteString("缓存诊断(只读:不发模型请求、不写盘)\n")

	// 第 1 层:累计命中率(与 /context 同源,便于对照)
	var stats sdk.UsageStatsService
	if h.c.Inject("ctx.usageStats", &stats) == nil && stats != nil {
		st := stats.Stats()
		if st.Requests > 0 && st.PromptTokens > 0 {
			pct := st.CachedTokens * 100 / st.PromptTokens
			fmt.Fprintf(&sb, "累计: 输入 %d · 缓存命中 %d(%d%%) · 请求 %d\n",
				st.PromptTokens, st.CachedTokens, pct, st.Requests)
		} else {
			sb.WriteString("累计: 本会话尚无请求\n")
		}
	} else {
		sb.WriteString("累计: 未知(ctx.usageStats 未装配)\n")
	}

	// 第 2 层:前缀指纹序列(这才是"为什么"的答案)
	var probe sdk.PrefixProbe
	if err := h.c.Inject("ctx.prefixProbe", &probe); err != nil || probe == nil {
		sb.WriteString("\n前缀探针: 未装配(host-agent-loop 未加载?)\n")
		return sb.String(), nil
	}
	samples := probe.PrefixSamples(limit)
	if len(samples) == 0 {
		fmt.Fprintf(&sb, "\n前缀探针: 本会话还没有发出过模型请求(共 %d 次)\n", probe.PrefixTurns())
		return sb.String(), nil
	}
	fmt.Fprintf(&sb, "\n前缀指纹(最近 %d 次;共发出 %d 次请求,探针只留最近 20 条)\n",
		len(samples), probe.PrefixTurns())
	for _, s := range samples {
		mark := "·"
		switch s.DiffToPrev {
		case "changed":
			mark = "✗"
		case "same":
			mark = "="
		case "first":
			mark = "※"
		}
		fmt.Fprintf(&sb, " %s #%-3d %s  %s  system %s  消息 %d 条\n",
			mark, s.Seq, s.At, s.Hash, fmtK(s.SysChars), s.MsgCount)
		if s.DiffWhere != "" {
			fmt.Fprintf(&sb, "        ↳ 前缀变化:%s\n", s.DiffWhere)
		}
	}
	sb.WriteString("\n判读:✗ 越多说明前缀每轮都在变 ⇒ 缓存无法复用(按上面指出的那条去修);\n")
	sb.WriteString("      全是 = 而命中率仍低 ⇒ 前缀太短或厂商侧缓存过期,改代码无效,只能减体积。\n")
	return sb.String(), nil
}
