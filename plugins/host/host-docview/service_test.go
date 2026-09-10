// 文档服务端到端单测(D0 基线抽取器):text/code/binary/image/unsupported +
// 预算拒绝 + 未交付格式显式提示 + 缓存 + Raw + 资产注册/取回。
package hostdocview

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func newSvc(t *testing.T, b Budget) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	sb := &fakeSandbox{mode: sdk.SandboxWorkspace, root: dir}
	s := New(Options{Sandbox: sb, Home: filepath.Join(dir, "gah-data"), Budget: b})
	return s, dir
}

func TestPreviewTextAndCode(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "note.txt", []byte("第一行\r\nsecond\n"))
	v, err := s.Preview(context.Background(), sdk.DocRequest{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if v.Format != sdk.DocFormatText || len(v.Blocks) != 1 || v.Blocks[0].Kind != sdk.DocBlockCode {
		t.Fatalf("非预期视图: %+v", v)
	}
	if !strings.Contains(v.Blocks[0].Text, "第一行\nsecond") {
		t.Fatalf("换行未归一化: %q", v.Blocks[0].Text)
	}

	// 行号化文本(CLI/模型/IM 共用渲染):文本族整体呈一个围栏代码块
	tx, err := s.Text(context.Background(), sdk.DocRequest{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.Lines) != 5 || tx.Lines[0].Text != "```txt" || tx.Lines[1].Number != 2 {
		t.Fatalf("行号化异常: %+v", tx)
	}
	if tx.Lines[2].Text != "second" || tx.TotalLines != 5 {
		t.Fatalf("行内容异常: %+v", tx.Lines)
	}

	// 代码文件带语言提示
	pc := writeFile(t, dir, "main.go", []byte("package main\n"))
	vc, err := s.Text(context.Background(), sdk.DocRequest{Path: pc})
	if err != nil {
		t.Fatal(err)
	}
	if vc.Format != sdk.DocFormatCode {
		t.Fatalf("应为 code,得 %q", vc.Format)
	}
}

func TestPreviewSizeBudget(t *testing.T) {
	s, dir := newSvc(t, Budget{MaxInputBytes: 4})
	p := writeFile(t, dir, "big.txt", []byte("0123456789"))
	_, err := s.Preview(context.Background(), sdk.DocRequest{Path: p})
	if !errors.Is(err, sdk.ErrDocTooLarge) {
		t.Fatalf("应报 ErrDocTooLarge,得 %v", err)
	}
}

// 预览字节预算:超限写 Truncated 且不静默。
func TestPreviewBytesTruncated(t *testing.T) {
	s, dir := newSvc(t, Budget{MaxPreviewBytes: 4})
	p := writeFile(t, dir, "long.txt", []byte("0123456789"))
	v, err := s.Preview(context.Background(), sdk.DocRequest{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Truncated) == 0 {
		t.Fatalf("应写 Truncated: %+v", v)
	}
	if len(v.Blocks[0].Text) != 4 {
		t.Fatalf("应按预算截断,得 %q", v.Blocks[0].Text)
	}
}

func TestPreviewBinaryFallback(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "blob.dat", append([]byte{0x00, 0x01, 0x02, 0x03}, bytes.Repeat([]byte{0xff}, 32)...))
	v, err := s.Preview(context.Background(), sdk.DocRequest{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if v.Format != sdk.DocFormatBinary {
		t.Fatalf("无扩展名二进制应判 binary,得 %q", v.Format)
	}
	if len(v.Blocks) != 2 || v.Blocks[1].Kind != sdk.DocBlockCode || !strings.Contains(v.Blocks[1].Text, "00000000") {
		t.Fatalf("应给信息卡 + hexdump: %+v", v.Blocks)
	}
}

func TestPreviewImageMetadata(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "pic.png", pngBytes(t, 7, 5))
	v, err := s.Preview(context.Background(), sdk.DocRequest{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if v.Format != sdk.DocFormatImage || v.Blocks[0].Kind != sdk.DocBlockImage {
		t.Fatalf("非预期: %+v", v)
	}
	if a := v.Blocks[0].Asset; a == nil || a.W != 7 || a.H != 5 {
		t.Fatalf("尺寸探测失败: %+v", a)
	}
}

func TestPreviewUnsupportedFormat(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "legacy.doc", []byte("D0CF11E0"))
	v, err := s.Preview(context.Background(), sdk.DocRequest{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if v.Format != sdk.DocFormatUnsupported || len(v.Warnings) == 0 {
		t.Fatalf("旧格式应显式不支持 + 警告: %+v", v)
	}
}

// 收口守护栏(D6-4 后 pending 清零):每个声明格式都必须有抽取器,绝不再静默降级为提示。
func TestAllFormatsHaveExtractor(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	if len(s.pending) != 0 {
		t.Fatalf("全部格式应已交付,pending 应为空: %+v", s.pending)
	}
	for _, f := range []sdk.DocFormat{
		sdk.DocFormatMarkdown, sdk.DocFormatText, sdk.DocFormatCSV, sdk.DocFormatCode,
		sdk.DocFormatNotebook, sdk.DocFormatDOCX, sdk.DocFormatXLSX, sdk.DocFormatPPTX,
		sdk.DocFormatPDF, sdk.DocFormatHTML, sdk.DocFormatImage, sdk.DocFormatBinary,
		sdk.DocFormatUnsupported,
	} {
		if s.extractors[f] == nil {
			t.Fatalf("格式 %s 无抽取器", f)
		}
	}
	// HTML 走源码视图(不再是 pendingView 提示)
	p := writeFile(t, dir, "a.html", []byte("<title>T</title>"))
	v, err := s.Preview(context.Background(), sdk.DocRequest{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if v.Format != sdk.DocFormatHTML || v.Title != "T" || len(v.Warnings) != 0 {
		t.Fatalf("HTML 应走源码视图: %+v", v)
	}
}

func TestCacheHitAndInvalidation(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "a.txt", []byte("v1"))
	if _, err := s.Preview(context.Background(), sdk.DocRequest{Path: p}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Preview(context.Background(), sdk.DocRequest{Path: p}); err != nil {
		t.Fatal(err)
	}
	s.cache.mu.Lock()
	hits, misses := s.cache.hits, s.cache.misses
	s.cache.mu.Unlock()
	if hits != 1 || misses != 1 {
		t.Fatalf("缓存应命中一次: hits=%d misses=%d", hits, misses)
	}
	// 参数变化 → 不进同一缓存条目
	if _, err := s.Preview(context.Background(), sdk.DocRequest{Path: p, MaxBytes: 8}); err != nil {
		t.Fatal(err)
	}
	s.cache.mu.Lock()
	misses2 := s.cache.misses
	s.cache.mu.Unlock()
	if misses2 != 2 {
		t.Fatalf("参数变化应视为未命中: misses=%d", misses2)
	}
}

func TestRawAndDetect(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "a.txt", []byte("hello"))
	rc, mimeType, err := s.Raw(context.Background(), sdk.DocRequest{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if !strings.HasPrefix(mimeType, "text/plain") {
		t.Fatalf("MIME 异常: %q", mimeType)
	}
	buf := make([]byte, 5)
	if _, err := rc.Read(buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("Raw 内容异常: %q", buf)
	}

	// 魔数特例:无扩展名但内容为 PDF
	pp := writeFile(t, dir, "noext", []byte("%PDF-1.7\n%âãÏÓ\n"))
	got, err := s.Detect(context.Background(), sdk.DocRequest{Path: pp})
	if err != nil {
		t.Fatal(err)
	}
	if got != sdk.DocFormatPDF {
		t.Fatalf("无扩展名 PDF 应判 pdf,得 %q", got)
	}
}

// 资产注册与取回(zip part;D2/D3 抽取器将调用同一通路)。
func TestAssetRoundTrip(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	container := filepath.Join(dir, "c.docx")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/media/image1.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("PNGDATA")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(container, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	id := s.RegisterAsset(container, "word/media/image1.png", "image/png")
	rc, mimeType, err := s.Asset(context.Background(), sdk.DocRequest{Path: container}, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if mimeType != "image/png" {
		t.Fatalf("MIME 异常: %q", mimeType)
	}
	data, _ := os.ReadFile(container)
	if len(data) == 0 {
		t.Fatal("夹具容器为空")
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "PNGDATA" {
		t.Fatalf("资产内容异常: %q", got)
	}
	// 未知 id → 结构化拒绝
	if _, _, err := s.Asset(context.Background(), sdk.DocRequest{Path: container}, "nope"); !errors.Is(err, sdk.ErrDocUnsupported) {
		t.Fatalf("未知资产应报 ErrDocUnsupported,得 %v", err)
	}
}

// 行渲染:块模型 → Markdown(表格/标题/代码/图片),并覆盖预算截断标记。
func TestRenderDocTextBlocks(t *testing.T) {
	v := &sdk.DocView{
		Path:   "/x/a.docx",
		Format: sdk.DocFormatDOCX,
		Blocks: []sdk.DocBlock{
			{Kind: sdk.DocBlockHeading, Level: 2, Text: "标题"},
			{Kind: sdk.DocBlockParagraph, Runs: []sdk.DocRun{{Text: "粗", Bold: true}, {Text: "与"}, {Text: "链", Link: "https://e.com"}}},
			{Kind: sdk.DocBlockList, Level: 1, Text: "项"},
			{Kind: sdk.DocBlockTable, Head: []string{"a", "b"}, Rows: [][]sdk.DocCell{{{Text: "1"}, {Text: "2"}}}},
			{Kind: sdk.DocBlockCode, Lang: "go", Text: "x := 1"},
			{Kind: sdk.DocBlockImage, Text: "图", Asset: &sdk.DocAsset{ID: "abc", Name: "i.png"}},
		},
	}
	tx := renderDocText(v, sdk.DocRequest{}, DefaultBudget())
	var got []string
	for _, l := range tx.Lines {
		got = append(got, l.Text)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"## 标题", "**粗**与[链](https://e.com)", "- 项", "| a | b |", "```go", "![图](asset:abc)"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("渲染缺少 %q\n%s", want, joined)
		}
	}
	// 单行超限 → 截断标记
	tx2 := renderDocText(&sdk.DocView{Blocks: []sdk.DocBlock{{Kind: sdk.DocBlockParagraph, Text: strings.Repeat("x", 100)}}},
		sdk.DocRequest{}, Budget{MaxLineChars: 10}.withDefaults())
	if !tx2.TruncatedByBytes || !strings.Contains(tx2.Lines[0].Text, "已截断") {
		t.Fatalf("超长行应截断标记: %+v", tx2.Lines[0])
	}
}

// 分页:offset/limit 生效,行号保留全局序号。
func TestRenderPaging(t *testing.T) {
	v := &sdk.DocView{Blocks: []sdk.DocBlock{{Kind: sdk.DocBlockCode, Text: "l1\nl2\nl3\nl4"}}}
	tx := renderDocText(v, sdk.DocRequest{Offset: 2, Limit: 1}, DefaultBudget())
	if len(tx.Lines) != 1 || tx.Lines[0].Number != 3 || tx.Lines[0].Text != "l2" || tx.TotalLines != 6 {
		t.Fatalf("分页异常: %+v", tx)
	}
}

// data 预算覆盖(插件 data → 生效预算)。
func TestApplyDataBudget(t *testing.T) {
	s := New(Options{})
	s.applyData(map[string]any{"max_input_mb": int64(2), "timeout_seconds": 3, "max_text_lines": 10})
	if s.budget.MaxInputBytes != 2<<20 || s.budget.MaxTextLines != 10 {
		t.Fatalf("data 覆盖失败: %+v", s.budget)
	}
	if s.budget.Timeout.Seconds() != 3 {
		t.Fatalf("超时覆盖失败: %v", s.budget.Timeout)
	}
	if s.budget.MaxPreviewBytes != DefaultBudget().MaxPreviewBytes {
		t.Fatalf("未覆盖维度应保持默认: %+v", s.budget)
	}
}

// 插件装配:Provide ctx.doc + Inject 可用;兼容 sdk.DocService 接口注入。
func TestPluginProvidesDocService(t *testing.T) {
	svc := New(Options{})
	var ds sdk.DocService = svc
	if ds == nil {
		t.Fatal("Service 应实现 sdk.DocService")
	}
	if _, ok := FormatByName("a.md"); !ok {
		t.Fatal("FormatByName 应识别 md")
	}
	if len(PreviewableExts()) < 30 {
		t.Fatalf("可预览扩展名过少: %d", len(PreviewableExts()))
	}
}

// pngBytes 生成指定尺寸的 PNG(夹具共用)。
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{G: 255, A: 255})
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
