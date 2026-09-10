// pptx 抽取器(D3):自研 OOXML 抽取(ppt/presentation.xml + slides/slideN.xml 流式解析)。
//
// 覆盖(对齐 DOC_PREVIEW_PLAN §5.3):
//   - 幻灯片顺序以 `p:sldIdLst` 为准(不信文件名排序),经 presentation rels 解析部件
//   - 每张 → slide 块 + 文本框段落(层级 a:pPr@lvl;标题占位 → heading);表格 a:tbl → table
//   - 图片(p:pic/a:blip@r:embed)→ DocAsset 真图(经资产端点)
//   - 布局/母版继承仅做**标题占位继承**(slide → slideLayout → slideMaster),正文继承不做 + warning
//   - 备注页默认不解析(存在则显式 warning);块/表格/资产三重预算封顶
package hostdocview

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// extractPPTX pptx → 块模型(每张幻灯片一个 slide 块 + 内容块)。
func extractPPTX(ctx context.Context, s *Service, abs string, fi os.FileInfo, req sdk.DocRequest, format sdk.DocFormat) (*sdk.DocView, error) {
	o, err := openOOXML(abs, s.budget)
	if err != nil {
		return nil, err
	}
	defer o.Close()

	v := baseView(req, abs, fi.Size(), fi.ModTime().UnixNano(), format)
	main := o.mainPart("pptx")
	if main == "" || !o.has(main) {
		return nil, fmt.Errorf("%w: pptx 缺少主演示文稿部件", sdk.ErrDocParse)
	}
	slides, err := pptxSlideList(o, main)
	if err != nil {
		return nil, err
	}
	v.Pages = len(slides)
	p := &pptxParser{s: s, o: o, v: v, abs: realPathOrClean(abs), req: req}
	for i, part := range slides {
		if err := ctx.Err(); err != nil {
			p.o.addWarning("解析超时,结果为部分内容")
			break
		}
		if p.stop {
			break
		}
		p.parseSlide(ctx, part, i+1)
	}
	v.Blocks = p.blocks
	v.Truncated = append(v.Truncated, p.truncated...)
	v.Warnings = append(v.Warnings, o.warnings...)
	if len(v.Blocks) == 0 {
		v.Blocks = []sdk.DocBlock{{Kind: sdk.DocBlockNote, Text: "pptx 无可见文本内容"}}
	}
	return v, nil
}

// pptxSlideList 依 sldIdLst 顺序解析幻灯片部件(不信文件名排序)。
func pptxSlideList(o *ooxml, main string) ([]string, error) {
	rc, err := o.reader(main)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	// sldId 同时带 id 与 r:id(命名空间不同),encoding/xml 结构体映射易混淆 → 手工 token 解析
	ids := pptxSlideRelIDs(rc)
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: pptx 未找到幻灯片列表(sldIdLst)", sdk.ErrDocParse)
	}
	rels := o.rels(main)
	out := make([]string, 0, len(ids))
	for _, rid := range ids {
		rel, ok := rels[rid]
		if !ok {
			o.addWarning(fmt.Sprintf("幻灯片关系 %s 缺失,已跳过一张", rid))
			continue
		}
		out = append(out, rel.Target)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: pptx 幻灯片关系全部缺失", sdk.ErrDocParse)
	}
	return out, nil
}

// pptxSlideRelIDs 从 presentation.xml 取 sldIdLst 内 sldId 的 r:id 序列。
func pptxSlideRelIDs(r io.Reader) []string {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	var out []string
	inList := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "sldIdLst":
				inList = true
			case "sldId":
				if !inList {
					continue
				}
				for _, a := range t.Attr {
					if a.Name.Local == "id" && strings.HasPrefix(a.Name.Space, "http://schemas.openxmlformats.org/officeDocument") {
						out = append(out, a.Value)
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "sldIdLst" {
				return out
			}
		}
	}
	return out
}

