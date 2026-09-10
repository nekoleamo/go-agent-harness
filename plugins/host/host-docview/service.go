// Package host-docview(D 组文档预览线,host 插件):把任意格式归一化为 sdk.DocView,
// 供 TUI / Web(+桌面壳)/ headless(CLI + 模型工具)/ IM 四端渲染同一个模型。
//
// 分层(见 docs/DOC_PREVIEW_PLAN.md §4.1):
//
//	resolver(沙箱/逃逸/deny-list)+ 预算 + 缓存 + 格式探测 + 各格式抽取器
//
// 纪律:插件只 import sdk 与标准库;默认纯内存(零写盘);缺失抽取器显式提示,不静默降级。
package hostdocview

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-docview(提供 ctx.doc;ctx.sandbox 可选注入)。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-docview" }

// Start 提供 ctx.doc 服务(预算可由 manifest data 覆盖)+ 注册 /preview 命令。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var sb sdk.Sandbox
	_ = c.Inject("ctx.sandbox", &sb) // 可选:未装配则不限制读(TUI/CLI 单机场景)
	svc := New(Options{Sandbox: sb, Home: os.Getenv("GAH_HOME"), Logger: c.Logger()})
	if m != nil {
		svc.applyData(m.Data)
	}
	if err := c.Provide("ctx.doc", svc); err != nil {
		return nil, err
	}
	// /preview <路径>:发出 doc/open 意图事件,各端 UI(TUI/Web/IM)订阅后本地打开预览
	// (命令不解锁任何读写能力,仅表达意图;真实访问仍经各端自己的 resolver 策略)。
	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		return func() {}, nil // 命令表未装配(极简 headless):服务可用,命令缺省
	}
	dis, err := cmds.Register(sdk.CommandSpec{
		Name:  "preview",
		Usage: "/preview <文件路径>",
		Desc:  "在文档预览工作台打开文件(TUI pager / Web 面板;含 markdown/Office/PDF)",
		Run: func(args []string) (string, error) {
			p := strings.TrimSpace(strings.Join(args, " "))
			if p == "" {
				return "", fmt.Errorf("/preview <文件路径>")
			}
			// 先本地校验(格式可识别 + 路径可解析),避免把无效意图广播给各端
			if f, ok := FormatByName(p); !ok || f == sdk.DocFormatUnsupported {
				return "", fmt.Errorf("不支持预览的文件类型: %s", filepath.Base(p))
			}
			// 非 strict:本步只校验"可识别格式 + 路径可达"(TUI/CLI 语境);
			// 各端真正打开时再按自身策略(Web/IM 经 /api/doc/* 的 strict 解析器)收窄根集合。
			if _, err := svc.Detect(context.Background(), sdk.DocRequest{Path: p}); err != nil {
				return "", err
			}
			if _, err := c.Emit(context.Background(), sdk.EventDocOpen, sdk.DocOpenEvent{Path: p}, sdk.Emit); err != nil {
				return "", err
			}
			return "已请求预览 " + p + "(TUI 弹 pager / Web 打开文档面板)", nil
		},
	})
	if err != nil {
		return func() {}, nil // 同名冲突(外部已注册):不覆盖,静默跳过(非致命)
	}
	return func() { dis() }, nil
}

// Options 构造选项。
type Options struct {
	Sandbox sdk.Sandbox
	Home    string // GAH_HOME(附件目录与 config 拒绝判定用)
	Budget  Budget
	Logger  *slog.Logger
}

// extractor 一个格式抽取器。abs 已过 resolver 与源大小预算;fi 为已 stat 的元信息。
type extractor func(ctx context.Context, s *Service, abs string, fi os.FileInfo, req sdk.DocRequest, format sdk.DocFormat) (*sdk.DocView, error)

// assetRef 资产位置:zip part(docx/pptx 的 media)或同目录文件(markdown 图片)。
// doc 为归属文档的 realpath(取回时校验,防跨文档串用资产 ID)。
type assetRef struct {
	kind string // zip | file
	doc  string
	abs  string
	part string
	mime string
}

// Service 实现 sdk.DocService。
type Service struct {
	sb     sdk.Sandbox
	res    *Resolver
	budget Budget
	cache  *docCache
	log    *slog.Logger

	extractors map[sdk.DocFormat]extractor
	pending    map[sdk.DocFormat]string // 未交付格式 → 计划切片(显式提示用)

	assets map[string]assetRef
}

