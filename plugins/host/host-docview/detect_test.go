// 格式探测表驱动单测(扩展名 × 大小写 × 无扩展名 × 魔数特例)。
package hostdocview

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestFormatByExt(t *testing.T) {
	cases := []struct {
		name string
		want sdk.DocFormat
		ok   bool
	}{
		{"a.md", sdk.DocFormatMarkdown, true},
		{"A.MARKDOWN", sdk.DocFormatMarkdown, true},
		{"notes.txt", sdk.DocFormatText, true},
		{"app.log", sdk.DocFormatText, true},
		{"data.csv", sdk.DocFormatCSV, true},
		{"data.tsv", sdk.DocFormatCSV, true},
		{"doc.docx", sdk.DocFormatDOCX, true},
		{"doc.docm", sdk.DocFormatDOCX, true},
		{"book.xlsx", sdk.DocFormatXLSX, true},
		{"deck.pptx", sdk.DocFormatPPTX, true},
		{"paper.pdf", sdk.DocFormatPDF, true},
		{"page.html", sdk.DocFormatHTML, true},
		{"logo.PNG", sdk.DocFormatImage, true},
		{"icon.svg", sdk.DocFormatImage, true},
		{"main.go", sdk.DocFormatCode, true},
		{"conf.yaml", sdk.DocFormatCode, true},
		{"nb.ipynb", sdk.DocFormatNotebook, true},
		{"old.doc", sdk.DocFormatUnsupported, true},
		{"old.xls", sdk.DocFormatUnsupported, true},
		{"pkg.zip", sdk.DocFormatUnsupported, true},
		{"clip.mp4", sdk.DocFormatUnsupported, true},
		{"README", "", false},
		{"weird.qqq", "", false},
		{"noext.", "", false},
	}
	for _, c := range cases {
		got, ok := formatByExt(c.name)
		if ok != c.ok || got != c.want {
			t.Errorf("formatByExt(%q) = (%q,%v), want (%q,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestSniffFormat(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want sdk.DocFormat
	}{
		{"pdf-magic", []byte("%PDF-1.7\nrest"), sdk.DocFormatPDF},
		{"utf8", []byte("你好,world\n"), sdk.DocFormatText},
		{"ascii", []byte("plain text"), sdk.DocFormatText},
		{"empty", nil, sdk.DocFormatText},
		{"nul-bytes", []byte{0x00, 0x01, 0x02, 0xff}, sdk.DocFormatBinary},
		{"invalid-utf8", []byte{0xff, 0xfe, 0xfd}, sdk.DocFormatBinary},
	}
	for _, c := range cases {
		if got := sniffFormat(bytes.NewReader(c.in)); got != c.want {
			t.Errorf("%s: sniffFormat = %q, want %q", c.name, got, c.want)
		}
	}
	// 长内容只读前 512 字节(不得整读)
	long := strings.NewReader(strings.Repeat("a", 100000))
	if got := sniffFormat(long); got != sdk.DocFormatText {
		t.Errorf("长文本应判为 text,得 %q", got)
	}
}
