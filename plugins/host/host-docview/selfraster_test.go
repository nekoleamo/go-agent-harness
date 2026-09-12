// SELF-1 路线 B 集成单测:自包含光栅后端(pdfium.wasm + wazero)接入 DocumentService.Raster。
// 需 `GAH_PDFIUM_WASM=<path>`(CI 无件/无网时跳过)。
package hostdocview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func selfRasterSvc(t *testing.T) (*Service, string) {
	t.Helper()
	wasm := os.Getenv("GAH_PDFIUM_WASM")
	if wasm == "" {
		t.Skip("未设置 GAH_PDFIUM_WASM,跳过自包含光栅集成测试")
	}
	home := t.TempDir()
	dir := t.TempDir()
	s := New(Options{
		Sandbox:             &fakeSandbox{mode: sdk.SandboxWorkspace, root: dir},
		Home:                home,
		SelfContainedRaster: true,
		PDFiumWASMPath:      wasm,
	})
	return s, dir
}

// TestRasterSelfContained:未启用外部光栅时,自包含后端仍能产出 PNG;同源同页同 dpi 走缓存。
func TestRasterSelfContained(t *testing.T) {
	s, dir := selfRasterSvc(t)
	p := writeFile(t, dir, "doc.pdf", pdfWithText(t, "self contained raster"))
	if !s.self.usable() {
		t.Fatal("自包含光栅应已启用")
	}
	r1, err := s.Raster(context.Background(), sdk.DocRequest{Path: p}, 1, 96)
	if err != nil {
		t.Fatalf("自包含光栅失败: %v", err)
	}
	if r1.Mime != "image/png" || len(r1.Data) == 0 || r1.W < 50 || r1.H < 50 {
		t.Fatalf("光栅结果异常: %+v(%d 字节)", r1, len(r1.Data))
	}
	if r1.CachePath == "" {
		t.Fatal("应给出缓存路径(RST-2 窄白名单依赖它)")
	}
	if _, err := os.Stat(r1.CachePath); err != nil {
		t.Fatalf("缓存产物应存在: %v", err)
	}
	// 缓存命中:同参数第二次不再渲染(产物路径一致、内容一致)
	r2, err := s.Raster(context.Background(), sdk.DocRequest{Path: p}, 1, 96)
	if err != nil {
		t.Fatal(err)
	}
	if r2.CachePath != r1.CachePath || string(r2.Data) != string(r1.Data) {
		t.Fatalf("缓存未命中: %q vs %q", r1.CachePath, r2.CachePath)
	}
	// 页码越界 → ErrDocNotFound
	if _, err := s.Raster(context.Background(), sdk.DocRequest{Path: p}, 9, 96); !errors.Is(err, sdk.ErrDocNotFound) {
		t.Fatalf("页码越界应为 ErrDocNotFound: %v", err)
	}
	// 非 PDF → ErrDocUnsupported
	txt := writeFile(t, dir, "note.txt", []byte("hi"))
	if _, err := s.Raster(context.Background(), sdk.DocRequest{Path: txt}, 1, 96); !errors.Is(err, sdk.ErrDocUnsupported) {
		t.Fatalf("非 PDF 应拒绝: %v", err)
	}
	s.self.close()
}

// TestRasterDisabledByDefault:两个开关都关 → 显式 unsupported(不得静默)。
func TestRasterDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	s := New(Options{Sandbox: &fakeSandbox{mode: sdk.SandboxWorkspace, root: dir}, Home: t.TempDir()})
	p := writeFile(t, dir, "doc.pdf", pdfWithText(t, "x"))
	_, err := s.Raster(context.Background(), sdk.DocRequest{Path: p}, 1, 96)
	if !errors.Is(err, sdk.ErrDocUnsupported) {
		t.Fatalf("默认应报未启用: %v", err)
	}
}

// TestRasterNoPopplerErrorNamesBackend 无 poppler + 内置兜底也失败 → 错误必须同时点明
// 「外部后端为什么不可用」与「兜底为什么失败」。
// 真机 RST-1 发现:修复前只有裸的 `pdfium: 下载读取失败: context deadline exceeded`,
// 会被误读成纯网络问题,用户不知道装 poppler pdftoppm 即可绕开整条链。
func TestRasterNoPopplerErrorNamesBackend(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	conv := newConverter(true, home)
	conv.pdftoppm = "" // 模拟本机未安装 poppler(不依赖真实 PATH)
	conv.rasterOn = true
	s := New(Options{
		Sandbox:             &fakeSandbox{mode: sdk.SandboxWorkspace, root: dir},
		Home:                home,
		ExternalRaster:      true,
		SelfContainedRaster: true,
		Converter:           &conv,
		PDFiumWASMPath:      filepath.Join(home, "no-such-pdfium.wasm"), // 兜底必然失败(不触网)
	})
	p := writeFile(t, dir, "doc.pdf", pdfWithText(t, "no poppler"))
	_, err := s.Raster(context.Background(), sdk.DocRequest{Path: p}, 1, 96)
	if err == nil {
		t.Fatal("两个光栅后端都不可用时应显式报错(不得静默)")
	}
	msg := err.Error()
	if !strings.Contains(msg, "poppler") {
		t.Fatalf("错误应点明本机缺 poppler: %q", msg)
	}
	if !strings.Contains(msg, "pdfium") {
		t.Fatalf("错误应说明内置兜底也失败: %q", msg)
	}
	if !errors.Is(err, sdk.ErrDocParse) {
		t.Fatalf("应归为 ErrDocParse: %v", err)
	}

	// 另一条分支:外部光栅**未启用**(而非缺 poppler)。错误不得谎报「本机没装」,
	// 应指向开关本身(rasterOn 关时的 pdftoppm 探测结果与可用性无关)。
	conv2 := newConverter(true, home)
	conv2.rasterOn = false
	s2 := New(Options{
		Sandbox:             &fakeSandbox{mode: sdk.SandboxWorkspace, root: dir},
		Home:                home,
		SelfContainedRaster: true,
		Converter:           &conv2,
		PDFiumWASMPath:      filepath.Join(home, "no-such-pdfium.wasm"),
	})
	_, err = s2.Raster(context.Background(), sdk.DocRequest{Path: p}, 1, 96)
	if err == nil {
		t.Fatal("外部未启用且兜底失败时应报错")
	}
	if msg := err.Error(); !strings.Contains(msg, "data.external_raster") || strings.Contains(msg, "本机未检测到 poppler") {
		t.Fatalf("开关未启用时不得谎报本机缺 poppler,应指向开关: %q", msg)
	}
}