// pptxParser 幻灯片解析状态机。
type pptxParser struct {
	s   *Service
	o   *ooxml
	v   *sdk.DocView
	abs string
	req sdk.DocRequest

	blocks    []sdk.DocBlock
	truncated []string
	nAsset    int
	assetByte int64
	stop      bool

	// 当前张
	slideNo      int
	curSlidePart string
	isTitle      bool
	para         []sdk.DocRun
	paraLevel    int
	pending      []docxImage // 复用图片载体(relID + name)

	// 表格
	inTable bool
	inTC    bool
	rows    [][]sdk.DocCell
	row     []sdk.DocCell
	cell    []sdk.DocRun

	// 文本态
	inT     bool
	inAPara bool
}

// parseSlide 解析一张幻灯片。
func (p *pptxParser) parseSlide(ctx context.Context, part string, no int) {
	p.slideNo = no
	p.curSlidePart = part
	if !p.o.has(part) {
		p.o.addWarning(fmt.Sprintf("第 %d 张幻灯片部件缺失,已跳过", no))
		return
	}
	p.blocks = append(p.blocks, sdk.DocBlock{Kind: sdk.DocBlockSlide, Page: no})

	rc, err := p.o.reader(part)
	if err != nil {
		p.o.addWarning(fmt.Sprintf("第 %d 张幻灯片读取失败: %v", no, err))
		return
	}
	defer rc.Close()

	dec := xml.NewDecoder(rc)
	dec.Strict = false
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		if p.stop {
			return
		}
		tok, err := dec.Token()
		if err == io.EOF || err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			p.start(t)
		case xml.EndElement:
			p.end(t)
		case xml.CharData:
			if p.inT {
				if p.inTC {
					p.cell = append(p.cell, sdk.DocRun{Text: string(t)})
				} else {
					p.para = append(p.para, sdk.DocRun{Text: string(t)})
				}
			}
		}
	}
	p.endTable()
	// DOC-3b:备注页文本(讲者备注常含实质内容)→ note 块(此前只告警「本期不解析」)
	if notesPart := pptxNotesRel(p.o, part); notesPart != "" {
		text, err := pptxNotesText(p.o, notesPart)
		switch {
		case err != nil:
			p.o.addWarning(fmt.Sprintf("第 %d 张幻灯片备注页解析失败: %v", no, err))
		case text != "":
			p.addBlock(sdk.DocBlock{Kind: sdk.DocBlockNote, Text: fmt.Sprintf("备注(第 %d 张): %s", no, text)})
		}
	}
	// DOC-3c:图表缓存数据(表格块)与 SmartArt 文字(note)
	if gblocks, gwarns := officeGraphicsBlocks(p.s, p.o, part, "ppt/charts/", "ppt/diagrams/"); len(gblocks) > 0 || len(gwarns) > 0 {
		for _, b := range gblocks {
			b.Page = no
			p.addBlock(b)
		}
		for _, w := range gwarns {
			p.o.addWarning(fmt.Sprintf("第 %d 张:%s", no, w))
		}
	}
	// 标题占位继承(仅标题):本张无标题时回退布局/母版
	if p.v.Title == "" {
		if t := p.inheritedTitle(part); t != "" {
			p.v.Title = t
		}
	}
}

// inheritedTitle 标题占位继承:slideLayout → slideMaster(正文继承不做)。
func (p *pptxParser) inheritedTitle(part string) string {
	layout := ""
	for _, rel := range p.o.rels(part) {
		if strings.Contains(rel.Type, "slideLayout") {
			layout = rel.Target
			break
		}
	}
	if layout == "" {
		return ""
	}
	if t := pptxTitleOf(p.o, layout); t != "" {
		return t
	}
	for _, rel := range p.o.rels(layout) {
		if strings.Contains(rel.Type, "slideMaster") {
			return pptxTitleOf(p.o, rel.Target)
		}
	}
	return ""
}

