// docx 抽取器(D2):自研 OOXML 抽取(word/document.xml 流式解析)。
//
// 覆盖(对齐 DOC_PREVIEW_PLAN §5.3):
//   - 段落 / 标题(styles.xml outlineLvl + basedOn 一层继承 + 行内 w:outlineLvl + "Heading N" 命名回退)
//   - 富文本运行(w:b/w:i/w:strike/w:rStyle=VerbatimChar)/ 超链接(rels 解析)/ 换行与制表
//   - 列表(w:numPr,含样式表 numPr 继承)/ 嵌套表格显式跳过 + warning
//   - 表格(w:gridSpan→ColSpan、vMerge→RowSpan、tblHeader 或整行加粗→表头;行列单元格三重封顶)
//   - 内嵌图片(a:blip / v:imagedata → rels → media → DocAsset,经资产端点取回真图)
//   - 页眉页脚/脚注/批注:忽略并记 Warnings(不静默丢弃);公式只取纯文本 + warning
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

// docxRunCap 单段落/单元格内最大 run 数(防异常文档构造超长 run 列表)。
const docxRunCap = 400

// docxVal 通用 w:val 属性载体。
type docxVal struct {
	Val string `xml:"val,attr"`
}

// docxStyle 样式表条目(仅取排版相关字段;encoding/xml 不支持 a>b,attr,故用嵌套结构)。
type docxStyle struct {
	ID      string  `xml:"styleId,attr"`
	BasedOn docxVal `xml:"basedOn"`
	PPr     struct {
		Outline docxVal `xml:"outlineLvl"`
		NumPr   struct {
			Ilvl  docxVal `xml:"ilvl"`
			NumID docxVal `xml:"numId"`
		} `xml:"numPr"`
	} `xml:"pPr"`
}

// outline 段落大纲级别(0-based);无 = nil。
func (st docxStyle) outline() *int {
	if st.PPr.Outline.Val == "" {
		return nil
	}
	return attrIntVal(st.PPr.Outline.Val)
}

// numID 列表编号定义 id(空 = 非列表)。
func (st docxStyle) numID() string { return st.PPr.NumPr.NumID.Val }

// numIlvl 列表层级。
func (st docxStyle) numIlvl() *int { return attrIntVal(st.PPr.NumPr.Ilvl.Val) }

// base 继承的父样式 id。
func (st docxStyle) base() string { return st.BasedOn.Val }

// attrIntVal 字符串 → *int(非法返回 nil)。
func attrIntVal(v string) *int {
	if v == "" {
		return nil
	}
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			return nil
		}
		n = n*10 + int(r-'0')
	}
	return &n
}

// docxImage 待登记的内嵌图片。
type docxImage struct {
	relID string
	name  string
}

// docxMeta docProps/core.xml 元信息。
type docxMeta struct {
	Title   string `xml:"title"`
	Creator string `xml:"creator"`
}

// docxParser word/document.xml 流式解析状态机。
type docxParser struct {
	s    *Service
	o    *ooxml
	v    *sdk.DocView
	req  sdk.DocRequest
	abs  string // 容器 realpath(资产登记用)
	main string // 主文档部件名

	styles map[string]docxStyle

	// 段落态
	para      []sdk.DocRun
	paraStyle string
	paraOlvl  *int
	paraNum   bool
	paraIlvl  int
	inText    bool
	inHyper   string // 当前超链接目标(空 = 不在链接内)
	runFmt    sdk.DocRun
	inRunPr   bool
	pPrDepth  int // 属性块(pPr/tcPr/trPr/tblPr/sectPr)深度:其中的 rPr 属段落标记,不计运行格式
	inMath    bool
	mathText  strings.Builder
	pendImgs  []docxImage

	// 表格态
	inTable  bool
	rows     [][]sdk.DocCell
	head     []sdk.DocCell
	row      []sdk.DocCell
	rowBold  []bool
	headFlag bool // 本行由 w:tblHeader 标记
	cell     *sdk.DocCell
	cellRuns []sdk.DocRun
	colIdx   int
	// 纵向合并(vMerge):按 (锚行, 列) 记账,行落定后统一回填 RowSpan
	// (p.row 以值追加,不能持指针;跨行改动只能在 endTable 前一次性应用)
	mergeCol     map[int]int    // 列 → 锚行号
	mergeSpan    map[[2]int]int // (锚行, 列) → 额外跨行数
	mergeAt      map[[2]int]int // (锚行, 列) → 该行内单元格下标
	pendingMerge int            // 当前单元格为 vMerge restart 的列(-1 = 否)
	nested       int            // 嵌套表格跳过深度(>0 = 正在跳过)
	firstBold    bool           // 首行整行加粗(无 tblHeader 时的表头启发)

	blocks    []sdk.DocBlock
	truncated []string
	nAsset    int
	assetByte int64
	stop      bool
}

