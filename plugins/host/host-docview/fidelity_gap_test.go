// 源文档高级特性 GAP 探针(DOC-1 / D6-3 判定;test-only,不进二进制)。
//
// 目的:把「自研 xlsx/docx/pptx 抽取器是否够用」从主观判断变成**可核对清单**——
// 直接读源 OOXML 容器,统计源文件实际使用了哪些高级特性,并对照我们当前输出能表达什么,
// 按严重度分类:
//
//	content = 内容级缺口(可见内容整体丢失,需用户判断是否值得引依赖)
//	style   = 样式级缺口(文字在,格式/条件格式丢失,通常可接受)
//	info    = 信息级差异(有意取舍或已有显式告警)
//
// 判定门槛(D4):**出现 content 级命中才评估引入 excelize 等重型依赖**;否则维持零依赖。
package hostdocview

import (
	"archive/zip"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// gapSeverity 缺口严重度。
const (
	gapContent = "content"
	gapStyle   = "style"
	gapInfo    = "info"
)

// gapHit 一次命中(承载部件 + 计数):同一特性可能出现在正文/页眉页脚等不同部件,
// 严重度随之不同(如页脚的文本框已被「页脚不解析」显式告警覆盖,不该报成内容级缺口)。
type gapHit struct {
	Part  string
	Count int
}

// gapProbe 一条特性探针。
type gapProbe struct {
	feature  string
	match    func(entries map[string][]byte) []gapHit
	ours     string // supported | text-only | warned | absent
	severity string
	// severityFor 可选:按承载部件调整严重度(返回空 = 用默认 severity)。
	severityFor func(part string) string
}

// gapFinding 一次命中结果(报告用)。
type gapFinding struct {
	Feature  string
	Hits     int
	Parts    []string
	Ours     string
	Severity string
}

// isAuxPart 承载部件是否为"已显式告警的辅助部件"(页眉/页脚/脚注/尾注/批注)。
// 这些部件的内容我们**显式不解析并告警**,不重复计为内容级缺口。
func isAuxPart(part string) bool {
	for _, p := range []string{"word/header", "word/footer", "word/footnotes", "word/endnotes", "word/comments"} {
		if strings.Contains(part, p) {
			return true
		}
	}
	return false
}

// downgradeAux 辅助部件 → info(默认严重度),否则用默认。
func downgradeAux(def string) func(string) string {
	return func(part string) string {
		if isAuxPart(part) {
			return gapInfo
		}
		return def
	}
}

// zipEntries 读入 OOXML 容器全部条目(体积受限:语料上限 8 MiB,条目总量与之同阶)。
func zipEntries(abs string) (map[string][]byte, error) {
	zr, err := zip.OpenReader(abs)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(rc, 8<<20))
		_ = rc.Close()
		if err == nil {
			out[f.Name] = b
		}
	}
	return out, nil
}

// nameHits 命中「条目名包含子串」的条目(每条目一条 hit)。
func nameHits(entries map[string][]byte, sub string) []gapHit {
	var out []gapHit
	for name := range entries {
		if strings.Contains(name, sub) {
			out = append(out, gapHit{Part: name, Count: 1})
		}
	}
	return out
}

// contentHits 统计各条目内容里 pattern 的出现次数(正则;0 次不入结果)。
func contentHits(entries map[string][]byte, pattern string) []gapHit {
	re := regexp.MustCompile(pattern)
	var out []gapHit
	for name, b := range entries {
		if n := len(re.FindAllIndex(b, -1)); n > 0 {
			out = append(out, gapHit{Part: name, Count: n})
		}
	}
	return out
}

