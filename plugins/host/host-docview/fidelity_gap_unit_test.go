// GAP 探针单测(DOC-1):合成 OOXML 容器锁定「特性 → 严重度」判定规则。
// 探针本身是 D6-3 判定依据,误报/漏报都会误导决策 → 必须与抽取器行为一一对应地测。
package hostdocview

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// zipOf 合成最小 OOXML 容器(name → 内容)。
func zipOf(t *testing.T, files map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sample.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func gapOf(t *testing.T, format sdk.DocFormat, files map[string]string) map[string]gapFinding {
	t.Helper()
	fs, err := probeGaps(format, zipOf(t, files))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]gapFinding{}
	for _, f := range fs {
		out[f.Feature] = f
	}
	return out
}

func TestGapProbeXLSX(t *testing.T) {
	g := gapOf(t, sdk.DocFormatXLSX, map[string]string{
		"[Content_Types].xml":                     `<Types/>`,
		"xl/sharedStrings.xml":                    `<sst><si><t>a</t></si><si><r><t>b</t></r><r><t>c</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml":                `<worksheet><mergeCells><mergeCell ref="A1:B1"/></mergeCells><row r="1" hidden="1"><c r="A1"><f>SUM(B1:B2)</f><v>3</v></c></row><autoFilter ref="A1:B2"/><conditionalFormatting sqref="A1"/><dataValidation/></worksheet>`,
		"xl/charts/chart1.xml":                    `<chart/>`,
		"xl/pivotCache/pivotCacheDefinition1.xml": `<pivotCacheDefinition/>`,
		"xl/comments1.xml":                        `<comments/>`,
		"xl/media/image1.png":                     "PNG",
		"xl/tables/table1.xml":                    `<table/>`,
	})
	want := map[string]string{
		"richTextCell": gapStyle, "conditionalFormatting": gapStyle, "dataValidation": gapStyle,
		"autoFilter": gapStyle, "tableObject": gapStyle,
		"chart": gapContent, "pivotTable": gapContent, "comments": gapContent, "image": gapContent,
		"formula": gapInfo, "hiddenRowCol": gapInfo, "mergeCell": gapInfo,
	}
	if len(g) != len(want) {
		t.Fatalf("命中特性数不符:得 %d want %d(%v)", len(g), len(want), keysOf(g))
	}
	for feat, sev := range want {
		f, ok := g[feat]
		if !ok {
			t.Fatalf("缺特性 %s(得 %v)", feat, keysOf(g))
		}
		if f.Severity != sev {
			t.Fatalf("%s 严重度应为 %s,得 %s", feat, sev, f.Severity)
		}
		if f.Ours == "" {
			t.Fatalf("%s 应声明 ours 对照", feat)
		}
	}
	if g["mergeCell"].Ours != "supported" {
		t.Fatalf("mergeCell 应标注 supported: %+v", g["mergeCell"])
	}
	// 无高级特性 → 无报告
	if g2 := gapOf(t, sdk.DocFormatXLSX, map[string]string{"xl/worksheets/sheet1.xml": `<worksheet><row r="1"><c r="A1"><v>1</v></c></row></worksheet>`}); len(g2) != 0 {
		t.Fatalf("普通表不应报缺口: %v", keysOf(g2))
	}
}

func TestGapProbeDOCXSeverityByPart(t *testing.T) {
	// 正文文本框 = 内容级缺口(文字会丢)
	g := gapOf(t, sdk.DocFormatDOCX, map[string]string{
		"word/document.xml": `<w:document><w:txbxContent><w:t>正文框内文字</w:t></w:txbxContent></w:document>`,
	})
	if f := g["textBox"]; f.Severity != gapContent {
		t.Fatalf("正文文本框应报内容级: %+v", f)
	}
	// 页眉/页脚文本框 = info(已由「页脚不解析」显式告警覆盖,不重复计为内容缺口)
	g2 := gapOf(t, sdk.DocFormatDOCX, map[string]string{
		"word/footer1.xml": `<w:ftr><w:txbxContent><w:t>11</w:t></w:txbxContent></w:ftr>`,
	})
	if f := g2["textBox"]; f.Severity != gapInfo {
		t.Fatalf("页脚文本框应降级为 info: %+v", f)
	}
	if !strings.Contains(strings.Join(f2parts(g2["textBox"]), ";"), "footer1.xml") {
		t.Fatalf("报告应给出承载部件: %+v", g2["textBox"].Parts)
	}
	// 正文与页脚同时命中 → 取最严重(content)
	g3 := gapOf(t, sdk.DocFormatDOCX, map[string]string{
		"word/document.xml": `<w:document><w:txbxContent><w:t>x</w:t></w:txbxContent></w:document>`,
		"word/header1.xml":  `<w:hdr><w:txbxContent><w:t>y</w:t></w:txbxContent></w:hdr>`,
	})
	if f := g3["textBox"]; f.Severity != gapContent || f.Hits != 2 {
		t.Fatalf("多部件应取最严重且计数合计: %+v", f)
	}
	// 图表/SmartArt(正文内嵌)= content;页眉内 → info
	g4 := gapOf(t, sdk.DocFormatDOCX, map[string]string{
		"word/charts/chart1.xml":  `<chart/>`,
		"word/diagrams/data1.xml": `<dgm:dataModel/>`,
		"word/header2.xml":        `<w:hdr><w:tbl><w:tbl><w:t>x</w:t></w:tbl></w:tbl></w:hdr>`,
		"word/footnotes.xml":      `<w:footnotes><w:ins ><w:t>f</w:t></w:ins></w:footnotes>`,
	})
	if g4["chart"].Severity != gapContent || g4["smartArt"].Severity != gapContent {
		t.Fatalf("正文图表/SmartArt 应报内容级: %+v %+v", g4["chart"], g4["smartArt"])
	}
	if g4["trackedChanges"].Severity != gapInfo {
		t.Fatalf("脚注内修订应降级: %+v", g4["trackedChanges"])
	}
	// 嵌套表格:仅正文计数
	g5 := gapOf(t, sdk.DocFormatDOCX, map[string]string{
		"word/document.xml": `<w:document><w:tbl><w:tbl><w:t>n</w:t></w:tbl></w:tbl></w:document>`,
	})
	if g5["nestedTable"].Hits != 1 {
		t.Fatalf("应识别嵌套表格: %+v", g5["nestedTable"])
	}
	if len(gapOf(t, sdk.DocFormatDOCX, map[string]string{"word/document.xml": `<w:document><w:tbl><w:t>x</w:t></w:tbl></w:document>`})) != 0 {
		t.Fatal("单层表格不应报嵌套")
	}
}

func TestGapProbePPTXAndSummary(t *testing.T) {
	g := gapOf(t, sdk.DocFormatPPTX, map[string]string{
		"ppt/slides/slide1.xml":           `<p:sld><p:grpSp><p:timing/></p:grpSp></p:sld>`,
		"ppt/notesSlides/notesSlide1.xml": `<p:notes/>`,
		"ppt/charts/chart1.xml":           `<chart/>`,
		"ppt/diagrams/data1.xml":          `<dgm/>`,
		"ppt/embeddings/oleObject1.bin":   "BIN",
		"ppt/media/image1.png":            "PNG",
	})
	for feat, sev := range map[string]string{
		"groupedShape": gapContent, "chart": gapContent, "smartArt": gapContent, "embeddedSheet": gapContent,
		"notes": gapInfo, "animation": gapInfo, "image": gapInfo,
	} {
		f, ok := g[feat]
		if !ok || f.Severity != sev {
			t.Fatalf("%s 应报 %s,得 %+v", feat, sev, f)
		}
	}
	// 汇总摘要:只列命中档位
	if got := gapSummary([]gapFinding{{Severity: gapContent}, {Severity: gapInfo}, {Severity: gapInfo}}); got != "content=1,info=2" {
		t.Fatalf("汇总格式异常: %q", got)
	}
	if got := gapSummary(nil); got != "none" {
		t.Fatalf("空汇总应为 none: %q", got)
	}
	// 排序:content 在前
	fs, _ := probeGaps(sdk.DocFormatXLSX, zipOf(t, map[string]string{
		"xl/charts/chart1.xml": `<chart/>`, "xl/worksheets/s1.xml": `<mergeCells><mergeCell/></mergeCells>`,
	}))
	if len(fs) != 2 || fs[0].Severity != gapContent || fs[1].Severity != gapInfo {
		t.Fatalf("应按严重度排序: %+v", fs)
	}
	// zip 不可读 → 结构化错误(不 panic)
	if _, err := probeGaps(sdk.DocFormatXLSX, filepath.Join(t.TempDir(), "nope.xlsx")); err == nil {
		t.Fatal("不存在文件应报错")
	}
	// 无探针格式(markdown)→ 空结果
	if fs, err := probeGaps(sdk.DocFormatMarkdown, "x.md"); err != nil || fs != nil {
		t.Fatalf("非 OOXML 格式应返回空: %v %v", fs, err)
	}
}

func f2parts(f gapFinding) []string { return f.Parts }

func keysOf(m map[string]gapFinding) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k+"/"+m[k].Severity)
	}
	return out
}
