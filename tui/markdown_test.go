// TUI S1.4 markdown 轻渲染单测:token 分段着色/标题/列表/分隔线/
// 未闭合与代码围栏安全/无 ANSI 泄漏/宽度不变。
package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// stripANSI 去掉全部 CSI 序列(校验"无 ANSI 泄漏"用)。
func stripANSI(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == 0x1b && i+1 < len(rs) && rs[i+1] == '[' {
			for i < len(rs) {
				if rs[i] >= 'A' && rs[i] <= 'Z' || rs[i] >= 'a' && rs[i] <= 'z' {
					break
				}
				i++
			}
			continue // 跳过 CSI…字母
		}
		b.WriteRune(rs[i])
	}
	return b.String()
}

// TestMDPlain 无 token 文本:整体 base 色,原文完整(字符无损)。
func TestMDPlain(t *testing.T) {
	in := "你好世界 plain text"
	out := mdAnnotateRow(in, styleAsst.GetForeground())
	if stripANSI(out) != in {
		t.Fatalf("plain 应无损: got=%q", stripANSI(out))
	}
	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("plain 应仍着色: %q", out)
	}
	if lipgloss.Width(out) != lipgloss.Width(in) {
		t.Fatalf("显示宽度不应变: %d vs %d", lipgloss.Width(out), lipgloss.Width(in))
	}
}

// TestMDBoldAndCode 粗体与行内 code 分段着色:成对标记被消费(渲染语义),文本内容保留。
func TestMDBoldAndCode(t *testing.T) {
	in := "使用 **git rebase** 或 `git merge` 都可以"
	want := "使用 git rebase 或 git merge 都可以" // 标记被消费
	out := mdAnnotateRow(in, styleAsst.GetForeground())
	if stripANSI(out) != want {
		t.Fatalf("token 应去标记: got %q want %q", stripANSI(out), want)
	}
	// 粗体段应含加粗 SGR(bold+color 合并为 1;38;5;.. 或独立 1m)
	if !strings.Contains(out, "\x1b[1m") && !strings.Contains(out, "\x1b[1;") {
		t.Fatalf("粗体应加 SGR bold: %q", out)
	}
	// code 段应有 code 色(179)反引号不残留
	if strings.Contains(stripANSI(out), "`") {
		t.Fatalf("反引号应被消费: %q", stripANSI(out))
	}
}

// TestMDTitleHeading 标题行整体标题色。
func TestMDTitleHeading(t *testing.T) {
	in := "## 实现方案"
	out := mdAnnotateRow(in, styleAsst.GetForeground())
	if stripANSI(out) != in {
		t.Fatalf("标题行应无损: %q", stripANSI(out))
	}
	if !strings.Contains(out, "\x1b[1m") && !strings.Contains(out, "\x1b[1;") {
		t.Fatalf("标题应加粗: %q", out)
	}
}

// TestMDList 列表行:marker 换色,正文仍在。
func TestMDList(t *testing.T) {
	in := "- 第一步做什么"
	out := mdAnnotateRow(in, styleAsst.GetForeground())
	if stripANSI(out) != in {
		t.Fatalf("列表行应无损: %q", stripANSI(out))
	}
	// marker 改色 + 粗体
	if !strings.Contains(out, "\x1b[1m") && !strings.Contains(out, "\x1b[1;") {
		t.Fatalf("marker 应强调: %q", out)
	}
}

// TestMDNumberedList 编号列表识别。
func TestMDNumberedList(t *testing.T) {
	for _, in := range []string{"1. 甲", "12. 乙", "* 丙"} {
		if !isListLine(strings.TrimSpace(in)) {
			t.Fatalf("应识别列表行 %q", in)
		}
	}
}

// TestMDHr 分隔线识别 + 灰色。
func TestMDHr(t *testing.T) {
	for _, in := range []string{"---", "***", "___", " - - - "} {
		if !isHrLine(strings.TrimSpace(in)) {
			t.Fatalf("应识别分隔线 %q", in)
		}
	}
	out := mdAnnotateRow("---", styleAsst.GetForeground())
	if stripANSI(out) != "---" {
		t.Fatalf("分隔线应无损: %q", stripANSI(out))
	}
}

// TestMDUnclosed 未闭合 ** / ` 安全:原样输出,无多余 SGR 截断污染。
func TestMDUnclosed(t *testing.T) {
	// 未配对标记原样保留(不猜测);已成对 token 正常消费。
	cases := []struct{ in, want string }{
		{"有个 ** 未闭合", "有个 ** 未闭合"},
		{"`反引号没结束", "`反引号没结束"},
		{"plain **x** 尾部 **", "plain x 尾部 **"}, // 前对消费,尾部孤立保留
	}
	for _, c := range cases {
		out := mdAnnotateRow(c.in, styleAsst.GetForeground())
		if stripANSI(out) != c.want {
			t.Fatalf("未闭合应保留: %q → %q want %q", c.in, stripANSI(out), c.want)
		}
	}
}

// TestMDCodeFence 代码围栏行不着色(整体 base 色即可,不误识别)。
func TestMDCodeFence(t *testing.T) {
	out := mdAnnotateRow("```go", styleAsst.GetForeground())
	if stripANSI(out) != "```go" {
		t.Fatalf("围栏行应无损: %q", stripANSI(out))
	}
}

// TestMDSingleStar 单个 * 不是粗体(星号列表项/通配符原样)。
func TestMDSingleStar(t *testing.T) {
	in := "a * b * c"
	out := mdAnnotateRow(in, styleAsst.GetForeground())
	if stripANSI(out) != in {
		t.Fatalf("单个 * 应原样: %q", stripANSI(out))
	}
}

