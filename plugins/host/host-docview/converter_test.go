// 外部转换器单测(D6-2):探测/启用门控/转换产物走 PDF 抽取/缓存落位/失败回退/裁剪。
// 全部离线:run 钩子注入假转换器(写测试内构造的确定性 PDF),不依赖本机 soffice。
package hostdocview

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubConverter 构造转换器 stub:run 把 PDF 写到 --outdir(模拟 soffice)。
func stubConverter(t *testing.T, s *Service, pdf []byte, runErr error) {
	t.Helper()
	s.conv = converter{
		enabled:  true,
		soffice:  "/usr/bin/soffice",
		cacheDir: filepath.Join(t.TempDir(), "cache", "doc"),
		run: func(_ context.Context, _ string, args ...string) error {
			if runErr != nil {
				return runErr
			}
			out := ""
			src := ""
			for i, a := range args {
				if a == "--outdir" && i+1 < len(args) {
					out = args[i+1]
				}
				if a == "--convert-to" {
					continue
				}
			}
			if len(args) > 0 {
				src = args[len(args)-1]
			}
			if out == "" || src == "" {
				return errors.New("参数不完整")
			}
			dst := filepath.Join(out, strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))+".pdf")
			return os.WriteFile(dst, pdf, 0o644)
		},
	}
}

func TestConverterLegacyOfficeHighFidelity(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "old.doc", []byte{0xd0, 0xcf, 0x11, 0xe0, 0x00}) // CFB 头
	stubConverter(t, s, pdfWithText(t, "legacy doc body"), nil)

	v := preview(t, s, p)
	if v.Format != sdk.DocFormatUnsupported {
		t.Fatalf("格式应保持 unsupported(源仍是 .doc): %s", v.Format)
	}
	if v.Size == 0 || v.Name != "old.doc" {
		t.Fatalf("身份应归源文件: size=%d name=%s", v.Size, v.Name)
	}
	if v.Meta["preview_via"] != "external-converter" || v.Meta["pdf_asset"] == "" {
		t.Fatalf("应标注转换来源与 PDF 资产: %+v", v.Meta)
	}
	var text string
	for _, b := range v.Blocks {
		text += b.Text
	}
	if !strings.Contains(text, "legacy doc body") {
		t.Fatalf("块应来自转换后 PDF: %+v", v.Blocks)
	}
	joined := strings.Join(v.Warnings, " | ")
	if !strings.Contains(joined, "外部转换器") {
		t.Fatalf("应显式标注经外部转换器: %v", v.Warnings)
	}

	// 转换产物落 $GAH_HOME 派生的 cache/doc(便携纪律),且经资产端点可取回
	ents, err := os.ReadDir(s.conv.cacheDir)
	if err != nil || len(ents) == 0 {
		t.Fatalf("转换产物应落在 cache/doc: %v %d", err, len(ents))
	}
	rc, mimeType, err := s.Asset(context.Background(), sdk.DocRequest{Path: p}, v.Meta["pdf_asset"])
	if err != nil {
		t.Fatalf("PDF 资产应可取回: %v", err)
	}
	if mimeType != "application/pdf" {
		t.Fatalf("资产 MIME 应为 PDF: %s", mimeType)
	}
	// 立即关掉:下面要让同源第二次转换覆盖这个缓存产物,而 Windows 不允许
	// os.Rename 替换已被打开的文件 —— 挂在 defer 上会活到测试结束,第二次预览就
	// 退化成「外部转换器回退」,pdf_asset 丢失(POSIX 允许 rename 覆盖已打开文件,
	// 所以本地永远看不到)。
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	// 缓存复用:同源同 mtime → 同一产物路径(不重转)
	first := v.Meta["pdf_asset"]
	s.cache = newDocCache(32, 64<<20) // 清视图缓存,强制重走抽取
	v2 := preview(t, s, p)
	if v2.Meta["pdf_asset"] != first {
		t.Fatalf("同源应复用同一转换产物: %s vs %s", v2.Meta["pdf_asset"], first)
	}
}

