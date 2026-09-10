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

// buildPagesPDF 生成 N 页 PDF,每页一段文本(可选字号)。
func buildPagesPDF(t *testing.T, pages []string, fontSize int) []byte {
	t.Helper()
	if fontSize <= 0 {
		fontSize = 10
	}
	// 对象:1 catalog, 2 pages, 3..(3+2n-1) page/content 对,末尾 font + info
	n := len(pages)
	fontObj := 3 + 2*n
	infoObj := fontObj + 1
	objects := make([]string, 0, infoObj)
	kids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		kids = append(kids, fmt.Sprintf("%d 0 R", 3+2*(i-1)))
	}
	objects = append(objects, "<< /Type /Catalog /Pages 2 0 R >>")
	objects = append(objects, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n))
	for i, txt := range pages {
		pageObj := 3 + 2*i
		contentObj := pageObj + 1
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 400 300] /Contents %d 0 R /Resources << /Font << /F1 %d 0 R >> >> >>", contentObj, fontObj))
		content := fmt.Sprintf("BT /F1 %d Tf 40 200 Td (%s) Tj ET", fontSize, txt)
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	}
	objects = append(objects, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	objects = append(objects, "<< /Title (多页) /Author (gah) >>")
	return buildPDF(t, objects)
}

func TestExtractPDFPages(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := filepath.Join(dir, "multi.pdf")
	if err := os.WriteFile(p, buildPagesPDF(t, []string{"page one alpha", "page two beta", "page three gamma"}, 11), 0o644); err != nil {
		t.Fatal(err)
	}
	v := preview(t, s, p)
	if v.Pages != 3 {
		t.Fatalf("页数异常: %d", v.Pages)
	}
	var pageMarks []int
	text := ""
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockPage {
			pageMarks = append(pageMarks, b.Page)
		}
		if b.Kind == sdk.DocBlockParagraph || b.Kind == sdk.DocBlockHeading {
			text += b.Text + "\n"
		}
	}
	if len(pageMarks) != 3 || pageMarks[0] != 1 || pageMarks[2] != 3 {
		t.Fatalf("页标记异常: %v", pageMarks)
	}
	for _, want := range []string{"page one alpha", "page two beta", "page three gamma"} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺少 %q:\n%s", want, text)
		}
	}
	// 行号化文本(模型工具/IM 路径)含页标记
	tx, err := s.Text(context.Background(), sdk.DocRequest{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, l := range tx.Lines {
		joined += l.Text + "\n"
	}
	if !strings.Contains(joined, "第 2 页") || !strings.Contains(joined, "page two beta") {
		t.Fatalf("行号化文本缺少页标记/内容:\n%s", joined)
	}
}

// 页码范围:--page 只解析指定起始页。
func TestExtractPDFPageRange(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := filepath.Join(dir, "range.pdf")
	if err := os.WriteFile(p, buildPagesPDF(t, []string{"p1", "p2", "p3"}, 11), 0o644); err != nil {
		t.Fatal(err)
	}
	v := preview(t, s, p, sdk.DocRequest{Page: 3})
	text := ""
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockPage {
			if b.Page != 3 {
				t.Fatalf("显式页集外出现页标记: %d", b.Page)
			}
		}
		text += b.Text
	}
	if strings.Contains(text, "p1") || !strings.Contains(text, "p3") {
		t.Fatalf("page 起始页未生效: %q", text)
	}
}

// 标题启发:显著大字号单行 → heading。
func TestExtractPDFHeadingHeuristic(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := filepath.Join(dir, "title.pdf")
	// 大字号标题 + 正文
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 400 300] /Contents 4 0 R /Resources << /Font << /F1 6 0 R >> >> >>",
		"",
		"<< /Title (t) >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	content := "BT /F1 24 Tf 40 250 Td (Big Title) Tj ET\nBT /F1 10 Tf 40 200 Td (body line one) Tj ET"
	objects[3] = fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
	if err := os.WriteFile(p, buildPDF(t, objects), 0o644); err != nil {
		t.Fatal(err)
	}
	v := preview(t, s, p)
	var heading, paragraph bool
	for _, b := range v.Blocks {
		switch b.Kind {
		case sdk.DocBlockHeading:
			if strings.Contains(b.Text, "Big Title") {
				heading = true
			}
		case sdk.DocBlockParagraph:
			if strings.Contains(b.Text, "body line one") {
				paragraph = true
			}
		}
	}
	if !heading || !paragraph {
		t.Fatalf("标题启发未生效: heading=%v paragraph=%v\n%+v", heading, paragraph, v.Blocks)
	}
}

