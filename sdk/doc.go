// 文档预览契约(D 组文档预览线 D0):一个文档模型 + 三个呈现器(TUI/Web/headless)。
//
// 设计纪律(见 docs/DOC_PREVIEW_PLAN.md §4.2):
//   - 本文件只声明接口与数据结构,零实现、零第三方依赖;
//   - DocView/DocText 必须可 JSON 序列化(web 端点直接回传,前端零转换),新增字段一律 omitempty;
//   - 富文本经结构化块模型表达,呈现端零 HTML 通道(禁 v-html 红线的结构化解法)。
package sdk

import (
	"context"
	"errors"
	"io"
	"time"
)

// DocFormat 文档格式分类(只按扩展名判定 + 少量魔数特例,不做内容嗅探)。
type DocFormat string

const (
	DocFormatMarkdown    DocFormat = "markdown"
	DocFormatText        DocFormat = "text"
	DocFormatCSV         DocFormat = "csv"
	DocFormatCode        DocFormat = "code"
	DocFormatNotebook    DocFormat = "notebook"
	DocFormatDOCX        DocFormat = "docx"
	DocFormatXLSX        DocFormat = "xlsx"
	DocFormatPPTX        DocFormat = "pptx"
	DocFormatPDF         DocFormat = "pdf"
	DocFormatHTML        DocFormat = "html"
	DocFormatImage       DocFormat = "image"
	DocFormatBinary      DocFormat = "binary"
	DocFormatUnsupported DocFormat = "unsupported"
)

// DocBlockKind 块类型(呈现端按 kind 分派组件,零 HTML 注入面)。
type DocBlockKind string

const (
	DocBlockHeading     DocBlockKind = "heading"
	DocBlockParagraph   DocBlockKind = "paragraph"
	DocBlockList        DocBlockKind = "list"
	DocBlockQuote       DocBlockKind = "quote"
	DocBlockCode        DocBlockKind = "code"
	DocBlockTable       DocBlockKind = "table"
	DocBlockImage       DocBlockKind = "image"
	DocBlockDivider     DocBlockKind = "divider"
	DocBlockPage        DocBlockKind = "page"
	DocBlockSheet       DocBlockKind = "sheet"
	DocBlockSlide       DocBlockKind = "slide"
	DocBlockNote        DocBlockKind = "note"
	DocBlockUnsupported DocBlockKind = "unsupported"
)

// 文档服务哨兵错误(CLI 退出码映射:unsupported→3 / too large→4 / parse→5)。
var (
	ErrDocUnsupported = errors.New("docview: 不支持的格式")
	ErrDocTooLarge    = errors.New("docview: 超出字节预算")
	ErrDocParse       = errors.New("docview: 解析失败")
	ErrDocDenied      = errors.New("docview: 路径不被允许")
	ErrDocNotFound    = errors.New("docview: 文件不存在")
)

// DocRun 富文本运行(行内)。Text 为纯文本,便于纯文本端直接使用。
type DocRun struct {
	Text   string `json:"text"`
	Bold   bool   `json:"bold,omitempty"`
	Italic bool   `json:"italic,omitempty"`
	Strike bool   `json:"strike,omitempty"`
	Code   bool   `json:"code,omitempty"`
	Link   string `json:"link,omitempty"` // 仅 http(s) 与相对路径
}

// DocCell 表格单元格(合并与对齐由抽取器填,呈现端只读)。
type DocCell struct {
	Text    string `json:"text"`
	ColSpan int    `json:"colSpan,omitempty"`
	RowSpan int    `json:"rowSpan,omitempty"`
	Align   string `json:"align,omitempty"` // left|center|right
	Numeric bool   `json:"numeric,omitempty"`
}

// DocAsset 内嵌资产(docx/pptx 的 media/* 等);经 DocService.Asset 取回。
// ID 为不透明标识(part 路径哈希),不暴露宿主路径。
type DocAsset struct {
	ID    string `json:"id"`
	Mime  string `json:"mime,omitempty"`
	Name  string `json:"name,omitempty"`
	W     int    `json:"w,omitempty"`
	H     int    `json:"h,omitempty"`
	Bytes int64  `json:"bytes,omitempty"`
}

