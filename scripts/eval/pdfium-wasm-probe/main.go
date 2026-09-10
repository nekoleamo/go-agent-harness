// pdfium.wasm × wazero 可行性探针(SELF-1 前置实测工具;评测件,不参与主模块构建——自带 go.mod)。
//
// 目标:证明/证伪「纯 Go 运行时(wazero)加载 pdfium.wasm 并可完成 初始化 → 载入 PDF → 渲染位图」,
// 并测出 wasm 线性内存占用与各阶段耗时。
// 用法见同目录 README.md(结论与实测数据也记录在 DESIGN.md §14.1「SELF-1 前置实测」)。
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

var (
	invokeCalls    int64
	syscallCalls   int64
	abortCalls     int64
	resizeCalls    int64
	resizeFailures int64
	memcpyCalls    int64
	dateCalls      int64
	wasmMem        api.Memory
)

func main() {
	wasmPath := flag.String("wasm", "", "pdfium.wasm 路径")
	initName := flag.String("init", "PDFiumExt_Init", "初始化导出名(PDFiumExt_Init / PDFium_Init)")
	pdfPath := flag.String("pdf", "probe.pdf", "探针 PDF")
	scale := flag.Int("scale", 1, "位图缩放(1=72dpi,2≈144dpi)")
	maxPages := flag.Int("pages", 1, "渲染页数")
	pngOut := flag.String("png", "", "首页渲染结果写出 PNG(用于与 pdftoppm 像素对照)")
	diffRef := flag.String("diff", "", "与参考 PNG(如 pdftoppm 产出)做像素对照")
	diffImg := flag.String("diffimg", "", "把差异像素标红后写出对照图")
	flag.Parse()

	wasmBytes, err := os.ReadFile(*wasmPath)
	if err != nil {
		panic(err)
	}
	pdfBytes, err := os.ReadFile(*pdfPath)
	if err != nil {
		panic(err)
	}
	fmt.Printf("wasm=%.2f MiB pdf=%d bytes init=%s\n", float64(len(wasmBytes))/1048576, len(pdfBytes), *initName)

	ctx := context.Background()
	var ms0 runtime.MemStats
	runtime.ReadMemStats(&ms0)

	// 1) 编译
	t0 := time.Now()
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)
	compiled, err := r.CompileModule(ctx, wasmBytes)
	if err != nil {
		fmt.Println("FAIL 编译:", err)
		return
	}
	fmt.Printf("compile(cold)=%.0fms\n", time.Since(t0).Seconds()*1000)
	{
		cache := wazero.NewCompilationCache()
		defer cache.Close(ctx)
		r2 := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCompilationCache(cache))
		defer r2.Close(ctx)
		if _, err := r2.CompileModule(ctx, wasmBytes); err == nil {
			ta := time.Now()
			_, _ = r2.CompileModule(ctx, wasmBytes)
			fmt.Printf("compile(cached)=%.0fms\n", time.Since(ta).Seconds()*1000)
		}
	}

	// 2) 宿主 stub:env + wasi_snapshot_preview1 两个命名空间,签名一律取自模块自身导入定义
	//   (Emscripten 产物的 WASI 偏移量为 32 位变体,不能直接复用 wazero 的标准 WASI 宿主)
	builders := map[string]wazero.HostModuleBuilder{}
	counts := map[string]int{}
	for _, f := range compiled.ImportedFunctions() {
		mod, name, ok := f.Import()
		if !ok || (mod != "env" && mod != "wasi_snapshot_preview1") {
			continue
		}
		b, ok := builders[mod]
		if !ok {
			b = r.NewHostModuleBuilder(mod)
			builders[mod] = b
		}
		counts[mod]++
		fname, fresults := name, f.ResultTypes()
		b.NewFunctionBuilder().
			WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, _ api.Module, stack []uint64) {
				stubEnv(ctx, fname, stack, fresults)
			}), f.ParamTypes(), fresults).
			Export(fname)
	}
	fmt.Printf("host imports=%v\n", counts)
	// 若模块导入 memory/table/global(旧式 asm.js 产物),此处显式报告不可行
	for _, m := range compiled.ImportedMemories() {
		mod, name, _ := m.Import()
		fmt.Printf("FAIL 模块导入 memory %s.%s(需宿主提供内存,自包含档不适用)\n", mod, name)
		return
	}
	for mod, b := range builders {
		if _, err := b.Instantiate(ctx); err != nil {
			fmt.Printf("FAIL %s 宿主实例化: %v\n", mod, err)
			return
		}
	}

	// 3) 实例化 + 初始化
	t1 := time.Now()
	inst, err := r.InstantiateModule(ctx, compiled, wazero.NewModuleConfig())
	if err != nil {
		fmt.Println("FAIL 实例化:", err)
		return
	}
	wasmMem = inst.Memory()
	ctor := inst.ExportedFunction("__wasm_call_ctors")
	if ctor != nil {
		if _, err := ctor.Call(ctx); err != nil {
			fmt.Println("FAIL __wasm_call_ctors:", err)
			return
		}
	}
	initFn := inst.ExportedFunction(*initName)
	if initFn == nil {
		fmt.Println("FAIL 无导出:", *initName)
		return
	}
	res, err := initFn.Call(ctx)
	if err != nil {
		fmt.Println("FAIL 初始化:", err)
		return
	}
	okInit := true
	if len(res) > 0 {
		okInit = res[0] != 0
	}
	fmt.Printf("init=%.0fms ok=%v wasmMemAfterInit=%.2f MiB\n",
		time.Since(t1).Seconds()*1000, okInit, float64(wasmMem.Size())/1048576)

	// 4) 载入 PDF(写入 wasm 内存)
	malloc := inst.ExportedFunction("malloc")
	loadMem := inst.ExportedFunction("FPDF_LoadMemDocument")
	getPages := inst.ExportedFunction("FPDF_GetPageCount")
	loadPage := inst.ExportedFunction("FPDF_LoadPage")
	if malloc == nil || loadMem == nil || getPages == nil || loadPage == nil {
		fmt.Println("FAIL 缺少 malloc/LoadMemDocument/GetPageCount/LoadPage 导出")
		return
	}
	p, err := malloc.Call(ctx, uint64(len(pdfBytes)))
	if err != nil {
		fmt.Println("FAIL malloc:", err)
		return
	}
	if !wasmMem.Write(uint32(p[0]), pdfBytes) {
		fmt.Println("FAIL 写 pdfBytes 到 wasm 内存")
		return
	}
	t2 := time.Now()
	doc, err := loadMem.Call(ctx, p[0], uint64(len(pdfBytes)), 0)
	if err != nil || doc[0] == 0 {
		fmt.Printf("FAIL LoadMemDocument: err=%v handle=%d\n", err, doc[0])
		return
	}
	pages, _ := getPages.Call(ctx, doc[0])
	fmt.Printf("load=%.0fms doc=%d pages=%d wasmMem=%.2f MiB\n",
		time.Since(t2).Seconds()*1000, doc[0], pages[0], float64(wasmMem.Size())/1048576)

	// 5) 渲染位图(BGRA,经 FPDFBitmap_CreateEx + FPDF_RenderPageBitmap)
	page, err := loadPage.Call(ctx, doc[0], 0)
	if err != nil || page[0] == 0 {
		fmt.Println("FAIL LoadPage:", err)
		return
	}
	wf, _ := inst.ExportedFunction("FPDF_GetPageWidth").Call(ctx, page[0])
	hf, _ := inst.ExportedFunction("FPDF_GetPageHeight").Call(ctx, page[0])
	w := int(math.Round(math.Float64frombits(wf[0])))
	h := int(math.Round(math.Float64frombits(hf[0])))
	createBmp, _ := inst.ExportedFunction("FPDFBitmap_CreateEx").Call(ctx, uint64(w), uint64(h), 4 /*BGRA*/, 0, uint64(w*4))
	bmp := createBmp[0]
	fill := inst.ExportedFunction("FPDFBitmap_FillRect")
	if fill != nil {
		_, _ = fill.Call(ctx, bmp, 0, 0, uint64(w), uint64(h), 0xFFFFFFFF)
	}
	// 多页 + 缩放渲染(测耗时/内存规模)
	render := inst.ExportedFunction("FPDF_RenderPageBitmap")
	closePage := inst.ExportedFunction("FPDF_ClosePage")
	t3 := time.Now()
	rendered := 0
	nPages := int(pages[0])
	if *maxPages < nPages {
		nPages = *maxPages
	}
	for pi := 0; pi < nPages; pi++ {
		pg, err := loadPage.Call(ctx, doc[0], uint64(pi))
		if err != nil || pg[0] == 0 {
			fmt.Printf("FAIL LoadPage(%d): %v\n", pi, err)
			return
		}
		sw, _ := inst.ExportedFunction("FPDF_GetPageWidth").Call(ctx, pg[0])
		sh, _ := inst.ExportedFunction("FPDF_GetPageHeight").Call(ctx, pg[0])
		pw := int(math.Round(math.Float64frombits(sw[0]))) * *scale
		ph := int(math.Round(math.Float64frombits(sh[0]))) * *scale
		bm, _ := inst.ExportedFunction("FPDFBitmap_CreateEx").Call(ctx, uint64(pw), uint64(ph), 4, 0, uint64(pw*4))
		bmp = bm[0]
		if fill != nil {
			_, _ = fill.Call(ctx, bmp, 0, 0, uint64(pw), uint64(ph), 0xFFFFFFFF)
		}
		if _, err := render.Call(ctx, bmp, pg[0], 0, 0, uint64(pw), uint64(ph), 0, 0); err != nil {
			fmt.Println("FAIL RenderPageBitmap:", err)
			return
		}
		if pi == 0 {
			w, h = pw, ph
		}
		rendered++
		if closePage != nil {
			_, _ = closePage.Call(ctx, pg[0])
		}
	}
	bufPtr, _ := inst.ExportedFunction("FPDFBitmap_GetBuffer").Call(ctx, bmp)
	buf, ok := wasmMem.Read(uint32(bufPtr[0]), uint32(w*h*4))
	if !ok {
		fmt.Println("FAIL 读位图缓冲")
		return
	}
	nonWhite := 0
	for i := 0; i+3 < len(buf); i += 4 {
		if buf[i] != 0xFF || buf[i+1] != 0xFF || buf[i+2] != 0xFF {
			nonWhite++
		}
	}
	fmt.Printf("render=%.0fms pages=%d size=%dx%d nonWhitePx=%d(%.2f%%)\n",
		time.Since(t3).Seconds()*1000, rendered, w, h, nonWhite, 100*float64(nonWhite)/float64(w*h))

	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i+3 < len(buf); i += 4 {
		rgba.Set(i/4%w, i/4/w, color.RGBA{R: buf[i+2], G: buf[i+1], B: buf[i], A: buf[i+3]})
	}
	if *pngOut != "" {
		f, err := os.Create(*pngOut)
		if err == nil {
			_ = png.Encode(f, rgba)
			f.Close()
			fmt.Printf("png=%s\n", *pngOut)
		}
	}
	if *diffRef != "" {
		comparePNG(*diffRef, rgba, *diffImg)
	}

	var ms1 runtime.MemStats
	runtime.ReadMemStats(&ms1)
	fmt.Printf("goHeap=%.1f→%.1f MiB goSys=%.1f MiB | wasmMem=%.2f MiB | invoke*=%d syscalls=%d abort=%d resize=%d(miss=%d) memcpyJs=%d dateNow=%d\n",
		float64(ms0.HeapAlloc)/1048576, float64(ms1.HeapAlloc)/1048576, float64(ms1.Sys)/1048576,
		float64(wasmMem.Size())/1048576,
		atomic.LoadInt64(&invokeCalls), atomic.LoadInt64(&syscallCalls), atomic.LoadInt64(&abortCalls),
		atomic.LoadInt64(&resizeCalls), atomic.LoadInt64(&resizeFailures), atomic.LoadInt64(&memcpyCalls),
		atomic.LoadInt64(&dateCalls))
	if nonWhite == 0 {
		fmt.Println("WARN 渲染结果全白(可能未真正绘制)")
	} else {
		fmt.Println("PASS pdfium.wasm 在 wazero 上完成 初始化→载入→渲染")
	}
}

