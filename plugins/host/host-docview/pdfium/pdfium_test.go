// pdfium 包单测:取件(本地/缓存/下载/zip/sha256)与(可选)真实 wasm 渲染。
// 真实渲染需 `GAH_PDFIUM_WASM=<path>`(CI 无网/无件时跳过,与 corpus/pdftoppm 测试同口径)。
package pdfium

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func zipBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestPickWASM:zip 优先取 .std.wasm;仅有一个 wasm 时回退;裸 wasm 直通;非 wasm 报错。
func TestPickWASM(t *testing.T) {
	std := []byte("\x00asm\x01\x00\x00\x00std")
	norm := []byte("\x00asm\x01\x00\x00\x00normal")
	z := zipBytes(t, map[string][]byte{
		"release/node/pdfium.wasm":     norm,
		"release/node/pdfium.std.wasm": std,
	})
	got, err := pickWASM("u", z)
	if err != nil || string(got) != string(std) {
		t.Fatalf("应优先取 .std.wasm: %v %q", err, got)
	}
	z2 := zipBytes(t, map[string][]byte{"a/pdfium.wasm": norm})
	if got, err := pickWASM("u", z2); err != nil || string(got) != string(norm) {
		t.Fatalf("仅一个 wasm 应回退: %v %q", err, got)
	}
	if got, err := pickWASM("u", std); err != nil || string(got) != string(std) {
		t.Fatalf("裸 wasm 应直通: %v %q", err, got)
	}
	if _, err := pickWASM("u", []byte("not-a-wasm")); err == nil {
		t.Fatal("非 wasm/zip 应报错")
	}
	if _, err := pickWASM("u", zipBytes(t, map[string][]byte{"a.txt": []byte("x")})); err == nil {
		t.Fatal("zip 内无 wasm 应报错")
	}
}

// TestSourceEnsureLocalPath:显式本地路径优先读取(不校验 sha、不联网)。
func TestSourceEnsureLocalPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "my.wasm")
	want := []byte("\x00asm\x01\x00\x00\x00local")
	if err := os.WriteFile(p, want, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Source{Path: p, Home: dir}.Ensure(context.Background())
	if err != nil || string(got) != string(want) {
		t.Fatalf("本地路径读取失败: %v", err)
	}
	if _, err := (Source{Path: filepath.Join(dir, "nope.wasm")}).Ensure(context.Background()); err == nil {
		t.Fatal("本地路径不存在应报错")
	}
}

// TestSourceEnsureCacheHit:缓存命中且 sha 一致 → 直接用缓存(不联网;用不可达 URL 反证)。
func TestSourceEnsureCacheHit(t *testing.T) {
	home := t.TempDir()
	blob := []byte("\x00asm\x01\x00\x00\x00cached")
	src := Source{Home: home, URL: "http://127.0.0.1:1/never", SHA256: sha256Hex(blob)}
	if err := os.MkdirAll(filepath.Dir(src.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src.CachePath(), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := src.Ensure(context.Background())
	if err != nil || string(got) != string(blob) {
		t.Fatalf("缓存命中应直接返回: %v", err)
	}
	// 缓存内容与 sha 不符 → 丢弃并尝试下载(此处 URL 不可达 → 报错而非静默使用)
	bad := Source{Home: home, URL: "http://127.0.0.1:1/never", SHA256: sha256Hex([]byte("other"))}
	if _, err := bad.Ensure(context.Background()); err == nil {
		t.Fatal("缓存 sha 不符且不可下载应报错")
	}
	if _, err := os.Stat(bad.CachePath()); !os.IsNotExist(err) {
		t.Fatal("sha 不符的缓存应被删除")
	}
}

// TestSourceEnsureDownloadAndVerify:下载 zip → 取 std wasm → sha 校验 → 落缓存;sha 不符报错。
func TestSourceEnsureDownloadAndVerify(t *testing.T) {
	blob := []byte("\x00asm\x01\x00\x00\x00downloaded")
	z := zipBytes(t, map[string][]byte{
		"release/node/pdfium.wasm":     []byte("\x00asm\x01\x00\x00\x00other"),
		"release/node/pdfium.std.wasm": blob,
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(z)
	}))
	defer srv.Close()
	home := t.TempDir()
	got, err := Source{Home: home, URL: srv.URL, SHA256: sha256Hex(blob), HTTP: srv.Client()}.Ensure(context.Background())
	if err != nil || string(got) != string(blob) {
		t.Fatalf("下载取件失败: %v", err)
	}
	if b, err := os.ReadFile(Source{Home: home}.CachePath()); err != nil || string(b) != string(blob) {
		t.Fatalf("应落缓存: %v", err)
	}
	// sha 不符 → 显式报错,且不落缓存
	home2 := t.TempDir()
	wrong := Source{Home: home2, URL: srv.URL, SHA256: sha256Hex([]byte("nope")), HTTP: srv.Client()}
	if _, err := wrong.Ensure(context.Background()); err == nil {
		t.Fatal("sha 不符应报错")
	}
	if _, err := os.Stat(wrong.CachePath()); !os.IsNotExist(err) {
		t.Fatal("sha 不符不应落缓存")
	}
	// HTTP 非 200 → 报错
	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv404.Close()
	if _, err := (Source{Home: t.TempDir(), URL: srv404.URL, HTTP: srv404.Client()}).Ensure(context.Background()); err == nil {
		t.Fatal("HTTP 404 应报错")
	}
}

// TestRenderPageRealWASM 真实渲染(需 GAH_PDFIUM_WASM;验证自包含光栅链路可用)。
func TestRenderPageRealWASM(t *testing.T) {
	wasmPath := os.Getenv("GAH_PDFIUM_WASM")
	if wasmPath == "" {
		t.Skip("未设置 GAH_PDFIUM_WASM,跳过真实 wasm 渲染")
	}
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r, err := New(ctx, wasm)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Close(ctx)
	img, err := r.RenderPage(ctx, tinyPDF(t), 1, 96)
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	b := img.Bounds()
	if b.Dx() < 200 || b.Dy() < 100 {
		t.Fatalf("位图尺寸异常: %v", b)
	}
	// 页面上应出现非白像素(画了矩形/文字)
	nonWhite := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			if cr>>8 != 0xFF || cg>>8 != 0xFF || cb>>8 != 0xFF {
				nonWhite++
			}
		}
	}
	if nonWhite == 0 {
		t.Fatal("渲染结果全白(未真正绘制)")
	}
	t.Logf("自包含光栅: %dx%d,非白像素 %d,init=%s invoke=%d syscalls=%d",
		b.Dx(), b.Dy(), nonWhite, r.InitExport(), r.InvokeCalls(), r.Syscalls())
	// 越界页显式报错
	if _, err := r.RenderPage(ctx, tinyPDF(t), 99, 72); err == nil {
		t.Fatal("页码越界应报错")
	}
}

// tinyPDF 单页(矩形 + 文字)最小合法 PDF。
func tinyPDF(t *testing.T) []byte {
	t.Helper()
	content := "1 0 0 RG 3 w 10 10 180 80 re S BT /F1 14 Tf 20 40 Td (self raster) Tj ET"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 100] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs)+1)
	for i, o := range objs {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objs)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return buf.Bytes()
}
