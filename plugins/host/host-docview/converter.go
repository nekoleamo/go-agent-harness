// 外部转换器探测与调用(D6-2 / E2):把旧二进制 Office(.doc/.xls/.ppt)交给本机
// LibreOffice 转成 PDF,再走 D4 的 PDF 抽取器 —— 高保真预览档,**默认关闭**。
//
// 纪律(见 docs/DOC_PREVIEW_PLAN.md §9 D6-2 与 AGENTS「便携纪律」):
//   - 默认不启用(基线不引入部署面/依赖);`data.external_converters: true` 才启用;
//   - 探测只读 PATH(soffice/libreoffice/pdftoppm),探测不到 = 显式提示,不静默降级;
//   - **唯一落盘点 = $GAH_HOME/cache/doc/**(经 cacheDirPath;GAH_HOME 空仅出现在嵌入/单测,
//     退回 TempDir,绝不落 cwd/系统根);产物为可复用的转换缓存(超过保留期按龄清理);
//   - 超时(默认 30s)与产物大小上限;失败显式带 stderr 片段,不假装成功。
package hostdocview

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

const (
	converterTimeout = 30 * time.Second
	converterMaxOut  = 200 << 20 // 转换产物上限(超过视为异常,拒绝)
	converterKeepFor = 7 * 24 * time.Hour
	converterLogTail = 400 // 失败时带入的 stderr 尾巴长度

	// RST-1 光栅预算:DPI 安全区间 + 单页 PNG 上限。
	rasterMinDPI   = 36
	rasterMaxDPI   = 300
	rasterMaxBytes = 8 << 20
)

// legacyOfficeExts 需要外部转换器的旧二进制 Office(CFB)扩展名。
var legacyOfficeExts = map[string]bool{"doc": true, "xls": true, "ppt": true}

// converter 外部转换器探测结果与调用封装(run 可注入以便离线单测)。
type converter struct {
	enabled  bool
	rasterOn bool   // D6-1a/RST-1:外部 pdftoppm 光栅开关(data.external_raster,默认关)
	soffice  string // 命中路径(空 = 未安装)
	pdftoppm string // PDF → PNG 光栅(RST-1;探测命中才可用)
	cacheDir string
	run      func(ctx context.Context, bin string, args ...string) error
}

// cacheDirPath 转换缓存目录(唯一落盘点;$GAH_HOME 空 = TempDir 兜底,嵌入/单测场景)。
func cacheDirPath(home string) string {
	if home == "" {
		return filepath.Join(os.TempDir(), "gah-doc-cache")
	}
	return filepath.Join(home, "cache", "doc")
}

// newConverter 探测 PATH 可用转换器(enabled 仅决定是否允许调用,不影响探测)。
func newConverter(enabled bool, home string) converter {
	return converter{
		enabled:  enabled,
		soffice:  findExecutable("soffice", "libreoffice"),
		pdftoppm: findExecutable("pdftoppm"),
		cacheDir: cacheDirPath(home),
		run:      runExternal,
	}
}

// findExecutable 在 PATH 中找第一个可用可执行文件(找不到返回空)。
func findExecutable(names ...string) string {
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil && p != "" {
			return p
		}
	}
	return ""
}

// available 是否具备 Office→PDF 转换能力。
func (c converter) available() bool { return c.soffice != "" }

// usable 是否可以真正执行转换(启用 + 工具在)。
func (c converter) usable() bool { return c.enabled && c.available() }

// rasterUsable 是否可执行光栅(开关开 + pdftoppm 在)。
func (c converter) rasterUsable() bool { return c.rasterOn && c.pdftoppm != "" }

// clampDPI 把请求 DPI 收进安全区间(0 = 默认 96)。
func clampDPI(dpi int) int {
	if dpi <= 0 {
		return 96
	}
	if dpi < rasterMinDPI {
		return rasterMinDPI
	}
	if dpi > rasterMaxDPI {
		return rasterMaxDPI
	}
	return dpi
}

// rasterPDF 把 PDF 指定页光栅化为 PNG(RST-1):pdftoppm -png -singlefile;
// 产物落 cache/doc/raster/(便携纪律;同源同页同 dpi 复用缓存),超时/超限显式报错。
// 返回 PNG 路径(位于缓存目录)。
func (c converter) rasterPDF(ctx context.Context, abs string, fileSize int64, modUnix int64, page, dpi int) (string, error) {
	if !c.rasterUsable() {
		return "", fmt.Errorf("外部光栅未启用或未安装 pdftoppm")
	}
	dir := filepath.Join(c.cacheDir, "raster")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建光栅缓存目录失败: %w", err)
	}
	c.pruneDir(dir)
	out := filepath.Join(dir, rasterCacheName(abs, fileSize, modUnix, page, dpi))
	if fi, err := os.Stat(out); err == nil && fi.Size() > 0 {
		return out, nil // 缓存命中(同源同页同 dpi)
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".tmp-%d-%d", os.Getpid(), time.Now().UnixNano()))
	cctx, cancel := context.WithTimeout(ctx, converterTimeout)
	defer cancel()
	// pdftoppm 会追加 .png 后缀(即便给了前缀);先写临时前缀再改名
	if err := c.run(cctx, c.pdftoppm,
		"-png", "-singlefile", "-r", strconv.Itoa(dpi),
		"-f", strconv.Itoa(page), "-l", strconv.Itoa(page), abs, tmp); err != nil {
		return "", fmt.Errorf("pdftoppm 光栅失败: %w", err)
	}
	if err := os.Rename(tmp+".png", out); err != nil {
		return "", fmt.Errorf("光栅产物落盘失败: %w", err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		return "", fmt.Errorf("光栅产物不可读: %w", err)
	}
	if fi.Size() > rasterMaxBytes {
		_ = os.Remove(out)
		return "", fmt.Errorf("光栅产物过大(%d 字节 > %d;可降 dpi 或改页)", fi.Size(), int64(rasterMaxBytes))
	}
	return out, nil
}