// pptxTitleOf 取部件中标题占位(type=title|ctrTitle)的首段文本。
func pptxTitleOf(o *ooxml, part string) string {
	rc, err := o.reader(part)
	if err != nil {
		return ""
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false
	isTitle, inT := false, false
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "ph":
				tp := xmlAttr(t, "type")
				isTitle = tp == "title" || tp == "ctrTitle"
			case "t":
				if isTitle {
					inT = true
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "p":
				if isTitle && sb.Len() > 0 {
					return strings.TrimSpace(sb.String())
				}
			}
		case xml.CharData:
			if inT && isTitle {
				sb.WriteString(string(t))
			}
		}
	}
	return strings.TrimSpace(sb.String())
}

// start 元素开始。
func (p *pptxParser) start(t xml.StartElement) {
	switch t.Name.Local {
	case "sp":
		p.isTitle = false
	case "ph":
		tp := xmlAttr(t, "type")
		if tp == "title" || tp == "ctrTitle" {
			p.isTitle = true
		}
	case "p":
		p.inAPara = true
		p.para = nil
		p.paraLevel = 0
	case "pPr":
		if v := xmlAttr(t, "lvl"); v != "" {
			if n := len(v); n == 1 && v[0] >= '0' && v[0] <= '9' {
				p.paraLevel = int(v[0] - '0')
			}
		}
	case "t":
		p.inT = true
	case "blip":
		if id := xmlAttr(t, "embed"); id != "" {
			p.pending = append(p.pending, docxImage{relID: id})
		}
	case "cNvPr":
		if n := xmlAttr(t, "name"); n != "" && len(p.pending) > 0 {
			p.pending[len(p.pending)-1].name = n
		}
	case "tbl":
		p.inTable = true
		p.rows = nil
	case "tr":
		p.row = nil
	case "tc":
		p.inTC = true
		p.cell = nil
	}
}

// end 元素结束。
func (p *pptxParser) end(t xml.EndElement) {
	switch t.Name.Local {
	case "t":
		p.inT = false
	case "pic":
		p.flushImages(p.curSlidePart)
	case "sp":
		p.isTitle = false
		p.flushImages(p.curSlidePart)
	case "p":
		if !p.inAPara {
			return
		}
		p.inAPara = false
		text := runsText(p.para)
		if p.inTC {
			p.cell = append(p.cell, p.para...)
			p.para = nil
			return
		}
		if text == "" {
			p.para = nil
			return
		}
		kind := sdk.DocBlockParagraph
		level := p.paraLevel
		if p.isTitle {
			kind = sdk.DocBlockHeading
			level = 1
			if p.v.Title == "" {
				p.v.Title = text
			}
		}
		p.addBlock(sdk.DocBlock{Kind: kind, Level: level, Runs: p.para, Text: text})
		p.para = nil
	case "tc":
		p.inTC = false
		cell := sdk.DocCell{Text: truncCell(runsText(p.cell), p.s.budget.MaxCellChars)}
		cell.Numeric = isNumeric(cell.Text)
		p.row = append(p.row, cell)
		p.cell = nil
	case "tr":
		if len(p.row) > 0 {
			p.rows = append(p.rows, p.row)
		}
		p.row = nil
	case "tbl":
		p.endTable()
	}
}

