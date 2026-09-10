// Package tooldoc 提供 tool-doc 插件(D 组 D5):给模型的文档阅读三工具。
//
//	read_document(file_path, offset?, limit?, pages?, sheet?) → 行号化 Markdown(预算分页)
//	doc_open(file_path, page?, sheet?)                      → 发出 doc/open 意图(三端弹预览)
//	doc_list(path?, depth?)                                 → 目录树(标注可预览格式)
//
// 契约对齐 docs/DOC_PREVIEW_PLAN.md §6.3(预算默认集与 dsh-document 同构,便于用户迁移直觉);
// 一切读取经 host-docview 的统一 resolver(沙箱 + 逃逸校验 + 密钥 deny-list),不绕沙箱。
package tooldoc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 tool-doc。requires ctx.tools + ctx.doc(缺失即显式失败:不静默降级)。
type Plugin struct{}

func (p *Plugin) Name() string { return "tool-doc" }

// budgets 工具预算(可由 manifest data 覆盖)。
type budgets struct {
	MaxInputBytes  int64
	ReadLimit      int
	MaxLineLength  int
	MaxOutputBytes int
	PDFMaxPages    int
}

// defaultBudgets 默认预算(对齐 dsh-document)。
func defaultBudgets() budgets {
	return budgets{
		MaxInputBytes:  50 << 20,
		ReadLimit:      2000,
		MaxLineLength:  2000,
		MaxOutputBytes: 50 << 10,
		PDFMaxPages:    100,
	}
}

// Start 注册三工具。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	var doc sdk.DocService
	if err := c.Inject("ctx.doc", &doc); err != nil {
		return nil, fmt.Errorf("tool-doc 需要 host-docview(ctx.doc): %w", err)
	}
	b := defaultBudgets()
	if m != nil && m.Data != nil {
		if v, ok := numOf(m.Data["read_limit"]); ok && v > 0 {
			b.ReadLimit = int(v)
		}
		if v, ok := numOf(m.Data["max_output_kb"]); ok && v > 0 {
			b.MaxOutputBytes = int(v) << 10
		}
		if v, ok := numOf(m.Data["pdf_max_pages"]); ok && v > 0 {
			b.PDFMaxPages = int(v)
		}
	}
	t := &docTools{doc: doc, b: b, c: c}
	return t.register(tools), nil
}

func numOf(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	}
	return 0, false
}

// docTools 三工具实现(单结构多定义)。
type docTools struct {
	doc sdk.DocService
	b   budgets
	c   sdk.Ctx
}

func (t *docTools) register(r sdk.ToolRegistry) sdk.Disposer {
	d1 := r.Register(&tool{name: "read_document", t: t})
	d2 := r.Register(&tool{name: "doc_open", t: t})
	d3 := r.Register(&tool{name: "doc_list", t: t})
	return func() { d1(); d2(); d3() }
}

// tool 单个具名工具。
type tool struct {
	name string
	t    *docTools
}

// Definition 工具定义。
func (x *tool) Definition() sdk.ToolDefinition {
	switch x.name {
	case "read_document":
		return sdk.ToolDefinition{
			Name: "read_document",
			Description: "读取文档并按行号返回文本(markdown/文本/代码/CSV/notebook/docx/xlsx/pptx/PDF)。" +
				"PDF 走文本层抽取(扫描件只有页事实);用 offset/limit 分页、pages/sheet 指定页或工作表。" +
				"返回 JSON:{path, format, offset, lines:[{number,text}], totalLines, truncatedByBytes, pdf?, warnings[]}。",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []any{"file_path"},
				"properties": map[string]any{
					"file_path": map[string]any{"type": "string", "description": "文件路径(相对工作区或绝对)"},
					"offset":    map[string]any{"type": "integer", "description": "起始行(0-based,默认 0)"},
					"limit":     map[string]any{"type": "integer", "description": "行数上限(默认 2000)"},
					"pages": map[string]any{
						"type":        "array",
						"description": "PDF 指定页集(1-based;省略=全部)",
						"items":       map[string]any{"type": "integer"},
					},
					"sheet": map[string]any{"type": "integer", "description": "xlsx 工作表序号(0-based)"},
				},
			},
		}
	case "doc_open":
		return sdk.ToolDefinition{
			Name: "doc_open",
			Description: "在用户界面打开文档预览(TUI pager / Web 文档面板并定位该文件)。" +
				"用于需要用户亲眼看文档时;不要在只需要文本时用它(那种情况用 read_document)。",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []any{"file_path"},
				"properties": map[string]any{
					"file_path": map[string]any{"type": "string"},
					"page":      map[string]any{"type": "integer", "description": "PDF/PPT 起始页(1-based)"},
					"sheet":     map[string]any{"type": "integer", "description": "xlsx 工作表序号(0-based)"},
				},
			},
		}
	default:
		return sdk.ToolDefinition{
			Name:        "doc_list",
			Description: "列出目录下可预览的文档(名称/格式/大小),用于发现工作区里有哪些文档。",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":  map[string]any{"type": "string", "description": "目录(默认当前工作区根)"},
					"depth": map[string]any{"type": "integer", "description": "递归深度 1–4(默认 2)"},
				},
			},
		}
	}
}

// Execute 分发执行。
func (x *tool) Execute(ctx context.Context, raw string) (any, error) {
	switch x.name {
	case "read_document":
		return x.t.readDocument(ctx, raw)
	case "doc_open":
		return x.t.docOpen(ctx, raw)
	default:
		return x.t.docList(ctx, raw)
	}
}

