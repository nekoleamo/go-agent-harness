// DOC-2 单测:xlsx 批注(legacy + 回复式)/文本框/内嵌图片/隐藏行列 抽取。
// 判定依据见 DESIGN §14.1「D6-3 判定实测」;夹具为测试内构造的 OOXML 容器(确定性,无二进制入库)。
package hostdocview

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// png1x1 合法 1×1 PNG(内嵌图片尺寸探测用)。
const png1x1B64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"

func png1x1(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(png1x1B64)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// xlsxAnnFixture 一份带批注/文本框/图片/隐藏行的工作簿部件集。
func xlsxAnnFixture(t *testing.T, extra map[string][]byte) map[string][]byte {
	t.Helper()
	parts := map[string][]byte{
		"[Content_Types].xml": []byte(xlsxContentTypes),
		"_rels/.rels":         []byte(xlsxRootRels),
		"xl/workbook.xml":     []byte(xlsxWorkbook),
		"xl/_rels/workbook.xml.rels": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>
  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/person" Target="persons/person.xml"/>
</Relationships>`),
		"xl/worksheets/sheet1.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<cols><col min="3" max="3" hidden="1"/></cols>
<sheetData>
<row r="1"><c r="A1" t="inlineStr"><is><t>科目</t></is></c></row>
<row r="2" hidden="1"><c r="A2" t="inlineStr"><is><t>隐藏行内容</t></is></c></row>
</sheetData>
<drawing r:id="rIdDraw"/></worksheet>`),
		"xl/worksheets/sheet2.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="inlineStr"><is><t>第二表</t></is></c></row>
</sheetData></worksheet>`),
		"xl/worksheets/_rels/sheet1.xml.rels": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rIdCom" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/comments" Target="../comments1.xml"/>
  <Relationship Id="rIdDraw" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/drawing" Target="../drawings/drawing1.xml"/>
  <Relationship Id="rIdTC" Type="http://schemas.microsoft.com/office/2017/10/relationships/threadedComment" Target="../threadedComments/threadedComment1.xml"/>
</Relationships>`),
		"xl/comments1.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<comments xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<authors><author>审稿人</author><author>复核人</author></authors>
<commentList>
<comment ref="A1" authorId="0"><text><r><rPr><b/></rPr><t>这段口径不对</t></r><r><t xml:space="preserve"> 请核对</t></r></text></comment>
<comment ref="A2" authorId="1"><text><t>隐藏行里的数字请确认</t></text></comment>
</commentList></comments>`),
		"xl/threadedComments/threadedComment1.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<ThreadedComments xmlns="http://schemas.microsoft.com/office/spreadsheetml/2018/threadedcomments">
<threadedComment ref="A1" personId="{p1}" id="{t1}" date="2026-10-11T02:00:00Z"><text>@复核人 请对齐合同</text></threadedComment>
<threadedComment ref="A1" personId="{p2}" id="{t2}" parentId="{t1}" date="2026-10-11T03:00:00Z"><text>已按 3.2 条修正</text></threadedComment>
</ThreadedComments>`),
		"xl/persons/person.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<personList xmlns="http://schemas.microsoft.com/office/spreadsheetml/2018/threadedcomments">
<person displayName="张三" id="{p1}"/><person displayName="李四" id="{p2}"/>
</personList>`),
		"xl/drawings/drawing1.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<xdr:wsDr xmlns:xdr="http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<xdr:twoCellAnchor><xdr:pic><xdr:blipFill><a:blip r:embed="rIdPic"/></xdr:blipFill></xdr:pic><xdr:clientData/></xdr:twoCellAnchor>
<xdr:twoCellAnchor><xdr:sp><xdr:txBody><a:bodyPr/><a:p><a:r><a:t>文本框说明一</a:t></a:r></a:p><a:p><a:r><a:t>文本框说明二</a:t></a:r></a:p></xdr:txBody></xdr:sp><xdr:clientData/></xdr:twoCellAnchor>
</xdr:wsDr>`),
		"xl/drawings/_rels/drawing1.xml.rels": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rIdPic" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/image1.png"/>
