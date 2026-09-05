// TUI S1.4+ Markdown 轻渲染:assistant 会话行按物理行做 token 级着色
// (粗体 **x** / 行内 code `x` / 标题行 # / 列表前缀),其余原样。
//
// 约束:
//   - 只做"轻渲染":折行之后按物理行独立着色(跨物理行 token 不跨行配对);
//   - 每段独立 lipgloss.Render(自带 SGR 开头 + reset 收尾)后拼接——避免把
//     ANSI 文本再整体包进外层 style(内嵌 reset 会清掉外层前景色);
//   - 不移动任何 rune(着色段与原文本字符一一对应),物理宽度 = lipgloss 感知,
//     选区/搜索高亮在含色文本上不叠加(render.go 先裁决降级路径);
//   - 代码围栏(P4-3):fence 行与块内行由 render 层跨行状态判定(inCode/codeFence),
//     整体代码色 + 极简语法着色(字符串/注释/关键字),块内不做 md token 解释;
//     折叠/搜索/选区命中行回落纯文本(render.go 先裁决)。
package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// markdown 着色调色板(token 化,P4-9):颜色一律经 palette.go 的 fg() 派生,
// 本文件零色值字面量;默认表值即既有 256 色基线。
// mdAnnotateRow 把单个物理行文本按 markdown token 分段着色,返回含 SGR 文本。
// baseFg 为普通段前景色(调用方传入该行 kind 的基础色);无 token 时返回整段 base 色。
// 代码围栏行/块内行不由此处理(render 层经 codeFence/inCode 路由到 mdCodeBlockRow)。
func mdAnnotateRow(text string, baseFg color.Color) string {
	trim := strings.TrimSpace(text)
	// 代码围栏行(``` / ~~~ 起头):整行 base 色防防误消费(render 层已路由到
	// mdCodeBlockRow;此处为直接调用/未标注路径的防御——反引号成对误吞块标记)。
	if _, ok := mdFenceInfo(trim); ok {
		return mdSeg(text, baseFg, false)
	}
	// 分隔线 --- / *** / ___ (仅由这些构成):统一灰,弱化占屏感。
	if isHrLine(trim) {
		return mdSeg(text, fg(TokMdHr), false)
	}
	// 标题行 #..###### :整行标题色 + 粗体。
	if isTitleLine(trim) {
		// 保留原行文本(含缩进),只整体换色提亮;行首 # 前缀不动(rune 一致)。
		return mdSeg(text, fg(TokMdTitle), true)
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
	b.WriteString(mdSeg(marker, fg(TokMdList), true))
	b.WriteString(mdTokens(body, baseFg))
	return b.String()
}

// —— 代码围栏(P4-3):fence 状态机与块内极简语法着色 ——

// mdFenceInfo 判定 trim 行是否为代码围栏行(``` 或 ~~~ 起头,≥3 标记)。
// 返回围栏语言(info 串首个词,如 ```go → go;纯 ``` → 空)。
// 围栏行本身是打开还是关闭由调用方状态机决定(render 层逐行扫描)。
func mdFenceInfo(trim string) (lang string, ok bool) {
	for _, m := range []string{"```", "~~~"} {
		if strings.HasPrefix(trim, m) {
			info := strings.TrimSpace(trim[len(m):])
			if w := strings.Fields(info); len(w) > 0 {
				return strings.ToLower(w[0]), true
			}
			return "", true
		}
	}
	return "", false
}

// mdCodeBlockRow 渲染代码块一行:统一代码前景色 + 极简语法着色。
// 单遍状态扫描(字符串→注释→关键字→普通),字符无损(rune 不变,只插 SGR);
// 不做跨行配对(字符串/注释行内闭合即可,未闭合按行尾止——轻渲染取舍)。
// fence 行(```/~~~ 本身,lang 为空进入)同样按普通代码行着色,便于块边缘识别。
// 含 \n 的输入按行拆分逐段处理(render 层逐物理行已无换行;此为直接调用防御)。
func mdCodeBlockRow(text string, lang string) string {
	if strings.IndexByte(text, '\n') < 0 {
		return mdCodeBlockLine(text, lang)
	}
	parts := strings.Split(text, "\n")
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(mdCodeBlockLine(p, lang))
	}
	return b.String()
}