// readResult read_document 的结构化返回(对齐 §6.3 契约)。
type readResult struct {
	Path             string        `json:"path"`
	Format           string        `json:"format"`
	Offset           int           `json:"offset"`
	Lines            []sdk.DocLine `json:"lines"`
	TotalLines       int           `json:"totalLines"`
	TruncatedByBytes bool          `json:"truncatedByBytes,omitempty"`
	PDF              *pdfFactsOut  `json:"pdf,omitempty"`
	Warnings         []string      `json:"warnings,omitempty"`
	Note             string        `json:"note,omitempty"`
}

type pdfFactsOut struct {
	PageCount       int    `json:"pageCount"`
	Kind            string `json:"kind"`
	PagesNeedingOCR []int  `json:"pagesNeedingOcr,omitempty"`
	PagesRequested  int    `json:"pagesRequested,omitempty"`
}

// readDocument 读文档(行号化 + 预算分页)。
func (t *docTools) readDocument(ctx context.Context, raw string) (any, error) {
	var a struct {
		FilePath string `json:"file_path"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
		Pages    []int  `json:"pages"`
		Sheet    int    `json:"sheet"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("read_document 参数解析失败: %w", err)
	}
	if strings.TrimSpace(a.FilePath) == "" {
		return nil, fmt.Errorf("read_document: 缺少 file_path")
	}
	limit := a.Limit
	if limit <= 0 || limit > t.b.ReadLimit {
		limit = t.b.ReadLimit
	}
	req := sdk.DocRequest{
		Path:          a.FilePath,
		Offset:        a.Offset,
		Limit:         limit,
		Pages:         a.Pages,
		Sheet:         a.Sheet,
		MaxInputBytes: t.b.MaxInputBytes,
	}
	tx, err := t.doc.Text(ctx, req)
	if err != nil {
		return nil, err
	}
	out := readResult{Path: tx.Path, Format: string(tx.Format), Offset: tx.Offset, TotalLines: tx.TotalLines}
	out.Warnings = append(out.Warnings, tx.Warnings...)
	out.TruncatedByBytes = tx.TruncatedByBytes
	// 输出字节预算(50KiB 默认):逐行累计,超限即停并提示
	used := 0
	for _, l := range tx.Lines {
		if used+len(l.Text) > t.b.MaxOutputBytes {
			out.Note = fmt.Sprintf("输出已达字节预算 %d,后续行未返回(用 offset 继续读)", t.b.MaxOutputBytes)
			break
		}
		used += len(l.Text) + 1
		out.Lines = append(out.Lines, l)
	}
	if len(out.Lines) < len(tx.Lines) {
		out.TruncatedByBytes = true
	}
	if tx.PDF != nil {
		f := &pdfFactsOut{PageCount: tx.PDF.PageCount, Kind: tx.PDF.Kind, PagesNeedingOCR: tx.PDF.PagesNeedingOCR}
		if f.PageCount > t.b.PDFMaxPages && len(a.Pages) == 0 {
			f.PagesRequested = t.b.PDFMaxPages
			out.Note = joinNote(out.Note, fmt.Sprintf("PDF 共 %d 页,建议用 pages 参数指定页集(默认关注前 %d 页)", f.PageCount, t.b.PDFMaxPages))
		}
		out.PDF = f
	}
	if len(out.Lines) == 0 {
		out.Note = joinNote(out.Note, "该范围内无文本(可能是扫描件/空文档,或 offset 超出行数)")
	}
	return out, nil
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + ";" + b
}

// docOpen 发出 doc/open 意图(三端各自弹预览)。
func (t *docTools) docOpen(ctx context.Context, raw string) (any, error) {
	var a struct {
		FilePath string `json:"file_path"`
		Page     int    `json:"page"`
		Sheet    int    `json:"sheet"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("doc_open 参数解析失败: %w", err)
	}
	if strings.TrimSpace(a.FilePath) == "" {
		return nil, fmt.Errorf("doc_open: 缺少 file_path")
	}
	// 先校验格式与可达性(失败意图不广播)
	format, err := t.doc.Detect(ctx, sdk.DocRequest{Path: a.FilePath})
	if err != nil {
		return nil, err
	}
	if format == sdk.DocFormatUnsupported {
		return nil, fmt.Errorf("%w: %s", sdk.ErrDocUnsupported, a.FilePath)
	}
	if _, err := t.c.Emit(ctx, sdk.EventDocOpen, sdk.DocOpenEvent{Path: a.FilePath, Page: a.Page, Sheet: a.Sheet}, sdk.Emit); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":      true,
		"path":    a.FilePath,
		"format":  string(format),
		"message": "已在用户界面打开预览(TUI pager / Web 文档面板);请据此与用户对话,不要重复朗读全文",
	}, nil
}

// docList 目录列举(标注格式与可预览性)。
func (t *docTools) docList(ctx context.Context, raw string) (any, error) {
	var a struct {
		Path  string `json:"path"`
		Depth int    `json:"depth"`
	}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &a)
	}
	tree, err := t.doc.List(ctx, sdk.DocRequest{Path: a.Path}, a.Depth)
	if err != nil {
		return nil, err
	}
	type entry struct {
		Path        string `json:"path"`
		Dir         bool   `json:"dir,omitempty"`
		Size        int64  `json:"size,omitempty"`
		Format      string `json:"format,omitempty"`
		Previewable bool   `json:"previewable,omitempty"`
	}
	out := struct {
		Path      string   `json:"path"`
		Entries   []entry  `json:"entries"`
		Truncated []string `json:"truncated,omitempty"`
		Warnings  []string `json:"warnings,omitempty"`
	}{Path: tree.Path, Truncated: tree.Truncated, Warnings: tree.Warnings}
	for _, e := range tree.Entries {
		out.Entries = append(out.Entries, entry{Path: e.Path, Dir: e.Dir, Size: e.Size, Format: string(e.Format), Previewable: e.Previewable})
	}
	return out, nil
}
