// xlsx 抽取器(D3):自研 OOXML 抽取(xl/workbook.xml + worksheets/sheetN.xml 流式解析)。
//
// 覆盖(对齐 DOC_PREVIEW_PLAN §5.3):
//   - 工作表清单(名/顺序/隐藏)、按请求 sheet 取单表(sheet 块 + 网格)
//   - 共享串**惰性建索引**(先扫工作表收集被引用下标,再按需物化 → 百万串不全量常驻)
//   - 单元格类型 s(共享)/inlineStr/str(公式串结果)/b/n;`<f>` 只取缓存值 `<v>`(不重算)
//   - 数字格式:styles.xml cellXfs → numFmtId;内建日期表 + 自定义格式判定 → ISO 日期;百分比
//   - 合并单元格(mergeCells)→ ColSpan/RowSpan,covered 单元格跳过
//   - 行/列/单元格三重封顶 + 稀疏空行折叠(连续空行 → note)+ Truncated/Warnings
package hostdocview

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// xlsxEmptyRowFold 连续空行折叠阈值(超过则折叠为一条 note)。
const xlsxEmptyRowFold = 3

// xlsxSheet 工作表元信息。
type xlsxSheet struct {
	Name  string
	RelID string
	State string
}

// xlsxStyleCell 一个 cellXf(仅取数字格式)。
type xlsxStyleCell struct {
	NumFmtID int `xml:"numFmtId,attr"`
}

// xlsxStyles 数字格式表。
type xlsxStyles struct {
	xfs       []xlsxStyleCell
	custom    map[int]string // numFmtId → formatCode
	dateXf    map[int]bool   // xf 下标 → 日期格式
	percentXf map[int]bool   // xf 下标 → 百分比格式
}

// builtinDateFmts Excel 内建日期/时间格式 id。
var builtinDateFmts = map[int]bool{
	14: true, 15: true, 16: true, 17: true, 18: true, 19: true, 20: true, 21: true, 22: true,
	27: true, 28: true, 29: true, 30: true, 31: true, 32: true, 33: true, 34: true, 35: true, 36: true,
	45: true, 46: true, 47: true,
	50: true, 51: true, 52: true, 53: true, 54: true, 55: true, 56: true, 57: true, 58: true,
}

// builtinPercentFmts Excel 内建百分比格式 id(9/10)。
var builtinPercentFmts = map[int]bool{9: true, 10: true}

// extractXLSX xlsx → 块模型(sheet 块 + table 块)。
func extractXLSX(ctx context.Context, s *Service, abs string, fi os.FileInfo, req sdk.DocRequest, format sdk.DocFormat) (*sdk.DocView, error) {
	o, err := openOOXML(abs, s.budget)
	if err != nil {
		return nil, err
	}
	defer o.Close()

	v := baseView(req, abs, fi.Size(), fi.ModTime().UnixNano(), format)
	sheets, err := xlsxSheetList(o)
	if err != nil {
		return nil, err
	}
	if len(sheets) == 0 {
		v.Blocks = []sdk.DocBlock{{Kind: sdk.DocBlockNote, Text: "xlsx 无工作表"}}
		return v, nil
	}
	styles := xlsxLoadStyles(o)
	v.Sheets = make([]sdk.DocSheet, 0, len(sheets))

	idx := req.Sheet
	if idx < 0 || idx >= len(sheets) {
		idx = 0
	}
	// 先建工作表元信息(需要每表行列数 → 轻量扫描标题行/维度;顺带统计隐藏行/列)
	hiddenRows, hiddenCols := 0, 0
	for _, sh := range sheets {
		part := xlsxSheetPart(o, sh)
		rows, cols, hr, hc := xlsxSheetSize(o, part)
		v.Sheets = append(v.Sheets, sdk.DocSheet{
			Name: sh.Name, Rows: rows, Cols: cols,
			Hidden: strings.EqualFold(sh.State, "hidden") || strings.EqualFold(sh.State, "veryHidden"),
		})
		if sh.Name == sheets[idx].Name {
			hiddenRows, hiddenCols = hr, hc
		}
	}
	// 当前表:块模型
	part := xlsxSheetPart(o, sheets[idx])
	blocks, trunc, warns, err := xlsxParseSheet(ctx, s, o, part, sheets[idx].Name, styles)
	if err != nil {
		return nil, err
	}
	// DOC-2:xlsx 批注(legacy + 回复式)/文本框/内嵌图片/隐藏行列(此前静默丢失)。
	// 必须在 o.warnings 汇总**之前**调用:内部经 o.addWarning 记的告警需一并收进 v.Warnings。
	extra, xwarns := xlsxSheetAnnotations(s, o, part, sheets[idx].Name, hiddenRows, hiddenCols)
	v.Blocks = append(blocks, extra...)
	v.Truncated = append(v.Truncated, trunc...)
	v.Warnings = append(v.Warnings, warns...)
	v.Warnings = append(v.Warnings, o.warnings...)
	v.Warnings = append(v.Warnings, xwarns...)
	if len(v.Sheets) > 1 {
		v.Warnings = append(v.Warnings, fmt.Sprintf("工作簿共 %d 个工作表,当前显示第 %d 个(%s);其余表用 Web 面板/--sheet 切换", len(v.Sheets), idx+1, sheets[idx].Name))
	}
	return v, nil
}

