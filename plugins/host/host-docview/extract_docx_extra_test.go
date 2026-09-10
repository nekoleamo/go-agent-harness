// DOC-3a 单测:docx 批注(legacy + 回复式)/文本框 抽取。
// 夹具为测试内构造的 OOXML 容器(确定性、无二进制入库);判定依据见 DESIGN §14.1。
package hostdocview

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// docxAnnFixture 一份含批注(含回复线程)、people 显示名与文本框的 docx 部件集。
func docxAnnFixture(t *testing.T) map[string][]byte {
	t.Helper()
	return map[string][]byte{
		"[Content_Types].xml": []byte(docxContentTypes + `
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`),
		"_rels/.rels": []byte(docxRootRels),
		"word/document.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
            xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"
            xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"
            xmlns:v="urn:schemas-microsoft-com:vml">
<w:body>
<w:p><w:r><w:t>正文第一段</w:t></w:r>
  <w:r><mc:AlternateContent>
    <mc:Choice Requires="wps"><w:drawing><wp:inline><a:graphic xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:graphicData>
      <wps:wsp xmlns:wps="http://schemas.microsoft.com/office/word/2010/wordprocessingShape"><wps:txbx>
        <w:txbxContent><w:p><w:r><w:t>文本框甲 说明一</w:t></w:r></w:p><w:p><w:r><w:t>说明二</w:t></w:r></w:p></w:txbxContent>
      </wps:txbx></wps:wsp>
    </a:graphicData></a:graphic></wp:inline></w:drawing></mc:Choice>
    <mc:Fallback><w:pict><v:shape><v:textbox>
      <w:txbxContent><w:p><w:r><w:t>文本框甲 说明一</w:t></w:r></w:p><w:p><w:r><w:t>说明二</w:t></w:r></w:p></w:txbxContent>
    </v:textbox></v:shape></w:pict></mc:Fallback>
  </mc:AlternateContent></w:r>
</w:p>
<w:p><w:r><w:t>正文第二段</w:t></w:r></w:p>
</w:body></w:document>`),
		"word/_rels/document.xml.rels": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rIdC" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/comments" Target="comments.xml"/>
</Relationships>`),
		"word/comments.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:comments xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
            xmlns:w14="http://schemas.microsoft.com/office/word/2010/wordml">
<w:comment w:id="1" w:author="张三" w:date="2026-10-11T02:00:00Z" w14:paraId="11111111">
  <w:p w14:paraId="11111111"><w:r><w:t>这里的口径要跟合同附件对齐</w:t></w:r></w:p>
</w:comment>
<w:comment w:id="2" w:author="李四" w:date="2026-10-11T03:00:00Z" w14:paraId="22222222">
  <w:p w14:paraId="22222222"><w:r><w:t>已按 3.2 条</w:t></w:r><w:r><w:t>修正</w:t></w:r></w:p>
  <w:p><w:r><w:t>请复核</w:t></w:r></w:p>
</w:comment>
<w:comment w:id="3" w:author="lisi@example.com" w:date="2026-10-11T04:00:00Z" w14:paraId="33333333">
  <w:p w14:paraId="33333333"><w:r><w:t>独立批注(无回复关系)</w:t></w:r></w:p>
</w:comment>
</w:comments>`),
		"word/commentsExtended.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w15:commentsEx xmlns:w15="http://schemas.microsoft.com/office/word/2012/wordml">
  <w15:commentEx w15:paraId="22222222" w15:done="0" w15:parentParaId="11111111"/>
</w15:commentsEx>`),
		"word/people.xml": []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w15:people xmlns:w15="http://schemas.microsoft.com/office/word/2012/wordml">
  <w15:person w15:author="lisi@example.com" w15:contact="lisi@example.com"><w15:presenceInfo w15:providerId="None" w15:userId="lisi@example.com"/></w15:person>