// extractDOCX docx → 块模型。
func extractDOCX(ctx context.Context, s *Service, abs string, fi os.FileInfo, req sdk.DocRequest, format sdk.DocFormat) (*sdk.DocView, error) {
	o, err := openOOXML(abs, s.budget)
	if err != nil {
		return nil, err
	}
	defer o.Close()

	v := baseView(req, abs, fi.Size(), fi.ModTime().UnixNano(), format)
	main := o.mainPart("docx")
	if main == "" || !o.has(main) {
		return nil, fmt.Errorf("%w: docx 缺少主文档部件(word/document.xml)", sdk.ErrDocParse)
	}
	p := &docxParser{s: s, o: o, v: v, req: req, abs: realPathOrClean(abs), main: main,
		mergeCol: map[int]int{}, mergeSpan: map[[2]int]int{}, mergeAt: map[[2]int]int{}, pendingMerge: -1}
	p.loadMeta()
	p.loadStyles()
	if err := p.parse(ctx); err != nil {
		return nil, err
	}
	p.finish()

	v.Blocks = p.blocks
	v.Truncated = append(v.Truncated, p.truncated...)
	v.Warnings = append(v.Warnings, o.warnings...)
	if len(v.Blocks) == 0 {
		v.Blocks = []sdk.DocBlock{{Kind: sdk.DocBlockNote, Text: "docx 无可见文本内容"}}
	}
	return v, nil
}

// loadMeta 解析 docProps/core.xml(标题/作者;缺失/损坏不致命)。
func (p *docxParser) loadMeta() {
	if !p.o.has("docProps/core.xml") {
		return
	}
	rc, err := p.o.reader("docProps/core.xml")
	if err != nil {
		return
	}
	defer rc.Close()
	var m docxMeta
	if err := xml.NewDecoder(rc).Decode(&m); err != nil {
		p.o.addWarning("docProps/core.xml 解析失败,标题/作者缺失")
		return
	}
	p.v.Title = strings.TrimSpace(m.Title)
	p.v.Author = strings.TrimSpace(m.Creator)
}

// loadStyles 解析 styles.xml 的 outlineLvl / numPr(basedOn 一层继承 + 命名回退)。
func (p *docxParser) loadStyles() {
	p.styles = map[string]docxStyle{}
	if p.o.has("word/styles.xml") {
		if rc, err := p.o.reader("word/styles.xml"); err == nil {
			func() {
				defer rc.Close()
				var doc struct {
					Styles []docxStyle `xml:"style"`
				}
				if err := xml.NewDecoder(rc).Decode(&doc); err != nil {
					p.o.addWarning("styles.xml 解析失败,标题层级回退命名判定")
					return
				}
				byID := map[string]docxStyle{}
				for _, st := range doc.Styles {
					if st.ID != "" {
						byID[st.ID] = st
					}
				}
				for id, st := range byID {
					if b := st.base(); b != "" {
						if base, ok := byID[b]; ok {
							if st.outline() == nil && base.outline() != nil {
								st.PPr.Outline.Val = base.PPr.Outline.Val
							}
							if st.numID() == "" && base.numID() != "" {
								st.PPr.NumPr = base.PPr.NumPr
							}
						}
					}
					p.styles[id] = st
				}
			}()
		}
	}
	// 命名回退:Heading1..6 / heading1..6 / 标题1..6 → outline 0..5
	for i := 1; i <= 6; i++ {
		for _, name := range []string{fmt.Sprintf("Heading%d", i), fmt.Sprintf("heading%d", i), fmt.Sprintf("标题%d", i)} {
			if _, ok := p.styles[name]; !ok {
				var st docxStyle
				st.ID = name
				st.PPr.Outline.Val = fmt.Sprint(i - 1)
				p.styles[name] = st
			}
		}
	}
}

// headingLevel 段落标题级别(0 = 非标题)。
func (p *docxParser) headingLevel(styleID string, inline *int) int {
	if inline != nil {
		return clampHeading(*inline + 1)
	}
	if st, ok := p.styles[styleID]; ok && st.outline() != nil {
		return clampHeading(*st.outline() + 1)
	}
	return 0
}

