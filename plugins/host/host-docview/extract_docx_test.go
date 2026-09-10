// docx 抽取器单测(D2):夹具在测试内构造真实 OOXML 容器(确定性、无二进制入库)。
package hostdocview

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// zipParts 写一个 zip 容器(part 名 → 内容)。
func zipParts(t *testing.T, parts map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// 稳定顺序:map 遍历随机,排序保证夹具确定性
	names := make([]string, 0, len(parts))
	for n := range parts {
		names = append(names, n)
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(parts[n]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const docxContentTypes = `<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Default Extension="png" ContentType="image/png"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
  <Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
  <Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>
</Types>`

const docxRootRels = `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>
</Relationships>`

const docxStyles = `<?xml version="1.0" encoding="UTF-8"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:style w:type="paragraph" w:styleId="Heading1"><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style>
  <w:style w:type="paragraph" w:styleId="Heading2"><w:pPr><w:outlineLvl w:val="1"/></w:pPr></w:style>
  <w:style w:type="paragraph" w:styleId="MyList"><w:pPr><w:numPr><w:ilvl w:val="1"/><w:numId w:val="3"/></w:numPr></w:pPr></w:style>
</w:styles>`

// docxDocument 覆盖:标题?段落(富文本+超链接)/列表/表格(gridSpan+vMerge+tblHeader)/图片/公式。
const docxDocument = `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
            xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"
            xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
            xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math">
  <w:body>
    <w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>标题一</w:t></w:r></w:p>
    <w:p><w:pPr><w:pStyle w:val="Heading2"/></w:pPr><w:r><w:t>标题二</w:t></w:r></w:p>
    <w:p>
      <w:r><w:rPr><w:b/></w:rPr><w:t>粗体</w:t></w:r>
      <w:r><w:rPr><w:i/></w:rPr><w:t>斜体</w:t></w:r>
      <w:r><w:rPr><w:rStyle w:val="VerbatimChar"/></w:rPr><w:t>码</w:t></w:r>
      <w:hyperlink r:id="rId20"><w:r><w:t>链接文本</w:t></w:r></w:hyperlink>
      <w:hyperlink r:id="rId21"><w:r><w:t>坏链接</w:t></w:r></w:hyperlink>
      <w:r><w:t>行一</w:t><w:br/><w:t>行二</w:t></w:r>
    </w:p>
    <w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>列表项</w:t></w:r></w:p>
    <w:p><w:pPr><w:pStyle w:val="MyList"/></w:pPr><w:r><w:t>样式列表项</w:t></w:r></w:p>
    <w:p><w:r><w:drawing><wp:inline xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"><wp:docPr id="1" name="图一"/><a:graphic><a:graphicData><pic:pic xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:blipFill><a:blip r:embed="rId10"/></pic:blipFill></pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r></w:p>
    <w:p><m:oMath><m:r><m:t>x+1</m:t></m:r></m:oMath></w:p>
    <w:tbl>
      <w:tr><w:trPr><w:tblHeader/></w:trPr>
        <w:tc><w:tcPr><w:gridSpan w:val="2"/></w:tcPr><w:p><w:r><w:t>合并表头</w:t></w:r></w:p></w:tc>
        <w:tc><w:p><w:r><w:t>第三列</w:t></w:r></w:p></w:tc>
      </w:tr>
      <w:tr>
        <w:tc><w:tcPr><w:vMerge w:val="restart"/></w:tcPr><w:p><w:r><w:t>纵向合并</w:t></w:r></w:p></w:tc>
        <w:tc><w:p><w:r><w:t>值1</w:t></w:r></w:p></w:tc>
        <w:tc><w:p><w:r><w:t>123</w:t></w:r></w:p></w:tc>
      </w:tr>
      <w:tr>
        <w:tc><w:tcPr><w:vMerge/></w:tcPr><w:p><w:r><w:t>不应出现</w:t></w:r></w:p></w:tc>
        <w:tc><w:p><w:r><w:t>值2</w:t></w:r></w:p>
          <w:tbl><w:tr><w:tc><w:p><w:r><w:t>内层表</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
        </w:tc>
        <w:tc><w:p><w:r><w:t>456</w:t></w:r></w:p></w:tc>
      </w:tr>
    </w:tbl>
  </w:body>
</w:document>`

const docxRels = `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId10" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image1.png"/>
  <Relationship Id="rId20" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://example.com/x" TargetMode="External"/>
  <Relationship Id="rId21" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="javascript:alert(1)" TargetMode="External"/>
</Relationships>`

const docxCore = `<?xml version="1.0" encoding="UTF-8"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/">
  <dc:title>示例文档</dc:title><dc:creator>gah</dc:creator>
</cp:coreProperties>`

// writeDocx 写夹具容器(png 为 2×3 图;含页眉/脚注部件以验证"忽略 + 警告")。
func writeDocx(t *testing.T, dir string, png []byte) string {
	t.Helper()
	parts := map[string][]byte{
		"[Content_Types].xml":          []byte(docxContentTypes),
		"_rels/.rels":                  []byte(docxRootRels),
		"word/document.xml":            []byte(docxDocument),
		"word/_rels/document.xml.rels": []byte(docxRels),
		"word/styles.xml":              []byte(docxStyles),
		"word/media/image1.png":        png,
		"docProps/core.xml":            []byte(docxCore),
		"word/header1.xml":             []byte(`<?xml version="1.0"?><w:hdr xmlns:w="x"><w:p><w:r><w:t>页眉文字</w:t></w:r></w:p></w:hdr>`),
		"word/footnotes.xml":           []byte(`<?xml version="1.0"?><w:footnotes xmlns:w="x"/>`),
	}
	p := filepath.Join(dir, "sample.docx")
	if err := os.WriteFile(p, zipParts(t, parts), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractDOCX(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeDocx(t, dir, pngBytes(t, 2, 3))
	v := preview(t, s, p)
	if v.Format != sdk.DocFormatDOCX {
		t.Fatalf("格式异常: %q", v.Format)
	}
	if v.Title != "示例文档" || v.Author != "gah" {
		t.Fatalf("元信息异常: title=%q author=%q", v.Title, v.Author)
	}

	// 标题层级(styles.xml outlineLvl)
	var levels []int
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockHeading {
			levels = append(levels, b.Level)
		}
	}
	if len(levels) != 2 || levels[0] != 1 || levels[1] != 2 {
		t.Fatalf("标题层级异常: %v", levels)
	}

	// 富文本 + 超链接安全
	var para *sdk.DocBlock
	for i := range v.Blocks {
		if v.Blocks[i].Kind == sdk.DocBlockParagraph && strings.Contains(v.Blocks[i].Text, "粗体") {
			para = &v.Blocks[i]
			break
		}
	}
	if para == nil {
		t.Fatalf("缺少富文本段落")
	}
	var bold, italic, code, link bool
	for _, r := range para.Runs {
		bold = bold || (r.Bold && r.Text == "粗体")
		italic = italic || (r.Italic && r.Text == "斜体")
		code = code || (r.Code && r.Text == "码")
		if r.Text == "链接文本" {
			link = r.Link == "https://example.com/x"
		}
		if r.Text == "坏链接" && r.Link != "" {
			t.Fatalf("不安全链接未被丢弃: %+v", r)
		}
	}
	if !bold || !italic || !code || !link {
		t.Fatalf("富文本/链接丢失: bold=%v italic=%v code=%v link=%v\n%+v", bold, italic, code, link, para.Runs)
	}
	if !strings.Contains(para.Text, "行一\n行二") {
		t.Fatalf("w:br 未转换行: %q", para.Text)
	}

	// 列表(行内 numPr 与样式表 numPr)
	var listLevels []int
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockList {
			listLevels = append(listLevels, b.Level)
		}
	}
	if len(listLevels) != 2 || listLevels[0] != 0 || listLevels[1] != 1 {
		t.Fatalf("列表层级异常: %v", listLevels)
	}

	// 表格:表头 / gridSpan / vMerge / 数值右对齐提示
	var tbl *sdk.DocBlock
	for i := range v.Blocks {
		if v.Blocks[i].Kind == sdk.DocBlockTable {
			tbl = &v.Blocks[i]
		}
	}
	if tbl == nil {
		t.Fatalf("缺少表格块")
	}
	if len(tbl.Head) != 3 || tbl.Head[0] != "合并表头" || tbl.Head[2] != "第三列" {
		t.Fatalf("表头异常: %+v", tbl.Head)
	}
	if len(tbl.Rows) != 2 {
		t.Fatalf("数据行应为 2(vMerge continue 合入上行),得 %d: %+v", len(tbl.Rows), tbl.Rows)
	}
	if tbl.Rows[0][0].Text != "纵向合并" || tbl.Rows[0][0].RowSpan != 2 {
		t.Fatalf("纵向合并未生效: %+v", tbl.Rows[0][0])
	}
	if len(tbl.Rows[0]) != 3 || !tbl.Rows[0][2].Numeric {
		t.Fatalf("行单元格异常: %+v", tbl.Rows[0])
	}

	// 图片 → 资产(尺寸 + 取回)
	var img *sdk.DocBlock
	for i := range v.Blocks {
		if v.Blocks[i].Kind == sdk.DocBlockImage {
			img = &v.Blocks[i]
		}
	}
	if img == nil || img.Asset == nil || img.Asset.W != 2 || img.Asset.H != 3 {
		t.Fatalf("图片资产异常: %+v", img)
	}
	rc, mimeType, err := s.Asset(context.Background(), sdk.DocRequest{Path: p}, img.Asset.ID)
	if err != nil {
		t.Fatalf("资产取回失败: %v", err)
	}
	defer rc.Close()
	if !strings.HasPrefix(mimeType, "image/png") {
		t.Fatalf("资产 MIME 异常: %q", mimeType)
	}

	// 显式警告:页眉/脚注忽略、公式纯文本、嵌套表格跳过
	warns := strings.Join(v.Warnings, " | ")
	for _, want := range []string{"页眉", "脚注", "公式", "嵌套表格"} {
		if !strings.Contains(warns, want) {
			t.Fatalf("缺少警告 %q:\n%s", want, warns)
		}
	}
}

func TestExtractDOCXTableCaps(t *testing.T) {
	s, dir := newSvc(t, Budget{MaxTableRows: 1, MaxTableCols: 2, MaxCellChars: 4})
	p := writeDocx(t, dir, pngBytes(t, 2, 3))
	v := preview(t, s, p)
	for _, b := range v.Blocks {
		if b.Kind != sdk.DocBlockTable {
			continue
		}
		if len(b.Rows) > 1 {
			t.Fatalf("行预算未生效: %d", len(b.Rows))
		}
		for _, r := range b.Rows {
			if len(r) > 2 {
				t.Fatalf("列预算未生效: %+v", r)
			}
			for _, c := range r {
				if n := len([]rune(c.Text)); n > 5 {
					t.Fatalf("单元格未截断(%d): %q", n, c.Text)
				}
			}
		}
	}
	if len(v.Truncated) == 0 {
		t.Fatalf("应写 Truncated: %+v", v)
	}
}

// 坏容器/缺主部件 → 结构化错误,不 panic。
func TestExtractDOCXBroken(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	bad := filepath.Join(dir, "bad.docx")
	if err := os.WriteFile(bad, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Preview(context.Background(), sdk.DocRequest{Path: bad}); err == nil {
		t.Fatal("非 zip 应报错")
	}
	// zip 但缺 document.xml
	noMain := filepath.Join(dir, "nomin.docx")
	if err := os.WriteFile(noMain, zipParts(t, map[string][]byte{
		"[Content_Types].xml": []byte(docxContentTypes),
		"_rels/.rels":         []byte(docxRootRels),
	}), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Preview(context.Background(), sdk.DocRequest{Path: noMain}); err == nil {
		t.Fatal("缺主部件应报错")
	}
	// 空 docx(仅空 body)→ 占位块不报错
	empty := filepath.Join(dir, "empty.docx")
	if err := os.WriteFile(empty, zipParts(t, map[string][]byte{
		"[Content_Types].xml":          []byte(docxContentTypes),
		"_rels/.rels":                  []byte(docxRootRels),
		"word/document.xml":            []byte(`<?xml version="1.0"?><w:document xmlns:w="x"><w:body/></w:document>`),
		"word/_rels/document.xml.rels": []byte(`<?xml version="1.0"?><Relationships/>`),
	}), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := s.Preview(context.Background(), sdk.DocRequest{Path: empty})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Blocks) != 1 || v.Blocks[0].Kind != sdk.DocBlockNote {
		t.Fatalf("空文档应给占位块: %+v", v.Blocks)
	}
}

// zip 爆炸防护:声明超大 part 直接拒绝(docx 通路)。
func TestExtractDOCXZipBomb(t *testing.T) {
	s, dir := newSvc(t, Budget{MaxZipPartBytes: 64, MaxZipRatio: 2, MaxZipTotal: 128})
	p := writeDocx(t, dir, pngBytes(t, 2, 3))
	if _, err := s.Preview(context.Background(), sdk.DocRequest{Path: p}); err == nil {
		t.Fatal("超预算容器应被拒绝")
	}
}
