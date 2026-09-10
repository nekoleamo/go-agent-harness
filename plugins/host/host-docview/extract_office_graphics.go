// Office 图形内容抽取(DOC-3c;DOC-2/3a/3b 同构的零依赖窄修):
//
//	① **图表数据**:docx `word/charts/chart*.xml` 与 pptx `ppt/charts/chart*.xml` 里的
//	   **缓存数据**(`c:strCache`/`c:numCache`/`c:multiLvlStrCache`)—— 类别轴标签与各系列数值。
//	   图表视觉不做(与「阅读视图」定位一致),但图表数据是**内容**,此前完全丢失;
//	   实测(判定文档)证明缓存与源数据同源,且 pptx/docx 内嵌 xlsx 的数据也体现在缓存里,
//	   故只取缓存即可覆盖内容(内嵌 xlsx 仍不解包,见 DESIGN 已登记缺口)。
//	② **SmartArt 文字**:`word/diagrams/data*.xml`、`ppt/diagrams/data*.xml` 的
//	   `dgm:dataModel` 内 `a:t` 文本 → note(图形版式不做)。
//
// 纪律:零第三方依赖;部件读取走 `o.reader` 预算;行列按 Budget 封顶并显式告警。
package hostdocview

import (
	"encoding/xml"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// chartSeries 一个图表系列(名称 + 类别 + 数值;缓存顺序即绘制顺序)。
type chartSeries struct {
	Name    string
	Cat     []string
	Vals    []string
	numeric bool
}

// officeGraphicsBlocks 抽取图表数据与 SmartArt 文字。
// relSource:关系表所属部件(如 word/document.xml / ppt/slides/slide1.xml);
// chartPrefix/diagramPrefix:rels 缺失时的回退扫描前缀。
func officeGraphicsBlocks(s *Service, o *ooxml, relSource, chartPrefix, diagramPrefix string) ([]sdk.DocBlock, []string) {
	var blocks []sdk.DocBlock
	var warns []string

	for _, part := range graphicsParts(o, relSource, "/chart", chartPrefix) {
		title, series := parseChartPart(o, part)
		if len(series) == 0 {
			if title != "" || o.has(part) {
				// 图表存在但缓存为空(未保存数据/仅链接外部数据源)→ 显式说明,不静默
				blocks = append(blocks, sdk.DocBlock{Kind: sdk.DocBlockNote,
					Text: fmt.Sprintf("图表(%s): 未含缓存数据(源数据在外部或未保存),仅视觉可见", chartLabel(title, part))})
			}
			continue
		}
		if title != "" {
			blocks = append(blocks, sdk.DocBlock{Kind: sdk.DocBlockNote, Text: "图表: " + title})
		}
		tbl, trunc := chartTable(s.budget, series)
		if len(tbl.Head) > 0 || len(tbl.Rows) > 0 {
			blocks = append(blocks, tbl)
		}
		if trunc != "" {
			warns = append(warns, fmt.Sprintf("图表(%s)数据%s", chartLabel(title, part), trunc))
		}
	}

	for _, part := range graphicsParts(o, relSource, "diagramData", diagramPrefix) {
		text, err := parseDiagramText(o, part)
		if err != nil {
			warns = append(warns, fmt.Sprintf("SmartArt 部件 %s 解析失败: %v", part, err))
			continue
		}
		if text != "" {
			blocks = append(blocks, sdk.DocBlock{Kind: sdk.DocBlockNote, Text: "SmartArt 文字: " + text})
		}
	}
	return blocks, warns
}

// chartLabel 图表标识(标题优先,否则部件名)。
func chartLabel(title, part string) string {
	if title != "" {
		return title
	}
	return strings.TrimSuffix(strings.TrimPrefix(part, "word/"), ".xml")
}

// graphicsParts 取关系表中匹配 kindSuffix 的部件(去重保序);rels 无命中时按前缀扫描回退。
func graphicsParts(o *ooxml, relSource, kindSuffix, prefix string) []string {
	var out []string
	seen := map[string]bool{}
	ids := make([]string, 0, 4)
	rels := o.rels(relSource)
	for id, rel := range rels {
		if strings.HasSuffix(rel.Type, kindSuffix) && rel.Target != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids) // 关系表为 map → 排序保证输出确定性
	for _, id := range ids {
		target := rels[id].Target
		if !seen[target] && o.has(target) {
			seen[target] = true
			out = append(out, target)
		}
	}
	if len(out) == 0 { // 回退:按部件名前缀扫描(部分产出器不写关系)
		names := make([]string, 0, 4)
		for n := range o.files {
			if strings.HasPrefix(n, prefix) && strings.HasSuffix(n, ".xml") {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// parseChartPart 流式解析图表部件:标题(`c:title` 内首个 `a:t`)+ 各系列
// (`c:ser`:名称 `c:tx`、类别 `c:cat`、数值 `c:val` 的缓存点)。
func parseChartPart(o *ooxml, part string) (string, []chartSeries) {
	rc, err := o.reader(part)
	if err != nil {
		return "", nil
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false
	var (
		title   string
		series  []chartSeries
		cur     *chartSeries
		zone    string // tx | cat | val | ""
		pts     []string
		idx     []int
		inTitle bool
		inV     bool // 正在 c:v 内
		inT     bool // 正在 a:t 内(标题)
	)
	flush := func() {
		if zone == "" {
			return
		}
		vals := reorderByIdx(pts, idx)
		switch zone {
		case "tx":
			if cur != nil && cur.Name == "" {
				cur.Name = strings.Join(vals, " ")
			}
		case "cat":
			if cur != nil {
				cur.Cat = vals
			}
		case "val":
			if cur != nil {
				cur.Vals = vals
			}
		}
		zone, pts, idx = "", nil, nil
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "ser":
				flush()
				cur = &chartSeries{}
			case "title":
				if title == "" && !inTitle {
					inTitle = true
				}
			case "tx", "cat", "val":
				flush()
				zone = t.Name.Local
			case "pt":
				pts = append(pts, "")
				idx = append(idx, -1)
				if v := xmlAttr(t, "idx"); v != "" {
					if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && len(idx) > 0 {
						idx[len(idx)-1] = n
					}
				}
			case "v":
				// c:v 紧跟在 c:pt 内;标题内的 a:t 由 CharData 处理
				if zone != "" && len(pts) > 0 {
					inV = true
				}
			case "t":
				if inTitle {
					inT = true
				}
			}
		case xml.CharData:
			if inV && zone != "" && len(pts) > 0 {
				pts[len(pts)-1] += string(t)
			}
			if inT && inTitle {
				title += string(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "title":
				inTitle = false
			case "ser":
				flush()
				if cur != nil {
					if len(cur.Vals) > 0 || len(cur.Cat) > 0 || cur.Name != "" {
						cur.numeric = numbersOnly(cur.Vals)
						series = append(series, *cur)
					}
					cur = nil
				}
			}
		}
	}
	flush()
	if cur != nil && (len(cur.Vals) > 0 || len(cur.Cat) > 0) {
		series = append(series, *cur)
	}
	return collapseSpace(title), series
}

// parseDiagramText 取 SmartArt 数据模型的文字(`dgm:dataModel` 内全部 `a:t`)。
func parseDiagramText(o *ooxml, part string) (string, error) {
	rc, err := o.reader(part)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false
	var texts []string
	seen := map[string]bool{}
	inT := false
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inT = true
			}
		case xml.CharData:
			if inT {
				sb.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				if s := strings.TrimSpace(sb.String()); s != "" && !seen[s] {
					seen[s] = true
					texts = append(texts, s)
				}
				sb.Reset()
				inT = false
			}
		}
	}
	return strings.Join(texts, " / "), nil
}

// chartTable 系列 → 表格块(首列类别 + 每系列一列);按 Budget 行列封顶。
func chartTable(b Budget, series []chartSeries) (sdk.DocBlock, string) {
	head := []string{"类别"}
	var cats []string
	for _, s := range series {
		name := s.Name
		if name == "" {
			name = fmt.Sprintf("系列 %d", len(head))
		}
		head = append(head, name)
		if len(s.Cat) > len(cats) {
			cats = s.Cat
		}
	}
	rows := len(cats)
	if rows == 0 {
		for _, s := range series {
			if len(s.Vals) > len(cats) {
				cats = make([]string, len(s.Vals))
			}
		}
		rows = len(cats)
	}
	trunc := ""
	if maxRows := b.MaxTableRows; maxRows > 0 && rows > maxRows {
		trunc += fmt.Sprintf("行数截断 %d→%d;", rows, maxRows)
		rows = maxRows
	}
	if maxCols := b.MaxTableCols; maxCols > 0 && len(head) > maxCols {
		trunc += fmt.Sprintf("列数截断 %d→%d;", len(head), maxCols)
		head = head[:maxCols]
	}
	out := sdk.DocBlock{Kind: sdk.DocBlockTable, Head: head}
	for i := 0; i < rows; i++ {
		cells := make([]sdk.DocCell, 0, len(head))
		label := ""
		if i < len(cats) {
			label = cats[i]
		}
		cells = append(cells, sdk.DocCell{Text: label})
		for si := 1; si < len(head); si++ {
			s := series[si-1]
			v := ""
			if i < len(s.Vals) {
				v = s.Vals[i]
			}
			cells = append(cells, sdk.DocCell{Text: v, Numeric: s.numeric})
		}
		out.Rows = append(out.Rows, cells)
	}
	return out, trunc
}

// reorderByIdx 按 c:pt 的 idx 属性排序(缓存点可能乱序;idx 缺失按出现顺序)。
func reorderByIdx(pts []string, idx []int) []string {
	if len(pts) == 0 {
		return nil
	}
	order := make([]int, len(pts))
	for i := range order {
		order[i] = i
	}
	missing := false
	for _, v := range idx {
		if v < 0 {
			missing = true
			break
		}
	}
	if !missing {
		sort.SliceStable(order, func(a, bb int) bool { return idx[order[a]] < idx[order[bb]] })
	}
	out := make([]string, 0, len(pts))
	for _, i := range order {
		out = append(out, strings.TrimSpace(pts[i]))
	}
	return out
}

// numbersOnly 全部取值是否为数值(表格单元格 Numeric 标记)。
func numbersOnly(vals []string) bool {
	n := 0
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		n++
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return false
		}
	}
	return n > 0
}
