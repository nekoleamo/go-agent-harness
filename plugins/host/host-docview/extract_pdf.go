// PDF 抽取器(D4):文本/块/表格经 gopdf 抽取,保真由浏览器原生查看器负责。
//
// 依赖决策 K1(已锁):github.com/Detective-XH/gopdf(BSD-3-Clause,纯 Go,无 CGO/渲染/OCR/网络;
// go.mod 仅需 golang.org/x/text —— gah 已依赖,传递依赖净增 0)。
//
// 分工(对齐 DOC_PREVIEW_PLAN §5.4):
//   - 保真:Web/桌面 → `/api/doc/raw` + 浏览器原生查看器(翻页/缩放/文本层);Linux 桌面壳
//     (WebKitGTK)不支持内嵌 PDF → K2 已锁:前端降级为「下载」,不引入 pdf.js;
//   - 文本:TUI/CLI/IM/模型 → gopdf `Page.Blocks()`(列优先阅读序)+ `GetPlainText` 兜底;
//   - 页事实:`DocumentSummary()` → `Kind`(text/scanned/empty/mixed)+ `pages_needing_ocr`;
//   - 表格:`Page.Tables()` 仅取全框线类,低置信度降级为文本并记 warning;
//   - 内嵌图:只给元数据(`Page.Images()` 不解码;真图由浏览器查看器呈现);
//   - 加密:`NewReaderEncrypted` + 口令回调(缺口令 → 结构化「需要口令」);
//   - 警告:`Reader.Warnings()` 透传(上限 50 条)。
package hostdocview

