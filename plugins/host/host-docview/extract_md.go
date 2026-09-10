// markdown 抽取器(D1):goldmark(CommonMark + GFM 表格/删除线/任务列表)AST → DocBlock 块模型。
//
// 纪律(对齐 DOC_PREVIEW_PLAN §5.2):
//   - 原始 HTML(块级/行内)**转义为纯文本**(块模型本身无 HTML 通道,天然免疫 XSS);
//   - 图片:相对路径且在工作区内 → DocAsset(经 asset 端点);http(s) 远程图**不自动加载**(防回连隐私泄漏),
//     渲染为占位 + 显式警告;data: 内联图不解析;
//   - 链接仅保留 http(s) 与相对路径,其余丢弃并记警告。
package hostdocview

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// mdParser CommonMark + GFM 子集(表格/删除线/任务列表;不开 Linkify,避免裸 URL 噪声)。
var mdParser = goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.TaskList))

// mdMaxInlineHTML 警告去重标记。
const (
	warnKeyInlineHTML = "md-html"
	warnKeyRemoteImg  = "md-remote-img"
)

// mdImage 段落内收集到的图片。
type mdImage struct {
	alt  string
	dest string
}

// mdCtx 一次抽取的遍历上下文。
type mdCtx struct {
	s      *Service
	abs    string
	src    []byte
	req    sdk.DocRequest
	blocks []sdk.DocBlock
	warns  []string
	seen   map[string]bool
	nAsset int
	bytes  int64
}

// extractMarkdown markdown → 块模型(+ 内嵌/同目录图片资产)。
func extractMarkdown(_ context.Context, s *Service, abs string, fi os.FileInfo, req sdk.DocRequest, format sdk.DocFormat) (*sdk.DocView, error) {
	max := req.MaxBytes
	if max <= 0 {
		max = s.budget.MaxPreviewBytes
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, truncated, err := readCap(f, max)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", sdk.ErrDocParse, err)
	}
	if truncated {
		data = trimPartialRune(data)
	}

	v := baseView(req, abs, fi.Size(), fi.ModTime().UnixNano(), format)
	m := mdWalk(s, abs, data, req)
	v.Blocks = m.blocks
	v.Warnings = append(v.Warnings, m.warns...)
	if truncated {
		v.Truncated = append(v.Truncated, fmt.Sprintf("bytes:%d/%d", max, fi.Size()))
		v.Warnings = append(v.Warnings, "markdown 源超出预览字节预算,已截断解析")
	}
	return v, nil
}

// mdWalk 解析 markdown 源为块序列(abs 为空 = 无文档上下文,图片不做资产解析)。
func mdWalk(s *Service, abs string, src []byte, req sdk.DocRequest) *mdCtx {
	m := &mdCtx{s: s, abs: abs, src: src, req: req, seen: map[string]bool{}}
	doc := mdParser.Parser().Parse(text.NewReader(src))
	for c := doc.FirstChild(); c != nil; c = c.NextSibling() {
		m.appendBlock(c, 0)
	}
	return m
}

// Render 把 markdown 文本直接转为块模型(会话流 md 渲染 / IM 降级;不触碰文件系统)。
func (s *Service) Render(_ context.Context, text string, maxBlocks int) (*sdk.DocView, error) {
	raw := []byte(text)
	if int64(len(raw)) > s.budget.MaxPreviewBytes {
		raw = trimPartialRune(raw[:s.budget.MaxPreviewBytes])
	}
	m := mdWalk(s, "", raw, sdk.DocRequest{})
	if maxBlocks <= 0 {
		maxBlocks = s.budget.MaxBlocks
	}
	v := &sdk.DocView{Format: sdk.DocFormatMarkdown, Blocks: m.blocks, Warnings: m.warns}
	if len(v.Blocks) > maxBlocks {
		v.Truncated = append(v.Truncated, fmt.Sprintf("blocks:%d/%d", maxBlocks, len(m.blocks)))
		v.Blocks = v.Blocks[:maxBlocks]
	}
	return v, nil
}

