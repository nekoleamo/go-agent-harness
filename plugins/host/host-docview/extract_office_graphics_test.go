// DOC-3c 单测:docx 图表缓存数据(→ 表格块)与 SmartArt 文字(→ note)。
package hostdocview

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func graphicsFixture(t *testing.T, chart, diagram string, withRels bool) map[string][]byte {
	t.Helper()
	fix := docxAnnFixture(t)
	rels := `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rIdChart" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/chart" Target="charts/chart1.xml"/>
  <Relationship Id="rIdDiag" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/diagramData" Target="diagrams/data1.xml"/>
</Relationships>`
	if !withRels {
		rels = `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>`
	}
	fix["word/_rels/document.xml.rels"] = []byte(rels)
	fix["word/charts/chart1.xml"] = []byte(chart)
	fix["word/diagrams/data1.xml"] = []byte(diagram)
	return fix
}

const chartTwoSeries = `<?xml version="1.0" encoding="UTF-8"?>
<c:chartSpace xmlns:c="http://schemas.openxmlformats.org/drawingml/2006/chart" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
<c:chart><c:title><c:tx><c:rich><a:p><a:r><a:t>季度对比</a:t></a:r></a:p></c:rich></c:tx></c:title>
<c:plotArea><c:barChart>
<c:ser><c:idx val="0"/><c:order val="0"/>
 <c:tx><c:strRef><c:strCache><c:pt idx="0"><c:v>预算</c:v></c:pt></c:strCache></c:strRef></c:tx>
 <c:cat><c:strRef><c:strCache>
   <c:pt idx="0"><c:v>Q1</c:v></c:pt><c:pt idx="1"><c:v>Q2</c:v></c:pt><c:pt idx="2"><c:v>Q3</c:v></c:pt></c:strCache></c:strRef></c:cat>
 <c:val><c:numRef><c:numCache>
   <c:pt idx="0"><c:v>100</c:v></c:pt><c:pt idx="2"><c:v>300</c:v></c:pt><c:pt idx="1"><c:v>200</c:v></c:pt></c:numCache></c:numRef></c:val>
</c:ser>
<c:ser><c:idx val="1"/><c:order val="1"/>
 <c:cat><c:strRef><c:strCache><c:pt idx="0"><c:v>Q1</c:v></c:pt><c:pt idx="1"><c:v>Q2</c:v></c:pt></c:strCache></c:strRef></c:cat>
 <c:val><c:numRef><c:numCache><c:pt idx="0"><c:v>120</c:v></c:pt><c:pt idx="1"><c:v>180</c:v></c:pt></c:numCache></c:numRef></c:val>
</c:ser>
</c:barChart></c:plotArea></c:chart></c:chartSpace>`

const chartNoCache = `<?xml version="1.0" encoding="UTF-8"?>
<c:chartSpace xmlns:c="http://schemas.openxmlformats.org/drawingml/2006/chart" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
<c:chart><c:title><c:tx><c:rich><a:p><a:r><a:t>外部数据图表</a:t></a:r></a:p></c:rich></c:tx></c:title>
<c:plotArea><c:barChart><c:ser><c:idx val="0"/><c:val><c:numRef><c:f>External!A1:A5</c:f></c:numRef></c:val></c:ser></c:barChart></c:plotArea></c:chart></c:chartSpace>`

const diagramText = `<?xml version="1.0" encoding="UTF-8"?>
<dgm:dataModel xmlns:dgm="http://schemas.openxmlformats.org/drawingml/2006/diagram" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
<dgm:ptLst>
<dgm:pt modelId="1"><dgm:t><a:p><a:r><a:t>流程一</a:t></a:r></a:p></dgm:t></dgm:pt>
<dgm:pt modelId="2"><dgm:t><a:p><a:r><a:t>流程二</a:t></a:r></a:p></dgm:t></dgm:pt>
<dgm:pt modelId="3"><dgm:t><a:p><a:r><a:t>流程一</a:t></a:r></a:p></dgm:t></dgm:pt>
</dgm:ptLst></dgm:dataModel>`

func chartBlockOf(v *sdk.DocView) *sdk.DocBlock {
	for i := range v.Blocks {
		if v.Blocks[i].Kind == sdk.DocBlockTable && len(v.Blocks[i].Head) > 1 && v.Blocks[i].Head[0] == "类别" {
			return &v.Blocks[i]
		}
	}
	return nil
}