func TestConverterDisabledAndUnavailableHints(t *testing.T) {
	// 未启用但已探测到 soffice:提示可启用
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "old.doc", []byte{0xd0, 0xcf, 0x11, 0xe0})
	conv := newConverter(false, t.TempDir())
	conv.soffice = "/usr/bin/soffice"
	s.conv = conv
	v := preview(t, s, p)
	if !strings.Contains(strings.Join(v.Warnings, " | "), "external_converters") {
		t.Fatalf("应提示启用方式: %v", v.Warnings)
	}
	if v.Meta["preview_via"] != "" {
		t.Fatalf("未启用不应转换: %+v", v.Meta)
	}
	// 未安装:提示安装 LibreOffice
	s2, dir2 := newSvc(t, Budget{})
	p2 := writeFile(t, dir2, "old.xls", []byte{0xd0, 0xcf})
	conv2 := newConverter(false, t.TempDir())
	conv2.soffice = ""
	s2.conv = conv2
	v2 := preview(t, s2, p2)
	if !strings.Contains(strings.Join(v2.Warnings, " | "), "未检测到 soffice") {
		t.Fatalf("应提示未安装: %v", v2.Warnings)
	}
	// 非旧 Office 格式(如归档)不提示转换器相关文案
	s3, dir3 := newSvc(t, Budget{})
	p3 := writeFile(t, dir3, "a.zip", []byte("PK\x03\x04"))
	v3 := preview(t, s3, p3)
	if strings.Contains(strings.Join(v3.Warnings, " | "), "soffice") {
		t.Fatalf("归档不应提示转换器: %v", v3.Warnings)
	}
}

func TestConverterFailureFallsBackExplicitly(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "old.doc", []byte{0xd0, 0xcf, 0x11, 0xe0})
	stubConverter(t, s, nil, errors.New("soffice: 崩溃"))
	v := preview(t, s, p)
	joined := strings.Join(v.Warnings, " | ")
	if !strings.Contains(joined, "外部转换器回退") || !strings.Contains(joined, "崩溃") {
		t.Fatalf("失败应显式回退说明: %v", v.Warnings)
	}
	if v.Meta["preview_via"] == "external-converter" {
		t.Fatal("失败不应标注为已转换")
	}
	// 产出于空/损坏 PDF → 解析失败也走回退(不 panic)
	s2, dir2 := newSvc(t, Budget{})
	p2 := writeFile(t, dir2, "old.xls", []byte{0xd0, 0xcf})
	stubConverter(t, s2, []byte("not a pdf"), nil)
	v2 := preview(t, s2, p2)
	if !strings.Contains(strings.Join(v2.Warnings, " | "), "外部转换器回退") {
		t.Fatalf("坏产物应回退: %v", v2.Warnings)
	}
}