// New 构造文档服务(注册基线抽取器:text/code/binary/image/unsupported)。
func New(o Options) *Service {
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	s := &Service{
		sb:     o.Sandbox,
		res:    NewResolver(o.Sandbox, o.Home),
		budget: o.Budget.withDefaults(),
		cache:  newDocCache(32, 64<<20),
		log:    o.Logger,
		assets: map[string]assetRef{},
	}
	s.extractors = map[sdk.DocFormat]extractor{
		sdk.DocFormatMarkdown:    extractMarkdown,
		sdk.DocFormatCSV:         extractTabular,
		sdk.DocFormatNotebook:    extractNotebook,
		sdk.DocFormatText:        extractTextOrCode,
		sdk.DocFormatCode:        extractTextOrCode,
		sdk.DocFormatBinary:      extractBinary,
		sdk.DocFormatImage:       extractImage,
		sdk.DocFormatPDF:         extractPDF,
		sdk.DocFormatDOCX:        extractDOCX,
		sdk.DocFormatUnsupported: extractUnsupported,
	}
	s.pending = map[sdk.DocFormat]string{
		sdk.DocFormatHTML: "切片 D6(源码视图 + 沙箱 iframe)",
		sdk.DocFormatXLSX: "切片 D3(自研 OOXML)",
		sdk.DocFormatPPTX: "切片 D3(自研 OOXML)",
	}
	return s
}

// applyData 让 manifest data 覆盖预算(部分覆盖,其余取默认)。
func (s *Service) applyData(data map[string]any) {
	if data == nil {
		return
	}
	num := func(key string) (int64, bool) {
		if v, ok := data[key]; ok {
			switch n := v.(type) {
			case int:
				return int64(n), true
			case int64:
				return n, true
			case float64:
				return int64(n), true
			}
		}
		return 0, false
	}
	if n, ok := num("max_input_mb"); ok && n > 0 {
		s.budget.MaxInputBytes = n << 20
	}
	if n, ok := num("max_preview_kb"); ok && n > 0 {
		s.budget.MaxPreviewBytes = n << 10
	}
	if n, ok := num("max_blocks"); ok && n > 0 {
		s.budget.MaxBlocks = int(n)
	}
	if n, ok := num("max_text_lines"); ok && n > 0 {
		s.budget.MaxTextLines = int(n)
	}
	if n, ok := num("timeout_seconds"); ok && n > 0 {
		s.budget.Timeout = time.Duration(n) * time.Second
	}
	s.budget = s.budget.withDefaults()
}

// Budget 返回生效预算(web 端点/工具展示与单测用)。
func (s *Service) Budget() Budget { return s.budget }

// Resolver 返回路径解析器(UI 层文件树复用同一策略)。
func (s *Service) Resolver() *Resolver { return s.res }

// RegisterExtractor 供后续切片(D1–D4)与外部测试注册抽取器。
func (s *Service) RegisterExtractor(f sdk.DocFormat, fn extractor) {
	s.extractors[f] = fn
	delete(s.pending, f)
}

// RegisterAsset 登记 zip 内嵌资产(docx/pptx 抽取器调用);返回不透明 ID。
// abs 会被 realpath 归—(与 resolver 产出的路径一致,避免 symlink 前缀差异)。
func (s *Service) RegisterAsset(abs, part, mime string) string {
	abs = realPathOrClean(abs)
	id := assetID(abs, part)
	s.assets[id] = assetRef{kind: assetKindZip, doc: abs, abs: abs, part: part, mime: mime}
	return id
}

// RegisterFileAsset 登记同目录文件资产(markdown 图片等)并归属到文档 doc;返回不透明 ID。
func (s *Service) RegisterFileAsset(doc, abs, mime string) string {
	doc = realPathOrClean(doc)
	abs = realPathOrClean(abs)
	id := assetID(doc+"\x00"+abs, "")
	s.assets[id] = assetRef{kind: assetKindFile, doc: doc, abs: abs, mime: mime}
	return id
}

func realPathOrClean(abs string) string {
	if rr, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(rr)
	}
	return filepath.Clean(abs)
}

// Detect 格式判定(经 resolver,仅扩展名 + %PDF- 魔数特例 + UTF-8 兜底)。
func (s *Service) Detect(ctx context.Context, req sdk.DocRequest) (sdk.DocFormat, error) {
	abs, _, err := s.prepare(req, false)
	if err != nil {
		return "", err
	}
	return s.detect(abs, req), nil
}