import (
	"context"
	"fmt"
	"os"
	"strings"

	pdf "github.com/Detective-XH/gopdf"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// pdfFactsMaxPages 页事实扫描上限(DocumentSummary 逐页跑抽取解释器,大文档不可中断,
// 超限则只给页数并显式警告,避免吃满抽取超时)。
const pdfFactsMaxPages = 1000

// pdfWarnCap 透传到 UI 的警告条数上限(超出计数提示)。
const pdfWarnCap = 50

// pdfImageNoteCap 单页图片元数据提示上限。
const pdfImageNoteCap = 8

// pdfPasswordEnv PDF 口令环境变量(加密文档;不写盘不入日志)。
const pdfPasswordEnv = "GAH_PDF_PASSWORD"

// extractPDF PDF:页事实 + 逐页文本块/表格/图片元数据(Warnings 全透传)。
func extractPDF(ctx context.Context, s *Service, abs string, fi os.FileInfo, req sdk.DocRequest, format sdk.DocFormat) (*sdk.DocView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r, err := openPDF(f, fi.Size())
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "encrypt") || strings.Contains(msg, "password") || strings.Contains(msg, "decrypt") {
			return nil, fmt.Errorf("%w: PDF 已加密,需口令(经 %s 提供,或先用本地查看器另存为无加密副本): %v",
				sdk.ErrDocUnsupported, pdfPasswordEnv, err)
		}
		return nil, fmt.Errorf("%w: 打开 PDF 失败: %v", sdk.ErrDocParse, err)
	}
	_ = s

	v := baseView(req, abs, fi.Size(), fi.ModTime().UnixNano(), format)
	info := r.Info()
	v.Title = strings.TrimSpace(info.Title())
	v.Author = strings.TrimSpace(info.Author())
	v.Pages = r.NumPage()

	// —— 页事实(DocumentSummary:Kind + 需 OCR 页)——
	ocrPages := make([]string, 0, 8)
	pageKind := map[int]string{}
	if v.Pages <= pdfFactsMaxPages {
		ds := r.DocumentSummary()
		v.Kind = classifyPDFKind(ds)
		for _, p := range ds.Pages {
			pageKind[p.Page] = string(p.Signal)
			if p.Signal == pdf.SignalImageOnly {
				ocrPages = append(ocrPages, fmt.Sprintf("%d", p.Page))
			}
		}
		if v.Kind == "scanned" {
			v.Warnings = append(v.Warnings, "PDF 无文本层(疑似扫描件),需 OCR 才能检索;当前只做识别与提示,不内置 OCR")
		} else if v.Kind == "mixed" {
			v.Warnings = append(v.Warnings, "PDF 部分页无文本层(图文混排/扫描页),这些页无文本可抽取")
		}
	} else {
		v.Warnings = append(v.Warnings, fmt.Sprintf("PDF 页数 %d 超过页事实扫描上限 %d,已跳过逐页分类", v.Pages, pdfFactsMaxPages))
	}
	if len(ocrPages) > 0 {
		if v.Meta == nil {
			v.Meta = map[string]string{}
		}
		v.Meta["pages_needing_ocr"] = strings.Join(ocrPages, ",")
	}
	// 页码标签(罗马数字前言等;仅供 pager/UI 展示)
	if labels := r.PageLabels(); len(labels) > 0 && len(labels) <= 64 {
		if v.Meta == nil {
			v.Meta = map[string]string{}
		}
		v.Meta["page_labels"] = strings.Join(labels, ",")
	}

	// —— 逐页文本/表格/图片元数据 ——
	pages := requestedPages(req, v.Pages)
	stopped := false
	for _, pn := range pages {
		if err := ctx.Err(); err != nil {
			v.Warnings = append(v.Warnings, fmt.Sprintf("抽取超时:已完成前 %d 页", pn-1))
			stopped = true
			break
		}
		if len(v.Blocks) >= s.budget.MaxBlocks {
			stopped = true
			break
		}
		page := r.Page(pn)
		if page.V.IsNull() {
			continue
		}
		sig := pageKind[pn]
		v.Blocks = append(v.Blocks, sdk.DocBlock{Kind: sdk.DocBlockPage, Page: pn})

		// 表格(仅全框线类;低置信度 → 文本 + warning)
		tables, terr := page.Tables()
		if terr != nil {
			v.Warnings = append(v.Warnings, fmt.Sprintf("第 %d 页表格重建失败: %v", pn, terr))
		}
		tblByCell := map[string]bool{}
		for _, t := range tables {
			if len(t.Cells) == 0 {
				continue
			}
			blk := sdk.DocBlock{Kind: sdk.DocBlockTable, Page: pn}
			lowConf := t.Confidence != pdf.TableConfidenceHigh
			rows := t.Cells
			if len(rows) > s.budget.MaxTableRows {
				rows = rows[:s.budget.MaxTableRows]
				v.Truncated = append(v.Truncated, fmt.Sprintf("p%d rows:%d/%d", pn, s.budget.MaxTableRows, len(t.Cells)))
			}
			for _, row := range rows {
				cells := make([]sdk.DocCell, 0, len(row))
				for i, c := range row {
					if i >= s.budget.MaxTableCols {
						break
					}
					txt := truncCell(c, s.budget.MaxCellChars)
					tblByCell[strings.TrimSpace(txt)] = true
					cells = append(cells, sdk.DocCell{Text: txt, Numeric: isNumeric(txt)})
				}
				blk.Rows = append(blk.Rows, cells)
			}
			if len(tblByCell) > 0 && len(blk.Rows) >= 2 {
				blk.Head = make([]string, len(blk.Rows[0]))
				for i, c := range blk.Rows[0] {
					blk.Head[i] = c.Text
				}
				blk.Rows = blk.Rows[1:]
				v.Blocks = append(v.Blocks, blk)
				if lowConf {
					v.Warnings = append(v.Warnings, fmt.Sprintf("第 %d 页表格置信度低(可能误判/漏线),已按文本网格呈现;请以原始 PDF 为准", pn))
				}
			}
		}

		// 文本块(列优先阅读序;gopdf Blocks)
		blocks, berr := page.Blocks()
		if berr != nil {
			v.Warnings = append(v.Warnings, fmt.Sprintf("第 %d 页文本块解析失败,回退整页纯文本: %v", pn, berr))
			if txt, terr := page.GetPlainText(nil); terr == nil {
				if t := strings.TrimSpace(txt); t != "" {
					v.Blocks = appendTextBlock(v.Blocks, s, pn, t, tblByCell)
				}
			}
		} else if len(blocks) == 0 {
			if sig == string(pdf.SignalImageOnly) {
				v.Blocks = append(v.Blocks, sdk.DocBlock{Kind: sdk.DocBlockNote, Page: pn, Text: "本页无文本层(图片页,需 OCR)"})
			}
		} else {
			domSize := dominantFontSize(blocks)
			for _, b := range blocks {
				text := strings.TrimSpace(b.S)
				if text == "" {
					continue
				}
				// 与表格单元格重复的文本不再重复输出(表格已呈现)
				if tblByCell[text] {
					continue
				}
				blk := sdk.DocBlock{Kind: sdk.DocBlockParagraph, Page: pn, Text: normalizeNewlines(text)}
				if looksLikeHeading(b, domSize) {
					blk.Kind = sdk.DocBlockHeading
					blk.Level = 2 // 无版式语义时不猜级别(避免假 h1)
				}
				v.Blocks = append(v.Blocks, blk)
				if len(v.Blocks) >= s.budget.MaxBlocks {
					stopped = true
					break
				}
			}
		}

		// 图片元数据(不解码;真图由浏览器原生查看器呈现)
		if imgs, ierr := page.Images(); ierr == nil && len(imgs) > 0 {
			var sb strings.Builder
			fmt.Fprintf(&sb, "本页图片 %d 张(仅元数据,不解码):", len(imgs))
			for i, im := range imgs {
				if i >= pdfImageNoteCap {
					fmt.Fprintf(&sb, " …另有 %d 张", len(imgs)-pdfImageNoteCap)
					break
				}
				fmt.Fprintf(&sb, " [%d] %dx%d pt", i+1, int(im.W), int(im.H))
				if im.DeclaredWidth > 0 && im.DeclaredHeight > 0 {
					fmt.Fprintf(&sb, " 像素 %dx%d", im.DeclaredWidth, im.DeclaredHeight)
				}
				if im.Filter != "" {
					fmt.Fprintf(&sb, " 滤镜 %s", im.Filter)
				}
			}
			v.Blocks = append(v.Blocks, sdk.DocBlock{Kind: sdk.DocBlockNote, Page: pn, Text: sb.String()})
		}
	}
	if stopped && len(v.Blocks) >= s.budget.MaxBlocks {
		v.Truncated = append(v.Truncated, fmt.Sprintf("blocks:%d", s.budget.MaxBlocks))
		v.Warnings = append(v.Warnings, fmt.Sprintf("PDF 内容块超出预算 %d,后续页未解析(可用 --page 指定起始页)", s.budget.MaxBlocks))
	}

	// —— gopdf 抽取警告透传(上限 50 条)——
	warns := r.Warnings()
	shown := 0
	for _, w := range warns {
		if shown >= pdfWarnCap {
			v.Warnings = append(v.Warnings, fmt.Sprintf("PDF 抽取警告过多,仅显示前 %d 条(共 %d 条)", pdfWarnCap, len(warns)))
			break
		}
		shown++
		if w.Page > 0 {
			v.Warnings = append(v.Warnings, fmt.Sprintf("PDF 第 %d 页 [%s] %s %s", w.Page, w.Code, w.Message, w.Detail))
		} else {
			v.Warnings = append(v.Warnings, fmt.Sprintf("PDF [%s] %s %s", w.Code, w.Message, w.Detail))
		}
	}
	if len(v.Blocks) == 0 {
		v.Blocks = []sdk.DocBlock{{Kind: sdk.DocBlockNote, Page: 1, Text: fmt.Sprintf("%s:%d 页,无可抽取文本(可用浏览器原生查看器阅读)", v.Name, v.Pages)}}
	}
	return v, nil
}

