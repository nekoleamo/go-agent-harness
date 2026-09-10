// PDF 抽取器(D0:依赖引入 + 页事实;文本/Blocks 与浏览器原生查看器归切片 D4)。
// 依赖决策 K1(已锁):github.com/Detective-XH/gopdf(BSD-3-Clause,纯 Go,无 CGO/
// 渲染/OCR/网络;go.mod 仅需 golang.org/x/text —— gah 已依赖,传递依赖净增 0)。
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

// extractPDF PDF:页数 + 页事实分类(text/scanned/image/mixed)+ 标题作者 + Warnings。
func extractPDF(ctx context.Context, s *Service, abs string, fi os.FileInfo, req sdk.DocRequest, format sdk.DocFormat) (*sdk.DocView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r, err := pdf.NewReader(f, fi.Size())
	if err != nil {
		// 加密 PDF:NewReader 无口令失败 → 结构化「需要口令」而非泛化解析错误
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "encrypt") || strings.Contains(msg, "password") || strings.Contains(msg, "decrypt") {
			return nil, fmt.Errorf("%w: PDF 已加密,需口令(D4 提供口令入口): %v", sdk.ErrDocUnsupported, err)
		}
		return nil, fmt.Errorf("%w: 打开 PDF 失败: %v", sdk.ErrDocParse, err)
	}
	_ = s

	v := baseView(req, abs, fi.Size(), fi.ModTime().UnixNano(), format)
	info := r.Info()
	v.Title = strings.TrimSpace(info.Title())
	v.Author = strings.TrimSpace(info.Author())
	v.Pages = r.NumPage()

	ocrPages := make([]string, 0, 8)
	if v.Pages <= pdfFactsMaxPages {
		ds := r.DocumentSummary()
		v.Kind = classifyPDFKind(ds)
		for _, p := range ds.Pages {
			if p.Signal == pdf.SignalImageOnly {
				ocrPages = append(ocrPages, fmt.Sprintf("%d", p.Page))
			}
		}
		if v.Kind == "scanned" {
			v.Warnings = append(v.Warnings, "PDF 无文本层(疑似扫描件),需 OCR 才能检索;当前只做识别与提示,不内置 OCR")
		}
		if v.Kind == "mixed" {
			v.Warnings = append(v.Warnings, "PDF 部分页无文本层(图文混排/扫描页),这些页无文本可抽取")
		}
	} else {
		v.Warnings = append(v.Warnings, fmt.Sprintf("PDF 页数 %d 超过页事实扫描上限 %d,已跳过逐页分类", v.Pages, pdfFactsMaxPages))
	}
	if len(ocrPages) > 0 {
		v.Meta = map[string]string{"pages_needing_ocr": strings.Join(ocrPages, ",")}
	}

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

	kind := v.Kind
	if kind == "" {
		kind = "未知"
	}
	v.Blocks = []sdk.DocBlock{{
		Kind: sdk.DocBlockNote,
		Page: 1,
		Text: fmt.Sprintf("%s:%d 页,内容类型 %s(文本抽取与浏览器原生查看器在切片 D4 交付)", v.Name, v.Pages, kind),
	}}
	return v, nil
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
