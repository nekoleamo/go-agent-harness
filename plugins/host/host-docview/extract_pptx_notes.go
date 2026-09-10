// pptx 备注页抽取(DOC-3b;DOC-2/3a 同构的零依赖窄修)。
//
// 背景:DOC-3 判定实测显示讲者备注此前只告警「本期不解析」,而备注页常含整套幻灯片的
// 实质内容(讲者脚本/数据来源/待办)。本文件把当前幻灯片的备注文本抽为 note 块:
//   - 跳过幻灯片编号占位(`p:ph type="sldNum"`,避免把页码当备注);
//   - 多形状/多段落以空格合并(与 docx 批注/文本框同口径:单行 note)。
//
// 纪律:零第三方依赖;部件读取走 `o.reader` 预算;不改动 `pptxParser` 的解析状态机。
package hostdocview

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// pptxNotesRel 当前幻灯片的备注页部件路径(无 → 空串)。
func pptxNotesRel(o *ooxml, slidePart string) string {
	for _, rel := range o.rels(slidePart) {
		if strings.Contains(rel.Type, "notesSlide") && rel.Target != "" {
			return rel.Target
		}
	}
	return ""
}

// pptxNotesText 抽取备注页文本(空文本返回空串;部件缺失/不可读返回错误)。
func pptxNotesText(o *ooxml, part string) (string, error) {
	if !o.has(part) {
		return "", fmt.Errorf("缺少部件 %s", part)
	}
	rc, err := o.reader(part)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false
	var all strings.Builder   // 全部形状文本
	var shape strings.Builder // 当前形状文本
	inShape, skipShape, inT := false, false, false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "sp":
				inShape, skipShape = true, false
				shape.Reset()
			case "ph":
				// 幻灯片编号占位不算备注内容
				if inShape && xmlAttr(t, "type") == "sldNum" {
					skipShape = true
				}
			case "p":
				if inShape && !skipShape && shape.Len() > 0 {
					shape.WriteString(" ")
				}
			case "t":
				if inShape && !skipShape {
					inT = true
				}
			case "br", "tab":
				if inShape && !skipShape {
					shape.WriteString(" ")
				}
			}
		case xml.CharData:
			if inShape && !skipShape && inT {
				shape.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "sp":
				if inShape {
					if s := collapseSpace(shape.String()); s != "" {
						if all.Len() > 0 {
							all.WriteString(" ")
						}
						all.WriteString(s)
					}
					inShape = false
				}
			}
		}
	}
	return collapseSpace(all.String()), nil
}
