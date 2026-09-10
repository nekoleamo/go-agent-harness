// 文档 pager 单测(D1):块摊平 / 表格 ASCII / 滚动 / 搜索 / 关闭 / 渲染宽度。
package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// docKey 构造 pager 按键(tea.Key 是 handleKey 解包后的形态)。
func docKey(code rune, text string) tea.Key {
	return tea.Key{Code: code, Text: text}
}

func sampleView() *sdk.DocView {
	return &sdk.DocView{
		Name:   "a.md",
		Format: sdk.DocFormatMarkdown,
		Title:  "示例",
		Pages:  2,
		Kind:   "text",
		Blocks: []sdk.DocBlock{
			{Kind: sdk.DocBlockHeading, Level: 1, Text: "标题"},
			{Kind: sdk.DocBlockParagraph, Text: "段落一"},
			{Kind: sdk.DocBlockList, Level: 1, Text: "项"},
			{Kind: sdk.DocBlockCode, Lang: "go", Text: "x := 1\ny := 2"},
			{Kind: sdk.DocBlockTable, Head: []string{"名称", "数量"}, Rows: [][]sdk.DocCell{{{Text: "苹果"}, {Text: "3", Numeric: true}}}},
			{Kind: sdk.DocBlockImage, Text: "图", Asset: &sdk.DocAsset{Name: "i.png", W: 10, H: 5, Bytes: 100}},
			{Kind: sdk.DocBlockPage, Page: 2},
			{Kind: sdk.DocBlockDivider},
			{Kind: sdk.DocBlockNote, Text: "备注"},
		},
		Truncated: []string{"rows:200/999"},
		Warnings:  []string{"警告一", "警告二"},
	}
}

func TestDocPagerFlatten(t *testing.T) {
	p := NewDocPager(sampleView(), 0, 0)
	joined := strings.Join(p.Lines, "\n")
	for _, want := range []string{"示例", "# 标题", "段落一", "· 项", "``` go", "x := 1", "| 名称 | 数量 |", "[图片] i.png(10×5,100 字节)", "── 第 2 页 ──", "› 备注"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("摊平缺少 %q\n%s", want, joined)
		}
	}
	if !strings.Contains(p.Status, "截断:rows:200/999") || !strings.Contains(p.Status, "警告一(共 2 条)") {
		t.Fatalf("状态行应显式提示截断/警告: %q", p.Status)
	}
}

func TestDocPagerScrollAndClose(t *testing.T) {
	p := NewDocPager(sampleView(), 0, 0)
	p.Lines = make([]string, 100)
	for i := range p.Lines {
		p.Lines[i] = "l" + strconv.Itoa(i)
	}
	if closed := p.HandleKey(docKey(tea.KeyDown, ""), 80, 24); closed {
		t.Fatal("↓ 不应关闭")
	}
	if p.Off != 1 {
		t.Fatalf("↓ 应下移一行, off=%d", p.Off)
	}
	p.HandleKey(docKey(tea.KeyPgDown, ""), 80, 24)
	if p.Off <= 1 {
		t.Fatalf("PgDn 应翻页, off=%d", p.Off)
	}
	p.HandleKey(docKey(tea.KeyHome, ""), 80, 24)
	if p.Off != 0 {
		t.Fatalf("Home 应回顶, off=%d", p.Off)
	}
	p.HandleKey(docKey('G', ""), 80, 24)
	if p.Off != 99 {
		t.Fatalf("G 应到底, off=%d", p.Off)
	}
	p.HandleKey(docKey(tea.KeyUp, ""), 80, 24)
	if p.Off != 98 {
		t.Fatalf("↑ 应上移, off=%d", p.Off)
	}
	// 水平滚动
	p.HandleKey(docKey(tea.KeyRight, ""), 80, 24)
	if p.HOff != 4 {
		t.Fatalf("→ 应水平偏移, hoff=%d", p.HOff)
	}
	p.HandleKey(docKey(tea.KeyLeft, ""), 80, 24)
	p.HandleKey(docKey(tea.KeyLeft, ""), 80, 24)
	if p.HOff != 0 {
		t.Fatalf("← 不应越界, hoff=%d", p.HOff)
	}
	if !p.HandleKey(docKey(tea.KeyEscape, ""), 80, 24) {
		t.Fatal("Esc 应关闭")
	}
	if !p.HandleKey(docKey('q', "q"), 80, 24) {
		t.Fatal("q 应关闭")
	}
}

