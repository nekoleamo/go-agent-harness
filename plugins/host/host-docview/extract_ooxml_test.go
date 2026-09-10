// xlsx / pptx 抽取器单测(D3):夹具在测试内构造真实 OOXML 容器(确定性,无二进制入库)。
package hostdocview

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

const xlsxContentTypes = `<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Default Extension="png" ContentType="image/png"/>
  <Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
  <Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
  <Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>
</Types>`

const xlsxRootRels = `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`

const xlsxWorkbook = `<?xml version="1.0" encoding="UTF-8"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"
          xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets>
    <sheet name="数据" sheetId="1" r:id="rId1"/>
    <sheet name="隐藏表" sheetId="2" state="hidden" r:id="rId2"/>
  </sheets>
</workbook>`

const xlsxWorkbookRels = `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>
  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>
  <Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`

// xlsxStyles:xf0 常规 / xf1 日期(内建 14)/ xf2 百分比(内建 9)/ xf3 自定义日期
const xlsxStylesXML = `<?xml version="1.0" encoding="UTF-8"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <numFmts count="1"><numFmt numFmtId="164" formatCode="yyyy&quot;年&quot;m&quot;月&quot;d&quot;日&quot;"/></numFmts>
  <cellXfs count="4">
    <xf numFmtId="0"/>
    <xf numFmtId="14"/>
    <xf numFmtId="9"/>
    <xf numFmtId="164"/>
  </cellXfs>
</styleSheet>`

const xlsxSharedStrings = `<?xml version="1.0" encoding="UTF-8"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="3" uniqueCount="3">
  <si><t>名称</t></si>
  <si><t>苹果</t></si>
  <si><r><t>梨</t></r><r><t>子</t></r></si>
</sst>`

// xlsxSheet1:共享串 / 内联串 / 布尔 / 数值 / 日期 / 百分比 / 公式缓存值 / 稀疏空行 / 合并
const xlsxSheet1 = `<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"
           xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheetData>
    <row r="1">
      <c r="A1" t="s"><v>0</v></c>
      <c r="B1" t="s"><v>1</v></c>
      <c r="C1" t="s"><v>2</v></c>
    </row>
    <row r="2">
      <c r="A2" t="inlineStr"><is><t>行内</t></is></c>
      <c r="B2" t="b"><v>1</v></c>
      <c r="C2" s="1"><v>44927</v></c>
    </row>
    <row r="3">
      <c r="A3" s="2"><v>0.125</v></c>
      <c r="B3" s="3"><v>45000</v></c>
      <c r="C3" t="str"><f>SUM(C2:C2)</f><v>公式串结果</v></c>
    </row>
    <row r="10">
      <c r="A10"><v>42.5</v></c>
    </row>
  </sheetData>
  <mergeCells count="1"><mergeCell ref="A10:B10"/></mergeCells>
</worksheet>`

const xlsxSheet2 = `<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
  <row r="1"><c r="A1" t="s"><v>1</v></c></row>
</sheetData></worksheet>`