</w15:people>`),
	}
}

// TestDOCXAnnotationsCommentsAndTextBox:批注(含回复线程合并 + 显示名回落)与文本框都应可见。
func TestDOCXAnnotationsCommentsAndTextBox(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "ann.docx", zipParts(t, docxAnnFixture(t)))
	v := preview(t, s, p)
	notes := annNotes(v)

	// �threaded 回复合并为一条(根 + 回复),并标注「(回复)」
	if !containsSub(notes, "回复式批注 1: 张三: 这里的口径要跟合同附件对齐 | 李四(回复): 已按 3.2 条修正 请复核") {
		t.Fatalf("回复式批注应合并线程并保留作者: %v", notes)
	}
	// 独立批注单独一条;author 为邮箱时经 people.xml 回落显示名(此处 people 未登记该邮箱 → 原样)
	if !containsSub(notes, "批注 3(lisi@example.com): 独立批注(无回复关系)") {
		t.Fatalf("独立批注应单独输出: %v", notes)
	}
	// 文本框(AlternateContent 的 Choice/Fallback 重复内容应被去重)
	if !containsSub(notes, "文本框: 文本框甲 说明一 说明二") {
		t.Fatalf("文本框文字应可见: %v", notes)
	}
	if n := strings.Count(strings.Join(notes, "\n"), "文本框甲"); n != 1 {
		t.Fatalf("Choice/Fallback 重复文本应去重,实得 %d 次", n)
	}
	// 已解析批注 → 不再出现「含批注,本期不解析」的误导告警
	if containsSub(v.Warnings, "含批注") {
		t.Fatalf("批注已解析,不应再报未解析: %v", v.Warnings)
	}
	// 正文块不受影响
	var bodyChars int
	for _, b := range v.Blocks {
		if b.Kind == sdk.DocBlockParagraph {
			bodyChars += len([]rune(b.Text))
		}
	}
	if bodyChars == 0 {
		t.Fatal("正文段落应保持可见")
	}
}

// TestDOCXAnnotationsMissingParts:无批注/无文本框的普通文档不得新增告警或块。
func TestDOCXAnnotationsMissingParts(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	plain := map[string][]byte{
		"[Content_Types].xml":          []byte(docxContentTypes),
		"_rels/.rels":                  []byte(docxRootRels),
		"word/document.xml":            []byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>只有正文</w:t></w:r></w:p></w:body></w:document>`),
		"word/_rels/document.xml.rels": []byte(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>`),
	}
	p := writeFile(t, dir, "plain.docx", zipParts(t, plain))
	v := preview(t, s, p)
	for _, n := range annNotes(v) {
		if strings.HasPrefix(n, "批注") || strings.HasPrefix(n, "文本框") || strings.HasPrefix(n, "回复式批注") {
			t.Fatalf("普通文档不应有批注/文本框块: %v", n)
		}
	}
	if containsSub(v.Warnings, "批注") || containsSub(v.Warnings, "文本框") {
		t.Fatalf("普通文档不应有相关告警: %v", v.Warnings)
	}
}

// TestDOCXAnnotationsBrokenParts:坏部件/缺 people 不得 panic,仅告警。
func TestDOCXAnnotationsBrokenParts(t *testing.T) {
	fix := docxAnnFixture(t)
	fix["word/commentsExtended.xml"] = []byte(`<w15:commentsEx><w15:commentEx`)
	fix["word/people.xml"] = []byte(`<w15:people><w15:person`)
	fix["word/comments.xml"] = []byte(`<w:comments><w:comment w:id="9" w:author="甲"><w:p><w:r><w:t>截断`)
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "broken.docx", zipParts(t, fix))
	v := preview(t, s, p) // 不得 panic
	if len(v.Blocks) == 0 {
		t.Fatal("坏部件仍应有正文块")
	}
	// 截断的 comments.xml 仍能取到已闭合的文本?此处未闭合 → 至少不应崩溃/不应误导为已解析
	if containsSub(v.Warnings, "含批注,本期不解析") {
		t.Fatalf("告警口径应已更新: %v", v.Warnings)
	}
}

// TestDOCXCommentsExtendedOnly:仅 commentsExtended(无线程指向的孤立 paraId)不应误判。
func TestDOCXCommentsExtendedOnly(t *testing.T) {
	fix := docxAnnFixture(t)
	fix["word/commentsExtended.xml"] = []byte(`<w15:commentsEx xmlns:w15="http://schemas.microsoft.com/office/word/2012/wordml">
  <w15:commentEx w15:paraId="99999999" w15:parentParaId="88888888"/>
</w15:commentsEx>`)
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "orphan.docx", zipParts(t, fix))
	v := preview(t, s, p)
	notes := annNotes(v)
	if !containsSub(notes, "批注 1(张三)") || !containsSub(notes, "批注 2(李四)") {
		t.Fatalf("无线程关系的批注应各自单独输出: %v", notes)
	}
}

// TestDOCXAlternateContentRules:① Choice+Fallback 双写 → 只保留一份(文本/文本框);
// ② 纯 Fallback(无 Choice)内容必须保留(不得因去重而丢内容)。
func TestDOCXAlternateContentRules(t *testing.T) {
	body := func(inner string) map[string][]byte {
		return map[string][]byte{
			"[Content_Types].xml":          []byte(docxContentTypes),
			"_rels/.rels":                  []byte(docxRootRels),
			"word/document.xml":            []byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"><w:body>` + inner + `</w:body></w:document>`),
			"word/_rels/document.xml.rels": []byte(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>`),
		}
	}
	ac := func(choice, fallback string) string {
		return `<w:p><w:r><mc:AlternateContent><mc:Choice Requires="wps">` + choice + `</mc:Choice><mc:Fallback>` + fallback + `</mc:Fallback></mc:AlternateContent></w:r></w:p>`
	}
	// ① 双写:正文只出现一次
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "ac.docx", zipParts(t, body(ac(`<w:t>双写内容</w:t>`, `<w:t>双写内容</w:t>`))))
	v := preview(t, s, p)
	var n int
	for _, b := range v.Blocks {
		if strings.Contains(b.Text, "双写内容") {
			n += strings.Count(b.Text, "双写内容")
		}
	}
	if n != 1 {
		t.Fatalf("Choice+Fallback 双写应只保留一份,实得 %d 次", n)
	}
	// ② 纯 Fallback(无 Choice):内容必须保留
	p2 := writeFile(t, dir, "ac2.docx", zipParts(t, body(
		`<w:p><w:r><mc:AlternateContent><mc:Fallback><w:t>仅 Fallback 内容</w:t></mc:Fallback></mc:AlternateContent></w:r></w:p>`)))
	v2 := preview(t, s, p2)
	found := false
	for _, b := range v2.Blocks {
		if strings.Contains(b.Text, "仅 Fallback 内容") {
			found = true
		}
	}
	if !found {
		t.Fatalf("纯 Fallback 内容不得丢失: %+v", v2.Blocks)
	}
}
