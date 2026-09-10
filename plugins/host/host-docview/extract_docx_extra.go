// docx 附加内容抽取(DOC-3a;DOC-2 同构的零依赖窄修)。
//
// 背景:D6-3 判定实测(DEsign §14.1)显示 docx 侧此三类内容此前只告警或静默丢失:
//
//	① 正文批注 `word/comments.xml`(仅告警「本期不解析」,文本不可见);
//	② 现代**回复式批注**(`word/commentsExtended.xml` 的 paraId/parentParaId 线程关系
//	   + `word/people.xml` 人员显示名)—— 完全未处理(批注文本仍只在 comments.xml);
//	③ 正文**文本框** `w:txbxContent`(抽取器零命中)→ 文字只在该子树里。
//
// 本文件只做「读文本 → note 块」,不做批注气泡/版式还原;页眉/页脚内的文本框仍属
// 「未解析部件」告警范围(探针 severityFor 亦把 aux 部件降为 info)。
//
// 纪律:零第三方依赖(stdlib zip/xml);部件读取走 `o.reader` 预算;独立流式扫描,
// 不改动正文解析器(docxParser)的状态机(文本框可嵌在任意段落/图形子树里)。
package hostdocview

import (
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// docxComment 一条批注(legacy 与现代回复式共用同一承载部件 comments.xml)。
type docxComment struct {
	ID       string // w:id
	Author   string // w:author(显示名;缺失时回落 people.xml)
	Date     string // w:date
	Text     string // 全部 w:t 拼接(段落间以空格分隔)
	ParaID   string // 首个段落的 w14:paraId(回复线程关系键)
	ParentID string // 由 commentsExtended.xml 回填
}

// docxAnnotations 抽取正文批注(legacy + 回复式)与文本框;返回追加块与警告。
func docxAnnotations(o *ooxml, mainPart string) ([]sdk.DocBlock, []string) {
	var blocks []sdk.DocBlock
	var warns []string

	// ① 批注部件:优先按主要部件的关系表(严格口径);rels 缺失时回退固定名。
	parts := []string{}
	for _, rel := range o.rels(mainPart) {
		if strings.HasSuffix(rel.Type, "/comments") {
			parts = append(parts, rel.Target)
		}
	}
	if len(parts) == 0 && o.has("word/comments.xml") {
		parts = append(parts, "word/comments.xml")
	}
	people := docxPersonNames(o)
	for _, part := range parts {
		if !o.has(part) {
			continue
		}
		cs, err := docxParseComments(o, part)
		if err != nil {
			warns = append(warns, fmt.Sprintf("docx 批注部件 %s 解析失败: %v", part, err))
			continue
		}
		if len(cs) == 0 {
			o.addWarning(fmt.Sprintf("docx 含批注部件 %s 但未解析出批注文本", part))
			continue
		}
		ext := docxCommentsExtended(o)
		blocks = append(blocks, docxCommentBlocks(cs, ext, people)...)
	}

	// ② 文本框:正文 document.xml 内 w:txbxContent 的文字
	texts, err := docxParseTextBoxes(o, mainPart)
	if err != nil {
		o.addWarning(fmt.Sprintf("docx 文本框扫描失败: %v", err))
	}
	for _, t := range texts {
		blocks = append(blocks, sdk.DocBlock{Kind: sdk.DocBlockNote, Text: "文本框: " + t})
	}
	return blocks, warns
}

// docxCommentBlocks 批注 → note 块:无线程关系者单独一条;同一线程(paraId/parentParaId)
// 合并为一条并标注「(回复)」,与 xlsx 回复式批注口径一致。
func docxCommentBlocks(cs []docxComment, ext map[string]string, people map[string]string) []sdk.DocBlock {
	byPara := map[string]docxComment{}
	children := map[string][]string{} // 根 paraId → 回复 paraId(保序)
	var order []string
	for _, c := range cs {
		if c.ParaID != "" {
			byPara[c.ParaID] = c
		}
		if p := ext[c.ParaID]; p != "" {
			children[p] = append(children[p], c.ParaID)
			order = append(order, p)
		}
	}
	author := func(c docxComment) string {
		if people != nil {
			if dn, ok := people[c.Author]; ok && dn != "" {
				return dn
			}
		}
		if c.Author == "" {
			return "未知作者"
		}
		return c.Author
	}
	var out []sdk.DocBlock
	emitted := map[string]bool{}
	// 线程根(被引用为 parent 的 paraId)按出现顺序输出
	for _, rootID := range order {
		if emitted[rootID] {
			continue
		}
		emitted[rootID] = true
		root, ok := byPara[rootID]
		if !ok {
			continue
		}
		var parts []string
		parts = append(parts, author(root)+": "+root.Text)
		for _, rid := range children[rootID] {
			emitted[rid] = true
			r := byPara[rid]
			parts = append(parts, author(r)+"(回复): "+r.Text)
		}
		out = append(out, sdk.DocBlock{Kind: sdk.DocBlockNote,
			Text: fmt.Sprintf("回复式批注 %s: %s", commentLabel(root), strings.Join(parts, " | "))})
	}
	// 无参与任何线程关系的批注单独输出
	for _, c := range cs {
		if c.ParaID != "" && emitted[c.ParaID] {
			continue
		}
		if p := ext[c.ParaID]; p != "" {
			continue // 归属线程但根缺失(异常文件):交给下面的兜底
		}
		if len(children[c.ParaID]) > 0 {
			continue
		}
		out = append(out, sdk.DocBlock{Kind: sdk.DocBlockNote,
			Text: fmt.Sprintf("批注 %s(%s): %s", commentLabel(c), author(c), c.Text)})
	}
	return out
}

// commentLabel 批注标识:优先 w:id(与 Word 批注序号一致),缺失回落 paraId 前 8 位。
func commentLabel(c docxComment) string {
	if c.ID != "" {
		return c.ID
	}
	if len(c.ParaID) > 8 {
		return c.ParaID[:8]
	}
	return c.ParaID
}

// docxParseComments 解析批注部件:每 `<w:comment w:id w:author w:date>` 的
// 全部 `w:t` 文本(段落边界 → 空格)+ 首个段落 `w14:paraId`。
func docxParseComments(o *ooxml, part string) ([]docxComment, error) {
	rc, err := o.reader(part)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false
	var out []docxComment
	var cur *docxComment
	var sb strings.Builder
	inT := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "comment":
				cur = &docxComment{ID: xmlAttr(t, "id"), Author: xmlAttr(t, "author"), Date: xmlAttr(t, "date")}
				sb.Reset()
			case "p":
				if cur != nil {
					if cur.ParaID == "" {
						cur.ParaID = xmlAttr(t, "paraId")
					}
					if sb.Len() > 0 {
						sb.WriteString(" ")
					}
				}
			case "t":
				if cur != nil {
					inT = true
				}
			case "tab", "br", "cr":
				if cur != nil {
					sb.WriteString(" ")
				}
			}
		case xml.CharData:
			if cur != nil && inT {
				sb.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "comment":
				if cur != nil {
					cur.Text = collapseSpace(sb.String())
					if cur.Text != "" {
						out = append(out, *cur)
					}
					cur = nil
				}
			}
		}
	}
	return out, nil
}