func writeXLSX(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "book.xlsx")
	data := zipParts(t, map[string][]byte{
		"[Content_Types].xml":        []byte(xlsxContentTypes),
		"_rels/.rels":                []byte(xlsxRootRels),
		"xl/workbook.xml":            []byte(xlsxWorkbook),
		"xl/_rels/workbook.xml.rels": []byte(xlsxWorkbookRels),
		"xl/styles.xml":              []byte(xlsxStylesXML),
		"xl/sharedStrings.xml":       []byte(xlsxSharedStrings),
		"xl/worksheets/sheet1.xml":   []byte(xlsxSheet1),
		"xl/worksheets/sheet2.xml":   []byte(xlsxSheet2),
	})
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractXLSX(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeXLSX(t, dir)
	v := preview(t, s, p)
	if v.Format != sdk.DocFormatXLSX {
		t.Fatalf("格式异常: %q", v.Format)
	}
	// 工作表元信息(名/隐藏/顺序)
	if len(v.Sheets) != 2 || v.Sheets[0].Name != "数据" || !v.Sheets[1].Hidden {
		t.Fatalf("工作表元信息异常: %+v", v.Sheets)
	}
	// sheet 块 + table 块
	var sheetBlk *sdk.DocBlock
	var tbl *sdk.DocBlock
	for i := range v.Blocks {
		switch v.Blocks[i].Kind {
		case sdk.DocBlockSheet:
			sheetBlk = &v.Blocks[i]
		case sdk.DocBlockTable:
			tbl = &v.Blocks[i]
		}
	}
	if sheetBlk == nil || sheetBlk.Text != "数据" {
		t.Fatalf("缺少 sheet 块: %+v", sheetBlk)
	}
	if tbl == nil {
		t.Fatalf("缺少 table 块")
	}
	// 行:1 表头 + 2 数据 + 折叠空行提示 + 第 10 行 = 4 行
	if len(tbl.Rows) != 5 {
		t.Fatalf("行数异常(含空行折叠提示): %d %+v", len(tbl.Rows), tbl.Rows)
	}
	joined := ""
	for _, r := range tbl.Rows {
		for _, c := range r {
			joined += c.Text + "|"
		}
		joined += "\n"
	}
	// 共享串(含 r 分段拼合)/ 内联串 / 布尔 / 日期 / 百分比 / 公式缓存值 / 数值
	for _, want := range []string{"名称", "苹果", "梨子", "行内", "TRUE", "2023-01-01", "12.50%", "2023-03-15", "公式串结果", "42.5", "省略 6 个空行"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %q:\n%s", want, joined)
		}
	}
	// 合并 A10:B10 → ColSpan=2(第 10 行是折叠提示后的最后一行)
	last := tbl.Rows[len(tbl.Rows)-1]
	if last[0].Text != "42.5" || last[0].ColSpan != 2 {
		t.Fatalf("合并单元格未生效: %+v", last)
	}
	// 多表提示
	if !strings.Contains(strings.Join(v.Warnings, " "), "工作簿共 2 个工作表") {
		t.Fatalf("应提示多工作表: %v", v.Warnings)
	}
}

// --sheet 切换工作表。
func TestExtractXLSXSheetSwitch(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeXLSX(t, dir)
	v := preview(t, s, p, sdk.DocRequest{Sheet: 1})
	var sheetBlk *sdk.DocBlock
	for i := range v.Blocks {
		if v.Blocks[i].Kind == sdk.DocBlockSheet {
			sheetBlk = &v.Blocks[i]
		}
	}
	if sheetBlk == nil || sheetBlk.Text != "隐藏表" {
		t.Fatalf("sheet 切换异常: %+v", sheetBlk)
	}
	// 越界 → 回退首表
	v2 := preview(t, s, p, sdk.DocRequest{Sheet: 99})
	for i := range v2.Blocks {
		if v2.Blocks[i].Kind == sdk.DocBlockSheet && v2.Blocks[i].Text != "数据" {
			t.Fatalf("越界应回退首表: %+v", v2.Blocks[i])
		}
	}
}

func TestExtractXLSXCaps(t *testing.T) {
	s, dir := newSvc(t, Budget{MaxTableRows: 2, MaxTableCols: 2, MaxCellChars: 2})
	p := writeXLSX(t, dir)
	v := preview(t, s, p)
	for _, b := range v.Blocks {
		if b.Kind != sdk.DocBlockTable {
			continue
		}
		if len(b.Rows) > 2 {
			t.Fatalf("行预算未生效: %d", len(b.Rows))
		}
		for _, r := range b.Rows {
			if len(r) > 2 {
				t.Fatalf("列预算未生效: %+v", r)
			}
			for _, c := range r {
				if n := len([]rune(c.Text)); n > 3 {
					t.Fatalf("单元格未截断(%d): %q", n, c.Text)
				}
			}
		}
	}
	if len(v.Truncated) == 0 {
		t.Fatalf("应写 Truncated: %+v", v)
	}
}