// stubEnv env 命名空间导入的最小实现(按名字给语义;其余零值返回)。
func stubEnv(ctx context.Context, name string, stack []uint64, results []api.ValueType) {
	zero := func() {
		for i := range results {
			stack[i] = 0
		}
	}
	switch {
	case len(name) > 7 && name[:7] == "invoke_": // Emscripten JS 侧函数指针 trampoline
		atomic.AddInt64(&invokeCalls, 1)
		zero()
	case len(name) > 9 && name[:9] == "__syscall": // 文件系统类:纯内存渲染不应触达
		atomic.AddInt64(&syscallCalls, 1)
		zero()
	case name == "emscripten_resize_heap":
		atomic.AddInt64(&resizeCalls, 1)
		req := stack[0]
		cur := uint64(wasmMem.Size())
		if req <= cur {
			stack[0] = 1
			return
		}
		delta := req - cur
		if _, ok := wasmMem.Grow(uint32((delta + 65535) / 65536)); ok {
			stack[0] = 1
			return
		}
		atomic.AddInt64(&resizeFailures, 1)
		stack[0] = 0
	case name == "_emscripten_memcpy_js":
		atomic.AddInt64(&memcpyCalls, 1)
		dst, src, n := uint32(stack[0]), uint32(stack[1]), uint32(stack[2])
		if data, ok := wasmMem.Read(src, n); ok {
			wasmMem.Write(dst, data)
		}
	case name == "emscripten_date_now":
		atomic.AddInt64(&dateCalls, 1)
		stack[0] = math.Float64bits(float64(time.Now().UnixMilli()))
	case name == "_abort_js" || name == "__assert_fail" || name == "_emscripten_throw_longjmp":
		atomic.AddInt64(&abortCalls, 1)
		zero()
	case name == "fd_write": // 把 pdfium/Emscripten 的诊断写到 stderr(便于观察)
		fd, iovs, iovsLen, nwritten := uint32(stack[0]), uint32(stack[1]), uint32(stack[2]), uint32(stack[3])
		total := uint32(0)
		for i := uint32(0); i < iovsLen; i++ {
			entry, ok := wasmMem.Read(iovs+i*8, 8)
			if !ok {
				break
			}
			ptr := uint32(entry[0]) | uint32(entry[1])<<8 | uint32(entry[2])<<16 | uint32(entry[3])<<24
			n := uint32(entry[4]) | uint32(entry[5])<<8 | uint32(entry[6])<<16 | uint32(entry[7])<<24
			if data, ok := wasmMem.Read(ptr, n); ok && len(data) > 0 {
				out := os.Stderr
				if fd == 1 {
					out = os.Stdout
				}
				out.Write(data)
				total += n
			}
		}
		wasmMem.Write(nwritten, []byte{byte(total), byte(total >> 8), byte(total >> 16), byte(total >> 24)})
		stack[0] = 0
	case name == "environ_sizes_get":
		wasmMem.Write(uint32(stack[0]), make([]byte, 4))
		wasmMem.Write(uint32(stack[1]), make([]byte, 4))
		stack[0] = 0
	case name == "fd_read" || name == "fd_seek" || name == "fd_close" || name == "fd_sync" || name == "environ_get":
		stack[0] = 0
	default:
		zero()
	}
}