// gapProbes 特性探针表(按格式)。ours/severity 是当前实现的**声明式**对照,
// 与抽取器行为一一对应(改动抽取器时须同步此表,harness 会打印声明与实测)。
func gapProbes(format sdk.DocFormat) []gapProbe {
	switch format {
	case sdk.DocFormatXLSX:
		return []gapProbe{
			{feature: "richTextCell", severity: gapStyle, ours: "text-only", match: func(e map[string][]byte) []gapHit {
				return contentHits(e, `<si>(?s:.*?)<r>`)
			}},
			{feature: "conditionalFormatting", severity: gapStyle, ours: "absent", match: func(e map[string][]byte) []gapHit {
				return contentHits(e, `conditionalFormatting`)
			}},
			{feature: "dataValidation", severity: gapStyle, ours: "absent", match: func(e map[string][]byte) []gapHit {
				return contentHits(e, `dataValidation`)
			}},
			{feature: "autoFilter", severity: gapStyle, ours: "absent", match: func(e map[string][]byte) []gapHit {
				return contentHits(e, `autoFilter`)
			}},
			{feature: "chart", severity: gapContent, ours: "absent", match: func(e map[string][]byte) []gapHit {
				return nameHits(e, "xl/charts/")
			}},
			{feature: "pivotTable", severity: gapContent, ours: "absent", match: func(e map[string][]byte) []gapHit {
				return append(nameHits(e, "pivotCache"), nameHits(e, "pivotTable")...)
			}},
			{feature: "comments", severity: gapContent, ours: "absent", match: func(e map[string][]byte) []gapHit {
				return nameHits(e, "xl/comments")
			}},
			// 现代「回复式批注」(Excel 2018+/WPS):与 legacy 批注同为独立部件承载,文字只在那里
			{feature: "threadedComments", severity: gapContent, ours: "absent", match: func(e map[string][]byte) []gapHit {
				return nameHits(e, "xl/threadedComments")
			}},
			// xlsx 文本框:文字只在 drawing 的 xdr:txBody 里(单元格中不存在)
			{feature: "textBox", severity: gapContent, ours: "absent", match: func(e map[string][]byte) []gapHit {
				var out []gapHit
				for name, b := range e {
					if !strings.Contains(name, "xl/drawings/") || !strings.HasSuffix(name, ".xml") {
						continue
					}
					if n := len(regexp.MustCompile(`<xdr:txBody`).FindAllIndex(b, -1)); n > 0 {
						out = append(out, gapHit{Part: name, Count: n})
					}
				}
				return out
			}},
			{feature: "image", severity: gapContent, ours: "absent", match: func(e map[string][]byte) []gapHit {
				return nameHits(e, "xl/media/")
			}},
			{feature: "formula", severity: gapInfo, ours: "cached-value-only", match: func(e map[string][]byte) []gapHit {
				return contentHits(e, `<f[ >]`)
			}},
			{feature: "hiddenRowCol", severity: gapInfo, ours: "absent", match: func(e map[string][]byte) []gapHit {
				return contentHits(e, `hidden="1"`)
			}},
			{feature: "mergeCell", severity: gapInfo, ours: "supported", match: func(e map[string][]byte) []gapHit {
				return contentHits(e, `<mergeCell`)
			}},
			{feature: "tableObject", severity: gapStyle, ours: "absent", match: func(e map[string][]byte) []gapHit {
				return nameHits(e, "xl/tables/")
			}},
		}
	case sdk.DocFormatDOCX:
		return []gapProbe{
			{feature: "headerFooter", severity: gapInfo, ours: "warned", match: func(e map[string][]byte) []gapHit {
				return append(nameHits(e, "word/header"), nameHits(e, "word/footer")...)
			}},
			{feature: "footnoteEndnote", severity: gapInfo, ours: "warned", match: func(e map[string][]byte) []gapHit {
				return append(nameHits(e, "word/footnotes"), nameHits(e, "word/endnotes")...)
			}},
			{feature: "comments", severity: gapContent, ours: "warned", match: func(e map[string][]byte) []gapHit {
				return nameHits(e, "word/comments")
			}},
			// 文本框:正文里的文本框会丢文字(内容级);页眉/页脚里的已由 headerFooter 告警覆盖 → info
			{feature: "textBox", severity: gapContent, ours: "absent", severityFor: downgradeAux(gapContent),
				match: func(e map[string][]byte) []gapHit { return contentHits(e, `<w:txbxContent`) }},
			{feature: "chart", severity: gapContent, ours: "absent", severityFor: downgradeAux(gapContent),
				match: func(e map[string][]byte) []gapHit { return nameHits(e, "word/charts/") }},
			{feature: "smartArt", severity: gapContent, ours: "absent", severityFor: downgradeAux(gapContent),
				match: func(e map[string][]byte) []gapHit { return nameHits(e, "word/diagrams/") }},
			{feature: "field", severity: gapInfo, ours: "absent", severityFor: downgradeAux(gapInfo),
				match: func(e map[string][]byte) []gapHit { return contentHits(e, `<w:fldSimple|<w:instrText`) }},
			{feature: "trackedChanges", severity: gapInfo, ours: "absent", severityFor: downgradeAux(gapInfo),
				match: func(e map[string][]byte) []gapHit { return contentHits(e, `<w:ins |<w:del `) }},
			{feature: "math", severity: gapInfo, ours: "text-fallback-warned", severityFor: downgradeAux(gapInfo),
				match: func(e map[string][]byte) []gapHit { return contentHits(e, `<m:oMath`) }},
			{feature: "nestedTable", severity: gapInfo, ours: "skipped-warned", severityFor: downgradeAux(gapInfo),
				match: func(e map[string][]byte) []gapHit { return nestedTableHits(e) }},
			{feature: "image", severity: gapInfo, ours: "supported", match: func(e map[string][]byte) []gapHit {
				return nameHits(e, "word/media/")
			}},
		}
	case sdk.DocFormatPPTX:
		return []gapProbe{
			{feature: "notes", severity: gapInfo, ours: "warned", match: func(e map[string][]byte) []gapHit {
				return nameHits(e, "ppt/notesSlides/")
			}},
			{feature: "chart", severity: gapContent, ours: "absent", match: func(e map[string][]byte) []gapHit {
				return nameHits(e, "ppt/charts/")
			}},
			{feature: "smartArt", severity: gapContent, ours: "absent", match: func(e map[string][]byte) []gapHit {
				return nameHits(e, "ppt/diagrams/")
			}},
			{feature: "groupedShape", severity: gapContent, ours: "absent", match: func(e map[string][]byte) []gapHit {
				return contentHits(e, `<p:grpSp`)
			}},
			{feature: "embeddedSheet", severity: gapContent, ours: "absent", match: func(e map[string][]byte) []gapHit {
				return nameHits(e, "ppt/embeddings/")
			}},
			{feature: "animation", severity: gapInfo, ours: "absent", match: func(e map[string][]byte) []gapHit {
				return contentHits(e, `<p:timing`)
			}},
			{feature: "image", severity: gapInfo, ours: "supported", match: func(e map[string][]byte) []gapHit {
				return nameHits(e, "ppt/media/")
			}},
		}
	}
	return nil
}