// 日期/百分比/数值格式化表驱动。
func TestXLSXNumberFormatting(t *testing.T) {
	st := &xlsxStyles{dateXf: map[int]bool{1: true}, percentXf: map[int]bool{2: true}}
	cases := []struct {
		raw  string
		xf   int
		want string
	}{
		{"44927", 1, "2023-01-01 00:00:00"},
		{"0.125", 2, "12.50%"},
		{"42.5", 0, "42.5"},
		{"7", 0, "7"},
		{"0.100000", 0, "0.1"},
		{"abc", 0, "abc"},
		{"", 0, ""},
	}
	for _, c := range cases {
		if got := xlsxFormatNumber(c.raw, c.xf, st); got != c.want {
			t.Fatalf("xlsxFormatNumber(%q,%d) = %q, want %q", c.raw, c.xf, got, c.want)
		}
	}
	// 内建/自定义日期格式判定
	if !isDateFormat(14, "") || !isDateFormat(0, "yyyy-mm-dd") || !isDateFormat(0, "hh:mm") {
		t.Fatal("日期格式判定漏判")
	}
	if isDateFormat(0, "0.00") || isDateFormat(0, "#,##0") || isDateFormat(0, `"yyyy"0`) {
		t.Fatal("日期格式判定误判")
	}
	// 1900 假闰日(S/N 60 = 1900-02-29 不存在 → 落 1900-02-28)
	if got := excelSerialToTime(60).Format("2006-01-02"); got != "1900-02-28" {
		t.Fatalf("序列 60 应为 1900-02-28,得 %s", got)
	}
}

// ---- pptx ----

const pptxContentTypes = `<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Default Extension="png" ContentType="image/png"/>
  <Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>
</Types>`

const pptxRootRels = `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>
</Relationships>`

// 顺序故意与文件名相反(sldIdLst 权威)
const pptxPresentation = `<?xml version="1.0" encoding="UTF-8"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
                xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst>
    <p:sldId id="256" r:id="rId2"/>
    <p:sldId id="257" r:id="rId1"/>
  </p:sldIdLst>
</p:presentation>`

const pptxPresentationRels = `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide2.xml"/>
</Relationships>`

const pptxSlide1 = `<?xml version="1.0" encoding="UTF-8"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
       xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
       xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:cSld><p:spTree>
    <p:sp>
      <p:nvSpPr><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr>
      <p:txBody><a:p><a:r><a:t>第一页标题</a:t></a:r></a:p></p:txBody>
    </p:sp>
    <p:sp>
      <p:nvSpPr><p:nvPr><p:ph type="body"/></p:nvPr></p:nvSpPr>
      <p:txBody>
        <a:p><a:r><a:t>正文一</a:t></a:r></a:p>
        <a:p><a:pPr lvl="1"/><a:r><a:t>二级要点</a:t></a:r></a:p>
      </p:txBody>
    </p:sp>
    <p:pic><p:blipFill><a:blip r:embed="rId5"/></p:blipFill></p:pic>
    <p:graphicFrame><a:graphic><a:graphicData>
      <a:tbl>
        <a:tr><a:tc><a:txBody><a:p><a:r><a:t>单元格A</a:t></a:r></a:p></a:txBody></a:tc>
              <a:tc><a:txBody><a:p><a:r><a:t>123</a:t></a:r></a:p></a:txBody></a:tc></a:tr>
      </a:tbl>
    </a:graphicData></a:graphic></p:graphicFrame>
  </p:spTree></p:cSld>
</p:sld>`

const pptxSlide1Rels = `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/image1.png"/>
  <Relationship Id="rId9" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide1.xml"/>
</Relationships>`

const pptxSlide2 = `<?xml version="1.0" encoding="UTF-8"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
       xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld><p:spTree>
    <p:sp>
      <p:nvSpPr><p:nvPr><p:ph type="body"/></p:nvPr></p:nvSpPr>
      <p:txBody><a:p><a:r><a:t>第二页正文</a:t></a:r></a:p></p:txBody>
    </p:sp>
  </p:spTree></p:cSld>
</p:sld>`

