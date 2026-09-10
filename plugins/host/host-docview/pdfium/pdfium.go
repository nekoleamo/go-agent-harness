// Package pdfium 在纯 Go 运行时(wazero)上运行 pdfium.wasm,提供 PDF 页光栅化(SELF-1 路线 B)。
//
// 背景与依据(见 DESIGN §14.1「SELF-1 前置实测 / 可能性分析(二轮)」):
//   - 产物为 Emscripten 构建(非 STANDALONE_WASM 也仍有 JS glue):除 WASI 外还导入
//     `env.invoke_*`(JS 侧函数指针 trampoline)、`__syscall_*`、`emscripten_notify_memory_growth` 等;
//   - **wazero 公开 API 无 table/函数引用调用能力** → `invoke_*` 无法忠实实现,只能空实现;
//     实测(毒化返回值 → 逐像素相同)其返回值未被使用,故本包**计数并在调用方告警**;
//   - WASI 为 Emscripten 的 32 位偏移变体,不能复用 wazero 标准 WASI 宿主 → 宿主模块的签名
//     一律**取自被编译模块的导入定义**;
//   - `__syscall_*` 必须返回 `-ENOSYS`(返回 0 会被 Emscripten FS 误判为成功)。
//
// 依赖:github.com/tetratelabs/wazero(纯 Go,CGO_ENABLED=0 兼容)。
package pdfium

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// 常量:FPDFBitmap 格式与渲染标志。
const (
	bitmapBGRA = 4 // FPDFBitmap_BGRA
	maxPixels  = 40_000_000
)

// GfxInit 库初始化导出名(不同构建命名不一);按顺序尝试,取第一个存在的。
var initExports = []string{"PDFiumExt_Init", "PDFium_Init", "FPDF_InitLibrary"}

// ErrNotPDFium 传入字节不是 pdfium wasm(缺关键导出)。
var ErrNotPDFium = errors.New("pdfium: 缺少必要导出(FPDF_LoadMemDocument/FPDF_RenderPageBitmap)")

// Renderer 一个已初始化(库 Init 已调用)的 pdfium 实例。
// 非并发安全:调用方(host-docview)以互斥方式串行使用。
type Renderer struct {
	rt   wazero.Runtime
	mod  api.Module
	mem  api.Memory
	fn   map[string]api.Function
	init string

	invokeCalls int64
	syscalls    int64
	aborts      int64
	diag        strings.Builder // fd_write 转出的 pdfium 诊断(截断保留)
}