// TestMDInlineCodeProtectsBold code 内 ** 不应被粗体化(先切 code)。
func TestMDInlineCodeProtectsBold(t *testing.T) {
	in := "值 `a**b` 结束"
	want := "值 a**b 结束" // code 内 ** 原样(不二次解释)
	out := mdAnnotateRow(in, styleAsst.GetForeground())
	if stripANSI(out) != want {
		t.Fatalf("code 内 ** 应原样: got %q want %q", stripANSI(out), want)
	}
}

// TestRenderSessionRowAssistantMD 集成:assistant 行无搜索/选区走 md;user/搜索回落纯文本。
func TestRenderSessionRowAssistantMD(t *testing.T) {
	s := &State{}
	s.Lines = []Line{{Kind: "assistant", Text: "加粗 **重点** 与 `code`"}}
	rows := flattenLines(s.Lines, 60)
	if len(rows) != 1 {
		t.Fatalf("应展平 1 行: %d", len(rows))
	}
	out := renderSessionRow(rows[0], s, 0)
	if !strings.Contains(out, "\x1b[1m") && !strings.Contains(out, "\x1b[1;") {
		t.Fatalf("assistant 行应 md 粗体: %q", out)
	}
	// user 行:不 md(保持现有样式语义,纯色)
	s.Lines = []Line{{Kind: "user", Text: "加粗 **重点**"}}
	rows = flattenLines(s.Lines, 60)
	out = renderSessionRow(rows[0], s, 0)
	if strings.Contains(out, "\x1b[1m") {
		t.Fatalf("user 行不应 md: %q", out)
	}
}

// TestMDCodeBlockFence 代码围栏渲染:围栏行与块内行整体代码色、块内不做 md 解释;
// 块外普通行仍走 markdown。P4-3。
func TestMDCodeBlockFence(t *testing.T) {
	lines := []Line{
		{Kind: "assistant", Text: "请看代码:"},
		{Kind: "assistant", Text: "```go\nfunc add(a, b int) int {\n\treturn a + b\n}\n```"},
		{Kind: "assistant", Text: "如上 **粗体** 正常"},
	}
	rows := flattenLines(lines, 120)
	annotateCodeFences(rows)
	// 围栏标注:开围栏行 codeFence、闭围栏行 codeFence、块内内容行 inCode 且沿用语言
	if !rows[1].codeFence || rows[1].inCode {
		t.Fatalf("开围栏行应标 codeFence 非 inCode: %+v", rows[1])
	}
	for i := 2; i < 5; i++ {
		if !rows[i].inCode || rows[i].lang != "go" {
			t.Fatalf("块内行 %d 应 inCode+lang go: %+v", i, rows[i])
		}
	}
	if !rows[5].codeFence {
		t.Fatalf("闭围栏行应标 codeFence: %+v", rows[5])
	}
	if rows[6].inCode {
		t.Fatalf("闭围栏后不应 inCode: %+v", rows[6])
	}
	// 渲染:块内行带代码色 SGR;块外 md 行仍着色且粗体不丢
	inner := renderSessionRow(rows[3], &State{Lines: lines}, 3)
	if !strings.Contains(inner, "38;5;") {
		t.Fatalf("块内行应带代码色 SGR: %q", inner)
	}
	out := renderSessionRow(rows[6], &State{Lines: lines}, 6)
	if !strings.Contains(out, "38;5;1;") && !strings.Contains(out, "38;5;") && !strings.Contains(out, "\x1b[1m") {
		t.Fatalf("块外行仍应 md 着色: %q", out)
	}
}

// TestCodeBlockRowHighlight 语法着色:字符串/注释/关键字分段,字符无损(去 ANSI 后原文一致)。
func TestCodeBlockRowHighlight(t *testing.T) {
	cases := []struct{ text, lang string }{
		{"func main() { return }", "go"},
		{"def f():\n    return 1", "python"},
		{"// 注释说明", "go"},
		{"# shebang 注释", "sh"},
		{"for x in items: # 注释", "python"},
		{"s := \"hello, world\"", "go"},
		{"key = '值' # 尾注", "python"},
		{"sql := `SELECT * FROM t WHERE id = 1`", ""},
		{"纯文本行 with 中文 和符号#not-comment", ""},
	}
	for _, c := range cases {
		out := mdCodeBlockRow(c.text, c.lang)
		if stripANSI(out) != c.text {
			t.Fatalf("[%s/%s] 字符应无损: got %q want %q", c.lang, c.text, stripANSI(out), c.text)
		}
	}
	// 关键字色(141)、注释色(244)、字符串色(215)段都应出现
	hl := mdCodeBlockRow("func main() {\nreturn \"x\" // c\n}", "go")
	if !strings.Contains(hl, "38;5;141") {
		t.Fatalf("关键字应着色: %q", hl)
	}
	if !strings.Contains(hl, "38;5;215") {
		t.Fatalf("字符串应着色: %q", hl)
	}
	if !strings.Contains(hl, "38;5;244") {
		t.Fatalf("注释应着色: %q", hl)
	}
}

// TestCodeBlockRowNoLangHash 无语言代码块不把 # 当注释(防普通文本误伤);URL 协议符不触发 //。
func TestCodeBlockRowNoLangHash(t *testing.T) {
	out := mdCodeBlockRow("# 标题样文本", "")
	if strings.Contains(out, "38;5;244") {
		t.Fatalf("无语言块内 # 不应按注释着色: %q", out)
	}
	out = mdCodeBlockRow("http://example.com/x", "go")
	if strings.Contains(out, "38;5;244") {
		t.Fatalf("http:// 不应触发注释: %q", out)
	}
}