func clampHeading(l int) int {
	if l < 1 {
		return 1
	}
	if l > 6 {
		return 6
	}
	return l
}

// parse 流式解析主文档部件(逐 token,不整体 Unmarshal)。
func (p *docxParser) parse(ctx context.Context) error {
	rc, err := p.o.reader(p.main)
	if err != nil {
		return err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false // 容错:真实文档常有未定义实体/命名空间怪癖
	for {
		if err := ctx.Err(); err != nil {
			p.o.addWarning("解析超时,结果为部分内容")
			return nil
		}
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			p.o.addWarning(fmt.Sprintf("document.xml 解析中断: %v", err))
			return nil
		}
		switch t := tok.(type) {
		case xml.StartElement:
			p.start(t)
		case xml.EndElement:
			p.end(t)
		case xml.CharData:
			p.chardata(string(t))
		}
		if p.stop {
			return nil
		}
	}
}

// start 元素开始分派(嵌套表格经 nested 深度计数整体跳过)。
func (p *docxParser) start(t xml.StartElement) {
	if p.nested > 0 {
		p.nested++
		return
	}
	if t.Name.Local == "tbl" && p.inTable {
		p.o.addWarning("docx 含嵌套表格,已跳过内层(仅保留外层文字)")
		p.nested = 1
		return
	}
	switch t.Name.Local {
	case "p":
		p.startPara()
	case "pStyle":
		p.paraStyle = attrVal(t, "val")
	case "outlineLvl":
		if v := attrInt(t, "val"); v != nil {
			p.paraOlvl = v
		}
	case "numPr":
		p.paraNum = true
		p.paraIlvl = 0
	case "ilvl":
		if p.paraNum {
			if v := attrInt(t, "val"); v != nil {
				p.paraIlvl = *v
			}
		}
	case "rPr":
		if p.pPrDepth == 0 {
			p.inRunPr = true
		}
	case "pPr", "tcPr", "trPr", "tblPr", "sectPr":
		p.pPrDepth++
	case "b", "i", "strike", "dstrike", "rStyle":
		if !p.inRunPr {
			return
		}
		switch t.Name.Local {
		case "b":
			p.runFmt.Bold = boolProp(t)
		case "i":
			p.runFmt.Italic = boolProp(t)
		case "strike", "dstrike":
			p.runFmt.Strike = boolProp(t)
		case "rStyle":
			if strings.Contains(attrVal(t, "val"), "Verbatim") {
				p.runFmt.Code = true
			}
		}
	case "t":
		p.inText = true
	case "tab":
		p.appendRun(sdk.DocRun{Text: "\t"})
	case "br", "cr":
		p.appendRun(sdk.DocRun{Text: "\n"})
	case "hyperlink":
		if id := attrVal(t, "id"); id != "" {
			p.inHyper = p.resolveRel(p.main, id)
		}
	case "tbl":
		p.startTable()
	case "tr":
		p.row = nil
		p.rowBold = nil
		p.headFlag = false
		p.colIdx = 0
	case "tblHeader":
		if boolProp(t) {
			p.headFlag = true
		}
	case "tc":
		p.cell = &sdk.DocCell{}
		p.cellRuns = nil
	case "gridSpan":
		if v := attrInt(t, "val"); v != nil && *v > 1 && p.cell != nil {
			p.cell.ColSpan = *v
		}
	case "vMerge":
		if p.cell == nil {
			return
		}
		// OOXML 语义:w:val="restart" = 起始单元格;缺省/continue = 延续(合入上方)
		if attrVal(t, "val") == "restart" {
			p.pendingMerge = p.colIdx
			p.mergeCol[p.colIdx] = len(p.rows)
		} else if ar, ok := p.mergeCol[p.colIdx]; ok {
			p.mergeSpan[[2]int{ar, p.colIdx}]++
			p.cell = nil // 合并延续:不产出独立单元格
		}
	case "blip":
		if id := attrVal(t, "embed"); id != "" {
			p.pendImgs = append(p.pendImgs, docxImage{relID: id})
		}
	case "imagedata":
		if id := attrVal(t, "id"); id != "" {
			p.pendImgs = append(p.pendImgs, docxImage{relID: id})
		}
	case "docPr":
		if n := attrVal(t, "name"); n != "" && len(p.pendImgs) > 0 {
			p.pendImgs[len(p.pendImgs)-1].name = n
		}
	case "oMath", "oMathPara":
		p.inMath = true
	}
}

