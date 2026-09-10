// xlsx 附加内容抽取(DOC-2;D6-3 判定实测后的零依赖窄修)。
//
// 背景:判定实测(见 DESIGN §14.1「D6-3 判定实测」)确认自研 xlsx 抽取器**静默丢失**四类内容,
// 而 docx 侧早有对应口径(批注=显式告警、图片=DocAsset 资产端点),xlsx 侧两者皆无:
//
//	① 单元格批注:legacy `xl/comments*.xml`(含 authors)与**现代回复式批注**
//	   `xl/threadedComments/*` + `xl/persons/person.xml`(文字只在这些部件里,单元格中不存在);
//	② drawing 文本框 `xdr:txBody` 的文字(只在 `xl/drawings/*.xml`);
//	③ 内嵌图片 `xl/media/*`(visual 唯一承载;复用 docx 的资产预算链路);
//	④ 隐藏行/列(内容照常显示但此前无任何提示)。
//
// 图表/透视表**不在本修范围**:实测其数据/结果仍在单元格中可见(视觉性损失),已登记为接受取舍。
//
// 纪律:零第三方依赖(stdlib zip/xml);所有部件读取过 `o.reader` 预算;批注/文本框以
// note 块输出(文本可见),图片以 image 块 + `DocAsset` 输出(与 docx 同构)。
package hostdocview