// New 编译 + 实例化 + 初始化 pdfium wasm。
func New(ctx context.Context, wasm []byte) (*Renderer, error) {
	r := &Renderer{fn: map[string]api.Function{}}
	rt := wazero.NewRuntime(ctx)
	r.rt = rt
	compiled, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("pdfium: wasm 编译失败: %w", err)
	}
	// 宿主面:env + wasi_snapshot_preview1,签名取自模块自身导入定义
	builders := map[string]wazero.HostModuleBuilder{}
	for _, f := range compiled.ImportedFunctions() {
		mod, name, ok := f.Import()
		if !ok {
			continue
		}
		if mod != "env" && mod != "wasi_snapshot_preview1" {
			_ = rt.Close(ctx)
			return nil, fmt.Errorf("pdfium: 未知宿主模块导入 %s.%s", mod, name)
		}
		b, ok2 := builders[mod]
		if !ok2 {
			b = rt.NewHostModuleBuilder(mod)
			builders[mod] = b
		}
		fname, fresults := name, f.ResultTypes()
		b.NewFunctionBuilder().
			WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) {
				r.stub(fname, stack, fresults)
			}), f.ParamTypes(), fresults).
			Export(fname)
	}
	for mod, b := range builders {
		if _, err := b.Instantiate(ctx); err != nil {
			_ = rt.Close(ctx)
			return nil, fmt.Errorf("pdfium: %s 宿主模块实例化失败: %w", mod, err)
		}
	}
	inst, err := rt.InstantiateModule(ctx, compiled, wazero.NewModuleConfig())
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("pdfium: 实例化失败: %w", err)
	}
	r.mod = inst
	r.mem = inst.Memory()
	if r.mem == nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("pdfium: 模块未导出 memory(自包含档要求自持内存)")
	}
	for _, name := range []string{"FPDF_LoadMemDocument", "FPDF_GetPageCount", "FPDF_LoadPage",
		"FPDF_GetPageWidth", "FPDF_GetPageHeight", "FPDFBitmap_CreateEx", "FPDFBitmap_GetBuffer",
		"FPDF_RenderPageBitmap", "malloc", "free"} {
		fn := inst.ExportedFunction(name)
		if fn == nil {
			_ = rt.Close(ctx)
			return nil, fmt.Errorf("%w: %s", ErrNotPDFium, name)
		}
		r.fn[name] = fn
	}
	// 可选导出(存在则用于清理/诊断)
	for _, name := range []string{"FPDFBitmap_FillRect", "FPDFBitmap_Destroy", "FPDF_ClosePage",
		"FPDF_CloseDocument", "FPDF_GetLastError", "__wasm_call_ctors", "_initialize"} {
		if fn := inst.ExportedFunction(name); fn != nil {
			r.fn[name] = fn
		}
	}
	if fn := r.fn["__wasm_call_ctors"]; fn != nil {
		if _, err := fn.Call(ctx); err != nil {
			_ = rt.Close(ctx)
			return nil, fmt.Errorf("pdfium: __wasm_call_ctors 失败: %w", err)
		}
	}
	for _, name := range initExports {
		fn := inst.ExportedFunction(name)
		if fn == nil {
			continue
		}
		if _, err := fn.Call(ctx); err != nil {
			_ = rt.Close(ctx)
			return nil, fmt.Errorf("pdfium: %s 初始化失败: %w", name, err)
		}
		r.init = name
		break
	}
	if r.init == "" {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("%w: 无库初始化导出(尝试 %s)", ErrNotPDFium, strings.Join(initExports, "/"))
	}
	return r, nil
}

// Close 释放运行时。
func (r *Renderer) Close(ctx context.Context) error {
	if r.rt == nil {
		return nil
	}
	return r.rt.Close(ctx)
}

// InitExport 实际使用的初始化导出名(诊断)。
func (r *Renderer) InitExport() string { return r.init }

// InvokeCalls `invoke_*`(JS trampoline)被调用次数;>0 表示该文档走过 JS glue 路径,
// 结果未经交叉校验,调用方应据此告警(见包注释)。
func (r *Renderer) InvokeCalls() int64 { return atomic.LoadInt64(&r.invokeCalls) }

// Syscalls `__syscall_*` 调用次数(-ENOSYS 返回)。
func (r *Renderer) Syscalls() int64 { return atomic.LoadInt64(&r.syscalls) }

// Diagnostics fd_write 收到的 pdfium/Emscripten 诊断文本(上限 4 KiB)。
func (r *Renderer) Diagnostics() string { return r.diag.String() }