// rasterCacheName 光栅缓存名(源路径+size+mtime+页+dpi 派生)。
func rasterCacheName(abs string, size, modUnix int64, page, dpi int) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d|%d|%d", abs, size, modUnix, page, dpi)))
	return fmt.Sprintf("p%d-%dx-%s.png", page, dpi, hex.EncodeToString(h[:8]))
}

// pruneDir 清理目录内超过保留期的文件(best-effort)。
func (c converter) pruneDir(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cut := time.Now().Add(-converterKeepFor)
	for _, e := range ents {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cut) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

// hints 当前探测结果的人类可读描述(供 unsupported 提示/诊断)。
func (c converter) hints() []string {
	out := make([]string, 0, 2)
	if c.soffice != "" {
		out = append(out, "已检测到 soffice("+c.soffice+")")
	} else {
		out = append(out, "未检测到 soffice/libreoffice")
	}
	if c.pdftoppm != "" {
		out = append(out, "已检测到 pdftoppm")
	}
	return out
}

// convertToPDF 用 LibreOffice 把 abs 转为 PDF,返回缓存中的 PDF 路径。
// 产物落在 cacheDir(便携纪律);失败/超时/产物异常均返回显式错误。
func (c converter) convertToPDF(ctx context.Context, abs string, fi os.FileInfo) (string, error) {
	if !c.usable() {
		return "", fmt.Errorf("外部转换器未启用或未安装")
	}
	if err := os.MkdirAll(c.cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("创建转换缓存目录失败: %w", err)
	}
	c.pruneCache()
	tmp, err := os.MkdirTemp(c.cacheDir, "conv-")
	if err != nil {
		return "", fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	cctx, cancel := context.WithTimeout(ctx, converterTimeout)
	defer cancel()
	if err := c.run(cctx, c.soffice,
		"--headless", "--norestore", "--convert-to", "pdf", "--outdir", tmp, abs); err != nil {
		return "", fmt.Errorf("soffice 转换失败: %w", err)
	}
	// 产物名 = 源文件名换 .pdf(soffice 命名约定);兼容大小写差异
	base := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs)) + ".pdf"
	produced := filepath.Join(tmp, base)
	if _, err := os.Stat(produced); err != nil {
		// 兜底:目录内唯一 PDF
		if e, _ := os.ReadDir(tmp); len(e) == 1 {
			produced = filepath.Join(tmp, e[0].Name())
		}
		_ = err
	}
	pfi, err := os.Stat(produced)
	if err != nil {
		return "", fmt.Errorf("转换未产出 PDF(检查 LibreOffice 是否支持该格式): %w", err)
	}
	if pfi.Size() <= 0 {
		return "", fmt.Errorf("转换产出为空文件")
	}
	if pfi.Size() > converterMaxOut {
		return "", fmt.Errorf("转换产物过大(%d 字节 > %d)", pfi.Size(), int64(converterMaxOut))
	}
	dst := filepath.Join(c.cacheDir, converterCacheName(abs, fi))
	if err := os.Rename(produced, dst); err != nil {
		return "", fmt.Errorf("落盘转换产物失败: %w", err)
	}
	return dst, nil
}

// converterCacheName 转换产物名(源路径+大小+mtime 派生 → 同源文件复用缓存,不改动即不重转)。
func converterCacheName(abs string, fi os.FileInfo) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d", abs, fi.Size(), fi.ModTime().UnixNano())))
	base := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs)))
	if len(base) > 48 {
		base = base[:48]
	}
	return base + "-" + hex.EncodeToString(h[:8]) + ".pdf"
}

// pruneCache 清理超过保留期的转换缓存(best-effort;失败不报错)。
func (c converter) pruneCache() {
	ents, err := os.ReadDir(c.cacheDir)
	if err != nil {
		return
	}
	cut := time.Now().Add(-converterKeepFor)
	for _, e := range ents {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cut) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(c.cacheDir, e.Name()))
	}
}

// runExternal 默认执行器:捕获输出,失败时把 stderr/stdout 尾巴带进错误。
func runExternal(ctx context.Context, bin string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	// 凭据隔离:外部转换器(libreoffice/pdftoppm)只需基础运行环境,不继承宿主配置与凭据
	cmd.Env = sdk.SanitizedChildEnv()
	// 独立进程组 + WaitDelay:转换器(shell 脚本包装)派生的孙进程持住 stdout 管道时
	// Run 会永久阻塞(预览请求挂死、ctx 超时也解不开);组杀 + 超时兜底。
	setProcessGroup(cmd)
	cmd.WaitDelay = 3 * time.Second
	var buf strings.Builder
	// 输出封顶:损坏文档可让转换器狂刷日志,只保留尾部一条错误信息所需量
	lim := &limitedBuilder{b: &buf, limit: converterOutputLimit}
	cmd.Stdout = lim
	cmd.Stderr = lim
	err := cmd.Run()
	killProcessGroup(cmd) // 组内残留(孙进程)一并回收;已退出时静默
	if err == nil {
		return nil
	}
	out := strings.TrimSpace(buf.String())
	r := []rune(out)
	if len(r) > converterLogTail {
		out = "…" + string(r[len(r)-converterLogTail:])
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%w(超时 %s)", ctx.Err(), converterTimeout)
	}
	if out == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, out)
}