// appendBlock 顶层/嵌套块分派。
func (m *mdCtx) appendBlock(n ast.Node, depth int) {
	switch t := n.(type) {
	case *ast.Heading:
		blk := sdk.DocBlock{Kind: sdk.DocBlockHeading, Level: t.Level}
		m.fillRuns(n, blk, depth)
	case *ast.Paragraph, *ast.TextBlock:
		blk := sdk.DocBlock{Kind: sdk.DocBlockParagraph}
		m.fillRuns(n, blk, depth)
	case *ast.Blockquote:
		m.blocks = append(m.blocks, sdk.DocBlock{Kind: sdk.DocBlockQuote, Text: normalizeNewlines(nodeText(n, m.src))})
	case *ast.List:
		for item := t.FirstChild(); item != nil; item = item.NextSibling() {
			m.appendListItem(item, depth)
		}
	case *ast.FencedCodeBlock:
		lang := strings.TrimSpace(string(t.Language(m.src)))
		m.blocks = append(m.blocks, sdk.DocBlock{Kind: sdk.DocBlockCode, Lang: lang, Text: blockLines(t, m.src)})
	case *ast.CodeBlock:
		m.blocks = append(m.blocks, sdk.DocBlock{Kind: sdk.DocBlockCode, Text: blockLines(t, m.src)})
	case *ast.ThematicBreak:
		m.blocks = append(m.blocks, sdk.DocBlock{Kind: sdk.DocBlockDivider})
	case *ast.HTMLBlock:
		m.warnOnce(warnKeyInlineHTML, "markdown 含原始 HTML,已按纯文本处理(渲染层无 HTML 通道)")
		m.blocks = append(m.blocks, sdk.DocBlock{Kind: sdk.DocBlockCode, Lang: "html", Text: blockLines(t, m.src)})
	case *east.Table:
		m.appendTable(t)
	default:
		// 未识别的块:退化为纯文本段落(不丢内容)
		if txt := strings.TrimSpace(nodeText(n, m.src)); txt != "" {
			m.blocks = append(m.blocks, sdk.DocBlock{Kind: sdk.DocBlockParagraph, Text: normalizeNewlines(txt)})
		}
	}
}

// appendListItem 列表项(内含段落 → list 块;嵌套列表 → 深度 +1)。
func (m *mdCtx) appendListItem(item ast.Node, depth int) {
	for c := item.FirstChild(); c != nil; c = c.NextSibling() {
		switch c.(type) {
		case *ast.Paragraph, *ast.TextBlock:
			blk := sdk.DocBlock{Kind: sdk.DocBlockList, Level: depth}
			m.fillRuns(c, blk, depth)
		case *ast.List:
			m.appendBlock(c, depth+1)
		default:
			if txt := strings.TrimSpace(nodeText(c, m.src)); txt != "" {
				m.blocks = append(m.blocks, sdk.DocBlock{Kind: sdk.DocBlockList, Level: depth, Text: normalizeNewlines(txt)})
			}
		}
	}
}

// fillRuns 填充富文本行内运行,并把图片抽成独立 image 块。
func (m *mdCtx) fillRuns(n ast.Node, blk sdk.DocBlock, _ int) {
	var runs []sdk.DocRun
	var imgs []mdImage
	m.walkInline(n, runStyle{}, &runs, &imgs)
	// 去掉首尾空 run,文本为空则跳过该块
	for len(runs) > 0 && strings.TrimSpace(runs[0].Text) == "" {
		runs = runs[1:]
	}
	for len(runs) > 0 && strings.TrimSpace(runs[len(runs)-1].Text) == "" {
		runs = runs[:len(runs)-1]
	}
	if len(runs) == 0 && len(imgs) == 0 {
		return
	}
	blk.Runs = runs
	var sb strings.Builder
	for _, r := range runs {
		sb.WriteString(r.Text)
	}
	blk.Text = normalizeNewlines(strings.TrimRight(sb.String(), "\n"))
	m.blocks = append(m.blocks, blk)
	for _, img := range imgs {
		m.appendImage(img)
	}
}

// runStyle 行内样式累积。
type runStyle struct{ bold, italic, strike, code bool }