// RenderPage 渲染 1-based 的 page 页为 dpi 对应尺寸的 RGBA 图像。
func (r *Renderer) RenderPage(ctx context.Context, pdf []byte, page, dpi int) (image.Image, error) {
	if len(pdf) == 0 {
		return nil, errors.New("pdfium: PDF 内容为空")
	}
	if page <= 0 {
		page = 1
	}
	if dpi < 36 {
		dpi = 36
	}
	if dpi > 300 {
		dpi = 300
	}
	ptr, err := r.calloc(ctx, uint64(len(pdf)))
	if err != nil {
		return nil, err
	}
	defer r.free(ctx, ptr)
	if !r.mem.Write(uint32(ptr), pdf) {
		return nil, errors.New("pdfium: 写入 PDF 到 wasm 内存失败")
	}
	doc, err := r.call(ctx, "FPDF_LoadMemDocument", ptr, uint64(len(pdf)), 0)
	if err != nil {
		return nil, err
	}
	if doc == 0 {
		return nil, fmt.Errorf("pdfium: 载入 PDF 失败(lastError=%d)", r.lastError(ctx))
	}
	if fn := r.fn["FPDF_CloseDocument"]; fn != nil {
		defer func() { _, _ = fn.Call(ctx, doc) }()
	}
	total, _ := r.call(ctx, "FPDF_GetPageCount", doc)
	if total == 0 {
		return nil, errors.New("pdfium: 文档无页面")
	}
	if uint64(page) > total {
		return nil, fmt.Errorf("pdfium: 页码越界(共 %d 页)", total)
	}
	pg, err := r.call(ctx, "FPDF_LoadPage", doc, uint64(page-1))
	if err != nil {
		return nil, err
	}
	if pg == 0 {
		return nil, fmt.Errorf("pdfium: 载入第 %d 页失败", page)
	}
	if fn := r.fn["FPDF_ClosePage"]; fn != nil {
		defer func() { _, _ = fn.Call(ctx, pg) }()
	}
	wf, _ := r.callF(ctx, "FPDF_GetPageWidth", pg)
	hf, _ := r.callF(ctx, "FPDF_GetPageHeight", pg)
	scale := float64(dpi) / 72.0
	w := int(math.Round(wf * scale))
	h := int(math.Round(hf * scale))
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("pdfium: 页面尺寸异常(%dx%d)", w, h)
	}
	if int64(w)*int64(h) > maxPixels {
		return nil, fmt.Errorf("pdfium: 目标位图过大(%dx%d;可降 dpi)", w, h)
	}
	bmp, err := r.call(ctx, "FPDFBitmap_CreateEx", uint64(w), uint64(h), bitmapBGRA, 0, uint64(w*4))
	if err != nil {
		return nil, err
	}
	if bmp == 0 {
		return nil, errors.New("pdfium: 创建位图失败")
	}
	if fn := r.fn["FPDFBitmap_Destroy"]; fn != nil {
		defer func() { _, _ = fn.Call(ctx, bmp) }()
	}
	if fn := r.fn["FPDFBitmap_FillRect"]; fn != nil {
		_, _ = fn.Call(ctx, bmp, 0, 0, uint64(w), uint64(h), 0xFFFFFFFF)
	}
	if _, err := r.call(ctx, "FPDF_RenderPageBitmap", bmp, pg, 0, 0, uint64(w), uint64(h), 0, 0); err != nil {
		return nil, fmt.Errorf("pdfium: 渲染失败: %w", err)
	}
	bufPtr, err := r.call(ctx, "FPDFBitmap_GetBuffer", bmp)
	if err != nil {
		return nil, err
	}
	raw, ok := r.mem.Read(uint32(bufPtr), uint32(w*h*4))
	if !ok {
		return nil, errors.New("pdfium: 读取位图缓冲失败")
	}
	return bgraToRGBA(raw, w, h), nil
}

// bgraToRGBA BGRA(自底向上=false,pdfium 输出自上而下)→ image.RGBA。
func bgraToRGBA(raw []byte, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i+3 < len(raw); i += 4 {
		p := i / 4
		a := raw[i+3]
		if a == 0 { // pdfium 未填充区域视为不透明白(与 pdftoppm 输出一致)
			a = 0xFF
			img.SetRGBA(p%w, p/w, color.RGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF})
			continue
		}
		img.SetRGBA(p%w, p/w, color.RGBA{R: raw[i+2], G: raw[i+1], B: raw[i], A: a})
	}
	return img
}

// call 调用导出函数(返回首个 i32/i64 结果;无结果返回 0)。
func (r *Renderer) call(ctx context.Context, name string, args ...uint64) (uint64, error) {
	fn, ok := r.fn[name]
	if !ok {
		return 0, fmt.Errorf("pdfium: 导出缺失 %s", name)
	}
	res, err := fn.Call(ctx, args...)
	if err != nil {
		return 0, fmt.Errorf("pdfium: %s 调用失败: %w", name, err)
	}
	if len(res) == 0 {
		return 0, nil
	}
	return res[0], nil
}

// callF 调用返回 f64 的导出函数。
func (r *Renderer) callF(ctx context.Context, name string, args ...uint64) (float64, error) {
	v, err := r.call(ctx, name, args...)
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(v), nil
}

// calloc malloc(失败显式报错)。
func (r *Renderer) calloc(ctx context.Context, n uint64) (uint64, error) {
	p, err := r.call(ctx, "malloc", n)
	if err != nil {
		return 0, err
	}
	if p == 0 {
		return 0, errors.New("pdfium: wasm 内存分配失败")
	}
	return p, nil
}