</Relationships>`),
		"xl/media/image1.png": png1x1(t),
	}
	for k, v := range extra {
		parts[k] = v
	}
	return parts
}

func annNotes(v *sdk.DocView) []string {
	var out []string
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockNote {
			out = append(out, b.Text)
		}
	}
	return out
}

func containsSub(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestXLSXAnnotationsCommentsTextBoxImageHidden:四类此前静默丢失的内容都应可见。
func TestXLSXAnnotationsCommentsTextBoxImageHidden(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "ann.xlsx", zipParts(t, xlsxAnnFixture(t, nil)))
	v := preview(t, s, p)

	notes := annNotes(v)
	// ① legacy 批注:作者名解析 + 多 run 文本拼接
	if !containsSub(notes, "批注 A1(审稿人): 这段口径不对 请核对") {
		t.Fatalf("legacy 批注(含作者与多 run)应可见: %v", notes)
	}
	if !containsSub(notes, "批注 A2(复核人)") {
		t.Fatalf("第二条批注应解析作者: %v", notes)
	}
	// ② 回复式批注:按单元格合并、显示名 + 回复标记
	if !containsSub(notes, "回复式批注 A1: 张三: @复核人 请对齐合同 | 李四(回复): 已按 3.2 条修正") {
		t.Fatalf("回复式批注应含作者与回复串: %v", notes)
	}
	// ③ 文本框:同一 txBody 内多段落合并
	if !containsSub(notes, "文本框: 文本框说明一 文本框说明二") {
		t.Fatalf("文本框文字应可见: %v", notes)
	}
	// ④ 隐藏行列提示
	if !containsSub(notes, "隐藏行 1 行、隐藏列 1 组") {
		t.Fatalf("隐藏行列应有提示: %v", notes)
	}
	if !containsSub(v.Warnings, "隐藏行/列") {
		t.Fatalf("隐藏行列应进 warning: %v", v.Warnings)
	}
	// ⑤ 内嵌图片 → image 块 + DocAsset(与 docx 同构)
	var img *sdk.DocBlock
	for i := range v.Blocks {
		if v.Blocks[i].Kind == sdk.DocBlockImage {
			img = &v.Blocks[i]
		}
	}
	if img == nil || img.Asset == nil || img.Asset.ID == "" {
		t.Fatalf("内嵌图片应产出带 DocAsset 的 image 块: %+v", v.Blocks)
	}
	if img.Asset.W != 1 || img.Asset.H != 1 {
		t.Fatalf("图片尺寸应探测到 1×1: %+v", img.Asset)
	}
	if !strings.HasSuffix(img.Text, "image1.png") {
		t.Fatalf("图片块名应为部件名: %q", img.Text)
	}
	// 资产可经资产端点取回(内容与源 PNG 一致)
	if rc, _, err := s.openAsset(t.Context(), assetRef{kind: assetKindZip, doc: p, abs: p, part: "xl/media/image1.png", mime: "image/png"}); err == nil {
		buf := make([]byte, 64)
		n, _ := rc.Read(buf)
		rc.Close()
		if n == 0 {
			t.Fatal("资产读取为空")
		}
	} else {
		t.Fatalf("资产应可打开: %v", err)
	}
}

// TestXLSXAnnotationsOnlyCurrentSheet:批注/图片随工作表走(第二表无批注 → 不串味)。
func TestXLSXAnnotationsOnlyCurrentSheet(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "ann.xlsx", zipParts(t, xlsxAnnFixture(t, nil)))
	v := preview(t, s, p, sdk.DocRequest{Sheet: 1})
	if got := annNotes(v); len(got) != 0 {
		t.Fatalf("第二表不应带出第一表批注: %v", got)
	}
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockImage {
			t.Fatalf("第二表不应带出第一表图片: %+v", b)
		}
	}
}

// TestXLSXAnnotationsImageBudget:图片超预算 → 占位 + 告警(不静默)。
func TestXLSXAnnotationsImageBudget(t *testing.T) {
	extra := map[string][]byte{
		"xl/drawings/drawing1.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<xdr:wsDr xmlns:xdr="http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<xdr:twoCellAnchor><xdr:pic><xdr:blipFill><a:blip r:embed="rIdPic"/></xdr:blipFill></xdr:pic></xdr:twoCellAnchor>
<xdr:twoCellAnchor><xdr:pic><xdr:blipFill><a:blip r:embed="rIdPic2"/></xdr:blipFill></xdr:pic></xdr:twoCellAnchor>
</xdr:wsDr>`),
		"xl/drawings/_rels/drawing1.xml.rels": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rIdPic" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/image1.png"/>
  <Relationship Id="rIdPic2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/image2.png"/>
</Relationships>`),
		"xl/media/image2.png": png1x1(t),
	}
	s, dir := newSvc(t, Budget{MaxAssets: 1})
	p := writeFile(t, dir, "ann2.xlsx", zipParts(t, xlsxAnnFixture(t, extra)))
	v := preview(t, s, p)
	var imgs int
	var withAsset int
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockImage {
			imgs++
			if b.Asset != nil && b.Asset.ID != "" {
				withAsset++
			}
		}
	}
	if imgs != 2 || withAsset != 1 {
		t.Fatalf("超预算应第二张只给占位: imgs=%d withAsset=%d", imgs, withAsset)
	}
	if !containsSub(v.Warnings, "超出预算") {
		t.Fatalf("超预算应有告警: %v", v.Warnings)
	}
}

// TestXLSXAnnotationsBrokenParts:Tolerant —— 部件坏/缺 rels 不得 panic,仅告警。
func TestXLSXAnnotationsBrokenParts(t *testing.T) {
	extra := map[string][]byte{
		"xl/comments1.xml":                         []byte(`<comments><commentList><comment ref="A1"`),
		"xl/threadedComments/threadedComment1.xml": []byte(`<ThreadedComments><threadedComment`),
		"xl/drawings/drawing1.xml":                 []byte(`<xdr:wsDr><xdr:txBody><a:t>截断`),
	}
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "bad.xlsx", zipParts(t, xlsxAnnFixture(t, extra)))
	v := preview(t, s, p) // 不得 panic/报错
	if len(v.Blocks) == 0 {
		t.Fatal("坏部件仍应有工作表块")
	}
}
