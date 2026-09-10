// 块模型 → 行号化 Markdown(headless CLI + 模型工具 + IM 文本降级共用)。
// 单一实现避免三端各写一套文本渲染;呈现端(TUI/Web)不走这里,直接读块模型。
package hostdocview

import (
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// renderLines 把块模型摊平为文本行(不含行号)。
func renderLines(v *sdk.DocView) []string {
	var out []string
	for i := range v.Blocks {
		b := &v.Blocks[i]
		switch b.Kind {
		case sdk.DocBlockHeading:
			lvl := b.Level
			if lvl < 1 {
				lvl = 1
			}
			if lvl > 6 {
				lvl = 6
			}
			out = append(out, strings.Repeat("#", lvl)+" "+blockText(b))
		case sdk.DocBlockParagraph:
			out = append(out, strings.Split(blockText(b), "\n")...)
		case sdk.DocBlockList:
			indent := strings.Repeat("  ", maxInt(b.Level, 0))
			out = append(out, indent+"- "+blockText(b))
		case sdk.DocBlockQuote:
			for _, l := range strings.Split(blockText(b), "\n") {
				out = append(out, "> "+l)
			}
		case sdk.DocBlockCode:
			out = append(out, "```"+b.Lang)
			out = append(out, strings.Split(b.Text, "\n")...)
			out = append(out, "```")
		case sdk.DocBlockTable:
			out = append(out, renderTableMarkdown(b)...)
		case sdk.DocBlockImage:
			name := ""
			id := ""
			if b.Asset != nil {
				name, id = b.Asset.Name, b.Asset.ID
			}
			if name == "" {
				name = "image"
			}
			alt := b.Text
			if alt == "" {
				alt = name
			}
			out = append(out, fmt.Sprintf("![%s](asset:%s)", alt, id))
		case sdk.DocBlockDivider:
			out = append(out, "---")
		case sdk.DocBlockPage:
			out = append(out, fmt.Sprintf("--- 第 %d 页 ---", b.Page))
		case sdk.DocBlockSheet:
			out = append(out, "## 工作表:"+b.Text)
			out = append(out, "")
		case sdk.DocBlockSlide:
			out = append(out, fmt.Sprintf("## 幻灯片 %d", b.Page))
		case sdk.DocBlockNote:
			out = append(out, "> 注:"+b.Text)
		case sdk.DocBlockUnsupported:
			out = append(out, "> 不支持:"+b.Text)
		default:
			if t := blockText(b); t != "" {
				out = append(out, t)
			}
		}
	}
	if len(out) == 0 {
		out = append(out, "("+string(v.Format)+" 文档无可见文本内容)")
	}
	return out
}

// renderTableMarkdown 表格 → Markdown 表(列宽不做补齐,交给呈现端)。
func renderTableMarkdown(b *sdk.DocBlock) []string {
	cols := len(b.Head)
	for _, r := range b.Rows {
		n := 0
		for _, c := range r {
			n += maxInt(c.ColSpan, 1)
		}
		if n > cols {
			cols = n
		}
	}
	if cols == 0 {
		return []string{}
	}
	cell := func(s string) string {
		s = strings.ReplaceAll(s, "|", "\\|")
		s = strings.ReplaceAll(s, "\n", " ")
		return s
	}
	var out []string
	head := make([]string, cols)
	for i := 0; i < cols; i++ {
		if i < len(b.Head) {
			head[i] = cell(b.Head[i])
		}
	}
	out = append(out, "| "+strings.Join(head, " | ")+" |")
	sep := make([]string, cols)
	for i := range sep {
		sep[i] = "---"
	}
	out = append(out, "| "+strings.Join(sep, " | ")+" |")
	for _, r := range b.Rows {
		row := make([]string, 0, cols)
		for _, c := range r {
			row = append(row, cell(c.Text))
			for k := 1; k < maxInt(c.ColSpan, 1); k++ {
				row = append(row, "")
			}
		}
		for len(row) < cols {
			row = append(row, "")
		}
		out = append(out, "| "+strings.Join(row, " | ")+" |")
	}
	return out
}

// blockText 块纯文本:优先 Runs 拼合(富文本端补标记),否则 Text。
func blockText(b *sdk.DocBlock) string {
	if len(b.Runs) == 0 {
		return strings.ReplaceAll(b.Text, "\r\n", "\n")
	}
	var sb strings.Builder
	for _, r := range b.Runs {
		switch {
		case r.Code:
			sb.WriteString("`" + r.Text + "`")
		case r.Bold && r.Italic:
			sb.WriteString("***" + r.Text + "***")
		case r.Bold:
			sb.WriteString("**" + r.Text + "**")
		case r.Italic:
			sb.WriteString("*" + r.Text + "*")
		case r.Link != "":
			sb.WriteString("[" + r.Text + "](" + r.Link + ")")
		default:
			sb.WriteString(r.Text)
		}
	}
	return sb.String()
}

// renderDocText 摊平 + 单行截断 + offset/limit 分页 + 输出字节预算。
func renderDocText(v *sdk.DocView, req sdk.DocRequest, b Budget) *sdk.DocText {
	raw := renderLines(v)
	out := &sdk.DocText{Path: v.Path, Format: v.Format, Offset: req.Offset, TotalLines: len(raw)}

	// 单行字符上限(超长行截断并标记,防单行撑爆预算)
	lines := make([]string, len(raw))
	for i, l := range raw {
		if t, cut := truncText(l, b.MaxLineChars); cut {
			lines[i] = t + "…(本行已截断)"
			out.TruncatedByBytes = true
		} else {
			lines[i] = l
		}
	}

	limit := req.Limit
	if limit <= 0 {
		limit = b.MaxTextLines
	}
	off := req.Offset
	if off < 0 {
		off = 0
	}
	if off > len(lines) {
		off = len(lines)
	}
	end := off + limit
	if end > len(lines) {
		end = len(lines)
	}
	if end < off {
		end = off
	}

	var byteBudget = b.MaxPreviewBytes
	var used int64
	for i := off; i < end; i++ {
		l := lines[i]
		used += int64(len(l)) + 1
		if used > byteBudget {
			out.TruncatedByBytes = true
			break
		}
		out.Lines = append(out.Lines, sdk.DocLine{Number: i + 1, Text: l})
	}
	out.Warnings = append(out.Warnings, v.Warnings...)
	if v.Format == sdk.DocFormatPDF {
		facts := &sdk.DocPDFFacts{PageCount: v.Pages, Kind: v.Kind}
		if v.Meta["pages_needing_ocr"] != "" {
			for _, s := range strings.Split(v.Meta["pages_needing_ocr"], ",") {
				var n int
				if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err == nil && n > 0 {
					facts.PagesNeedingOCR = append(facts.PagesNeedingOCR, n)
				}
			}
		}
		out.PDF = facts
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
