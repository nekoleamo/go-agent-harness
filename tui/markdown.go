// TUI S1.4 Markdown 轻渲染:assistant 会话行按物理行做 token 级着色
// (粗体 **x** / 行内 code `x` / 标题行 # / 列表前缀),其余原样。
//
// 约束:
//   - 只做"轻渲染":折行之后按物理行独立着色(跨物理行 token 不跨行配对);
//   - 每段独立 lipgloss.Render(自带 SGR 开头 + reset 收尾)后拼接——避免把
//     ANSI 文本再整体包进外层 style(内嵌 reset 会清掉外层前景色);
//   - 不移动任何 rune(着色段与原文本字符一一对应),物理宽度 = lipgloss 感知,
//     选区/搜索高亮在含色文本上不叠加(render.go 先裁决降级路径);
//   - 代码围栏行(```)不着色防误伤,围栏内行不识别 token(轻渲染取舍)。
package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// markdown 着色调色板(256 色,与既有 styleForKind 风格统一)。
var (
	mdCodeFg  = lipgloss.Color("179") // 行内 code:暗金
	mdBoldFg  = lipgloss.Color("231") // 粗体:亮白(粗体 + 提亮)
	mdTitleFg = lipgloss.Color("51")  // 标题:青
	mdListFg  = lipgloss.Color("220") // 列表符:琥珀
	mdHrFg    = lipgloss.Color("245") // 分隔线(---/***):灰
)

// mdAnnotateRow 把单个物理行文本按 markdown token 分段着色,返回含 SGR 文本。
// baseFg 为普通段前景色(调用方传入该行 kind 的基础色);无 token 时返回整段 base 色。
func mdAnnotateRow(text string, baseFg color.Color) string {
	trim := strings.TrimSpace(text)
	// 代码围栏 ``` / ~~~ 行:不着色(防代码块正文被误当 markdown)。
	if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
		return mdSeg(text, baseFg, false)
	}
	// 分隔线 --- / *** / ___ (仅由这些构成):统一灰,弱化占屏感。
	if isHrLine(trim) {
		return mdSeg(text, mdHrFg, false)
	}
	// 标题行 #..###### :整行标题色 + 粗体。
	if isTitleLine(trim) {
		// 保留原行文本(含缩进),只整体换色提亮;行首 # 前缀不动(rune 一致)。
		return mdSeg(text, mdTitleFg, true)
	}
	// 列表行(纯文本前缀着色,列表符改色,后续正文走通用 token 解析)。
	if isListLine(trim) {
		return mdListRow(text, baseFg)
	}
	return mdTokens(text, baseFg)
}

// mdSeg 渲染一段文本为单色段(粗体可选)。空文本返回空串。
func mdSeg(text string, fg color.Color, bold bool) string {
	if text == "" {
		return ""
	}
	st := lipgloss.NewStyle().Foreground(fg)
	if bold {
		st = st.Bold(true)
	}
	return st.Render(text)
}

// isTitleLine 行首(去空白)为 1-6 个 # 后随空格。
func isTitleLine(trim string) bool {
	i := 0
	for i < len(trim) && trim[i] == '#' && i < 6 {
		i++
	}
	return i > 0 && i < len(trim) && trim[i] == ' '
}

// isHrLine 行完全由 - / * / _ 组成(≥3 个,可含空白)。
func isHrLine(trim string) bool {
	if len(trim) < 3 {
		return false
	}
	kind := byte(0)
	for i := 0; i < len(trim); i++ {
		c := trim[i]
		if c == ' ' || c == '\t' {
			continue
		}
		if c != '-' && c != '*' && c != '_' {
			return false
		}
		if kind == 0 {
			kind = c
		} else if c != kind {
			return false // 混合符号不当作分隔线(如 "- item" 前导)
		}
	}
	return true
}

// isListLine 行首(去空白)为 "- " "* " "+ " 或 "N. " 列表符。
func isListLine(trim string) bool {
	if len(trim) < 2 {
		return false
	}
	switch {
	case strings.HasPrefix(trim, "- "), strings.HasPrefix(trim, "* "),
		strings.HasPrefix(trim, "+ "):
		return true
	}
	// "1. " "10. " 编号列表
	for i := 0; i < len(trim) && i < 8; i++ {
		if trim[i] < '0' || trim[i] > '9' {
			return i > 0 && trim[i] == '.' && i+1 < len(trim) && trim[i+1] == ' '
		}
	}
	return false
}

// mdListRow 列表行:列表符(marker)改色,正文从通用 token 解析继续着色。
func mdListRow(text string, baseFg color.Color) string {
	trimIdx := strings.IndexFunc(text, func(r rune) bool { return r != ' ' && r != '\t' })
	if trimIdx < 0 {
		return mdSeg(text, baseFg, false)
	}
	rest := text[trimIdx:]
	// 找 marker 结束(首个空格后为正文;保持 rune 位置不变,仅按字节切分 ASCII marker)。
	sp := strings.IndexByte(rest, ' ')
	if sp < 0 {
		sp = len(rest)
	}
	marker, body := rest[:sp+1], rest[sp+1:]
	var b strings.Builder
	if trimIdx > 0 {
		b.WriteString(mdSeg(text[:trimIdx], baseFg, false))
	}
	b.WriteString(mdSeg(marker, mdListFg, true))
	b.WriteString(mdTokens(body, baseFg))
	return b.String()
}

// mdTokens 行内 token 着色:单遍成对扫描——遇 `…` code 段、**…** 粗体段,
// 未配对的 ` / * 作为普通字符原样输出(字符无损,不猜测)。code 优先于 bold
// (code 内 ** 不二次解释);bold 段内不再细分(轻渲染取舍)。
func mdTokens(text string, baseFg color.Color) string {
	var b strings.Builder
	segStart := 0
	flushPlain := func(upTo int) {
		if upTo > segStart {
			b.WriteString(mdSeg(text[segStart:upTo], baseFg, false))
		}
	}
	for i := 0; i < len(text); {
		switch {
		case text[i] == '`':
			if j := strings.IndexByte(text[i+1:], '`'); j >= 0 {
				flushPlain(i)
				b.WriteString(mdCode(text[i+1 : i+1+j]))
				i += j + 2
				segStart = i
				continue
			}
			i++
		case text[i] == '*' && i+1 < len(text) && text[i+1] == '*':
			if j := strings.Index(text[i+2:], "**"); j >= 0 {
				flushPlain(i)
				b.WriteString(mdSeg(text[i+2:i+2+j], mdBoldFg, true))
				i += j + 4
				segStart = i
				continue
			}
			i += 2 // 未配对 ** :按普通推进(字符仍会进 plain)
		default:
			i++
		}
	}
	flushPlain(len(text))
	return b.String()
}

// mdCode 行内 code 段着色:不含反引号(成对标记被消费,阅读更干净),code 色区分。
func mdCode(inner string) string {
	return lipgloss.NewStyle().Foreground(mdCodeFg).Render(inner)
}
