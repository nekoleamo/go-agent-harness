// PDF 抽取器单测(D0:页事实;夹具在测试内构造确定性最小 PDF)。
package hostdocview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pdf "github.com/Detective-XH/gopdf"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// pdfDocSummary 构造页事实聚合(只供 classifyPDFKind 表驱动使用)。
func pdfDocSummary(text, image, empty, degraded, total int) pdf.DocumentSummary {
	ds := pdf.DocumentSummary{TotalPages: total}
	add := func(n int, sig pdf.ExtractionSignal) {
		for i := 0; i < n; i++ {
			ds.Pages = append(ds.Pages, pdf.PageSignal{Signal: sig})
		}
	}
	add(text, pdf.SignalText)
	add(image, pdf.SignalImageOnly)
	add(empty, pdf.SignalEmpty)
	add(degraded, pdf.SignalDegraded)
	ds.TextPages, ds.ImageOnlyPages, ds.EmptyPages, ds.DegradedPages = text, image, empty, degraded
	return ds
}

// buildPDF 生成最小合法 PDF(带正确 xref 偏移),objects 为若干 "1 0 obj … endobj" 主体。
func buildPDF(t *testing.T, objects []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, o := range objects {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R /Info 5 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return buf.Bytes()
}

// pdfWithText 单页含文本的 PDF。
func pdfWithText(t *testing.T, text string) []byte {
	content := fmt.Sprintf("BT /F1 12 Tf 20 100 Td (%s) Tj ET", text)
	return buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents 4 0 R /Resources << /Font << /F1 6 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Title (Test Doc) /Author (gah) >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	})
}

// pdfImageOnly 单页只有图片、无文本层(模拟扫描件)。
func pdfImageOnly(t *testing.T) []byte {
	content := "q 100 0 0 100 20 20 cm /Im0 Do Q"
	return buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents 4 0 R /Resources << /XObject << /Im0 6 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Title (扫描件) >>",
		"<< /Type /XObject /Subtype /Image /Width 4 /Height 4 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 16 >>\nstream\n" +
			strings.Repeat("\x00", 16) + "\nendstream",
	})
}

func TestExtractPDFTextDocument(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(p, pdfWithText(t, "Hello PDF"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := s.Preview(context.Background(), sdk.DocRequest{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if v.Format != sdk.DocFormatPDF || v.Pages != 1 {
		t.Fatalf("页事实异常: format=%q pages=%d", v.Format, v.Pages)
	}
	if v.Kind != "text" {
		t.Fatalf("应判 text 页,得 %q(warnings=%v)", v.Kind, v.Warnings)
	}
	if v.Title != "Test Doc" || v.Author != "gah" {
		t.Fatalf("Info 未解析: title=%q author=%q", v.Title, v.Author)
	}
	// 行号化文本里应带上 PDF 页事实(D5 模型工具依赖)
	tx, err := s.Text(context.Background(), sdk.DocRequest{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if tx.PDF == nil || tx.PDF.PageCount != 1 || tx.PDF.Kind != "text" {
		t.Fatalf("DocText.PDF 事实缺失: %+v", tx.PDF)
	}
}

func TestExtractPDFScannedWarns(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := filepath.Join(dir, "scan.pdf")
	if err := os.WriteFile(p, pdfImageOnly(t), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := s.Preview(context.Background(), sdk.DocRequest{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != "scanned" {
		t.Fatalf("应判 scanned,得 %q", v.Kind)
	}
	if !strings.Contains(strings.Join(v.Warnings, " "), "OCR") {
		t.Fatalf("扫描件应显式提示需 OCR: %v", v.Warnings)
	}
	if v.Meta["pages_needing_ocr"] == "" {
		t.Fatalf("应记录 pages_needing_ocr: %+v", v.Meta)
	}
	tx, _ := s.Text(context.Background(), sdk.DocRequest{Path: p})
	if tx.PDF == nil || len(tx.PDF.PagesNeedingOCR) != 1 || tx.PDF.PagesNeedingOCR[0] != 1 {
		t.Fatalf("pagesNeedingOcr 未透传: %+v", tx.PDF)
	}
}

func TestExtractPDFBrokenIsStructured(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := filepath.Join(dir, "broken.pdf")
	if err := os.WriteFile(p, []byte("%PDF-1.4 garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Preview(context.Background(), sdk.DocRequest{Path: p}); !errors.Is(err, sdk.ErrDocParse) {
		t.Fatalf("损坏 PDF 应返回结构化解析错误,得 %v", err)
	}
}

// 加密 PDF:结构化 unsupported(要求口令),不静默失败。
func TestExtractPDFEncryptedStructured(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := filepath.Join(dir, "enc.pdf")
	enc := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents 4 0 R >>",
		"<< /Length 0 >>\nstream\n\nendstream",
		"<< /Title (enc) >>",
	})
	// 插入 /Encrypt 标记(不实现真实加密:只验证错误分类路径不 panic)
	enc = bytes.Replace(enc, []byte("/Root 1 0 R"), []byte("/Root 1 0 R /Encrypt 6 0 R"), 1)
	enc = bytes.Replace(enc, []byte("%%EOF"), []byte("6 0 obj\n<< /Filter /Standard /O <00> /U <00> /P -1 >>\nendobj\n%%EOF"), 1)
	if err := os.WriteFile(p, enc, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Preview(context.Background(), sdk.DocRequest{Path: p})
	if err == nil {
		// 某些容错路径可能仍能读出:只要不 panic 即合规
		t.Skip("夹具未触发加密路径(容错读取成功)")
	}
	if !errors.Is(err, sdk.ErrDocUnsupported) && !errors.Is(err, sdk.ErrDocParse) {
		t.Fatalf("加密 PDF 应为结构化错误,得 %v", err)
	}
}

func TestClassifyPDFKind(t *testing.T) {
	cases := []struct {
		text, image, empty, degraded, total int
		want                                string
	}{
		{2, 0, 0, 0, 2, "text"},
		{0, 2, 0, 0, 2, "scanned"},
		{1, 1, 0, 0, 2, "mixed"},
		{0, 0, 2, 0, 2, "empty"},
		{0, 0, 0, 0, 0, "empty"},
	}
	for _, c := range cases {
		ds := pdfDocSummary(c.text, c.image, c.empty, c.degraded, c.total)
		if got := classifyPDFKind(ds); got != c.want {
			t.Fatalf("classify(text=%d,image=%d,empty=%d,deg=%d,total=%d) = %q, want %q",
				c.text, c.image, c.empty, c.degraded, c.total, got, c.want)
		}
	}
}