// end 元素结束分派。
func (p *docxParser) end(t xml.EndElement) {
	if p.nested > 0 {
		p.nested--
		return
	}
	switch t.Name.Local {
	case "p":
		p.endPara()
	case "rPr":
		p.inRunPr = false
	case "pPr", "tcPr", "trPr", "tblPr", "sectPr":
		if p.pPrDepth > 0 {
			p.pPrDepth--
		}
	case "r":
		p.runFmt = sdk.DocRun{} // 运行结束才复位格式(w:rPr 结束早于 w:t)
	case "t":
		p.inText = false
	case "hyperlink":
		p.inHyper = ""
	case "tc":
		p.endCell()
	case "tr":
		p.endRow()
	case "tbl":
		p.endTable()
	case "oMath", "oMathPara":
		p.inMath = false
		if txt := strings.TrimSpace(p.mathText.String()); txt != "" {
			p.appendRun(sdk.DocRun{Text: txt})
			p.o.addWarning("docx 含公式,已按纯文本提取(不做 OMML 排版)")
			p.mathText.Reset()
		}
	}
}

// chardata 文本节点(区分正文/公式)。
func (p *docxParser) chardata(s string) {
	if p.inMath {
		p.mathText.WriteString(s)
		return
	}
	if !p.inText {
		return
	}
	p.appendRun(sdk.DocRun{Text: s})
}

// appendRun 追加运行(携带当前格式与链接目标)。
func (p *docxParser) appendRun(r sdk.DocRun) {
	if r.Text == "" {
		return
	}
	r.Bold, r.Italic, r.Strike, r.Code = p.runFmt.Bold, p.runFmt.Italic, p.runFmt.Strike, p.runFmt.Code
	if p.inHyper != "" {
		r.Link = safeLink(p.inHyper)
	}
	if p.cell != nil {
		if len(p.cellRuns) < docxRunCap {
			p.cellRuns = append(p.cellRuns, r)
		}
		return
	}
	if len(p.para) < docxRunCap {
		p.para = append(p.para, r)
	}
}

func (p *docxParser) startPara() {
	p.para = nil
	p.paraStyle = ""
	p.paraOlvl = nil
	p.paraNum = false
	p.paraIlvl = 0
	p.runFmt = sdk.DocRun{}
}

// endPara 段落落定(标题/列表/普通段落);单元格内段落由 endCell 汇总。
func (p *docxParser) endPara() {
	if p.cell != nil {
		return
	}
	runs, text := normalizeRuns(p.para)
	p.para = nil
	if text == "" && len(p.pendImgs) == 0 {
		return
	}
	styleNum, styleIlvl := false, 0
	if st, ok := p.styles[p.paraStyle]; ok && st.numID() != "" {
		styleNum = true
		if st.numIlvl() != nil {
			styleIlvl = *st.numIlvl()
		}
	}
	switch {
	case text != "" && p.headingLevel(p.paraStyle, p.paraOlvl) > 0:
		p.addBlock(sdk.DocBlock{Kind: sdk.DocBlockHeading, Level: p.headingLevel(p.paraStyle, p.paraOlvl), Runs: runs, Text: text})
	case text != "" && (p.paraNum || styleNum):
		lvl := p.paraIlvl
		if !p.paraNum && styleNum {
			lvl = styleIlvl
		}
		p.addBlock(sdk.DocBlock{Kind: sdk.DocBlockList, Level: lvl, Runs: runs, Text: text})
	case text != "":
		p.addBlock(sdk.DocBlock{Kind: sdk.DocBlockParagraph, Runs: runs, Text: text})
	}
	p.flushImages()
}

// flushImages 段落内图片 → image 块。
func (p *docxParser) flushImages() {
	for _, im := range p.pendImgs {
		p.appendImageBlock(im)
	}
	p.pendImgs = nil
}