// DocBlock 结构化块模型(单一真相,tui/web/desktop 三端同源渲染)。
type DocBlock struct {
	Kind  DocBlockKind      `json:"kind"`
	Level int               `json:"level,omitempty"` // heading 级别 / list 缩进 / slide 文本框层级
	Text  string            `json:"text,omitempty"`  // paragraph/quote/code/note 纯文本(归一化换行)
	Runs  []DocRun          `json:"runs,omitempty"`  // paragraph/heading/list 富文本(Text 为 Runs 拼合)
	Head  []string          `json:"head,omitempty"`  // table 表头
	Rows  [][]DocCell       `json:"rows,omitempty"`  // table 数据行(每行若干单元格)
	Lang  string            `json:"lang,omitempty"`  // code 语言提示
	Asset *DocAsset         `json:"asset,omitempty"` // image / slide 图片
	Page  int               `json:"page,omitempty"`  // pdf 页号(1-based)/ pptx 幻灯片号 / xlsx 工作表号
	Meta  map[string]string `json:"meta,omitempty"`
}

// DocSheet xlsx 工作表元信息。
type DocSheet struct {
	Name   string `json:"name"`
	Rows   int    `json:"rows"`
	Cols   int    `json:"cols"`
	Hidden bool   `json:"hidden,omitempty"`
}

// DocView 归一化文档视图(web 端点直接回传)。
type DocView struct {
	Path      string            `json:"path"`
	Name      string            `json:"name"`
	Format    DocFormat         `json:"format"`
	Size      int64             `json:"size"`
	ModTime   time.Time         `json:"modTime"`
	Title     string            `json:"title,omitempty"`
	Author    string            `json:"author,omitempty"`
	Blocks    []DocBlock        `json:"blocks,omitempty"`
	Sheets    []DocSheet        `json:"sheets,omitempty"`
	Pages     int               `json:"pages,omitempty"` // pdf 页数 / pptx 张数
	Kind      string            `json:"kind,omitempty"`  // pdf: text|scanned|image|mixed
	RawURL    string            `json:"rawUrl,omitempty"`
	Truncated []string          `json:"truncated,omitempty"` // 被截断的维度,如 "rows:200/1048576"
	Warnings  []string          `json:"warnings,omitempty"`  // 非致命解析问题(绝不静默丢弃)
	Meta      map[string]string `json:"meta,omitempty"`      // 端无关的附加事实(如 pdf pages_needing_ocr)
}

// DocRaster 光栅化结果(D6-1a;外部 pdftoppm 优先,RST-1)。
// Data 为 PNG 字节(单页;上限由实现方预算约束),供 Web 端点 / CLI 使用。
type DocRaster struct {
	Path  string `json:"path"`
	Page  int    `json:"page"` // 1-based
	DPI   int    `json:"dpi"`
	W     int    `json:"width,omitempty"`
	H     int    `json:"height,omitempty"`
	Bytes int64  `json:"bytes"`
	Mime  string `json:"mime,omitempty"` // image/png
	Data  []byte `json:"-"`
	// CachePath 宿主侧光栅产物路径($GAH_HOME/cache/doc/raster/…;不进 JSON)。
	// 宿主侧产物,不进模型输入/前端 JSON。
	CachePath string `json:"-"`
}

// DocRasterService 可选能力:把 PDF 页光栅化为图片(实现方 = host-docview;
// 未实现/未启用 → 调用方显式报「该环境不支持光栅预览」)。
// 取舍(见 DESIGN §14.1 实施方案 RST-1/SELF-1):默认走**外部 pdftoppm**(零二进制增量);
// 需要零外部依赖部署时才评估 pdfium-WASM 自包含档。
type DocRasterService interface {
	// Raster 光栅化指定页(page 1-based;dpi 由实现方裁剪到安全区间)。
	Raster(ctx context.Context, req DocRequest, page, dpi int) (*DocRaster, error)
}