func writePPTX(t *testing.T, dir string, png []byte) string {
	t.Helper()
	p := filepath.Join(dir, "deck.pptx")
	data := zipParts(t, map[string][]byte{
		"[Content_Types].xml":              []byte(pptxContentTypes),
		"_rels/.rels":                      []byte(pptxRootRels),
		"ppt/presentation.xml":             []byte(pptxPresentation),
		"ppt/_rels/presentation.xml.rels":  []byte(pptxPresentationRels),
		"ppt/slides/slide1.xml":            []byte(pptxSlide1),
		"ppt/slides/_rels/slide1.xml.rels": []byte(pptxSlide1Rels),
		"ppt/slides/slide2.xml":            []byte(pptxSlide2),
		"ppt/media/image1.png":             png,
		"ppt/notesSlides/notesSlide1.xml":  []byte(`<?xml version="1.0"?><p:notes xmlns:p="x"/>`),
	})
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractPPTX(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writePPTX(t, dir, pngBytes(t, 4, 2))
	v := preview(t, s, p)
	if v.Format != sdk.DocFormatPPTX || v.Pages != 2 {
		t.Fatalf("页数/格式异常: %q %d", v.Format, v.Pages)
	}
	// 顺序以 sldIdLst 为准:rId2 = slide2(第二页正文)在前
	var slides []int
	firstText := ""
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockSlide {
			slides = append(slides, b.Page)
			firstText = ""
			continue
		}
		if firstText == "" {
			firstText = b.Text
		}
	}
	if len(slides) != 2 || slides[0] != 1 || slides[1] != 2 {
		t.Fatalf("幻灯片序号异常: %v", slides)
	}
	if !strings.Contains(v.Blocks[1].Text+v.Blocks[2].Text, "第二页正文") {
		t.Fatalf("sldIdLst 顺序未生效:\n%+v", v.Blocks[:3])
	}
	// 标题 → heading + DocView.Title
	var titleBlk *sdk.DocBlock
	for i := range v.Blocks {
		if v.Blocks[i].Kind == sdk.DocBlockHeading {
			titleBlk = &v.Blocks[i]
		}
	}
	if titleBlk == nil || titleBlk.Level != 1 || titleBlk.Text != "第一页标题" || v.Title != "第一页标题" {
		t.Fatalf("标题占位处理异常: %+v title=%q", titleBlk, v.Title)
	}
	// 文本框层级
	var found bool
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockParagraph && b.Level == 1 && b.Text == "二级要点" {
			found = true
		}
	}
	if !found {
		t.Fatalf("段落层级未生效: %+v", v.Blocks)
	}
	// 表格与图片
	var tbl *sdk.DocBlock
	var img *sdk.DocBlock
	for i := range v.Blocks {
		switch v.Blocks[i].Kind {
		case sdk.DocBlockTable:
			tbl = &v.Blocks[i]
		case sdk.DocBlockImage:
			img = &v.Blocks[i]
		}
	}
	if tbl == nil || len(tbl.Rows) != 1 || tbl.Rows[0][0].Text != "单元格A" || !tbl.Rows[0][1].Numeric {
		t.Fatalf("表格异常: %+v", tbl)
	}
	if img == nil || img.Asset == nil || img.Asset.W != 4 || img.Asset.H != 2 {
		t.Fatalf("图片资产异常: %+v", img)
	}
	rc, _, err := s.Asset(context.Background(), sdk.DocRequest{Path: p}, img.Asset.ID)
	if err != nil {
		t.Fatalf("资产取回失败: %v", err)
	}
	rc.Close()
	// 备注页显式警告
	if !strings.Contains(strings.Join(v.Warnings, " "), "备注页") {
		t.Fatalf("应提示备注页未解析: %v", v.Warnings)
	}
}