// appendImageBlock 解析图片关系并登记资产(超预算 → 占位 + warning)。
func (p *docxParser) appendImageBlock(im docxImage) {
	rel, ok := p.o.rels(p.main)[im.relID]
	if !ok || !strings.HasSuffix(rel.Type, "/image") {
		p.o.addWarning("docx 图片关系缺失或类型异常,已跳过一张图片")
		return
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
		p.o.addWarning(fmt.Sprintf("docx 内嵌图片超出预算(上限 %d 张 / %d 字节),其余图片只给占位", p.s.budget.MaxAssets, p.s.budget.MaxAssetsBytes))
		p.addBlock(sdk.DocBlock{Kind: sdk.DocBlockImage, Text: name})
		return
	}
	p.nAsset++
	p.assetByte += size
	asset := &sdk.DocAsset{ID: p.s.RegisterAsset(p.abs, rel.Target, mime), Mime: mime, Name: name, Bytes: size}
	if w, h, err := p.o.imageDims(rel.Target); err == nil {
		asset.W, asset.H = w, h
	}
	p.addBlock(sdk.DocBlock{Kind: sdk.DocBlockImage, Text: name, Asset: asset})
}

// startTable 表格开始。
func (p *docxParser) startTable() {
	p.inTable = true
	p.rows = nil
	p.head = nil
	p.row = nil
	p.firstBold = false
	p.mergeCol = map[int]int{}
	p.mergeSpan = map[[2]int]int{}
	p.mergeAt = map[[2]int]int{}
	p.pendingMerge = -1
	p.pendImgs = nil
}

// endCell 单元格落定。
func (p *docxParser) endCell() {
	if p.cell == nil {
		return // vMerge continue:已合入上方
	}
	cell := p.cell
	runs, text := normalizeRuns(p.cellRuns)
	cell.Text = text
	cell.Numeric = isNumeric(text)
	p.row = append(p.row, *cell)
	if p.pendingMerge >= 0 {
		p.mergeAt[[2]int{len(p.rows), p.pendingMerge}] = len(p.row) - 1
		p.pendingMerge = -1
	}
	p.rowBold = append(p.rowBold, runsAllBold(runs))
	p.cell = nil
	p.cellRuns = nil
	p.colIdx++
	p.para = nil
	if len(p.pendImgs) > 0 {
		p.o.addWarning("docx 表格内图片暂不导出(仅保留文字)")
		p.pendImgs = nil
	}
}

// endRow 行落定(显式 tblHeader → 表头;否则首行整行加粗 → 表头启发)。
func (p *docxParser) endRow() {
	if len(p.row) == 0 {
		return
	}
	isFirst := len(p.rows) == 0 && p.head == nil
	switch {
	case p.headFlag && p.head == nil:
		p.head = append([]sdk.DocCell(nil), p.row...)
	case isFirst:
		allBold := len(p.rowBold) > 0
		for _, b := range p.rowBold {
			allBold = allBold && b
		}
		p.firstBold = allBold
		p.rows = append(p.rows, p.row)
	default:
		p.rows = append(p.rows, p.row)
	}
	p.row = nil
	p.rowBold = nil
}

// endTable 表格落定(行列/单元格三重封顶)。
func (p *docxParser) endTable() {
	p.inTable = false
	// vMerge 回填必须先于表头提升/行截断:锚行下标是相对 p.rows 记录的
	for key, extra := range p.mergeSpan {
		ai, ok := p.mergeAt[key]
		if !ok || key[0] >= len(p.rows) || ai >= len(p.rows[key[0]]) {
			continue
		}
		p.rows[key[0]][ai].RowSpan = 1 + extra
	}
	p.mergeSpan = map[[2]int]int{}
	p.mergeAt = map[[2]int]int{}
	p.mergeCol = map[int]int{}

	rows := p.rows
	head := p.head
	if head == nil && p.firstBold && len(rows) > 0 {
		head = rows[0]
		rows = rows[1:]
	}
	p.rows, p.head, p.firstBold = nil, nil, false
	if head == nil && len(rows) == 0 {
		return
	}
	blk := sdk.DocBlock{Kind: sdk.DocBlockTable}
	// 表头单元格按 ColSpan 展开(Head 为扁平 []string,跨列以空串占位)
	for _, c := range head {
		blk.Head = append(blk.Head, truncCell(c.Text, p.s.budget.MaxCellChars))
		for i := 1; i < maxInt(c.ColSpan, 1); i++ {
			blk.Head = append(blk.Head, "")
		}
	}
	if len(rows) > p.s.budget.MaxTableRows {
		p.truncated = append(p.truncated, fmt.Sprintf("rows:%d/%d", p.s.budget.MaxTableRows, len(rows)))
		p.o.addWarning(fmt.Sprintf("docx 表格行 %d 超出预算 %d,已截断", len(rows), p.s.budget.MaxTableRows))
		rows = rows[:p.s.budget.MaxTableRows]
	}
	maxCols := len(blk.Head)
	for _, r := range rows {
		n := 0
		for _, c := range r {
			n += maxInt(c.ColSpan, 1)
		}
		if n > maxCols {
			maxCols = n
		}
	}
	if maxCols > p.s.budget.MaxTableCols {
		p.truncated = append(p.truncated, fmt.Sprintf("cols:%d/%d", p.s.budget.MaxTableCols, maxCols))
		p.o.addWarning(fmt.Sprintf("docx 表格列 %d 超出预算 %d,已截断", maxCols, p.s.budget.MaxTableCols))
		if len(blk.Head) > p.s.budget.MaxTableCols {
			blk.Head = blk.Head[:p.s.budget.MaxTableCols]
		}
	}
	for _, r := range rows {
		out := make([]sdk.DocCell, 0, len(r))
		used := 0
		for _, c := range r {
			span := maxInt(c.ColSpan, 1)
			if used+span > p.s.budget.MaxTableCols {
				break
			}
			used += span
			c.Text = truncCell(c.Text, p.s.budget.MaxCellChars)
			out = append(out, c)
		}
		blk.Rows = append(blk.Rows, out)
	}
	p.addBlock(blk)
}

