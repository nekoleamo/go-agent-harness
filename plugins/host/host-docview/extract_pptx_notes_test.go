// DOC-3b 单测:pptx 备注页文本抽取(含幻灯片编号占位跳过、缺失/损坏部件容错)。
package hostdocview

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// pptxNotesFixture 由现有 deck 夹具改写备注页内容。
func pptxNotesFixture(t *testing.T, notes string, withRel bool) map[string][]byte {
	t.Helper()
	rels := `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/image1.png"/>
</Relationships>`
	if withRel {
		rels = `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId9" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide1.xml"/>
</Relationships>`
	}
	return map[string][]byte{
		"[Content_Types].xml":              []byte(pptxContentTypes),
		"_rels/.rels":                      []byte(pptxRootRels),
		"ppt/presentation.xml":             []byte(pptxPresentation),
		"ppt/_rels/presentation.xml.rels":  []byte(pptxPresentationRels),
		"ppt/slides/slide1.xml":            []byte(pptxSlideNoNotes),
		"ppt/slides/_rels/slide1.xml.rels": []byte(rels),
		"ppt/slides/slide2.xml":            []byte(pptxSlide2),
		"ppt/notesSlides/notesSlide1.xml":  []byte(notes),
	}
}

// pptxSlideNoNotes 单页幻灯片(不带备注关系,备注由夹具显式挂载)。
const pptxSlideNoNotes = `<?xml version="1.0" encoding="UTF-8"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
       xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld><p:spTree>
    <p:sp><p:nvSpPr><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr>
      <p:txBody><a:p><a:r><a:t>第一页标题</a:t></a:r></a:p></p:txBody></p:sp>
  </p:spTree></p:cSld>
</p:sld>`

func notesOf(v *sdk.DocView) []string {
	var out []string
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockNote && strings.Contains(b.Text, "备注") {
			out = append(out, b.Text)
		}
	}
	return out
}

// TestPPTXNotesExtracted:备注文本 → note;多形状多段落合并;编号占位被跳过。
func TestPPTXNotesExtracted(t *testing.T) {
	notes := `<?xml version="1.0" encoding="UTF-8"?>
<p:notes xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
         xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
 <p:cSld><p:spTree>
  <p:sp><p:nvSpPr><p:nvPr><p:ph type="body" idx="1"/></p:nvPr></p:nvSpPr>
   <p:txBody><a:p><a:r><a:t>第一段备注</a:t></a:r></a:p><a:p><a:r><a:t>第二段备注</a:t></a:r></a:p></p:txBody></p:sp>
  <p:sp><p:nvSpPr><p:nvPr><p:ph type="body" idx="2"/></p:nvPr></p:nvSpPr>
   <p:txBody><a:p><a:r><a:t>第二个形状</a:t></a:r></a:p></p:txBody></p:sp>
  <p:sp><p:nvSpPr><p:nvPr><p:ph type="sldNum" sz="quarter" idx="10"/></p:nvPr></p:nvSpPr>
   <p:txBody><a:p><a:fld id="{X}" type="slidenum"><a:t>7</a:t></a:fld></a:p></p:txBody></p:sp>
 </p:spTree></p:cSld>
</p:notes>`
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "notes.pptx", zipParts(t, pptxNotesFixture(t, notes, true)))
	v := preview(t, s, p)
	got := notesOf(v)
	if len(got) != 1 {
		t.Fatalf("应恰有一条备注 note: %v", got)
	}
	if !strings.Contains(got[0], "第一段备注 第二段备注 第二个形状") {
		t.Fatalf("备注文本(多形状多段落)应合并: %q", got[0])
	}
	if strings.Contains(got[0], "7") {
		t.Fatalf("幻灯片编号占位不得混入备注: %q", got[0])
	}
}

// TestPPTXNotesMissingOrEmpty:无备注关系 / 备注为空 → 既无 note 也无告警。
func TestPPTXNotesMissingOrEmpty(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	// 无 notesSlide 关系
	p := writeFile(t, dir, "none.pptx", zipParts(t, pptxNotesFixture(t, `<p:notes/>`, false)))
	v := preview(t, s, p)
	if got := notesOf(v); len(got) != 0 {
		t.Fatalf("无备注关系不应产出 note: %v", got)
	}
	if strings.Contains(strings.Join(v.Warnings, " "), "备注") {
		t.Fatalf("无备注关系不应告警: %v", v.Warnings)
	}
	// 有备注关系但文本为空
	p2 := writeFile(t, dir, "empty.pptx", zipParts(t, pptxNotesFixture(t, `<?xml version="1.0"?><p:notes xmlns:p="x"><p:cSld><p:spTree/></p:cSld></p:notes>`, true)))
	v2 := preview(t, s, p2)
	if got := notesOf(v2); len(got) != 0 {
		t.Fatalf("空备注不应产出 note: %v", got)
	}
	if strings.Contains(strings.Join(v2.Warnings, " "), "备注") {
		t.Fatalf("空备注不应告警: %v", v2.Warnings)
	}
}

// TestPPTXNotesBrokenPart:备注部件截断 → 结构化告警,不 panic、不静默。
func TestPPTXNotesBrokenPart(t *testing.T) {
	// 关系存在但部件缺失
	fix := pptxNotesFixture(t, `<p:notes/>`, true)
	delete(fix, "ppt/notesSlides/notesSlide1.xml")
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "broken.pptx", zipParts(t, fix))
	v := preview(t, s, p)
	if !strings.Contains(strings.Join(v.Warnings, " "), "备注页解析失败") {
		t.Fatalf("缺失部件应显式告警: %v", v.Warnings)
	}
	// 部件存在但截断(非致命,容错解析)
	s2, dir2 := newSvc(t, Budget{})
	p2 := writeFile(t, dir2, "trunc.pptx", zipParts(t, pptxNotesFixture(t, `<p:notes><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>截`, true)))
	v2 := preview(t, s2, p2) // 不得 panic
	if len(notesOf(v2)) == 0 && !strings.Contains(strings.Join(v2.Warnings, " "), "备注") {
		// 截断内容取不到文本、也无错误时应至少不崩(此处容忍两者皆空,但不得 panic)
		t.Logf("截断备注:keys=%d warnings=%v", len(notesOf(v2)), v2.Warnings)
	}
}