// mdCodeBlockLine 单行(无 \n)代码着色扫描。
func mdCodeBlockLine(text string, lang string) string {
	if text == "" {
		return ""
	}
	base := fg(TokMdCode)
	// 该语言的注释标记集(按相邻语言族近似;无语言只认 //,防 ``` 包裹的普通文本误伤)。
	markers := []string{"//"}
	switch lang {
	case "python", "py", "sh", "bash", "zsh", "shell", "yaml", "yml", "toml",
		"ini", "ruby", "rb", "perl", "pl", "make", "makefile", "docker", "dockerfile", "fish":
		markers = append(markers, "#")
	case "sql", "mysql", "postgres", "postgresql", "lua", "vim", "conf":
		markers = append(markers, "--")
	}
	var b strings.Builder
	flush := func(seg string, c color.Color) {
		if seg != "" {
			b.WriteString(mdSeg(seg, c, false))
		}
	}
	plain := func(i0, i1 int) { flush(text[i0:i1], base) }

	i := 0
	segStart := 0
	for i < len(text) {
		c := text[i]
		// 注释(行内从标记到行尾;要求标记前为行首/空白,避免 URL:// 等误判)
		if (c == '/' && i+1 < len(text) && text[i+1] == '/') ||
			(c == '#' && hasStr(markers, "#")) ||
			(c == '-' && i+1 < len(text) && text[i+1] == '-' && hasStr(markers, "--")) {
			if i == 0 || text[i-1] == ' ' || text[i-1] == '\t' {
				plain(segStart, i)
				flush(text[i:], fg(TokMdCmt))
				segStart = len(text)
				break
			}
		}
		// 字符串:成对 ' " 到行尾未闭合则整段按字符串色(不跨行),\ 转义跳下一个字符
		if c == '"' || c == '\'' {
			j := i + 1
			for j < len(text) {
				if text[j] == '\\' && j+1 < len(text) {
					j += 2
					continue
				}
				if text[j] == c {
					break
				}
				j++
			}
			if j < len(text) { // 闭合
				plain(segStart, i)
				flush(text[i:j+1], fg(TokMdStr))
				segStart = j + 1
				i = j + 1
				continue
			}
			// 未闭合:本行余下整体按字符串色(可读优先)
			plain(segStart, i)
			flush(text[i:], fg(TokMdStr))
			segStart = len(text)
			break
		}
		// 关键字:整词匹配(字母/下划线开头,数字续)
		if isWordStart(c) {
			j := i + 1
			for j < len(text) && isWordChar(text[j]) {
				j++
			}
			if mdKeywords[text[i:j]] {
				plain(segStart, i)
				flush(text[i:j], fg(TokMdKey))
				segStart = j
			}
			i = j
			continue
		}
		i++
	}
	plain(segStart, len(text))
	return b.String()
}

// isWordStart 词首字节(ASCII 标识符;UTF-8 多字节一律按普通字符,不参与关键字匹配)。
func isWordStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isWordChar 词内字节(ASCII)。
func isWordChar(c byte) bool {
	return isWordStart(c) || (c >= '0' && c <= '9')
}

// hasStr 切片是否含目标串。
func hasStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// mdKeywords 常见语言关键字(与语言无关的通用词表;大小写精确)。
var mdKeywords = map[string]bool{
	"func": true, "return": true, "if": true, "else": true, "for": true,
	"while": true, "break": true, "continue": true, "package": true,
	"import": true, "const": true, "var": true, "type": true, "struct": true,
	"interface": true, "range": true, "select": true, "defer": true, "go": true,
	"chan": true, "map": true, "nil": true, "true": true, "false": true,
	"new": true, "make": true, "len": true, "cap": true, "def": true,
	"class": true, "from": true, "as": true, "lambda": true, "raise": true,
	"try": true, "except": true, "finally": true, "with": true, "yield": true,
	"None": true, "True": true, "False": true, "async": true, "await": true,
	"export": true, "default": true, "extends": true, "this": true, "static": true,
	"void": true, "public": true, "private": true, "protected": true,
	"let": true, "then": true, "throw": true, "catch": true, "switch": true,
	"case": true, "do": true, "fn": true, "use": true, "mod": true, "where": true,
	"insert": true, "update": true, "delete": true, "create": true, "table": true,
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
				b.WriteString(mdSeg(text[i+2:i+2+j], fg(TokMdBold), true))
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
	return lipgloss.NewStyle().Foreground(fg(TokMdCode)).Render(inner)
}