// DocRequest 预览/抽取请求(预算与分页)。
type DocRequest struct {
	Path          string `json:"path"`
	MaxBytes      int64  `json:"maxBytes,omitempty"`      // 预览字节预算(0 = 默认 1MiB)
	MaxInputBytes int64  `json:"maxInputBytes,omitempty"` // 源文件大小上限覆盖(0 = 服务默认 50MiB)
	MaxBlocks     int    `json:"maxBlocks,omitempty"`     // 块数上限(0 = 默认 4000)
	Offset        int    `json:"offset,omitempty"`        // Text:起始行(0-based)
	Limit         int    `json:"limit,omitempty"`         // Text:行数上限(0 = 默认 2000)
	Page          int    `json:"page,omitempty"`          // pdf 起始页(0 = 全部)
	Pages         []int  `json:"pages,omitempty"`         // 显式页集(空 = 全部)
	Sheet         int    `json:"sheet,omitempty"`         // xlsx 工作表(0 = 首表)
	NoAssets      bool   `json:"noAssets,omitempty"`      // 只取结构,不抽内嵌资产
	Strict        bool   `json:"strict,omitempty"`        // 严格路径策略(Web 端;根集合收窄 + deny-list)
}

// DocLine 行号化文本行(模型工具与 CLI 共用)。
type DocLine struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

// DocPDFFacts PDF 页事实(供模型判断是否需要 OCR)。
type DocPDFFacts struct {
	PageCount       int    `json:"pageCount"`
	Kind            string `json:"kind"` // text|scanned|image|mixed
	PagesNeedingOCR []int  `json:"pagesNeedingOcr,omitempty"`
}

// DocText 行号化 Markdown 文本(模型/CLI 共用;预算分页)。
type DocText struct {
	Path             string       `json:"path"`
	Format           DocFormat    `json:"format"`
	Offset           int          `json:"offset"`
	Lines            []DocLine    `json:"lines"`
	TotalLines       int          `json:"totalLines"`
	TruncatedByBytes bool         `json:"truncatedByBytes,omitempty"`
	PDF              *DocPDFFacts `json:"pdf,omitempty"`
	Warnings         []string     `json:"warnings,omitempty"`
	// Meta 端无关附加事实(D6-2:preview_via=external-converter 表示内容来自外部转换器
	// 产物而非源格式本身,CLI 据此判定退出码)。
	Meta map[string]string `json:"meta,omitempty"`
}

// DocEntry 文件树条目(工作台左侧树;目录条目也可预览其子项)。
// Path 为**调用方视角的逻辑路径**(相对路径优先),不含宿主绝对路径。
type DocEntry struct {
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Dir         bool      `json:"dir"`
	Size        int64     `json:"size,omitempty"`
	ModTime     time.Time `json:"modTime,omitempty"`
	Format      DocFormat `json:"format,omitempty"`
	Previewable bool      `json:"previewable,omitempty"`
}

// DocTree 有界目录列举结果(depth 与条目数封顶,超限写 Truncated)。
type DocTree struct {
	Path      string     `json:"path"`
	Name      string     `json:"name"`
	Entries   []DocEntry `json:"entries"`
	Truncated []string   `json:"truncated,omitempty"`
	Warnings  []string   `json:"warnings,omitempty"`
}

// DocService 文档预览服务(ctx.doc,由 host-docview 提供)。
// 所有路径经统一 resolver(沙箱 + 逃逸校验 + deny-list);预算超限写 Truncated/Warnings,绝不静默。
// 每个方法都在 DocRequest 中携带路径与 Strict(Web 端更严的路径策略)。
type DocService interface {
	// Detect 判定格式(仅扩展名 + %PDF- 魔数特例;未知扩展名时才读头部按 UTF-8 合法性兜底)。
	Detect(ctx context.Context, req DocRequest) (DocFormat, error)
	// Preview 富预览(结构化块模型 + 资产元数据)。
	Preview(ctx context.Context, req DocRequest) (*DocView, error)
	// Text 行号化 Markdown(模型工具/CLI 用;Offset/Limit 分页)。
	Text(ctx context.Context, req DocRequest) (*DocText, error)
	// Asset 取回内嵌资产(docx/pptx 的 media);mime 由实现给出。
	Asset(ctx context.Context, req DocRequest, assetID string) (io.ReadCloser, string, error)
	// Raw 原生字节(Range 支持;web 端点与下载复用)。
	Raw(ctx context.Context, req DocRequest) (io.ReadSeekCloser, string, error)
	// List 有界目录列举(工作台文件树;depth 1–4,条目数封顶)。
	List(ctx context.Context, req DocRequest, depth int) (*DocTree, error)
	// Render 把 markdown 文本直接转为块模型(会话流 md 渲染;不触碰文件系统)。
	Render(ctx context.Context, text string, maxBlocks int) (*DocView, error)
}