// endTable 表格落定(行列封顶)。
func (p *pptxParser) endTable() {
	if !p.inTable {
		return
	}
	p.inTable = false
	rows := p.rows
	p.rows = nil
	if len(rows) == 0 {
		return
	}
	if len(rows) > p.s.budget.MaxTableRows {
		p.truncated = append(p.truncated, fmt.Sprintf("rows:%d/%d", p.s.budget.MaxTableRows, len(rows)))
		p.o.addWarning(fmt.Sprintf("pptx 表格行 %d 超出预算 %d,已截断", len(rows), p.s.budget.MaxTableRows))
		rows = rows[:p.s.budget.MaxTableRows]
	}
	maxCols := 0
	for _, r := range rows {
		if len(r) > maxCols {
			maxCols = len(r)
		}
	}
	blk := sdk.DocBlock{Kind: sdk.DocBlockTable}
	for _, r := range rows {
		out := make([]sdk.DocCell, 0, len(r))
		used := 0
		for _, c := range r {
			if used >= p.s.budget.MaxTableCols {
				break
			}
			used++
			out = append(out, c)
		}
		blk.Rows = append(blk.Rows, out)
	}
	if maxCols > p.s.budget.MaxTableCols {
		p.truncated = append(p.truncated, fmt.Sprintf("cols:%d/%d", p.s.budget.MaxTableCols, maxCols))
		p.o.addWarning(fmt.Sprintf("pptx 表格列 %d 超出预算 %d,已截断", maxCols, p.s.budget.MaxTableCols))
	}
	p.addBlock(blk)
}

// flushImages 图片 → image 块(经资产端点)。
func (p *pptxParser) flushImages(part string) {
	if len(p.pending) == 0 {
		return
	}
	imgs := p.pending
	p.pending = nil
	slidePart := part
	if slidePart == "" {
		slidePart = p.curSlidePart
	}
	for _, im := range imgs {
		rel, ok := p.o.rels(slidePart)[im.relID]
		if !ok || !strings.HasSuffix(rel.Type, "/image") {
			p.o.addWarning("pptx 图片关系缺失或类型异常,已跳过一张图片")
			continue
		}
		mime := p.o.partType(rel.Target)
		if !strings.HasPrefix(mime, "image/") {
			mime = mimeOf(rel.Target, nil)
		}
		var size int64
		if f, ok := p.o.files[rel.Target]; ok {
			size = int64(f.UncompressedSize64)
		}
		name := im.name
		if name == "" {
			name = path.Base(rel.Target)
		}
		if p.nAsset >= p.s.budget.MaxAssets || p.assetByte+size > p.s.budget.MaxAssetsBytes {
			p.o.addWarning(fmt.Sprintf("pptx 内嵌图片超出预算(上限 %d 张 / %d 字节),其余图片只给占位", p.s.budget.MaxAssets, p.s.budget.MaxAssetsBytes))
			p.addBlock(sdk.DocBlock{Kind: sdk.DocBlockImage, Text: name, Page: p.slideNo})
			continue
		}
		p.nAsset++
		p.assetByte += size
		asset := &sdk.DocAsset{ID: p.s.RegisterAsset(p.abs, rel.Target, mime), Mime: mime, Name: name, Bytes: size}
		if w, h, err := p.o.imageDims(rel.Target); err == nil {
			asset.W, asset.H = w, h
		}
		p.addBlock(sdk.DocBlock{Kind: sdk.DocBlockImage, Text: name, Asset: asset, Page: p.slideNo})
	}
}

// addBlock 追加块(块数封顶)。
func (p *pptxParser) addBlock(b sdk.DocBlock) {
	if len(p.blocks) >= p.s.budget.MaxBlocks {
		if !containsMarker(p.truncated, "blocks") {
			p.truncated = append(p.truncated, fmt.Sprintf("blocks:%d", p.s.budget.MaxBlocks))
			p.o.addWarning(fmt.Sprintf("pptx 内容块超出预算 %d,已截断", p.s.budget.MaxBlocks))
		}
		p.stop = true
		return
	}
	if b.Page == 0 {
		b.Page = p.slideNo
	}
	p.blocks = append(p.blocks, b)
}

// runsText 运行序列纯文本。
func runsText(runs []sdk.DocRun) string {
	var sb strings.Builder
	for _, r := range runs {
		sb.WriteString(r.Text)
	}
	return strings.TrimSpace(normalizeNewlines(sb.String()))
}
