// 外部转换器单测(D6-2):探测/启用门控/转换产物走 PDF 抽取/缓存落位/失败回退/裁剪。
// 全部离线:run 钩子注入假转换器(写测试内构造的确定性 PDF),不依赖本机 soffice。
package hostdocview

import (
	"context"
	"errors"
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
	defer rc.Close()
	if mimeType != "application/pdf" {
		t.Fatalf("资产 MIME 应为 PDF: %s", mimeType)
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