// free free(best-effort)。
func (r *Renderer) free(ctx context.Context, p uint64) {
	if fn := r.fn["free"]; fn != nil && p != 0 {
		_, _ = fn.Call(ctx, p)
	}
}

// lastError FPDF_GetLastError(无导出返回 0)。
func (r *Renderer) lastError(ctx context.Context) uint64 {
	v, _ := r.call(ctx, "FPDF_GetLastError")
	return v
}

// stub 宿主导入实现(按名字给语义;其余零值返回)。
func (r *Renderer) stub(name string, stack []uint64, results []api.ValueType) {
	zero := func() {
		for i := range results {
			stack[i] = 0
		}
	}
	i32 := func(v int32) { stack[0] = uint64(uint32(v)) }
	switch {
	case strings.HasPrefix(name, "invoke_"): // JS 函数指针 trampoline:无法忠实实现 → 空实现 + 计数
		atomic.AddInt64(&r.invokeCalls, 1)
		zero()
	case strings.HasPrefix(name, "__syscall"): // 文件系统类:返回 -ENOSYS(不可返回 0)
		atomic.AddInt64(&r.syscalls, 1)
		i32(-38) // -ENOSYS
	case name == "emscripten_resize_heap":
		req := stack[0]
		cur := uint64(r.mem.Size())
		if req <= cur {
			i32(1)
			return
		}
		if _, ok := r.mem.Grow(uint32((req - cur + 65535) / 65536)); ok {
			i32(1)
			return
		}
		i32(0)
	case name == "emscripten_notify_memory_growth":
		zero()
	case name == "_emscripten_memcpy_js":
		dst, src, n := uint32(stack[0]), uint32(stack[1]), uint32(stack[2])
		if b, ok := r.mem.Read(src, n); ok {
			r.mem.Write(dst, b)
		}
	case name == "emscripten_date_now":
		stack[0] = math.Float64bits(float64(time.Now().UnixMilli()))
	case name == "clock_time_get":
		// (clockid i32, precision i64, out_ptr i32) → 写纳秒时间戳
		out := uint32(stack[2])
		now := uint64(time.Now().UnixNano())
		r.mem.Write(out, []byte{byte(now), byte(now >> 8), byte(now >> 16), byte(now >> 24),
			byte(now >> 32), byte(now >> 40), byte(now >> 48), byte(now >> 56)})
		i32(0)
	case name == "fd_write":
		fd, iovs, n, nwritten := uint32(stack[0]), uint32(stack[1]), uint32(stack[2]), uint32(stack[3])
		total := uint32(0)
		for k := uint32(0); k < n; k++ {
			ent, ok := r.mem.Read(iovs+k*8, 8)
			if !ok {
				break
			}
			ptr := uint32(ent[0]) | uint32(ent[1])<<8 | uint32(ent[2])<<16 | uint32(ent[3])<<24
			ln := uint32(ent[4]) | uint32(ent[5])<<8 | uint32(ent[6])<<16 | uint32(ent[7])<<24
			if b, ok := r.mem.Read(ptr, ln); ok {
				total += ln
				if fd == 2 && r.diag.Len() < 4096 {
					r.diag.Write(b)
				}
			}
		}
		r.mem.Write(nwritten, []byte{byte(total), byte(total >> 8), byte(total >> 16), byte(total >> 24)})
		i32(0)
	case name == "fd_read" || name == "fd_seek" || name == "fd_close" || name == "fd_sync" || name == "environ_get":
		if name == "fd_read" && len(stack) > 3 { // 写 nread=0,避免误判读到数据
			r.mem.Write(uint32(stack[3]), []byte{0, 0, 0, 0})
		}
		zero()
	case name == "environ_sizes_get":
		r.mem.Write(uint32(stack[0]), []byte{0, 0, 0, 0})
		r.mem.Write(uint32(stack[1]), []byte{0, 0, 0, 0})
		zero()
	case name == "_abort_js" || name == "__assert_fail" || name == "_emscripten_throw_longjmp":
		atomic.AddInt64(&r.aborts, 1)
		zero()
	default:
		zero()
	}
}
