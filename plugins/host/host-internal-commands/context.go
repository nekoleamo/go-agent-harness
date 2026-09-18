package hostintcmd

// context.go:/context 上下文占用分解视图(S-P0-4,对标 Hermes Agent /context)。
//
// 输出严格分两层,不混报(纪律:估算绝不当真实值展示):
//  1. 真实 token —— ctx.usageStats 的累计口径(与 TUI 底部指标行 / Web 用量同源);
//  2. 本地估算 —— 系统提示(组装后的真串)、工具定义 schema、会话投影历史按
//     (rune 数, 字节数) 粗估 token。
//
// 零模型请求、零外部依赖、零写盘:纯只读诊断。

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// estTokens 由 (rune 数, 字节数) 粗估 token。
//
// 依据:UTF-8 下 3 字节字符(CJK 等)≈ 1 token/字,ASCII ≈ 4 字符/token。
// 由 runes/bytes 反推宽字符数 W =(bytes-runes)/2、ASCII 数 A = runes-W
// (对 3 字节字符精确,对 2 字节字符略偏保守)。仅作量级参考,调用方必须标「估算」。
func estTokens(runes, byteLen int) int {
	if runes <= 0 {
		return 0
	}
	wide := (byteLen - runes) / 2
	if wide < 0 {
		wide = 0
	}
	if wide > runes {
		wide = runes
	}
	ascii := runes - wide
	return wide + (ascii+3)/4
}

// estTokensStr 直接对字符串估算。
func estTokensStr(s string) int {
	return estTokens(utf8.RuneCountInString(s), len(s))
}

// fmtK 数值短格式(>=1000 用 k/M,保留一位小数;仅展示用)。
func fmtK(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1000000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1000000)
}