// addBlock 追加块(块数封顶 → 标记截断并停止解析)。
func (p *docxParser) addBlock(b sdk.DocBlock) {
	if len(p.blocks) >= p.s.budget.MaxBlocks {
		if !containsMarker(p.truncated, "blocks") {
			p.truncated = append(p.truncated, fmt.Sprintf("blocks:%d", p.s.budget.MaxBlocks))
			p.o.addWarning(fmt.Sprintf("docx 内容块超出预算 %d,已截断", p.s.budget.MaxBlocks))
		}
		p.stop = true
		return
	}
	p.blocks = append(p.blocks, b)
}

// finish 收尾:未解析部件(页眉页脚/脚注/批注)显式提示。
func (p *docxParser) finish() {
	for _, probe := range []struct{ prefix, label string }{
		{"word/header", "页眉"}, {"word/footer", "页脚"}, {"word/footnotes.xml", "脚注"},
		{"word/endnotes.xml", "尾注"}, {"word/comments.xml", "批注"},
	} {
		if p.o.hasPrefix(probe.prefix) {
			p.o.addWarning(fmt.Sprintf("docx 含%s,本期不解析(仅正文)", probe.label))
		}
	}
}

// resolveRel 关系目标(超链接等)。
func (p *docxParser) resolveRel(part, relID string) string {
	if rel, ok := p.o.rels(part)[relID]; ok {
		return rel.Target
	}
	return ""
}

// normalizeRuns 合并相邻同格式 run 并生成纯文本。
func normalizeRuns(in []sdk.DocRun) ([]sdk.DocRun, string) {
	if len(in) == 0 {
		return nil, ""
	}
	out := make([]sdk.DocRun, 0, len(in))
	for _, r := range in {
		if len(out) > 0 {
			last := &out[len(out)-1]
			if last.Bold == r.Bold && last.Italic == r.Italic && last.Strike == r.Strike && last.Code == r.Code && last.Link == r.Link {
				last.Text += r.Text
				continue
			}
		}
		out = append(out, r)
	}
	var sb strings.Builder
	for _, r := range out {
		sb.WriteString(r.Text)
	}
	return out, strings.TrimRight(normalizeNewlines(sb.String()), "\n")
}

// runsAllBold 运行序列是否全部加粗(表头启发;无运行视为否)。
func runsAllBold(runs []sdk.DocRun) bool {
	if len(runs) == 0 {
		return false
	}
	for _, r := range runs {
		if strings.TrimSpace(r.Text) == "" {
			continue
		}
		if !r.Bold {
			return false
		}
	}
	return true
}

// attrVal 取属性值(命名空间无关,按 Local 名匹配)。
func attrVal(t xml.StartElement, local string) string {
	for _, a := range t.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// attrInt 取无符号整型属性(无/非法则 nil)。
func attrInt(t xml.StartElement, local string) *int {
	v := attrVal(t, local)
	if v == "" {
		return nil
	}
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			return nil
		}
		n = n*10 + int(r-'0')
	}
	return &n
}

// boolProp OOXML 布尔属性(缺省/1/true = 真;0/false/off = 假)。
func boolProp(t xml.StartElement) bool {
	switch strings.ToLower(attrVal(t, "val")) {
	case "0", "false", "off":
		return false
	}
	return true
}