// openPDF 打开 PDF(加密文档支持口令回调:env 取口令,缺失则报错)。
func openPDF(f *os.File, size int64) (*pdf.Reader, error) {
	r, err := pdf.NewReader(f, size)
	if err == nil {
		return r, nil
	}
	// 加密路径:尝试 env 口令(不落盘、不入日志)
	pw := os.Getenv(pdfPasswordEnv)
	if pw == "" {
		return nil, err
	}
	once := false
	return pdf.NewReaderEncrypted(f, size, func() string {
		if once {
			return ""
		}
		once = true
		return pw
	})
}

// requestedPages 请求页集合(空 = 全部;显式 Pages 优先,其次 Page 起)。
func requestedPages(req sdk.DocRequest, total int) []int {
	out := make([]int, 0, total)
	if len(req.Pages) > 0 {
		for _, p := range req.Pages {
			if p >= 1 && p <= total {
				out = append(out, p)
			}
		}
		return out
	}
	start := 1
	if req.Page > 1 {
		start = req.Page
	}
	for p := start; p <= total; p++ {
		out = append(out, p)
	}
	return out
}

// appendTextBlock 整页纯文本兜底(按空行分段)。
func appendTextBlock(blocks []sdk.DocBlock, s *Service, page int, text string, tblByCell map[string]bool) []sdk.DocBlock {
	for _, para := range strings.Split(normalizeNewlines(text), "\n\n") {
		p := strings.TrimSpace(para)
		if p == "" || tblByCell[p] {
			continue
		}
		blocks = append(blocks, sdk.DocBlock{Kind: sdk.DocBlockParagraph, Page: page, Text: p})
		if len(blocks) >= s.budget.MaxBlocks {
			break
		}
	}
	return blocks
}

// dominantFontSize 本页主导字号(按行数加权众数;0 = 无从判定)。
func dominantFontSize(blocks []pdf.Block) float64 {
	count := map[float64]int{}
	for _, b := range blocks {
		for _, l := range b.Lines {
			if l.FontSize > 0 {
				count[l.FontSize]++
			}
		}
	}
	best, bestN := 0.0, 0
	for size, n := range count {
		// 同票数取较小字号(标题通常少而大,正文多而小;宁把大字号当正文,不误判标题)
		if n > bestN || (n == bestN && size < best) {
			best, bestN = size, n
		}
	}
	return best
}

// looksLikeHeading 启发式:块字号显著大于本页主导字号,且行数少、文本短。
// (PDF 无版式语义,只做保守判定:宁少判不误判。)
func looksLikeHeading(b pdf.Block, domSize float64) bool {
	if domSize <= 0 || len(b.Lines) == 0 || len(b.Lines) > 2 {
		return false
	}
	maxSize := 0.0
	for _, l := range b.Lines {
		if l.FontSize > maxSize {
			maxSize = l.FontSize
		}
	}
	if maxSize < domSize*1.35 {
		return false
	}
	return len([]rune(strings.TrimSpace(b.S))) <= 80
}

// classifyPDFKind 页事实 → 文档类型(text|scanned|empty|mixed)。
func classifyPDFKind(ds pdf.DocumentSummary) string {
	n := len(ds.Pages)
	switch {
	case ds.TotalPages == 0 || n == 0:
		return "empty"
	case ds.TextPages == n:
		return "text"
	case ds.TextPages == 0 && ds.ImageOnlyPages > 0:
		return "scanned"
	case ds.TextPages == 0 && ds.ImageOnlyPages == 0:
		return "empty" // 全空页或全降级
	default:
		return "mixed"
	}
}