// 标题占位继承:幻灯片本身无标题 → 从布局/母版取。
func TestPPTXTitleInheritance(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	deck := zipParts(t, map[string][]byte{
		"[Content_Types].xml":               []byte(pptxContentTypes),
		"_rels/.rels":                       []byte(pptxRootRels),
		"ppt/presentation.xml":              []byte(pptxPresentation),
		"ppt/_rels/presentation.xml.rels":   []byte(pptxPresentationRels),
		"ppt/slides/slide1.xml":             []byte(pptxSlide2), // 无标题
		"ppt/slides/slide2.xml":             []byte(pptxSlide2),
		"ppt/slides/_rels/slide1.xml.rels":  []byte(`<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/></Relationships>`),
		"ppt/slideLayouts/slideLayout1.xml": []byte(`<?xml version="1.0"?><p:sldLayout xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:nvSpPr><p:nvPr><p:ph type="ctrTitle"/></p:nvPr></p:nvSpPr><p:txBody><a:p><a:r><a:t>布局标题</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sldLayout>`),
	})
	p := filepath.Join(dir, "inherit.pptx")
	if err := os.WriteFile(p, deck, 0o644); err != nil {
		t.Fatal(err)
	}
	v := preview(t, s, p)
	if v.Title != "布局标题" {
		t.Fatalf("标题占位继承失败: %q", v.Title)
	}
}

func TestExtractPPTXCaps(t *testing.T) {
	s, dir := newSvc(t, Budget{MaxBlocks: 3, MaxTableRows: 1, MaxTableCols: 1})
	p := writePPTX(t, dir, pngBytes(t, 4, 2))
	v := preview(t, s, p)
	if len(v.Blocks) > 3 {
		t.Fatalf("块预算未生效: %d", len(v.Blocks))
	}
	if len(v.Truncated) == 0 {
		t.Fatalf("应写 Truncated: %+v", v)
	}
}

// 坏容器 / 空 sldIdLst → 结构化错误。
func TestExtractOOXMLBroken(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	for _, c := range []struct {
		name  string
		parts map[string][]byte
	}{
		{"empty-slides.pptx", map[string][]byte{
			"[Content_Types].xml":             []byte(pptxContentTypes),
			"_rels/.rels":                     []byte(pptxRootRels),
			"ppt/presentation.xml":            []byte(`<?xml version="1.0"?><p:presentation xmlns:p="x"/>`),
			"ppt/_rels/presentation.xml.rels": []byte(`<?xml version="1.0"?><Relationships/>`),
		}},
		{"bad.xlsx", map[string][]byte{
			"[Content_Types].xml": []byte(xlsxContentTypes),
			"_rels/.rels":         []byte(xlsxRootRels),
		}},
	} {
		p := filepath.Join(dir, c.name)
		if err := os.WriteFile(p, zipParts(t, c.parts), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Preview(context.Background(), sdk.DocRequest{Path: p}); err == nil {
			t.Fatalf("%s 应返回结构化错误", c.name)
		}
	}
}

// 大表稀疏:10 万行声明不该拖垮解析(预算封顶 + 空行折叠)。
func TestXLSXLargeSparse(t *testing.T) {
	s, dir := newSvc(t, Budget{MaxTableRows: 50})
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for i := 1; i <= 100000; i += 1000 {
		sb.WriteString(fmt.Sprintf(`<row r="%d"><c r="A%d"><v>%d</v></c></row>`, i, i, i))
	}
	sb.WriteString(`</sheetData></worksheet>`)
	p := filepath.Join(dir, "big.xlsx")
	data := zipParts(t, map[string][]byte{
		"[Content_Types].xml":        []byte(xlsxContentTypes),
		"_rels/.rels":                []byte(xlsxRootRels),
		"xl/workbook.xml":            []byte(xlsxWorkbook),
		"xl/_rels/workbook.xml.rels": []byte(xlsxWorkbookRels),
		"xl/worksheets/sheet1.xml":   []byte(sb.String()),
	})
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	v := preview(t, s, p)
	var rows int
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockTable {
			rows += len(b.Rows)
		}
	}
	if rows > 51 {
		t.Fatalf("行预算未生效: %d", rows)
	}
	if len(v.Truncated) == 0 {
		t.Fatalf("应写 Truncated: %+v", v)
	}
	// 空行折叠提示存在(在表格行内)
	found := false
	for _, b := range v.Blocks {
		for _, r := range b.Rows {
			for _, c := range r {
				if strings.Contains(c.Text, "省略") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("空行折叠提示缺失")
	}
}