// docxCommentsExtended 解析 `word/commentsExtended.xml` → paraId → parentParaId。
func docxCommentsExtended(o *ooxml) map[string]string {
	out := map[string]string{}
	for _, part := range []string{"word/commentsExtended.xml", "word/commentsExtensible.xml"} {
		if !o.has(part) {
			continue
		}
		rc, err := o.reader(part)
		if err != nil {
			continue
		}
		dec := xml.NewDecoder(rc)
		dec.Strict = false
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			start, ok := tok.(xml.StartElement)
			if !ok {
				continue
			}
			if start.Name.Local != "commentEx" {
				continue
			}
			pid, parent := xmlAttr(start, "paraId"), xmlAttr(start, "parentParaId")
			if pid != "" && parent != "" {
				out[pid] = parent
			}
		}
		rc.Close()
	}
	return out
}

// docxPersonNames 解析 `word/people.xml` → author(或 id)→ 显示名。
func docxPersonNames(o *ooxml) map[string]string {
	out := map[string]string{}
	for _, part := range []string{"word/people.xml"} {
		if !o.has(part) {
			continue
		}
		rc, err := o.reader(part)
		if err != nil {
			continue
		}
		var pl struct {
			Persons []struct {
				Author       string   `xml:"author,attr"`
				DisplayName  string   `xml:"displayName,attr"`
				Contact      string   `xml:"contact,attr"`
				ProviderID   string   `xml:"providerId,attr"`
				UserID       string   `xml:"userId,attr"`
				PresenceInfo struct{} `xml:"presenceInfo"`
			} `xml:"person"`
		}
		err = xml.NewDecoder(rc).Decode(&pl)
		rc.Close()
		if err != nil {
			o.addWarning(fmt.Sprintf("docx people 部件 %s 解析失败: %v", part, err))
			continue
		}
		for _, p := range pl.Persons {
			name := p.DisplayName
			if name == "" {
				name = p.Author
			}
			if name == "" {
				continue
			}
			if p.Author != "" {
				out[p.Author] = name
			}
			if p.Contact != "" {
				out[p.Contact] = name
			}
			if p.UserID != "" {
				out[p.UserID] = name
			}
		}
	}
	return out
}

// docxParseTextBoxes 扫描正文取全部 `w:txbxContent` 的文字(每个文本框一条;去重:
// Word 的 mc:AlternateContent 会把同一文本框同时写进 Choice 与 Fallback)。
func docxParseTextBoxes(o *ooxml, part string) ([]string, error) {
	rc, err := o.reader(part)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false
	var out []string
	seen := map[string]bool{}
	inBox := 0
	inT := false
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "txbxContent":
				inBox++
				if inBox == 1 {
					sb.Reset()
				}
			case "p":
				if inBox > 0 && sb.Len() > 0 {
					sb.WriteString(" ")
				}
			case "t":
				if inBox > 0 {
					inT = true
				}
			case "tab", "br", "cr":
				if inBox > 0 {
					sb.WriteString(" ")
				}
			}
		case xml.CharData:
			if inBox > 0 && inT {
				sb.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "txbxContent":
				if inBox > 0 {
					inBox--
					if inBox == 0 {
						text := collapseSpace(sb.String())
						if text != "" && !seen[text] {
							seen[text] = true
							out = append(out, text)
						}
					}
				}
			}
		}
	}
	return out, nil
}

// collapseSpace 折叠全部空白(批注/文本框文本单行化,便于 note 块渲染)。
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