func TestConverterCacheNameAndPrune(t *testing.T) {
	home := t.TempDir()
	if got := cacheDirPath(home); got != filepath.Join(home, "cache", "doc") {
		t.Fatalf("缓存目录应派生自 GAH_HOME: %s", got)
	}
	if got := cacheDirPath(""); !strings.HasSuffix(got, filepath.Join("gah-doc-cache")) {
		t.Fatalf("无 GAH_HOME 应退 TempDir: %s", got)
	}
	dir := t.TempDir()
	fi, err := os.Stat(writeFile(t, dir, "名字 with 空格.doc", []byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	name := converterCacheName(filepath.Join(dir, "名字 with 空格.doc"), fi)
	if !strings.HasSuffix(name, ".pdf") || strings.ContainsAny(name, " ") || len(name) > 64 {
		t.Fatalf("产物名应安全可读: %q", name)
	}
	// 不同 mtime → 不同产物名(缓存失效正确)
	fi2 := fakeFileInfo{name: fi.Name(), size: fi.Size(), mod: fi.ModTime().Add(time.Second)}
	if converterCacheName(filepath.Join(dir, "名字 with 空格.doc"), fi2) == name {
		t.Fatal("mtime 变化应产生不同产物名")
	}
	// 裁剪:超保留期产物被清理,新产物保留
	conv := converter{cacheDir: dir}
	old := filepath.Join(dir, "old-cache.pdf")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, "fresh-cache.pdf")
	if err := os.WriteFile(fresh, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	conv.pruneCache()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("超期缓存应被清理")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("未超期缓存应保留")
	}
}

// fakeFileInfo 仅覆盖 name/size/mod 的最小 os.FileInfo。
type fakeFileInfo struct {
	name string
	size int64
	mod  time.Time
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() os.FileMode  { return 0o644 }
func (f fakeFileInfo) ModTime() time.Time { return f.mod }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

// TestConverterHints 探测结果描述(诊断用)。
func TestConverterHints(t *testing.T) {
	c := converter{soffice: "/x/soffice"}
	h := strings.Join(c.hints(), " | ")
	if !strings.Contains(h, "已检测到 soffice") || !c.available() || c.usable() {
		t.Fatalf("探测描述异常: %v", c.hints())
	}
	c.enabled = true
	if !c.usable() {
		t.Fatal("启用且有工具应可用")
	}
	if e := (converter{enabled: true}).hints(); !strings.Contains(strings.Join(e, " | "), "未检测到 soffice") {
		t.Fatalf("未安装描述异常: %v", e)
	}
	if _, err := (converter{}).convertToPDF(context.Background(), "/tmp/x.doc", fakeFileInfo{}); err == nil {
		t.Fatal("不可用时应显式报错")
	}
}

// —— RST-1 外部光栅单测(注入 run,不依赖本机 poppler) ——

// tinyPNG 生成最小合法 PNG(2×1,红/蓝),供光栅 stub 写盘。
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{B: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// rasterStub 构造可用光栅的转换器(记录调用次数 + 可注入 run 错误)。
func rasterStub(t *testing.T, pngBytes []byte, runErr error) (*converter, *int) {
	t.Helper()
	calls := 0
	return &converter{
		rasterOn: true,
		pdftoppm: "/usr/bin/pdftoppm",
		cacheDir: filepath.Join(t.TempDir(), "cache", "doc"),
		run: func(_ context.Context, _ string, args ...string) error {
			calls++
			if runErr != nil {
				return runErr
			}
			// 最后两个参数:输入 PDF 与输出前缀(pdftoppm 追加 .png)
			prefix := args[len(args)-1]
			return os.WriteFile(prefix+".png", pngBytes, 0o644)
		},
	}, &calls
}

func TestConverterRasterPDF(t *testing.T) {
	pngBytes := tinyPNG(t)
	conv, calls := rasterStub(t, pngBytes, nil)
	out, err := conv.rasterPDF(context.Background(), "/tmp/a.pdf", 100, 42, 1, 96)
	if err != nil {
		t.Fatalf("光栅应成功: %v", err)
	}
	if !strings.HasSuffix(out, ".png") || filepath.Dir(out) != filepath.Join(conv.cacheDir, "raster") {
		t.Fatalf("产物应落 cache/doc/raster: %s", out)
	}
	got, err := os.ReadFile(out)
	if err != nil || len(got) != len(pngBytes) {
		t.Fatalf("产物内容异常: %v %d", err, len(got))
	}
	if *calls != 1 {
		t.Fatalf("应只调用一次: %d", *calls)
	}
	// 缓存命中:同源同页同 dpi 不重跑
	if _, err := conv.rasterPDF(context.Background(), "/tmp/a.pdf", 100, 42, 1, 96); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("缓存命中不应重跑: %d", *calls)
	}
	// 页/dpi 不同 → 不同产物(重跑)
	if _, err := conv.rasterPDF(context.Background(), "/tmp/a.pdf", 100, 42, 2, 96); err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Fatalf("换页应重跑: %d", *calls)
	}
	if _, err := conv.rasterPDF(context.Background(), "/tmp/a.pdf", 100, 42, 2, 150); err != nil {
		t.Fatal(err)
	}
	if *calls != 3 {
		t.Fatalf("换 dpi 应重跑: %d", *calls)
	}
	// 源文件变更(mtime/size)→ 不同缓存名
	if n1, n2 := rasterCacheName("/a.pdf", 1, 1, 1, 96), rasterCacheName("/a.pdf", 1, 2, 1, 96); n1 == n2 {
		t.Fatal("mtime 变化应产生不同光栅缓存名")
	}
}

func TestConverterRasterGuardsAndErrors(t *testing.T) {
	// 未启用 / 未安装 → 显式错误
	if _, err := (converter{}).rasterPDF(context.Background(), "/tmp/a.pdf", 1, 1, 1, 96); err == nil {
		t.Fatal("未启用应报错")
	}
	off := &converter{rasterOn: true, pdftoppm: "", cacheDir: t.TempDir()}
	if _, err := off.rasterPDF(context.Background(), "/tmp/a.pdf", 1, 1, 1, 96); err == nil {
		t.Fatal("无 pdftoppm 应报错")
	}
	if off.rasterUsable() {
		t.Fatal("rasterUsable 应为 false")
	}
	// run 失败 → 带上下文错误
	bad, _ := rasterStub(t, nil, errors.New("pdftoppm: boom"))
	if _, err := bad.rasterPDF(context.Background(), "/tmp/a.pdf", 1, 1, 1, 96); err == nil ||
		!strings.Contains(err.Error(), "pdftoppm 光栅失败") {
		t.Fatalf("失败应带上下文: %v", err)
	}
	// DPI 裁剪
	for in, want := range map[int]int{-5: 96, 0: 96, 10: rasterMinDPI, 96: 96, 900: rasterMaxDPI} {
		if got := clampDPI(in); got != want {
			t.Fatalf("clampDPI(%d)=%d want %d", in, got, want)
		}
	}
	// 产物超限 → 报错且不留缓存
	big := make([]byte, rasterMaxBytes+1)
	conv, _ := rasterStub(t, big, nil)
	if _, err := conv.rasterPDF(context.Background(), "/tmp/a.pdf", 1, 1, 1, 96); err == nil ||
		!strings.Contains(err.Error(), "过大") {
		t.Fatalf("超限应报错: %v", err)
	}
	ents, _ := os.ReadDir(filepath.Join(conv.cacheDir, "raster"))
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".png") {
			t.Fatalf("超限产物不应留缓存: %s", e.Name())
		}
	}
	// 缓存按龄清理(超期文件被删,新文件保留)
	conv2, _ := rasterStub(t, tinyPNG(t), nil)
	dir := filepath.Join(conv2.cacheDir, "raster")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "old.png")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	conv2.pruneDir(dir)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("超期光栅缓存应被清理")
	}
}