import (
	"encoding/xml"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// xlsxComment 一条 legacy 批注。
type xlsxComment struct {
	Ref      string
	AuthorID string
	Text     string
}

// xlsxThreadedComment 一条现代回复式批注(可带 parentId 形成回复串)。
type xlsxThreadedComment struct {
	Ref      string
	PersonID string
	ParentID string
	Text     string
}

// xlsxSheetAnnotations 抽取当前工作表的批注/文本框/内嵌图片/隐藏行列(blocks 追加 + 警告)。
// hiddenRows/hiddenCols 由工作表尺寸扫描顺带统计(0 = 无)。
func xlsxSheetAnnotations(s *Service, o *ooxml, sheetPart, sheetName string, hiddenRows, hiddenCols int) ([]sdk.DocBlock, []string) {
	var blocks []sdk.DocBlock
	var warns []string

	for _, rel := range xlsxSheetRels(o, sheetPart) {
		switch {
		case strings.HasSuffix(rel.Type, "/comments"):
			cs, err := xlsxParseComments(o, rel.Target)
			if err != nil {
				o.addWarning(fmt.Sprintf("xlsx 批注部件 %s 解析失败: %v", rel.Target, err))
				continue
			}
			for _, c := range cs {
				author := ""
				if c.AuthorID != "" {
					author = "(" + c.AuthorID + ")"
				}
				blocks = append(blocks, sdk.DocBlock{
					Kind: sdk.DocBlockNote,
					Text: fmt.Sprintf("批注 %s%s: %s", c.Ref, author, c.Text),
				})
			}
		case strings.Contains(rel.Type, "threadedComment"):
			tcs, err := xlsxParseThreadedComments(o, rel.Target)
			if err != nil {
				o.addWarning(fmt.Sprintf("xlsx 回复式批注部件 %s 解析失败: %v", rel.Target, err))
				continue
			}
			authors := xlsxPersonNames(o)
			blocks = append(blocks, xlsxThreadedBlocks(tcs, authors)...)
		case strings.HasSuffix(rel.Type, "/drawing"):
			texts, pics, err := xlsxParseDrawing(o, rel.Target)
			if err != nil {
				o.addWarning(fmt.Sprintf("xlsx drawing 部件 %s 解析失败: %v", rel.Target, err))
				continue
			}
			for _, t := range texts {
				blocks = append(blocks, sdk.DocBlock{Kind: sdk.DocBlockNote, Text: "文本框: " + t})
			}
			blocks = append(blocks, xlsxImageBlocks(s, o, pics, &warns)...)
		}
	}

	if hiddenRows > 0 || hiddenCols > 0 {
		blocks = append(blocks, sdk.DocBlock{Kind: sdk.DocBlockNote,
			Text: fmt.Sprintf("本表含隐藏行 %d 行、隐藏列 %d 组(隐藏状态未在视图中标注)", hiddenRows, hiddenCols)})
		o.addWarning("xlsx 存在隐藏行/列:内容照常显示,但隐藏状态未在预览中标注")
	}
	return blocks, warns
}

// xlsxSheetRels 当前工作表的关系条目(稳定顺序:按 Id 排序,保证输出确定性)。
func xlsxSheetRels(o *ooxml, sheetPart string) []ooxmlRel {
	m := o.rels(sheetPart)
	out := make([]ooxmlRel, 0, len(m))
	for _, rel := range m {
		if rel.Target == "" {
			continue
		}
		out = append(out, rel)
	}
	// 简单插入排序(关系数极小,避免引入 sort 依赖顺序问题)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// xlsxThreadedBlocks 回复式批注 → note 块(同一单元格的回复串合并为一条,保留作者与父子关系)。
func xlsxThreadedBlocks(cs []xlsxThreadedComment, authors map[string]string) []sdk.DocBlock {
	order := []string{}
	grouped := map[string][]xlsxThreadedComment{}
	for _, c := range cs {
		if _, ok := grouped[c.Ref]; !ok {
			order = append(order, c.Ref)
		}
		grouped[c.Ref] = append(grouped[c.Ref], c)
	}
	var out []sdk.DocBlock
	for _, ref := range order {
		var parts []string
		for _, c := range grouped[ref] {
			who := authors[c.PersonID]
			if who == "" {
				who = "未知作者"
			}
			if c.ParentID != "" {
				who += "(回复)"
			}
			parts = append(parts, who+": "+c.Text)
		}
		out = append(out, sdk.DocBlock{
			Kind: sdk.DocBlockNote,
			Text: fmt.Sprintf("回复式批注 %s: %s", ref, strings.Join(parts, " | ")),
		})
	}
	return out
}

// xlsxParseComments 解析 legacy 批注部件(comments*.xml):
// `<authors><author>名</author>…` + `<commentList><comment ref authorId><text>跑/runs…`.
func xlsxParseComments(o *ooxml, part string) ([]xlsxComment, error) {
	rc, err := o.reader(part)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false
	var authors []string
	var out []xlsxComment
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "author":
			authors = append(authors, xmlElementText(dec, start))
		case "comment":
			c := xlsxComment{Ref: xmlAttr(start, "ref"), AuthorID: xmlAttr(start, "authorId")}
			c.Text = xmlElementText(dec, start)
			if idx, err := strconv.Atoi(strings.TrimSpace(c.AuthorID)); err == nil && idx >= 0 && idx < len(authors) {
				c.AuthorID = authors[idx]
			}
			if c.Text != "" || c.Ref != "" {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// xlsxParseThreadedComments 解析现代回复式批注(threadedComments/*.xml)。
func xlsxParseThreadedComments(o *ooxml, part string) ([]xlsxThreadedComment, error) {
	rc, err := o.reader(part)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false
	var out []xlsxThreadedComment
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "threadedComment" {
			continue
		}
		c := xlsxThreadedComment{
			Ref: xmlAttr(start, "ref"), PersonID: xmlAttr(start, "personId"),
			ParentID: xmlAttr(start, "parentId"),
		}
		c.Text = xmlElementText(dec, start)
		if c.Text != "" {
			out = append(out, c)
		}
	}
	return out, nil
}

// xlsxPersonNames 解析 persons/person.xml → personId → 显示名(经 workbook rels 找部件)。
func xlsxPersonNames(o *ooxml) map[string]string {
	out := map[string]string{}
	for _, rel := range o.rels(o.mainPart("xlsx")) {
		if !strings.Contains(rel.Type, "/person") || rel.Target == "" {
			continue
		}
		rc, err := o.reader(rel.Target)
		if err != nil {
			continue
		}
		var pl struct {
			Persons []struct {
				DisplayName string `xml:"displayName,attr"`
				ID          string `xml:"id,attr"`
			} `xml:"person"`
		}
		err = xml.NewDecoder(rc).Decode(&pl)
		rc.Close()
		if err != nil {
			o.addWarning(fmt.Sprintf("xlsx persons 部件 %s 解析失败: %v", rel.Target, err))
			continue
		}
		for _, p := range pl.Persons {
			out[p.ID] = p.DisplayName
		}
	}
	return out
}

// xlsxParseDrawing 解析 drawing 部件:返回文本框文字(仅 `xdr:txBody` 内)+ 图片部件路径(去重保序)。
func xlsxParseDrawing(o *ooxml, part string) (texts []string, pics []string, err error) {
	rc, err := o.reader(part)
	if err != nil {
		return nil, nil, err
	}
	defer rc.Close()
	rels := o.rels(part)
	dec := xml.NewDecoder(rc)
	dec.Strict = false
	inTx := 0 // txBody 嵌套深度
	inT := false
	var run, tx strings.Builder // 当前 a:t / 当前 txBody(一个文本框的多段落合并为一条 note)
	seen := map[string]bool{}
	for {
		tok, terr := dec.Token()
		if terr != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Local == "txBody":
				inTx++
			case inTx > 0 && t.Name.Local == "t":
				inT = true
			case t.Name.Local == "blip":
				for _, a := range t.Attr {
					if a.Name.Local != "embed" {
						continue
					}
					if rel, ok := rels[a.Value]; ok && strings.HasSuffix(rel.Type, "/image") {
						if !seen[rel.Target] {
							seen[rel.Target] = true
							pics = append(pics, rel.Target)
						}
					}
				}
			}
		case xml.CharData:
			if inT {
				run.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				if inT {
					if v := strings.TrimSpace(run.String()); v != "" {
						if tx.Len() > 0 {
							tx.WriteString(" ")
						}
						tx.WriteString(v)
					}
					run.Reset()
				}
				inT = false
			case "txBody":
				if inTx > 0 {
					inTx--
					if inTx == 0 {
						if v := strings.TrimSpace(tx.String()); v != "" {
							texts = append(texts, v)
						}
						tx.Reset()
					}
				}
			}
		}
	}
	return texts, pics, nil
}

// xlsxImageBlocks 内嵌图片 → image 块(+ DocAsset,复用预算;超预算只给占位并告警)。
func xlsxImageBlocks(s *Service, o *ooxml, pics []string, warns *[]string) []sdk.DocBlock {
	var blocks []sdk.DocBlock
	var used int
	var bytes int64
	budgetWarned := false
	for _, p := range pics {
		size := int64(0)
		if f, ok := o.files[p]; ok {
			size = int64(f.UncompressedSize64)
		}
		name := path.Base(p)
		if used >= s.budget.MaxAssets || bytes+size > s.budget.MaxAssetsBytes {
			if !budgetWarned {
				*warns = append(*warns, fmt.Sprintf("xlsx 内嵌图片超出预算(上限 %d 张 / %d 字节),其余图片只给占位",
					s.budget.MaxAssets, s.budget.MaxAssetsBytes))
				budgetWarned = true
			}
			blocks = append(blocks, sdk.DocBlock{Kind: sdk.DocBlockImage, Text: name})
			continue
		}
		mime := o.partType(p)
		if !strings.HasPrefix(mime, "image/") {
			mime = mimeOf(p, nil)
		}
		used++
		bytes += size
		asset := &sdk.DocAsset{ID: s.RegisterAsset(o.abs, p, mime), Mime: mime, Name: name, Bytes: size}
		if w, h, err := o.imageDims(p); err == nil {
			asset.W, asset.H = w, h
		}
		blocks = append(blocks, sdk.DocBlock{Kind: sdk.DocBlockImage, Text: name, Asset: asset})
	}
	return blocks
}

// xmlElementText 消费 start 元素的整棵子树,返回其中全部字符数据(trim)。
// 用于批注/作者的文本取值(批注文本由多个 run 组成,须拼接)。
func xmlElementText(dec *xml.Decoder, start xml.StartElement) string {
	var sb strings.Builder
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			if depth == 0 {
				return strings.TrimSpace(sb.String())
			}
			depth--
		case xml.CharData:
			sb.Write(t)
		}
	}
	return strings.TrimSpace(sb.String())
}

// xmlBoolAttr OOXML 布尔属性(hidden="1"/"true")。
func xmlBoolAttr(el xml.StartElement, name string) bool {
	v := strings.ToLower(xmlAttr(el, name))
	return v == "1" || v == "true"
}
