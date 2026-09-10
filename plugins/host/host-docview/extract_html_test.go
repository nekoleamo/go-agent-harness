// HTML 源码视图单测(D6-4):标题提取 / 实体解码 / 脚本告警 / 截断 / 零 HTML 通道。
package hostdocview

import (
	"context"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestExtractHTMLSourceView(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	src := "<!DOCTYPE html>\n<html><head><title>  Hello &amp; \n World </title></head>\n" +
		"<body><h1>T&amp;t</h1></body></html>"
	p := writeFile(t, dir, "a.html", []byte(src))
	v := preview(t, s, p)

	if v.Format != sdk.DocFormatHTML {
		t.Fatalf("格式判定异常: %s", v.Format)
	}
	if v.Title != "Hello & World" {
		t.Fatalf("标题提取异常: %q", v.Title)
	}
	if len(v.Blocks) != 1 || v.Blocks[0].Kind != sdk.DocBlockCode || v.Blocks[0].Lang != "html" {
		t.Fatalf("非预期块: %+v", v.Blocks)
	}
	if !strings.Contains(v.Blocks[0].Text, "<h1>T&amp;t</h1>") {
		t.Fatalf("源码未原样保留: %q", v.Blocks[0].Text)
	}
	if v.Meta["mime"] != "text/html" {
		t.Fatalf("mime 异常: %+v", v.Meta)
	}
	// 无 script → 不告警;块模型无 HTML 通道(仅 code 块)
	if len(v.Warnings) != 0 {
		t.Fatalf("不应有告警: %v", v.Warnings)
	}
}

func TestExtractHTMLScriptWarningAndNoTitle(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "b.htm", []byte("<div onerror=\"x\"><script>alert(1)</script></div>"))
	v := preview(t, s, p)

	if v.Title != "" {
		t.Fatalf("无 title 应留空: %q", v.Title)
	}
	if len(v.Warnings) != 1 || !strings.Contains(v.Warnings[0], "不执行") {
		t.Fatalf("脚本告警缺失: %v", v.Warnings)
	}
}

func TestExtractHTMLTruncated(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	body := "<p>" + strings.Repeat("内容", 200) + "</p>"
	p := writeFile(t, dir, "c.xhtml", []byte("<?xml version=\"1.0\"?>"+body))
	v := preview(t, s, p, sdk.DocRequest{MaxBytes: 40})

	if len(v.Truncated) != 1 || !strings.Contains(v.Truncated[0], "bytes:40/") {
		t.Fatalf("截断标记缺失: %v", v.Truncated)
	}
}

func TestExtractHTMLLineNumberedText(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "d.html", []byte("<title>T</title>\n<p>a</p>\n"))
	txt, err := s.Text(context.Background(), sdk.DocRequest{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if txt.TotalLines < 2 || !strings.Contains(txt.Lines[1].Text, "<title>T</title>") {
		t.Fatalf("行号化文本异常: %+v", txt)
	}
}

func TestHTMLTitleEdgeCases(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"<title>", ""},
		{"<title>abc", ""},
		{"<TITLE>A</TITLE>", "A"},
		{"<html><title> 多\n行  折叠 </title></html>", "多 行 折叠"},
		{"<title>&lt;x&gt;&#39;q&#39;</title>", "<x>'q'"},
	}
	for _, c := range cases {
		if got := htmlTitle(c.in); got != c.want {
			t.Fatalf("htmlTitle(%q)=%q want %q", c.in, got, c.want)
		}
	}
	long := "<title>" + strings.Repeat("字", htmlTitleMax+10) + "</title>"
	got := []rune(htmlTitle(long))
	if len(got) != htmlTitleMax+1 || got[len(got)-1] != '…' {
		t.Fatalf("长标题未按上限截断: %d", len(got))
	}
}