// xlsxSheetList 解析 workbook.xml 的工作表顺序/名称/隐藏状态。
func xlsxSheetList(o *ooxml) ([]xlsxSheet, error) {
	main := o.mainPart("xlsx")
	if main == "" || !o.has(main) {
		return nil, fmt.Errorf("%w: xlsx 缺少主工作簿部件", sdk.ErrDocParse)
	}
	rc, err := o.reader(main)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var wb struct {
		Sheets []struct {
			Name    string `xml:"name,attr"`
			SheetID string `xml:"sheetId,attr"`
			State   string `xml:"state,attr"`
			RID     string `xml:"id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.NewDecoder(rc).Decode(&wb); err != nil {
		return nil, fmt.Errorf("%w: workbook.xml 解析失败: %v", sdk.ErrDocParse, err)
	}
	out := make([]xlsxSheet, 0, len(wb.Sheets))
	for _, sh := range wb.Sheets {
		out = append(out, xlsxSheet{Name: sh.Name, RelID: sh.RID, State: sh.State})
	}
	return out, nil
}

// xlsxSheetPart 工作表部件路径(经 workbook rels 解析;失败回退 worksheets/sheetN.xml)。
func xlsxSheetPart(o *ooxml, sh xlsxSheet) string {
	if rel, ok := o.rels(o.mainPart("xlsx"))[sh.RelID]; ok {
		return rel.Target
	}
	return "xl/worksheets/sheet1.xml"
}

// xlsxSheetSize 轻量扫描工作表取维度(仅读 sheetData 的行/列上界)+ 隐藏行/列统计(DOC-2)。
func xlsxSheetSize(o *ooxml, part string) (rows, cols, hiddenRows, hiddenCols int) {
	rc, err := o.reader(part)
	if err != nil {
		return 0, 0, 0, 0
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false
	inRow := false
	rowCells := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				if inRow {
					if rowCells > cols {
						cols = rowCells
					}
					rowCells = 0
				}
				inRow = true
				rows++
				if xmlBoolAttr(t, "hidden") {
					hiddenRows++
				}
			case "col":
				if xmlBoolAttr(t, "hidden") {
					hiddenCols++
				}
			case "c":
				if inRow {
					rowCells++
				}
			}
		case xml.EndElement:
			if t.Name.Local == "row" && inRow {
				if rowCells > cols {
					cols = rowCells
				}
				rowCells = 0
				inRow = false
			}
		}
	}
	return rows, cols, hiddenRows, hiddenCols
}

// xlsxLoadStyles 解析 styles.xml → 每个 cellXf 的日期/百分比判定。
func xlsxLoadStyles(o *ooxml) *xlsxStyles {
	st := &xlsxStyles{custom: map[int]string{}, dateXf: map[int]bool{}, percentXf: map[int]bool{}}
	if !o.has("xl/styles.xml") {
		return st
	}
	rc, err := o.reader("xl/styles.xml")
	if err != nil {
		return st
	}
	defer rc.Close()
	var doc struct {
		NumFmts []struct {
			NumFmtID   int    `xml:"numFmtId,attr"`
			FormatCode string `xml:"formatCode,attr"`
		} `xml:"numFmts>numFmt"`
		CellXfs []xlsxStyleCell `xml:"cellXfs>xf"`
	}
	if err := xml.NewDecoder(rc).Decode(&doc); err != nil {
		o.addWarning("xlsx styles.xml 解析失败,日期格式回退内建表判定")
		return st
	}
	for _, nf := range doc.NumFmts {
		st.custom[nf.NumFmtID] = nf.FormatCode
	}
	st.xfs = doc.CellXfs
	for i, xf := range doc.CellXfs {
		code := st.custom[xf.NumFmtID]
		if isDateFormat(xf.NumFmtID, code) {
			st.dateXf[i] = true
		}
		if builtinPercentFmts[xf.NumFmtID] || strings.Contains(code, "%") {
			st.percentXf[i] = true
		}
	}
	return st
}

// isDateFormat 判定数字格式是否为日期/时间(内建表 + 自定义格式码扫描)。
func isDateFormat(numFmtID int, code string) bool {
	if builtinDateFmts[numFmtID] {
		return true
	}
	if code == "" {
		return false
	}
	// 去引号字面量与 [..] 段,避免 "yyyy" 之类误判
	var sb strings.Builder
	inQuote := false
	for _, r := range code {
		switch {
		case r == '"':
			inQuote = !inQuote
		case inQuote:
		case r == '[':
			sb.WriteRune(' ')
		case r == ']':
		default:
			sb.WriteRune(r)
		}
	}
	clean := strings.ToLower(sb.String())
	for _, tok := range []string{"yy", "dd", "hh", "ss", "mm"} {
		if strings.Contains(clean, tok) {
			return true
		}
	}
	// 单个 d/m/y 也可能(如 "d/m")——但 "0.00" 这类不含字母则不是
	return strings.ContainsAny(clean, "yd") && !strings.Contains(clean, "general")
}

// xlsxParseSheet 解析单个工作表 → 块(sheet 块 + 单个 table 块;空行折叠为一行提示)。
// 行号映射(rowMap:原始 0-based 行 → 表内行下标)保证合并区域在折叠后仍指向正确行。
func xlsxParseSheet(ctx context.Context, s *Service, o *ooxml, part, name string, styles *xlsxStyles) ([]sdk.DocBlock, []string, []string, error) {
	var trunc, warns []string

	used, err := xlsxUsedSharedStrings(ctx, o, part, s.budget)
	if err != nil {
		return nil, nil, nil, err
	}
	shared, err := xlsxLoadSharedStrings(ctx, o, used, s.budget)
	if err != nil {
		return nil, nil, nil, err
	}
	merges, err := xlsxMergeCells(o, part)
	if err != nil {
		warns = append(warns, "xlsx 合并单元格信息解析失败:"+err.Error())
	}

	var rows [][]sdk.DocCell
	rowMap := map[int]int{}
	emptyRun := 0
	rowIdx := -1
	maxCols := 0
	capped := false

	rc, err := o.reader(part)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false

	var inRow bool
	var curRow int
	var cells []sdk.DocCell
	var cellRef, cellType string
	cellStyle := -1
	var inV, inT bool
	var valStr, inlineStr strings.Builder
	seenSheetData := false

	for {
		if err := ctx.Err(); err != nil {
			warns = append(warns, "解析超时,结果为部分内容")
			break
		}
		if capped {
			break
		}
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			warns = append(warns, fmt.Sprintf("工作表解析中断: %v", err))
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "sheetData":
				seenSheetData = true
			case "row":
				if !seenSheetData {
					continue
				}
				inRow = true
				curRow = xlsxRowIndex(t, rowIdx+1)
				cells = nil
				if curRow > rowIdx+1 { // 稀疏:缺行按空行累积
					emptyRun += curRow - rowIdx - 1
				}
			case "c":
				if !inRow {
					continue
				}
				cellRef = xmlAttr(t, "r")
				cellType = xmlAttr(t, "t")
				cellStyle = -1
				if sv := xmlAttr(t, "s"); sv != "" {
					if n, err := strconv.Atoi(sv); err == nil {
						cellStyle = n
					}
				}
			case "v":
				inV = true
			case "t":
				if !inV {
					inT = true
				}
			case "is":
				inlineStr.Reset()
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "c":
				if !inRow {
					continue
				}
				col := xlsxColIndex(cellRef, len(cells))
				text := xlsxCellText(cellType, valStr.String(), inlineStr.String(), cellStyle, shared, styles)
				for len(cells) < col {
					cells = append(cells, sdk.DocCell{})
				}
				cells = append(cells, sdk.DocCell{Text: truncCell(text, s.budget.MaxCellChars), Numeric: cellType == "" || cellType == "n"})
				valStr.Reset()
				inlineStr.Reset()
			case "row":
				if !inRow {
					continue
				}
				inRow = false
				rowIdx = curRow
				isEmpty := true
				for _, c := range cells {
					if strings.TrimSpace(c.Text) != "" {
						isEmpty = false
						break
					}
				}
				if isEmpty {
					emptyRun++
					continue
				}
				// 非空行:先把积压的空行折叠成一行提示(保持单表网格)
				if emptyRun > xlsxEmptyRowFold {
					rowMap[rowIdx-emptyRun] = len(rows)
					rows = append(rows, []sdk.DocCell{{Text: fmt.Sprintf("⋯ 省略 %d 个空行", emptyRun)}})
				} else if emptyRun > 0 {
					for i := 0; i < emptyRun; i++ {
						rowMap[rowIdx-emptyRun+i] = len(rows)
						rows = append(rows, []sdk.DocCell{})
					}
				}
				emptyRun = 0
				if len(cells) > maxCols {
					maxCols = len(cells)
				}
				if len(rows) >= s.budget.MaxTableRows {
					capped = true
					trunc = append(trunc, fmt.Sprintf("rows:%d+/%d", len(rows), rowIdx+1))
					warns = append(warns, fmt.Sprintf("工作表行数超出预算 %d,已截断", s.budget.MaxTableRows))
					break
				}
				rowMap[rowIdx] = len(rows)
				rows = append(rows, cells)
			}
		case xml.CharData:
			if inV {
				valStr.WriteString(string(t))
			} else if inT {
				inlineStr.WriteString(string(t))
			}
		}
	}
	if emptyRun > xlsxEmptyRowFold && !capped {
		rowMap[rowIdx-emptyRun+1] = len(rows)
		rows = append(rows, []sdk.DocCell{{Text: fmt.Sprintf("⋯ 省略 %d 个空行", emptyRun)}})
	}
	if maxCols > s.budget.MaxTableCols {
		trunc = append(trunc, fmt.Sprintf("cols:%d/%d", s.budget.MaxTableCols, maxCols))
		warns = append(warns, fmt.Sprintf("工作表列数 %d 超出预算 %d,已截断", maxCols, s.budget.MaxTableCols))
		for i := range rows {
			if len(rows[i]) > s.budget.MaxTableCols {
				rows[i] = rows[i][:s.budget.MaxTableCols]
			}
		}
	}

	var blocks []sdk.DocBlock
	if name != "" {
		blocks = append(blocks, sdk.DocBlock{Kind: sdk.DocBlockSheet, Text: name})
	}
	blk := sdk.DocBlock{Kind: sdk.DocBlockTable, Rows: rows}
	if len(merges) > 0 {
		blk = applyMergesToTable(blk, merges, rowMap)
	}
	blocks = append(blocks, blk)
	if len(rows) == 0 {
		blocks = append(blocks, sdk.DocBlock{Kind: sdk.DocBlockNote, Text: "工作表为空"})
	}
	return blocks, trunc, warns, nil
}

// xlsxMerge 一条合并区域(A1:B2,均 0-based 闭区间)。
type xlsxMerge struct {
	r1, c1, r2, c2 int
}

// xlsxMergeCells 解析 mergeCells 区域(上限 2000 条)。
func xlsxMergeCells(o *ooxml, part string) ([]xlsxMerge, error) {
	rc, err := o.reader(part)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false
	var out []xlsxMerge
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "mergeCell" {
			continue
		}
		a, b, ok := strings.Cut(xmlAttr(se, "ref"), ":")
		if !ok {
			continue
		}
		r1, c1 := xlsxRefToCoord(a)
		r2, c2 := xlsxRefToCoord(b)
		if r1 < 0 || r2 < 0 {
			continue
		}
		out = append(out, xlsxMerge{r1: r1, c1: c1, r2: r2, c2: c2})
		if len(out) >= 2000 {
			break
		}
	}
	return out, nil
}

// xlsxCellText 单元格取值(共享串/内联串/布尔/公式串结果/数值+日期格式)。
func xlsxCellText(cellType, v, inline string, xf int, shared []string, styles *xlsxStyles) string {
	switch cellType {
	case "s":
		if idx, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && idx >= 0 && idx < len(shared) {
			return shared[idx]
		}
		return ""
	case "inlineStr":
		return inline
	case "b":
		if strings.TrimSpace(v) == "1" {
			return "TRUE"
		}
		return "FALSE"
	case "str":
		return v
	default:
		return xlsxFormatNumber(v, xf, styles)
	}
}

// applyMergesToTable 合并区域 → 表内单元格 ColSpan/RowSpan(经 rowMap 映射;covered 置空)。
func applyMergesToTable(blk sdk.DocBlock, merges []xlsxMerge, rowMap map[int]int) sdk.DocBlock {
	for _, m := range merges {
		top, ok := rowMap[m.r1]
		if !ok || top >= len(blk.Rows) || m.c1 >= len(blk.Rows[top]) {
			continue
		}
		cell := &blk.Rows[top][m.c1]
		if m.c2 > m.c1 {
			cell.ColSpan = m.c2 - m.c1 + 1
		}
		if m.r2 > m.r1 {
			cell.RowSpan = m.r2 - m.r1 + 1
		}
		for r := m.r1; r <= m.r2; r++ {
			tr, ok := rowMap[r]
			if !ok || tr >= len(blk.Rows) {
				continue
			}
			for c := m.c1; c <= m.c2; c++ {
				if tr == top && c == m.c1 {
					continue
				}
				if c < len(blk.Rows[tr]) {
					blk.Rows[tr][c].Text = ""
					blk.Rows[tr][c].Numeric = false
				}
			}
		}
	}
	return blk
}

// xmlAttr 取元素属性(命名空间无关)。
func xmlAttr(t xml.StartElement, local string) string {
	for _, a := range t.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// xlsxUsedSharedStrings 第一遍扫描:收集被引用的共享串下标(预算内的单元格)。
func xlsxUsedSharedStrings(ctx context.Context, o *ooxml, part string, b Budget) (map[int]bool, error) {
	used := map[int]bool{}
	rc, err := o.reader(part)
	if err != nil {
		return used, err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false
	count := 0
	limit := b.MaxTableRows * maxInt(b.MaxTableCols, 1) * 4
	var cellType string
	var inV bool
	var buf strings.Builder
	for {
		if err := ctx.Err(); err != nil {
			return used, nil
		}
		tok, err := dec.Token()
		if err == io.EOF || err != nil {
			return used, nil
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "c":
				cellType = xmlAttr(t, "t")
				buf.Reset()
			case "v":
				inV = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "c":
				if cellType == "s" && count < limit {
					if idx, err := strconv.Atoi(strings.TrimSpace(buf.String())); err == nil {
						used[idx] = true
						count++
					}
				}
			}
		case xml.CharData:
			if inV {
				buf.WriteString(string(t))
			}
		}
	}
}

// xlsxLoadSharedStrings 第二遍:只物化被引用的共享串(百万串不全量常驻)。
func xlsxLoadSharedStrings(ctx context.Context, o *ooxml, used map[int]bool, b Budget) ([]string, error) {
	out := map[int]string{}
	if len(used) == 0 || !o.has("xl/sharedStrings.xml") {
		return nil, nil
	}
	rc, err := o.reader("xl/sharedStrings.xml")
	if err != nil {
		return nil, nil
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false
	idx := -1
	inSi, inT := false, false
	var sb strings.Builder
	for {
		if err := ctx.Err(); err != nil {
			break
		}
		tok, err := dec.Token()
		if err == io.EOF || err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				idx++
				inSi = true
				sb.Reset()
			case "t":
				if inSi {
					inT = true
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "si":
				inSi = false
				if used[idx] {
					out[idx] = sb.String()
				}
			}
		case xml.CharData:
			if inT {
				sb.WriteString(string(t))
			}
		}
	}
	// 转成下标索引切片(仅到最大被引用下标)
	maxIdx := -1
	for i := range used {
		if i > maxIdx {
			maxIdx = i
		}
	}
	arr := make([]string, maxIdx+1)
	for i, v := range out {
		if i < len(arr) {
			arr[i] = v
		}
	}
	return arr, nil
}

// xlsxRowIndex 行号(`r` 属性 1-based);缺失则用 fallback(上一行 +1)。
func xlsxRowIndex(t xml.StartElement, fallback int) int {
	if v := xmlAttr(t, "r"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n - 1 // 0-based
		}
	}
	return fallback
}

// xlsxColIndex 单元格列下标(由 ref "B7" 解析;失败用 fallback)。
func xlsxColIndex(ref string, fallback int) int {
	if ref == "" {
		return fallback
	}
	n := 0
	seen := false
	for _, r := range ref {
		switch {
		case r >= 'A' && r <= 'Z':
			n = n*26 + int(r-'A'+1)
			seen = true
		case r >= 'a' && r <= 'z':
			n = n*26 + int(r-'a'+1)
			seen = true
		default:
			if seen {
				return n - 1
			}
			return fallback
		}
	}
	if seen {
		return n - 1
	}
	return fallback
}

// xlsxRefToCoord "A1" → (row0, col0);非法返回 -1。
func xlsxRefToCoord(ref string) (int, int) {
	i := 0
	col := 0
	for i < len(ref) && ((ref[i] >= 'A' && ref[i] <= 'Z') || (ref[i] >= 'a' && ref[i] <= 'z')) {
		c := ref[i]
		if c >= 'a' {
			c -= 32
		}
		col = col*26 + int(c-'A'+1)
		i++
	}
	if i == 0 || i >= len(ref) {
		return -1, -1
	}
	row, err := strconv.Atoi(ref[i:])
	if err != nil || row <= 0 {
		return -1, -1
	}
	return row - 1, col - 1
}

// xlsxFormatNumber 数值/日期格式化(日期按 xf 判定;百分比;其余去掉多余尾零)。
func xlsxFormatNumber(raw string, xf int, st *xlsxStyles) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw // 非数值原样(str/bool 等)
	}
	if st != nil {
		if st.dateXf[xf] {
			return excelSerialToTime(f).Format("2006-01-02 15:04:05")
		}
		if st.percentXf[xf] {
			return strconv.FormatFloat(f*100, 'f', 2, 64) + "%"
		}
	}
	// 普通数值:整数不带小数点;小数保留至多 6 位有效且去尾零
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(f, 'f', 6, 64), "0"), ".")
}

// excelSerialToTime Excel 序列号 → 时间(1900 日期系统,含 1900-02-29 假闰日的偏移修正)。
func excelSerialToTime(serial float64) time.Time {
	days := int(serial)
	frac := serial - float64(days)
	base := time.Date(1899, 12, 31, 0, 0, 0, 0, time.UTC)
	if days >= 60 {
		days-- // 1900 年闰日不存在
	}
	t := base.AddDate(0, 0, days)
	if frac > 0 {
		t = t.Add(time.Duration(math.Round(frac*86400)) * time.Second)
	}
	return t
}