// Preview 富预览(带缓存)。
func (s *Service) Preview(ctx context.Context, req sdk.DocRequest) (*sdk.DocView, error) {
	ctx, cancel := context.WithTimeout(ctx, s.budget.Timeout)
	defer cancel()

	abs, fi, err := s.prepare(req, true)
	if err != nil {
		return nil, err
	}
	format := s.detect(abs, req)
	key := cacheKey{path: abs, size: fi.Size(), mtime: fi.ModTime().UnixNano(), params: paramSig(req)}
	if v, ok := s.cache.get(key); ok {
		return v, nil
	}
	var v *sdk.DocView
	if ex, ok := s.extractors[format]; ok {
		v, err = ex(ctx, s, abs, fi, req, format)
		if err != nil {
			return nil, err
		}
	} else {
		v = s.pendingView(req, abs, fi, format)
	}
	s.cache.put(key, v)
	return v, nil
}

// Text 行号化 Markdown(模型工具/IM/CLI)。
func (s *Service) Text(ctx context.Context, req sdk.DocRequest) (*sdk.DocText, error) {
	v, err := s.Preview(ctx, req)
	if err != nil {
		return nil, err
	}
	return renderDocText(v, req, s.budget), nil
}

// Asset 取回内嵌资产(D0 无抽取器产出;D2/D3 起生效)。
func (s *Service) Asset(ctx context.Context, req sdk.DocRequest, assetID string) (io.ReadCloser, string, error) {
	abs, _, err := s.prepare(req, false)
	if err != nil {
		return nil, "", err
	}
	ref, ok := s.assets[assetID]
	if !ok || ref.doc != abs {
		return nil, "", fmt.Errorf("%w: 未知内嵌资产 %s", sdk.ErrDocUnsupported, assetID)
	}
	rc, mimeType, err := s.openAsset(ctx, ref)
	return rc, mimeType, err
}

// Raw 原生字节(Range/下载用;调用方负责 Close)。
func (s *Service) Raw(ctx context.Context, req sdk.DocRequest) (io.ReadSeekCloser, string, error) {
	abs, _, err := s.prepare(req, true)
	if err != nil {
		return nil, "", err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", sdk.ErrDocNotFound, err)
	}
	head := make([]byte, 16)
	n, _ := f.Read(head)
	_, _ = f.Seek(0, 0)
	return f, mimeOf(abs, head[:n]), nil
}

// prepare 解析路径 + stat + 源大小预算。
func (s *Service) prepare(req sdk.DocRequest, checkSize bool) (string, os.FileInfo, error) {
	abs, err := s.res.Resolve(req.Path, req.Strict)
	if err != nil {
		return "", nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", sdk.ErrDocNotFound, err)
	}
	limit := s.budget.MaxInputBytes
	if req.MaxInputBytes > 0 {
		limit = req.MaxInputBytes
	}
	if checkSize && fi.Size() > limit {
		return "", nil, fmt.Errorf("%w: %s 大小 %d 字节超出上限 %d", sdk.ErrDocTooLarge, fi.Name(), fi.Size(), limit)
	}
	return abs, fi, nil
}

// detect 扩展名优先;未知/缺失才读头部兜底。
func (s *Service) detect(abs string, req sdk.DocRequest) sdk.DocFormat {
	if f, ok := formatByExt(abs); ok {
		return f
	}
	if req.NoAssets {
		return sdk.DocFormatBinary
	}
	f, err := os.Open(abs)
	if err != nil {
		return sdk.DocFormatBinary
	}
	defer f.Close()
	return sniffFormat(f)
}

// pendingView 未交付格式的显式提示视图(仍然可下载/raw;绝不假装完整)。
func (s *Service) pendingView(req sdk.DocRequest, abs string, fi os.FileInfo, format sdk.DocFormat) *sdk.DocView {
	v := baseView(req, abs, fi.Size(), fi.ModTime().UnixNano(), format)
	slice := s.pending[format]
	if slice == "" {
		slice = "后续切片"
	}
	v.Warnings = append(v.Warnings, fmt.Sprintf("格式 %s 的解析抽取器尚未交付(%s);当前仅返回文件信息", format, slice))
	v.Blocks = []sdk.DocBlock{{
		Kind: sdk.DocBlockNote,
		Text: fmt.Sprintf("%s(%s,%d 字节)的在线预览依赖 %s", v.Name, format, fi.Size(), slice),
	}}
	return v
}

func timeUnix(ns int64) time.Time { return time.Unix(0, ns) }

// FormatByName 纯名字判定(不触碰文件系统;供 doc_list/UI 行尾按钮等做候选标注)。
func FormatByName(name string) (sdk.DocFormat, bool) { return formatByExt(name) }

// PreviewableExts 可预览扩展名(升序,UI 白名单与文档用)。
func PreviewableExts() []string {
	out := make([]string, 0, len(extFormats))
	for e := range extFormats {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// BaseName 便捷:取展示名(去掉目录,空则原样)。
func BaseName(p string) string { return filepath.Base(strings.TrimSpace(p)) }