// nestedTableHits 检测嵌套表格(表格未闭合即再开 <w:tbl),只针对正文部件。
// 用顺序扫描而非正则(Go RE2 不支持负向环视)。
func nestedTableHits(entries map[string][]byte) []gapHit {
	doc, ok := entries["word/document.xml"]
	if !ok {
		return nil
	}
	rest := string(doc)
	depth, nested := 0, 0
	for {
		iOpen := strings.Index(rest, "<w:tbl")
		iClose := strings.Index(rest, "</w:tbl>")
		if iOpen < 0 && iClose < 0 {
			break
		}
		if iOpen >= 0 && (iClose < 0 || iOpen < iClose) {
			depth++
			if depth >= 2 {
				nested++
			}
			rest = rest[iOpen+len("<w:tbl"):]
			continue
		}
		if depth > 0 {
			depth--
		}
		rest = rest[iClose+len("</w:tbl>"):]
	}
	if nested == 0 {
		return nil
	}
	return []gapHit{{Part: "word/document.xml", Count: 1}}
}

// probeGaps 对单个文件跑探针,返回命中的特性(按严重度 + 特性名排序)。
func probeGaps(format sdk.DocFormat, abs string) ([]gapFinding, error) {
	probes := gapProbes(format)
	if len(probes) == 0 {
		return nil, nil
	}
	entries, err := zipEntries(abs)
	if err != nil {
		return nil, err
	}
	var out []gapFinding
	for _, p := range probes {
		hits := p.match(entries)
		if len(hits) == 0 {
			continue
		}
		total := 0
		parts := make([]string, 0, len(hits))
		sev := ""
		for _, h := range hits {
			total += h.Count
			parts = append(parts, h.Part)
			// 每部件先算自身严重度(severityFor 可下调),多个部件取**最严重**者
			ps := p.severity
			if p.severityFor != nil {
				if s := p.severityFor(h.Part); s != "" {
					ps = s
				}
			}
			if sev == "" || sevRank(ps) < sevRank(sev) {
				sev = ps
			}
		}
		sort.Strings(parts)
		out = append(out, gapFinding{Feature: p.feature, Hits: total, Parts: parts, Ours: p.ours, Severity: sev})
	}
	sort.Slice(out, func(i, j int) bool {
		if sevRank(out[i].Severity) != sevRank(out[j].Severity) {
			return sevRank(out[i].Severity) < sevRank(out[j].Severity)
		}
		return out[i].Feature < out[j].Feature
	})
	return out, nil
}

// sevRank 严重度排序(越小越严重:content < style < info)。
func sevRank(s string) int {
	switch s {
	case gapContent:
		return 0
	case gapStyle:
		return 1
	}
	return 2
}

// gapSummary 按严重度汇总("content=2,style=1,info=3")。
func gapSummary(fs []gapFinding) string {
	counts := map[string]int{}
	for _, f := range fs {
		counts[f.Severity]++
	}
	if len(counts) == 0 {
		return "none"
	}
	parts := []string{}
	for _, sev := range []string{gapContent, gapStyle, gapInfo} {
		if counts[sev] > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", sev, counts[sev]))
		}
	}
	return strings.Join(parts, ",")
}