// walkInline 递归收集行内节点为 DocRun。
func (m *mdCtx) walkInline(n ast.Node, st runStyle, out *[]sdk.DocRun, imgs *[]mdImage) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch t := c.(type) {
		case *ast.Text:
			s := string(t.Value(m.src))
			if t.SoftLineBreak() || t.HardLineBreak() {
				s += "\n"
			}
			m.pushRun(out, sdk.DocRun{Text: s, Bold: st.bold, Italic: st.italic, Strike: st.strike, Code: st.code})
		case *ast.String:
			m.pushRun(out, sdk.DocRun{Text: string(t.Value), Bold: st.bold, Italic: st.italic, Strike: st.strike, Code: st.code})
		case *ast.CodeSpan:
			m.pushRun(out, sdk.DocRun{Text: nodeText(c, m.src), Code: true})
		case *ast.Emphasis:
			ns := st
			if t.Level >= 2 {
				ns.bold = true
			} else {
				ns.italic = true
			}
			m.walkInline(c, ns, out, imgs)
		case *east.TaskCheckBox:
			mark := "[ ] "
			if t.IsChecked {
				mark = "[x] "
			}
			m.pushRun(out, sdk.DocRun{Text: mark})
		case *ast.Link:
			dest := safeLink(string(t.Destination))
			if dest == "" {
				m.warnOnce("md-badlink:"+string(t.Destination), "已丢弃不安全链接目标:"+string(t.Destination))
			}
			m.pushRun(out, sdk.DocRun{
				Text: nodeText(c, m.src), Link: dest,
				Bold: st.bold, Italic: st.italic, Strike: st.strike,
			})
		case *ast.Image:
			alt := nodeText(c, m.src)
			if alt == "" {
				alt = "图片"
			}
			*imgs = append(*imgs, mdImage{alt: alt, dest: string(t.Destination)})
			m.pushRun(out, sdk.DocRun{Text: alt, Italic: st.italic})
		case *ast.AutoLink:
			m.pushRun(out, sdk.DocRun{Text: nodeText(c, m.src), Link: string(t.URL(m.src))})
		case *ast.RawHTML:
			m.warnOnce(warnKeyInlineHTML, "markdown 含原始 HTML,已按纯文本处理(渲染层无 HTML 通道)")
			m.pushRun(out, sdk.DocRun{Text: rawHTMLText(t, m.src), Code: true})
		default:
			m.walkInline(c, st, out, imgs)
		}
	}
}

func (m *mdCtx) pushRun(out *[]sdk.DocRun, r sdk.DocRun) {
	if r.Text == "" {
		return
	}
	*out = append(*out, r)
}

// appendImage 图片 → image 块(本地图登记资产;远程图占位 + 警告)。
func (m *mdCtx) appendImage(img mdImage) {
	dest := strings.TrimSpace(img.dest)
	lower := strings.ToLower(dest)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		m.warnOnce(warnKeyRemoteImg, "远程图片不自动加载(防回连隐私泄漏),已渲染为占位")
		m.blocks = append(m.blocks, sdk.DocBlock{Kind: sdk.DocBlockImage, Text: img.alt, Meta: map[string]string{"remote": dest}})
		return
	}
	if strings.HasPrefix(lower, "data:") {
		m.warnOnce("md-dataimg", "内联 data: 图片已忽略")
		m.blocks = append(m.blocks, sdk.DocBlock{Kind: sdk.DocBlockImage, Text: img.alt})
		return
	}
	if m.abs == "" {
		// 无文档上下文(会话流文本渲染):只给占位,不做文件解析
		m.blocks = append(m.blocks, sdk.DocBlock{Kind: sdk.DocBlockImage, Text: img.alt, Meta: map[string]string{"unresolved": img.dest}})
		return
	}
	// 去 query/fragment,按文档目录解析相对路径(仅工作区内且存在的图)
	rel := dest
	if i := strings.IndexAny(rel, "?#"); i >= 0 {
		rel = rel[:i]
	}
	if rel == "" {
		m.blocks = append(m.blocks, sdk.DocBlock{Kind: sdk.DocBlockImage, Text: img.alt})
		return
	}
	target := rel
	if !filepath.IsAbs(rel) {
		target = filepath.Join(filepath.Dir(m.abs), filepath.FromSlash(rel))
	}
	resolved, err := m.s.res.Resolve(target, m.req.Strict)
	if err != nil {
		m.warnOnce("md-img-miss:"+rel, "图片不在可见范围或不存在,未加载:"+rel)
		m.blocks = append(m.blocks, sdk.DocBlock{Kind: sdk.DocBlockImage, Text: img.alt, Meta: map[string]string{"unresolved": rel}})
		return
	}
	fi, err := os.Stat(resolved)
	if err != nil || fi.IsDir() {
		m.blocks = append(m.blocks, sdk.DocBlock{Kind: sdk.DocBlockImage, Text: img.alt, Meta: map[string]string{"unresolved": rel}})
		return
	}
	if m.nAsset >= m.s.budget.MaxAssets || m.bytes+fi.Size() > m.s.budget.MaxAssetsBytes {
		m.warnOnce("md-img-budget", fmt.Sprintf("图片数量/体积超出预算(上限 %d 张 / %d 字节),其余图片只给占位", m.s.budget.MaxAssets, m.s.budget.MaxAssetsBytes))
		m.blocks = append(m.blocks, sdk.DocBlock{Kind: sdk.DocBlockImage, Text: img.alt, Meta: map[string]string{"unresolved": rel}})
		return
	}
	m.nAsset++
	m.bytes += fi.Size()
	mime := mimeOf(resolved, nil)
	asset := &sdk.DocAsset{ID: m.s.RegisterFileAsset(m.abs, resolved, mime), Mime: mime, Name: path.Base(filepath.ToSlash(rel)), Bytes: fi.Size()}
	if w, h := imageDims(resolved); w > 0 {
		asset.W, asset.H = w, h
	}
	m.blocks = append(m.blocks, sdk.DocBlock{Kind: sdk.DocBlockImage, Text: img.alt, Asset: asset})
}