// TestDOCXChartDataExtracted:标题 → note;类别 + 每系列数值 → 表格(idx 乱序须按 idx 排序)。
func TestDOCXChartDataExtracted(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "chart.docx", zipParts(t, graphicsFixture(t, chartTwoSeries, diagramText, true)))
	v := preview(t, s, p)
	notes := annNotes(v)
	if !containsSub(notes, "图表: 季度对比") {
		t.Fatalf("图表标题应为 note: %v", notes)
	}
	if !containsSub(notes, "SmartArt 文字: 流程一 / 流程二") {
		t.Fatalf("SmartArt 文字应去重并合并: %v", notes)
	}
	tbl := chartBlockOf(v)
	if tbl == nil {
		t.Fatalf("应产出图表数据表格: %+v", v.Blocks)
	}
	if strings.Join(tbl.Head, "|") != "类别|预算|系列 2" {
		t.Fatalf("表头应为 类别 + 系列名(缺失用系列 N): %v", tbl.Head)
	}
	if len(tbl.Rows) != 3 {
		t.Fatalf("行数应为最长系列行数 3: %d", len(tbl.Rows))
	}
	// 首行:Q1 / 100 / 120;且 idx 乱序(0,2,1)须按 idx 重排
	got := []string{tbl.Rows[0][0].Text, tbl.Rows[0][1].Text, tbl.Rows[0][2].Text}
	if strings.Join(got, "|") != "Q1|100|120" {
		t.Fatalf("首行不符(应按 idx 排序): %v", got)
	}
	if tbl.Rows[1][1].Text != "200" || tbl.Rows[2][1].Text != "300" {
		t.Fatalf("idx 乱序未按 idx 重排: %v", tbl.Rows)
	}
	if !tbl.Rows[0][1].Numeric {
		t.Fatal("数值列应标记 Numeric")
	}
	if tbl.Rows[2][2].Text != "" {
		t.Fatalf("短系列缺位应留空: %q", tbl.Rows[2][2].Text)
	}
}

// TestDOCXChartNoCacheAndFallback:无缓存 → 显式说明;rels 缺失 → 按前缀回退仍能抽取。
func TestDOCXChartNoCacheAndFallback(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "nocache.docx", zipParts(t, graphicsFixture(t, chartNoCache, diagramText, true)))
	v := preview(t, s, p)
	if !containsSub(annNotes(v), "未含缓存数据") {
		t.Fatalf("无缓存图表应显式说明: %v", annNotes(v))
	}
	if chartBlockOf(v) != nil {
		t.Fatal("无缓存不应产出表格")
	}
	// rels 缺失:仅靠部件前缀扫描
	p2 := writeFile(t, dir, "norel.docx", zipParts(t, graphicsFixture(t, chartTwoSeries, diagramText, false)))
	v2 := preview(t, s, p2)
	if chartBlockOf(v2) == nil {
		t.Fatalf("rels 缺失时应按前缀回退抽取图表: %+v", v2.Blocks)
	}
	if !containsSub(annNotes(v2), "SmartArt 文字") {
		t.Fatalf("rels 缺失时应按前缀回退抽取 SmartArt: %v", annNotes(v2))
	}
}

// TestDOCXChartTableBudget:行列预算封顶 + 显式告警。
func TestDOCXChartTableBudget(t *testing.T) {
	s, dir := newSvc(t, Budget{MaxTableRows: 2, MaxTableCols: 2})
	p := writeFile(t, dir, "cap.docx", zipParts(t, graphicsFixture(t, chartTwoSeries, diagramText, true)))
	v := preview(t, s, p)
	tbl := chartBlockOf(v)
	if tbl == nil || len(tbl.Rows) != 2 || len(tbl.Head) != 2 {
		t.Fatalf("行列预算未生效: %+v", tbl)
	}
	if !containsSub(v.Warnings, "截断") {
		t.Fatalf("截断应告警: %v", v.Warnings)
	}
}

// TestPPTXChartExtracted:pptx 幻灯片级图表同样抽取(rels 在 slideN.xml.rels)。
func TestPPTXChartExtracted(t *testing.T) {
	fix := pptxNotesFixture(t, `<p:notes/>`, false)
	fix["ppt/slides/_rels/slide1.xml.rels"] = []byte(`<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rIdChart" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/chart" Target="../charts/chart1.xml"/>
</Relationships>`)
	fix["ppt/charts/chart1.xml"] = []byte(chartTwoSeries)
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "deck-chart.pptx", zipParts(t, fix))
	v := preview(t, s, p)
	tbl := chartBlockOf(v)
	if tbl == nil {
		t.Fatalf("pptx 图表数据应抽取为表格: %+v", v.Blocks)
	}
	if tbl.Page == 0 {
		t.Fatal("pptx 表格块应带幻灯片号")
	}
	if !containsSub(annNotes(v), "图表: 季度对比") {
		t.Fatalf("pptx 图表标题应为 note: %v", annNotes(v))
	}
}
