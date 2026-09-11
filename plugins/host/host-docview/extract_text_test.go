// 文本族抽取器单测(D1):markdown(块/行内/表格/HTML 转义/图片/链接安全)、CSV/TSV、notebook。
package hostdocview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// blockKinds 块类型序列(断言粗粒度结构)。
func blockKinds(v *sdk.DocView) []string {
	out := make([]string, 0, len(v.Blocks))
	for _, b := range v.Blocks {
		out = append(out, string(b.Kind))
	}
	return out
}

func preview(t *testing.T, s *Service, p string, req ...sdk.DocRequest) *sdk.DocView {
	t.Helper()
	r := sdk.DocRequest{Path: p}
	if len(req) > 0 {
		r = req[0]
		r.Path = p
	}
	v, err := s.Preview(context.Background(), r)
	if err != nil {
		t.Fatalf("Preview(%s): %v", p, err)
	}
	return v
}

func TestExtractMarkdownBlocks(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	src := "# 标题一\n\n段落 **粗** *斜* `码` [链](https://e.com)\n\n" +
		"- 项一\n- 项二\n  - 嵌套\n\n> 引用\n\n```go\nx := 1\n```\n\n---\n\n" +
		"| a | b |\n|---|---:|\n| 1 | 2 |\n"
	p := writeFile(t, dir, "a.md", []byte(src))
	v := preview(t, s, p)

	kinds := strings.Join(blockKinds(v), ",")
	for _, want := range []string{"heading", "paragraph", "list", "quote", "code", "divider", "table"} {
		if !strings.Contains(kinds, want) {
			t.Fatalf("缺少 %s 块\nkinds=%s", want, kinds)
		}
	}
	if v.Blocks[0].Kind != sdk.DocBlockHeading || v.Blocks[0].Level != 1 || v.Blocks[0].Text != "标题一" {
		t.Fatalf("标题块异常: %+v", v.Blocks[0])
	}
	// 行内富文本
	var para *sdk.DocBlock
	for i := range v.Blocks {
		if v.Blocks[i].Kind == sdk.DocBlockParagraph {
			para = &v.Blocks[i]
			break
		}
	}
	if para == nil {
		t.Fatal("缺少段落块")
	}
	var bold, italic, code, link bool
	for _, r := range para.Runs {
		bold = bold || r.Bold
		italic = italic || r.Italic
		code = code || r.Code
		link = link || r.Link == "https://e.com"
	}
	if !(bold && italic && code && link) {
		t.Fatalf("行内样式丢失: %+v", para.Runs)
	}
	// 嵌套列表深度
	var maxLevel int
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockList && b.Level > maxLevel {
			maxLevel = b.Level
		}
	}
	if maxLevel != 1 {
		t.Fatalf("嵌套列表深度应为 1,得 %d", maxLevel)
	}
	// 表格对齐
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockTable {
			if len(b.Head) != 2 || len(b.Rows) != 1 || b.Rows[0][1].Align != "right" {
				t.Fatalf("表格块异常: %+v", b)
			}
		}
	}
	// 代码块语言
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockCode && b.Lang != "go" {
			t.Fatalf("代码块语言应为 go,得 %q", b.Lang)
		}
	}
}

// 原始 HTML 必须按纯文本处理(渲染层零 HTML 通道),XSS 载荷不得进入任何 HTML 通道。
func TestExtractMarkdownHTMLEscaped(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	src := "<script>alert(1)</script>\n\n段落 <img src=x onerror=alert(2)> 尾\n"
	p := writeFile(t, dir, "x.md", []byte(src))
	v := preview(t, s, p)
	joined := ""
	for _, b := range v.Blocks {
		joined += b.Text + "\n"
		for _, r := range b.Runs {
			joined += r.Text
		}
	}
	if !strings.Contains(joined, "<script>") {
		t.Fatalf("原始 HTML 应作为纯文本保留(不解析): %q", joined)
	}
	if !strings.Contains(strings.Join(v.Warnings, " "), "原始 HTML") {
		t.Fatalf("应显式警告 HTML 已纯文本化: %v", v.Warnings)
	}
}

// 链接安全:javascript:/data: 丢弃;远程图片不加载。
func TestExtractMarkdownLinkAndImageSafety(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	src := "[坏](javascript:alert(1)) [好](https://e.com)\n\n![远程](https://evil.example/x.png)\n"
	p := writeFile(t, dir, "y.md", []byte(src))
	v := preview(t, s, p)
	for _, b := range v.Blocks {
		for _, r := range b.Runs {
			if strings.HasPrefix(r.Link, "javascript:") || strings.HasPrefix(r.Link, "data:") {
				t.Fatalf("不安全链接未被丢弃: %+v", r)
			}
			if r.Link != "" && r.Link != "https://e.com" {
				t.Fatalf("非预期链接: %q", r.Link)
			}
		}
	}
	if !strings.Contains(strings.Join(v.Warnings, " "), "远程图片不自动加载") {
		t.Fatalf("远程图片应显式警告: %v", v.Warnings)
	}
}

// 本地相对图片 → 资产(经 asset 端点取回真图)。
func TestExtractMarkdownLocalImageAsset(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	writeFile(t, dir, "img.png", pngBytes(t, 3, 2))
	p := writeFile(t, dir, "z.md", []byte("段落\n\n![本地图](img.png)\n"))
	v := preview(t, s, p)
	var img *sdk.DocBlock
	for i := range v.Blocks {
		if v.Blocks[i].Kind == sdk.DocBlockImage {
			img = &v.Blocks[i]
		}
	}
	if img == nil || img.Asset == nil {
		t.Fatalf("本地图片应生成资产块: %+v", v.Blocks)
	}
	if img.Asset.W != 3 || img.Asset.H != 2 || img.Asset.Mime != "image/png" {
		t.Fatalf("资产元信息异常: %+v", img.Asset)
	}
	rc, mimeType, err := s.Asset(context.Background(), sdk.DocRequest{Path: p}, img.Asset.ID)
	if err != nil {
		t.Fatalf("资产取回失败: %v", err)
	}
	defer rc.Close()
	if !strings.HasPrefix(mimeType, "image/png") {
		t.Fatalf("资产 MIME 异常: %q", mimeType)
	}
}

