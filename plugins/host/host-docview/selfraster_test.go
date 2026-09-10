// SELF-1 路线 B 集成单测:自包含光栅后端(pdfium.wasm + wazero)接入 DocumentService.Raster。
// 需 `GAH_PDFIUM_WASM=<path>`(CI 无件/无网时跳过)。
package hostdocview

import (
	"context"
	"errors"
	"os"
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