func TestDocPagerSearch(t *testing.T) {
	p := NewDocPager(&sdk.DocView{Name: "a.txt", Blocks: []sdk.DocBlock{{Kind: sdk.DocBlockCode, Text: "alpha\nbeta\nalpha again"}}}, 0, 0)
	// 进入搜索输入模式
	p.HandleKey(docKey('/', "/"), 80, 24)
	if !p.searching {
		t.Fatal("/ 应进入搜索输入")
	}
	for _, r := range "alpha" {
		p.HandleKey(docKey(r, string(r)), 80, 24)
	}
	p.HandleKey(docKey(tea.KeyEnter, ""), 80, 24)
	if p.searching || p.Search != "alpha" {
		t.Fatalf("搜索未生效: searching=%v q=%q", p.searching, p.Search)
	}
	if len(p.Hits) != 2 || p.Off != p.Hits[0] {
		t.Fatalf("命中应跳到首个: hits=%v off=%d", p.Hits, p.Off)
	}
	p.HandleKey(docKey('n', "n"), 80, 24)
	if p.Off != p.Hits[1] {
		t.Fatalf("n 应跳下一命中, off=%d", p.Off)
	}
	p.HandleKey(docKey('n', "n"), 80, 24)
	if p.Off != p.Hits[0] {
		t.Fatalf("n 应回绕, off=%d", p.Off)
	}
	p.HandleKey(docKey('N', "N"), 80, 24)
	if p.Off != p.Hits[1] {
		t.Fatalf("N 应反向跳转, off=%d", p.Off)
	}
	// Esc 取消输入不关闭浮层
	p.HandleKey(docKey('/', "/"), 80, 24)
	if closed := p.HandleKey(docKey(tea.KeyEscape, ""), 80, 24); closed {
		t.Fatal("搜索输入态 Esc 应只退出输入")
	}
	if p.searching {
		t.Fatal("Esc 应退出搜索输入")
	}
}

func TestDocPagerRender(t *testing.T) {
	p := NewDocPager(sampleView(), 0, 0)
	out := p.Render(60, 16)
	lines := strings.Split(out, "\n")
	if len(lines) != 16 {
		t.Fatalf("渲染行数应为 height=16,得 %d", len(lines))
	}
	if !strings.Contains(lines[0], "a.md") || !strings.Contains(lines[0], "markdown") {
		t.Fatalf("首行应为标题+格式: %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "关闭") || !strings.Contains(lines[len(lines)-1], "行") {
		t.Fatalf("末行应为操作提示: %q", lines[len(lines)-1])
	}
	// 搜索态状态行
	p.SetSearch("标题")
	out2 := p.Render(80, 12)
	if !strings.Contains(out2, "命中") {
		t.Fatalf("搜索后状态行应显示命中数:\n%s", out2)
	}
}

func TestDocPagerFindPage(t *testing.T) {
	v := &sdk.DocView{Name: "p.pdf", Format: sdk.DocFormatPDF, Pages: 3, Blocks: []sdk.DocBlock{
		{Kind: sdk.DocBlockPage, Page: 1}, {Kind: sdk.DocBlockParagraph, Text: "p1"},
		{Kind: sdk.DocBlockPage, Page: 2}, {Kind: sdk.DocBlockParagraph, Text: "p2"},
		{Kind: sdk.DocBlockPage, Page: 3}, {Kind: sdk.DocBlockParagraph, Text: "p3"},
	}}
	p := NewDocPager(v, 3, 0)
	if p.Lines[p.Off] != "── 第 3 页 ──" {
		t.Fatalf("page 参数应定位页标记, off=%d line=%q", p.Off, p.Lines[p.Off])
	}
}

// 表格 ASCII:CJK 对齐与超宽截断。
func TestAsciiTableCJKAndBudget(t *testing.T) {
	b := &sdk.DocBlock{Kind: sdk.DocBlockTable, Head: []string{"名称", "描述"}, Rows: [][]sdk.DocCell{
		{{Text: "苹果"}, {Text: strings.Repeat("很长的描述", 20)}},
	}}
	lines := asciiTable(b, 40)
	if len(lines) != 3 {
		t.Fatalf("表格应有表头+分隔+1 行,得 %d", len(lines))
	}
	for _, l := range lines {
		if w := displayWidth(l); w > 44 {
			t.Fatalf("表格行超宽(%d): %q", w, l)
		}
	}
	if !strings.Contains(lines[0], "名称") || !strings.Contains(lines[2], "苹果") {
		t.Fatalf("表格内容缺失: %v", lines)
	}
	if !strings.Contains(lines[2], "…") {
		t.Fatalf("超宽单元格应截断标记: %q", lines[2])
	}
}

// DocPager 状态:搜索缓冲键入 / 滚轮;确保不 panic 且区间合法。
func TestDocPagerWheelAndEdges(t *testing.T) {
	p := NewDocPager(&sdk.DocView{Name: "a.txt", Blocks: []sdk.DocBlock{{Kind: sdk.DocBlockCode, Text: "1\n2\n3"}}}, 0, 0)
	p.ScrollWheel(true) // 顶部上滚
	if p.Off != 0 {
		t.Fatalf("顶部上滚应钳制, off=%d", p.Off)
	}
	for i := 0; i < 50; i++ {
		p.ScrollWheel(false)
	}
	if p.Off < 0 || p.Off > len(p.Lines)-1 {
		t.Fatalf("滚轮越界: off=%d lines=%d", p.Off, len(p.Lines))
	}
	// 空视图不 panic
	empty := NewDocPager(&sdk.DocView{Name: "empty"}, 0, 0)
	if len(empty.Lines) == 0 {
		t.Fatal("空视图应给占位行")
	}
	empty.Render(0, 0)
	// 搜索输入退格
	empty.HandleKey(docKey('/', "/"), 80, 24)
	empty.HandleKey(docKey('a', "a"), 80, 24)
	empty.HandleKey(docKey(tea.KeyBackspace, ""), 80, 24)
	if empty.searchBuf != "" {
		t.Fatalf("退格应清空缓冲: %q", empty.searchBuf)
	}
}

// displayWidth 简单显示宽度(测试用:CJK 记 2)。
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if r > 0x2E80 {
			w += 2
		} else {
			w++
		}
	}
	return w
}
