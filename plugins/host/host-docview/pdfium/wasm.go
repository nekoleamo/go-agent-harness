// pdfium.wasm 获取(SELF-1 路线 B):本地路径 → GAH_HOME 缓存 → 下载(可选 zip 内取件)+ sha256 校验。
//
// 便携纪律:缓存落 `$GAH_HOME/cache/pdfium/pdfium.wasm`(GAH_HOME 空 → TempDir 兜底,仅单测/嵌入场景);
// 下载为**一次性**动作,之后离线可用;校验失败/损坏 → 删除重取,不静默使用可疑产物。
package pdfium

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultWASMURL 默认取件地址(pdfium-lib 官方 release 的 wasm.zip;内含 pdfium.std.wasm)。
const DefaultWASMURL = "https://github.com/paulocoutinhox/pdfium-lib/releases/download/8046d/wasm.zip"

// DefaultWASMSHA256 默认产物 sha256(pdfium-lib 8046d 的 release/node/pdfium.std.wasm,
// 即 STANDALONE_WASM 构建:宿主面最小,19 个导入、无需 _emscripten_memcpy_js)。
const DefaultWASMSHA256 = "223e608c0980885d8fb31638f1533b6c62dba1803b9595ba9fd34879064ceee0"

// wasmMaxBytes 下载上限(防异常大件)。
const wasmMaxBytes = 64 << 20

// Source wasm 取件配置。
type Source struct {
	Path   string // 显式本地路径(优先;部署可随二进制附带)
	URL    string // 下载地址(空 = DefaultWASMURL)
	SHA256 string // 期望 sha256(空 = DefaultWASMSHA256;显式本地路径不校验)
	Home   string // GAH_HOME(空 → TempDir 兜底)
	HTTP   *http.Client
}

// CachePath 缓存路径($GAH_HOME/cache/pdfium/pdfium.wasm)。
func (s Source) CachePath() string {
	home := s.Home
	if home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, "cache", "pdfium", "pdfium.wasm")
}

// Ensure 返回可用 wasm 字节(必要时下载/解包/校验)。
func (s Source) Ensure(ctx context.Context) ([]byte, error) {
	if p := strings.TrimSpace(s.Path); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("pdfium: 读取指定 wasm 失败: %w", err)
		}
		return b, nil
	}
	want := s.SHA256
	if want == "" {
		want = DefaultWASMSHA256
	}
	cache := s.CachePath()
	if b, err := os.ReadFile(cache); err == nil && sha256Hex(b) == want {
		return b, nil
	} else if err == nil {
		_ = os.Remove(cache) // 校验不符:丢弃重取(不静默使用可疑产物)
	}
	url := s.URL
	if url == "" {
		url = DefaultWASMURL
	}
	raw, err := s.download(ctx, url)
	if err != nil {
		return nil, err
	}
	wasm, err := pickWASM(url, raw)
	if err != nil {
		return nil, err
	}
	if got := sha256Hex(wasm); got != want {
		return nil, fmt.Errorf("pdfium: wasm sha256 不符(期望 %s,实得 %s;可用 data.pdfium_wasm_path 指定本地件)", want[:12], got[:12])
	}
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		return nil, fmt.Errorf("pdfium: 创建缓存目录失败: %w", err)
	}
	tmp := cache + fmt.Sprintf(".tmp-%d", time.Now().UnixNano())
	if err := os.WriteFile(tmp, wasm, 0o644); err != nil {
		return nil, fmt.Errorf("pdfium: 写缓存失败: %w", err)
	}
	if err := os.Rename(tmp, cache); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("pdfium: 落缓存失败: %w", err)
	}
	return wasm, nil
}

// download 取件(限长;非 200 显式报错)。
func (s Source) download(ctx context.Context, url string) ([]byte, error) {
	cli := s.HTTP
	if cli == nil {
		cli = &http.Client{Timeout: 180 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pdfium: 下载失败(%s): %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pdfium: 下载 HTTP %d(%s)", resp.StatusCode, url)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, wasmMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("pdfium: 下载读取失败: %w", err)
	}
	if len(b) > wasmMaxBytes {
		return nil, fmt.Errorf("pdfium: 下载件超上限(%d 字节)", wasmMaxBytes)
	}
	return b, nil
}

// pickWASM 取件解包:zip → 选 std wasm(优先名含 ".std.wasm");否则视为裸 wasm。
func pickWASM(url string, raw []byte) ([]byte, error) {
	if !bytes.HasPrefix(raw, []byte("PK\x03\x04")) {
		if !bytes.HasPrefix(raw, []byte("\x00asm")) {
			return nil, fmt.Errorf("pdfium: 取件既非 zip 也非 wasm(来自 %s)", url)
		}
		return raw, nil
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("pdfium: zip 解析失败: %w", err)
	}
	var fallback *zip.File
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".wasm") {
			continue
		}
		if strings.Contains(f.Name, ".std.wasm") {
			return readZipFile(f)
		}
		if fallback == nil {
			fallback = f
		}
	}
	if fallback == nil {
		return nil, fmt.Errorf("pdfium: zip 内无 .wasm(来自 %s)", url)
	}
	return readZipFile(fallback)
}

// readZipFile 读 zip 条目(限长)。
func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("pdfium: 打开 zip 条目失败: %w", err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, wasmMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("pdfium: 读取 zip 条目失败: %w", err)
	}
	if len(b) > wasmMaxBytes {
		return nil, fmt.Errorf("pdfium: zip 条目超上限")
	}
	return b, nil
}

// sha256Hex 十六进制 sha256。
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
