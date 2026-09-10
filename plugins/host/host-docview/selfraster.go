// 自包含光栅后端(SELF-1 路线 B;DESIGN §14.1「SELF-1 可能性分析(二轮)」)。
//
// 定位:**pdftoppm 优先,本后端兜底** —— 未安装/未启用 poppler 的部署仍能光栅化 PDF:
//   - wasm 取件:本地路径(data.pdfium_wasm_path / GAH_PDFIUM_WASM)→ $GAH_HOME/cache/pdfium/
//     缓存 → 下载(默认 pdfium-lib 8046d 的 STANDALONE_WASM 构建,sha256 固定);
//   - 产物落 `$GAH_HOME/cache/doc/raster/`(与 RST-1 同一目录/命名,同源同页同 dpi 复用);
//   - `invoke_*`(Emscripten JS trampoline,wazero 无法忠实实现)计数 > 0 时**显式告警**
//     (实测毒化返回值逐像素相同,但不可证明对所有文档安全)。
package hostdocview

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/plugins/host/host-docview/pdfium"
)

// selfRaster 自包含光栅后端(懒加载 wasm 与运行时)。
type selfRaster struct {
	enabled bool
	src     pdfium.Source
	cache   string // 光栅产物目录($GAH_HOME/cache/doc/raster)

	mu sync.Mutex
	r  *pdfium.Renderer
}

// usable 是否启用。
func (s *selfRaster) usable() bool { return s != nil && s.enabled }

// renderer 懒加载(编译 + 实例化 + 库初始化;wasm 取件含下载)。
func (s *selfRaster) renderer(ctx context.Context) (*pdfium.Renderer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.r != nil {
		return s.r, nil
	}
	wasm, err := s.src.Ensure(ctx)
	if err != nil {
		return nil, err
	}
	r, err := pdfium.New(ctx, wasm)
	if err != nil {
		return nil, err
	}
	s.r = r
	return r, nil
}

// close 释放运行时(best-effort;插件卸载路径)。
func (s *selfRaster) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.r != nil {
		_ = s.r.Close(context.Background())
		s.r = nil
	}
}

// renderPDF 自包含光栅化:PDF 指定页 → PNG 落缓存;返回 (pngPath, 告警文本, err)。
// 缓存命中直接返回(不重复渲染);产物超 RST-1 上限 → 报错。
func (s *selfRaster) renderPDF(ctx context.Context, abs string, fileSize, modUnix int64, page, dpi int) (string, string, error) {
	dir := s.cache
	if dir == "" {
		return "", "", fmt.Errorf("自包含光栅缓存目录未初始化")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("创建光栅缓存目录失败: %w", err)
	}
	out := filepath.Join(dir, rasterCacheName(abs, fileSize, modUnix, page, dpi))
	if fi, err := os.Stat(out); err == nil && fi.Size() > 0 {
		return out, "", nil // 缓存命中(同源同页同 dpi)
	}
	pdfBytes, err := os.ReadFile(abs)
	if err != nil {
		return "", "", fmt.Errorf("读取 PDF 失败: %w", err)
	}
	r, err := s.renderer(ctx)
	if err != nil {
		return "", "", err
	}
	before := r.InvokeCalls()
	img, err := r.RenderPage(ctx, pdfBytes, page, dpi)
	if err != nil {
		return "", "", err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img.(*image.RGBA)); err != nil {
		return "", "", fmt.Errorf("PNG 编码失败: %w", err)
	}
	if buf.Len() > rasterMaxBytes {
		return "", "", fmt.Errorf("光栅产物过大(%d 字节 > %d;可降 dpi 或改页)", buf.Len(), int64(rasterMaxBytes))
	}
	warn := ""
	if after := r.InvokeCalls(); after > before {
		warn = fmt.Sprintf("该文档触发 pdfium JS-glue 回调路径(%d 次),自包含光栅结果未经交叉校验;建议安装 poppler pdftoppm 复核", after-before)
	}
	tmp := out + fmt.Sprintf(".tmp-%d", time.Now().UnixNano())
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return "", "", fmt.Errorf("光栅产物落盘失败: %w", err)
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		return "", "", fmt.Errorf("光栅产物落盘失败: %w", err)
	}
	return out, warn, nil
}