// comparePNG 与参考实现(poppler pdftoppm)逐像素对照:尺寸一致 + 差异像素占比/最大通道差。
func comparePNG(refPath string, got *image.RGBA, diffImgPath string) {
	f, err := os.Open(refPath)
	if err != nil {
		fmt.Println("diff: 打不开参考图:", err)
		return
	}
	defer f.Close()
	ref, err := png.Decode(f)
	if err != nil {
		fmt.Println("diff: 参考图解码失败:", err)
		return
	}
	rb := ref.Bounds()
	gb := got.Bounds()
	if rb.Dx() != gb.Dx() || rb.Dy() != gb.Dy() {
		fmt.Printf("diff: 尺寸不一致 ref=%dx%d got=%dx%d\n", rb.Dx(), rb.Dy(), gb.Dx(), gb.Dy())
		return
	}
	overlay := image.NewRGBA(image.Rect(0, 0, rb.Dx(), rb.Dy()))
	diffPx, maxDelta, sumDelta := 0, 0, 0
	total := rb.Dx() * rb.Dy()
	refInk, gotInk := 0, 0
	minX, minY, maxX, maxY := rb.Dx(), rb.Dy(), -1, -1
	for y := 0; y < rb.Dy(); y++ {
		for x := 0; x < rb.Dx(); x++ {
			r1, g1, b1, _ := ref.At(rb.Min.X+x, rb.Min.Y+y).RGBA()
			r2, g2, b2, _ := got.At(gb.Min.X+x, gb.Min.Y+y).RGBA()
			d := absDiff(int(r1>>8), int(r2>>8))
			d = max(d, absDiff(int(g1>>8), int(g2>>8)))
			d = max(d, absDiff(int(b1>>8), int(b2>>8)))
			if d > 8 { // 容差:抗锯齿/色彩管理差异
				diffPx++
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
			}
			gray := uint8(int(r1>>8)/3 + int(g1>>8)/3 + int(b1>>8)/3)
			if d > 8 {
				overlay.Set(x, y, color.RGBA{R: 255, A: 255})
			} else {
				overlay.Set(x, y, color.RGBA{R: gray, G: gray, B: gray, A: 255})
			}
			if int(r1>>8) < 250 || int(g1>>8) < 250 || int(b1>>8) < 250 {
				refInk++
			}
			if int(r2>>8) < 250 || int(g2>>8) < 250 || int(b2>>8) < 250 {
				gotInk++
			}
			if d > maxDelta {
				maxDelta = d
			}
			sumDelta += d
		}
	}
	if diffImgPath != "" {
		if f, err := os.Create(diffImgPath); err == nil {
			_ = png.Encode(f, overlay)
			f.Close()
			fmt.Printf("diffimg=%s(灰=参考,红=差异>8)\n", diffImgPath)
		}
	}
	fmt.Printf("diff: vs %s => differingPx=%d(%.2f%%) maxChannelDelta=%d meanDelta=%.2f | ink ref=%d got=%d(%.1f%%) | diffBBox=[%d,%d..%d,%d]\n",
		refPath, diffPx, 100*float64(diffPx)/float64(total), maxDelta, float64(sumDelta)/float64(total),
		refInk, gotInk, 100*float64(gotInk-refInk)/float64(refInk+1), minX, minY, maxX, maxY)
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