func TestServiceRaster(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "doc.pdf", pdfWithText(t, "hello raster"))
	conv, _ := rasterStub(t, tinyPNG(t), nil)
	s.conv = *conv

	out, err := s.Raster(context.Background(), sdk.DocRequest{Path: p}, 1, 0)
	if err != nil {
		t.Fatalf("Raster 应成功: %v", err)
	}
	if out.Page != 1 || out.DPI != 96 || out.Mime != "image/png" || out.Bytes == 0 || out.W != 2 || out.H != 1 {
		t.Fatalf("光栅结果异常: %+v", out)
	}
	if out.Path != p {
		t.Fatalf("对外路径应为调用方视角: %s", out.Path)
	}
	// 页码越界 → ErrDocNotFound
	if _, err := s.Raster(context.Background(), sdk.DocRequest{Path: p}, 9, 96); !errors.Is(err, sdk.ErrDocNotFound) {
		t.Fatalf("越界应报 not found: %v", err)
	}
	// 未启用 → ErrDocUnsupported
	s2, dir2 := newSvc(t, Budget{})
	p2 := writeFile(t, dir2, "doc.pdf", pdfWithText(t, "x"))
	if _, err := s2.Raster(context.Background(), sdk.DocRequest{Path: p2}, 1, 96); !errors.Is(err, sdk.ErrDocUnsupported) {
		t.Fatalf("未启用应报 unsupported: %v", err)
	}
	// 非 PDF → ErrDocUnsupported
	s3, dir3 := newSvc(t, Budget{})
	p3 := writeFile(t, dir3, "note.txt", []byte("hi"))
	s3.conv = *conv
	if _, err := s3.Raster(context.Background(), sdk.DocRequest{Path: p3}, 1, 96); !errors.Is(err, sdk.ErrDocUnsupported) {
		t.Fatalf("非 PDF 应报 unsupported: %v", err)
	}
	// 产物超限 → ErrDocTooLarge
	s4, dir4 := newSvc(t, Budget{})
	p4 := writeFile(t, dir4, "doc.pdf", pdfWithText(t, "x"))
	bigConv, _ := rasterStub(t, make([]byte, rasterMaxBytes+1), nil)
	s4.conv = *bigConv
	if _, err := s4.Raster(context.Background(), sdk.DocRequest{Path: p4}, 1, 96); !errors.Is(err, sdk.ErrDocTooLarge) {
		t.Fatalf("超限应报 too large: %v", err)
	}
	// 契约自检
	var _ sdk.DocRasterService = s
}