func TestExtractCSVAndTSV(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	csvPath := writeFile(t, dir, "d.csv", []byte("name,qty\n苹果,3\n梨,10\n"))
	v := preview(t, s, csvPath)
	if len(v.Blocks) != 1 || v.Blocks[0].Kind != sdk.DocBlockTable {
		t.Fatalf("CSV 应为 table 块: %+v", v.Blocks)
	}
	if got := v.Blocks[0].Head; len(got) != 2 || got[0] != "name" {
		t.Fatalf("表头异常: %v", got)
	}
	if len(v.Blocks[0].Rows) != 2 || !v.Blocks[0].Rows[0][1].Numeric {
		t.Fatalf("数据行异常: %+v", v.Blocks[0].Rows)
	}
	// TSV:制表符优先
	tsvPath := writeFile(t, dir, "d.tsv", []byte("a\tb\tc\n1\t2\t3\n"))
	vt := preview(t, s, tsvPath)
	if vt.Meta["delimiter"] != "\t" || len(vt.Blocks[0].Head) != 3 {
		t.Fatalf("TSV 定界符嗅探失败: meta=%v head=%v", vt.Meta, vt.Blocks[0].Head)
	}
	// 分号 CSV
	scPath := writeFile(t, dir, "e.csv", []byte("a;b;c\n1;2;3\n4;5;6\n"))
	vs := preview(t, s, scPath)
	if vs.Meta["delimiter"] != ";" {
		t.Fatalf("分号定界符嗅探失败: %v", vs.Meta)
	}
}

func TestExtractCSVTruncation(t *testing.T) {
	s, dir := newSvc(t, Budget{MaxTableRows: 1, MaxTableCols: 2, MaxCellChars: 3})
	p := writeFile(t, dir, "big.csv", []byte("aaa,bbb,ccc\n1111,2222,3333\n4444,5555,6666\n"))
	v := preview(t, s, p)
	if len(v.Blocks[0].Rows) != 1 {
		t.Fatalf("行预算未生效: %+v", v.Blocks[0].Rows)
	}
	if len(v.Blocks[0].Head) != 2 || v.Blocks[0].Head[0] != "aaa" {
		t.Fatalf("列/单元格预算未生效: %+v", v.Blocks[0].Head)
	}
	if n := utf8.RuneCountInString(v.Blocks[0].Rows[0][0].Text); n > 4 { // 3 字符 + 省略号
		t.Fatalf("单元格未截断(%d runes): %q", n, v.Blocks[0].Rows[0][0].Text)
	}
	if len(v.Truncated) == 0 || !strings.Contains(strings.Join(v.Warnings, " "), "预算") {
		t.Fatalf("应写 Truncated/Warnings: %+v / %v", v.Truncated, v.Warnings)
	}
}

func TestExtractNotebook(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	nb := `{"metadata":{"kernelspec":{"language":"python"}},"cells":[
      {"cell_type":"markdown","source":["## 说明\n","文字\n"]},
      {"cell_type":"code","source":["print(1)\n"],"outputs":[{"output_type":"stream","text":["1\n"]}]},
      {"cell_type":"code","source":["1/0"],"outputs":[{"output_type":"error","ename":"ZeroDivisionError","evalue":"division by zero","traceback":["line1"]}]}
    ]}`
	p := writeFile(t, dir, "nb.ipynb", []byte(nb))
	v := preview(t, s, p)
	if v.Format != sdk.DocFormatNotebook {
		t.Fatalf("格式异常: %q", v.Format)
	}
	kinds := strings.Join(blockKinds(v), ",")
	if !strings.Contains(kinds, "heading") || !strings.Contains(kinds, "code") {
		t.Fatalf("notebook 块结构异常: %s", kinds)
	}
	joined := ""
	for _, b := range v.Blocks {
		joined += b.Text + "\n"
	}
	if !strings.Contains(joined, "print(1)") || !strings.Contains(joined, "ZeroDivisionError") {
		t.Fatalf("notebook 内容缺失:\n%s", joined)
	}
	// 坏 JSON → 结构化解析错误
	bad := writeFile(t, dir, "bad.ipynb", []byte("{not json"))
	if _, err := s.Preview(context.Background(), sdk.DocRequest{Path: bad}); err == nil {
		t.Fatal("坏 ipynb 应报错")
	}
}

// markdown 渲染到行号化文本(D5 模型工具依赖的形态)。
func TestMarkdownToTextRendering(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "r.md", []byte("# T\n\n- a\n\n| x |\n|---|\n| 1 |\n"))
	tx, err := s.Text(context.Background(), sdk.DocRequest{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range tx.Lines {
		got = append(got, l.Text)
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "# T") || !strings.Contains(joined, "- a") || !strings.Contains(joined, "| x |") {
		t.Fatalf("渲染异常:\n%s", joined)
	}
}

// 无扩展名但内容为 markdown 之外:仍按 UTF-8 兜底为 text(不误判 markdown)。
func TestNoExtFallsBackToText(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "NOTES", []byte("# 看起来像标题\n"))
	v := preview(t, s, p)
	if v.Format != sdk.DocFormatText {
		t.Fatalf("无扩展名应兜底 text(不做内容嗅探),得 %q", v.Format)
	}
	_ = os.Remove(filepath.Join(dir, "nonexistent"))
}