// appendTable GFM 表格 → table 块(表头 + 数据行 + 对齐)。
func (m *mdCtx) appendTable(t *east.Table) {
	blk := sdk.DocBlock{Kind: sdk.DocBlockTable}
	aligns := t.Alignments
	for r := t.FirstChild(); r != nil; r = r.NextSibling() {
		row, ok := r.(*east.TableRow)
		if !ok {
			if hdr, ok2 := r.(*east.TableHeader); ok2 {
				for _, cell := range cellsOf(hdr) {
					blk.Head = append(blk.Head, strings.TrimSpace(nodeText(cell, m.src)))
				}
			}
			continue
		}
		cells := cellsOf(row)
		out := make([]sdk.DocCell, 0, len(cells))
		for ci, cell := range cells {
			c := sdk.DocCell{Text: strings.TrimSpace(nodeText(cell, m.src))}
			if ci < len(aligns) {
				c.Align = alignName(aligns[ci])
			}
			out = append(out, c)
		}
		blk.Rows = append(blk.Rows, out)
	}
	if len(blk.Head) == 0 && len(blk.Rows) == 0 {
		return
	}
	// 行/列预算
	if len(blk.Rows) > m.s.budget.MaxTableRows {
		blk.Rows = blk.Rows[:m.s.budget.MaxTableRows]
		m.warnOnce("md-table-rows", fmt.Sprintf("表格行超出预算(>%d 行),已截断", m.s.budget.MaxTableRows))
	}
	m.blocks = append(m.blocks, blk)
}

func cellsOf(n ast.Node) []*east.TableCell {
	var out []*east.TableCell
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if cell, ok := c.(*east.TableCell); ok {
			out = append(out, cell)
		}
	}
	return out
}

func alignName(a east.Alignment) string {
	switch a {
	case east.AlignCenter:
		return "center"
	case east.AlignRight:
		return "right"
	case east.AlignLeft:
		return "left"
	}
	return ""
}

// blockLines 块的行文本(围栏/缩进代码/HTML 块共用)。
func blockLines(n ast.Node, src []byte) string {
	var sb strings.Builder
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		sb.Write(seg.Value(src))
	}
	return normalizeNewlines(strings.TrimRight(sb.String(), "\n"))
}

// rawHTMLText 行内原始 HTML 的源码文本。
func rawHTMLText(n *ast.RawHTML, src []byte) string {
	var sb strings.Builder
	if n.Segments != nil {
		for i := 0; i < n.Segments.Len(); i++ {
			seg := n.Segments.At(i)
			sb.Write(seg.Value(src))
		}
	}
	return sb.String()
}

// nodeText 递归收集节点纯文本(引用/单元格/链接标签/代码段等)。
func nodeText(n ast.Node, src []byte) string {
	var sb strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := c.(type) {
		case *ast.Text:
			sb.Write(t.Value(src))
			if t.SoftLineBreak() {
				sb.WriteString("\n")
			}
		case *ast.String:
			sb.Write(t.Value)
		case *ast.AutoLink:
			sb.Write(t.URL(src))
		}
		return ast.WalkContinue, nil
	})
	return sb.String()
}

// safeLink 只放行 http(s)/mailto 与相对路径(过滤 javascript:/data: 等注入面)。
func safeLink(dest string) string {
	d := strings.TrimSpace(dest)
	if d == "" {
		return ""
	}
	lower := strings.ToLower(d)
	switch {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"), strings.HasPrefix(lower, "mailto:"):
		return d
	case strings.HasPrefix(lower, "//"), strings.Contains(d, ":"):
		return "" // 协议相对/未知 scheme(javascript:、data:、file:)一律丢弃
	}
	return d
}

func (m *mdCtx) warnOnce(key, msg string) {
	if m.seen[key] {
		return
	}
	m.seen[key] = true
	m.warns = append(m.warns, msg)
}