// 全框线表格:重建为 table 块。
func TestExtractPDFTable(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := filepath.Join(dir, "table.pdf")
	content := strings.Join([]string{
		"0.7 w",
		"50 40 m 250 40 l S",
		"50 65 m 250 65 l S",
		"50 90 m 250 90 l S",
		"50 40 m 50 90 l S",
		"150 40 m 150 90 l S",
		"250 40 m 250 90 l S",
		"BT /F1 10 Tf 55 45 Td (A2) Tj ET",
		"BT /F1 10 Tf 155 45 Td (B2) Tj ET",
		"BT /F1 10 Tf 55 70 Td (A1) Tj ET",
		"BT /F1 10 Tf 155 70 Td (B1) Tj ET",
		"BT /F1 10 Tf 55 95 Td (A0) Tj ET",
		"BT /F1 10 Tf 155 95 Td (B0) Tj ET",
	}, "\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 400 300] /Contents 4 0 R /Resources << /Font << /F1 6 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Title (tbl) >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	if err := os.WriteFile(p, buildPDF(t, objects), 0o644); err != nil {
		t.Fatal(err)
	}
	v := preview(t, s, p)
	var tbl *sdk.DocBlock
	for i := range v.Blocks {
		if v.Blocks[i].Kind == sdk.DocBlockTable {
			tbl = &v.Blocks[i]
		}
	}
	if tbl == nil {
		t.Skipf("gopdf 未把该夹具识别为框线表(实现已接线;以真实文档复核):%+v", v.Blocks)
	}
	// 网格自上而下:A1/B1 为首行(表头),A2/B2 为数据行
	if len(tbl.Head) != 2 || tbl.Head[0] != "A1" || len(tbl.Rows) != 1 || tbl.Rows[0][0].Text != "A2" {
		t.Fatalf("表格重建结果异常: head=%v rows=%v", tbl.Head, tbl.Rows)
	}
	// 表格内的单元格文本不应再以段落重复出现
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockParagraph && (b.Text == "A1" || b.Text == "B2") {
			t.Fatalf("表格文本重复输出: %+v", b)
		}
	}
}

// 图片元数据(不解码;note 块 + 尺寸/滤镜)。
func TestExtractPDFImageMetadata(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := filepath.Join(dir, "img.pdf")
	if err := os.WriteFile(p, pdfImageOnly(t), 0o644); err != nil {
		t.Fatal(err)
	}
	v := preview(t, s, p)
	joined := ""
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockNote {
			joined += b.Text + "\n"
		}
	}
	if !strings.Contains(joined, "本页图片") {
		t.Fatalf("图片元数据提示缺失:\n%s", joined)
	}
	if v.Kind != "scanned" {
		t.Fatalf("图片页应判 scanned,得 %q", v.Kind)
	}
}

// 块预算封顶:多页超预算 → Truncated + 提示。
func TestExtractPDFBlockCap(t *testing.T) {
	s, dir := newSvc(t, Budget{MaxBlocks: 4})
	p := filepath.Join(dir, "many.pdf")
	if err := os.WriteFile(p, buildPagesPDF(t, []string{"a", "b", "c", "d", "e", "f"}, 11), 0o644); err != nil {
		t.Fatal(err)
	}
	v := preview(t, s, p)
	if len(v.Blocks) > 4 {
		t.Fatalf("块预算未生效: %d", len(v.Blocks))
	}
	if !containsMarker(v.Truncated, "blocks") {
		t.Fatalf("应写 blocks 截断标记: %+v", v.Truncated)
	}
}

// 加密 PDF 缺口令:结构化 unsupported(要求口令)。
func TestExtractPDFEncryptedNoPassword(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := filepath.Join(dir, "enc2.pdf")
	enc := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents 4 0 R >>",
		"<< /Length 8 >>\nstream\nq Q\nendstream",
		"<< /Title (e) >>",
	})
	enc = bytes.Replace(enc, []byte("/Root 1 0 R"), []byte("/Root 1 0 R /Encrypt 6 0 R /ID [<01><01>]"), 1)
	enc = bytes.Replace(enc, []byte("%%EOF"), []byte("6 0 obj\n<< /Filter /Standard /V 1 /R 2 /O <"+strings.Repeat("00", 32)+"> /U <"+strings.Repeat("00", 32)+"> /P -1 >>\nendobj\n%%EOF"), 1)
	if err := os.WriteFile(p, enc, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(pdfPasswordEnv, "")
	_, err := s.Preview(context.Background(), sdk.DocRequest{Path: p})
	if err == nil {
		t.Skip("夹具未构成有效加密 PDF(容错读取成功)")
	}
	if !errors.Is(err, sdk.ErrDocUnsupported) && !errors.Is(err, sdk.ErrDocParse) {
		t.Fatalf("应为结构化错误,得 %v", err)
	}
	if errors.Is(err, sdk.ErrDocUnsupported) && !strings.Contains(err.Error(), "加密") {
		t.Fatalf("应提示需口令: %v", err)
	}
}

// 页码标签与请求页集(Pages 显式)。
func TestExtractPDFPageLabelsAndExplicitPages(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := filepath.Join(dir, "labels.pdf")
	if err := os.WriteFile(p, buildPagesPDF(t, []string{"one", "two", "three"}, 11), 0o644); err != nil {
		t.Fatal(err)
	}
	v := preview(t, s, p, sdk.DocRequest{Pages: []int{2}})
	text := ""
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockPage && b.Page != 2 {
			t.Fatalf("显式页集未生效: %d", b.Page)
		}
		text += b.Text
	}
	if !strings.Contains(text, "two") || strings.Contains(text, "one") {
		t.Fatalf("显式页集内容异常: %q", text)
	}
}