// 光栅结果应带宿主侧 CachePath(供出站登记),且该文件确实存在。
func TestServiceRasterCachePath(t *testing.T) {
	s, dir := newSvc(t, Budget{})
	p := writeFile(t, dir, "doc.pdf", pdfWithText(t, "cache path"))
	conv, _ := rasterStub(t, tinyPNG(t), nil)
	s.conv = *conv
	out, err := s.Raster(context.Background(), sdk.DocRequest{Path: p}, 1, 96)
	if err != nil {
		t.Fatal(err)
	}
	if out.CachePath == "" {
		t.Fatal("应带宿主侧 CachePath")
	}
	if _, err := os.Stat(out.CachePath); err != nil {
		t.Fatalf("CachePath 应指向实际产物: %v", err)
	}
	if !strings.Contains(out.CachePath, filepath.Join("cache", "doc", "raster")) {
		t.Fatalf("CachePath 应位于 cache/doc/raster: %s", out.CachePath)
	}
}

// 条目 77:真 exec 路径(PATH 探测 → 真进程 → argv → 退出码 → 产物发现/缓存落位)。
// 用 PATH 前置**假 soffice** 覆盖,不装 ≈700MB 的 LibreOffice:契约(参数/命名/退出语义)照查。
func TestConverterRealExecPATHShim(t *testing.T) {
	shimDir, logFile := t.TempDir(), filepath.Join(t.TempDir(), "argv.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> '" + logFile + "'\n" +
		"out=''; in=''\n" +
		"while [ $# -gt 0 ]; do if [ \"$1\" = '--outdir' ]; then out=\"$2\"; fi; in=\"$1\"; shift; done\n" +
		"base=$(basename \"$in\"); base=${base%.*}\n" +
		"printf '%%PDF-1.4\\n' > \"$out/$base.pdf\"\n" +
		"exit 0\n"
	writeShim := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(shimDir, "soffice"), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeShim(script)
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	conv := newConverter(true, t.TempDir())
	if filepath.Dir(conv.soffice) != shimDir {
		t.Fatalf("PATH 探测应命中 shim,得 %q", conv.soffice)
	}
	if !conv.usable() {
		t.Fatal("探测到 soffice 且启用 → 应 usable")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "季度报告.docx")
	if err := os.WriteFile(src, []byte("fake docx"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := conv.convertToPDF(context.Background(), src, fi)
	if err != nil {
		t.Fatalf("真 exec 转换应成功: %v", err)
	}
	// ① argv 契约:soffice 的参数形状是硬契约(裸 --convert-to 不带 pdf 会静默转成别的格式)
	argv, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--headless", "--norestore", "--convert-to pdf", "--outdir", src} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("argv 应含 %q,实际 %q", want, strings.TrimSpace(string(argv)))
		}
	}
	// ② 缓存落位:产物名 = 派生键(调用方以该路径存在即判命中复用),内容来自转换器
	wantPath := filepath.Join(conv.cacheDir, converterCacheName(src, fi))
	if out != wantPath {
		t.Errorf("产物应落在派生缓存路径 %q,得 %q", wantPath, out)
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		t.Fatalf("缓存产物应非空:%v", err)
	}
	// ③ 源文件改动 → 键变化(不误用旧缓存)
	fi2, _ := os.Stat(src)
	if err := os.Chtimes(src, fi2.ModTime(), fi2.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	fi3, _ := os.Stat(src)
	if converterCacheName(src, fi3) == converterCacheName(src, fi) {
		t.Error("源文件 mtime 变化后缓存键应变化")
	}
	// ④ 退出码非 0 → 显式错误(不静默降级)
	writeShim("#!/bin/sh\nexit 3\n")
	if _, err := conv.convertToPDF(context.Background(), src, fi); err == nil ||
		!strings.Contains(err.Error(), "soffice 转换失败") {
		t.Errorf("非 0 退出应显式报错,得 %v", err)
	}
	// ⑤ 失败原因要透出(stderr 尾部进错误文本,否则用户只看到"转换失败"没法排查)
	writeShim("#!/bin/sh\necho 'Error: source file could not be loaded' >&2\nexit 1\n")
	if _, err := conv.convertToPDF(context.Background(), src, fi); err == nil ||
		!strings.Contains(err.Error(), "could not be loaded") {
		t.Errorf("转换器 stderr 应透出到错误文本,得 %v", err)
	}
	// ⑥ 退出码 0 但没产出 → 显式错误(不当作成功)
	writeShim("#!/bin/sh\nexit 0\n")
	if _, err := conv.convertToPDF(context.Background(), src, fi); err == nil ||
		!strings.Contains(err.Error(), "未产出 PDF") {
		t.Errorf("无产物应显式报错,得 %v", err)
	}
}