// contextBar 生成占比条(20 格;超 100% 全满)。
func contextBar(used, total int) string {
	const cells = 20
	if total <= 0 {
		return strings.Repeat("░", cells)
	}
	filled := used * cells / total
	if filled > cells {
		filled = cells
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", cells-filled)
}

// cmdContext 上下文占用分解。args[0] == "all" 时展开逐工具 schema 成本。
func (h *Host) cmdContext(args []string) (string, error) {
	all := len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "all")

	tools := []sdk.ToolDefinition{}
	var reg sdk.ToolRegistry
	hasTools := h.c.Inject("ctx.tools", &reg) == nil && reg != nil
	if hasTools {
		tools = reg.List()
	}

	var sb strings.Builder
	totalEst := 0

	// —— 第 1 层:真实 token(usageStats 累计口径) ——
	var stats sdk.UsageStatsService
	if h.c.Inject("ctx.usageStats", &stats) != nil || stats == nil {
		// 未装配也输出窗口行(与 TUI 指标行的「上下文 -」同口径:显式表示未知,不留空白)
		sb.WriteString("上下文窗口: 未知(ctx.usageStats 未装配 → host-usage-stats 未加载)\n")
		sb.WriteString("累计: 未知\n")
	} else {
		st := stats.Stats()
		window := st.Window
		unknown := window <= 0
		if unknown {
			window = 65536 // 与 sdk.UsageStats.Window 文档默认口径一致
		}
		src := "模型窗口表/配置"
		if unknown {
			src = "未识别模型 → 默认值"
		}
		fmt.Fprintf(&sb, "上下文窗口: %d token(%s)\n", window, src)
		if st.Requests > 0 {
			used := st.PromptTokens
			cache := 0
			if st.PromptTokens > 0 {
				cache = st.CachedTokens * 100 / st.PromptTokens
			}
			fmt.Fprintf(&sb, "占用: %s/%s(%d%%)  %s\n", fmtK(used), fmtK(window), used*100/window, contextBar(used, window))
			fmt.Fprintf(&sb, "累计: 输入 %d · 输出 %d · 缓存命中 %d(%d%%) · 请求 %d\n",
				st.PromptTokens, st.CompletionTokens, st.CachedTokens, cache, st.Requests)
		} else {
			sb.WriteString("累计: 本会话尚无请求(占用待首轮对话后统计)\n")
		}
	}

	// —— 第 2 层:本地估算 ——
	sb.WriteString("\n—— 本地上限估算(不发模型请求;非真实计费值)——\n")

	var sp sdk.SystemPromptService
	if h.c.Inject("ctx.systemPrompt", &sp) == nil && sp != nil {
		assembled := sp.Assemble(nil, tools)
		sysText := ""
		if len(assembled) > 0 {
			sysText = assembled[0].Content
		}
		sysEst := estTokensStr(sysText)
		totalEst += sysEst
		fmt.Fprintf(&sb, "系统提示(组装后真串): %s ≈ %s token\n", fmtK(utf8.RuneCountInString(sysText)), fmtK(sysEst))
		if insp, ok := sp.(sdk.SystemPromptInspector); ok {
			for _, p := range insp.Breakdown(tools) {
				fmt.Fprintf(&sb, "    %-36s %7s 字符 ≈ %6s\n", p.Label, fmtK(p.Chars), fmtK(estTokens(p.Chars, p.Bytes)))
			}
		}
	} else {
		sb.WriteString("系统提示: ctx.systemPrompt 未装配\n")
	}

	if hasTools {
		if blob, err := json.Marshal(tools); err == nil {
			est := estTokensStr(string(blob))
			totalEst += est
			fmt.Fprintf(&sb, "工具定义 schema(%d 个):%s ≈ %s token\n", len(tools), fmtK(len(blob)), fmtK(est))
		}
	}

	var sess sdk.SessionLog
	if h.c.Inject("ctx.sessions", &sess) == nil && sess != nil {
		msgs := sess.DeriveMessages()
		chars, bytes := 0, 0
		for _, m := range msgs {
			if m.Role == sdk.RoleSystem {
				continue // 系统提示已单列,避免重复计
			}
			chars += utf8.RuneCountInString(m.Content)
			bytes += len(m.Content)
		}
		est := estTokens(chars, bytes)
		totalEst += est
		fmt.Fprintf(&sb, "会话投影历史: %d 条消息 / %s 字符 ≈ %s token\n", len(msgs), fmtK(chars), fmtK(est))
		fmt.Fprintf(&sb, "历史事件帧: %d(压缩后进入投影的即上一行)\n", len(sess.Replay()))
	} else {
		sb.WriteString("会话历史: ctx.sessions 未装配\n")
	}

	// 下一轮请求的本地上限 ≈ 系统提示 + 工具 schema + 投影历史
	if totalEst > 0 {
		fmt.Fprintf(&sb, "\n下一轮请求本地估算下限: ≈ %s token(上三层之和;不含回答)\n", fmtK(totalEst))
	}

	if all {
		sb.WriteString("\n—— 逐工具 schema 成本(/context all)——\n")
		if hasTools {
			type row struct {
				name  string
				chars int
				est   int
			}
			rows := make([]row, 0, len(tools))
			for _, t := range tools {
				blob, err := json.Marshal(t)
				if err != nil {
					continue
				}
				rows = append(rows, row{name: t.Name, chars: len(blob), est: estTokensStr(string(blob))})
			}
			for i := 1; i < len(rows); i++ { // 冒泡:工具数小,免为一次诊断引 sort
				for j := i; j > 0 && rows[j].est > rows[j-1].est; j-- {
					rows[j], rows[j-1] = rows[j-1], rows[j]
				}
			}
			for _, r := range rows {
				fmt.Fprintf(&sb, "    %-36s %7s 字符 ≈ %6s\n", r.name, fmtK(r.chars), fmtK(r.est))
			}
			if len(rows) == 0 {
				sb.WriteString("    (无工具)\n")
			}
		} else {
			sb.WriteString("    (ctx.tools 未装配)\n")
		}
	}

	sb.WriteString("\n口径:估算 = ASCII 4 字符/token、非 ASCII 1 字符/token;/context all 展开逐工具成本。")
	return sb.String(), nil
}
