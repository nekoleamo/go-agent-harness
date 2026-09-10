// 基线抽取器(D0):text / code / binary / image / unsupported。
// markdown / csv / notebook(D1)、docx(D2)、xlsx+pptx(D3)、pdf(D4)、html(D6)在后续切片注册。
package hostdocview

import (
	"context"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"  // 注册 GIF 解码器(尺寸探测)
	_ "image/jpeg" // 注册 JPEG 解码器
	_ "image/png"  // 注册 PNG 解码器
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// displayPath 视图对外路径 = 调用方视角逻辑路径(不回传宿主绝对路径;
// 调用方未给路径时才退回 realpath)。
func displayPath(req sdk.DocRequest, abs string) string {
	if p := strings.TrimSpace(req.Path); p != "" {
		return p
	}
	return abs
}

// baseView 构造带基础元信息的视图。
func baseView(req sdk.DocRequest, abs string, size int64, modTime int64, format sdk.DocFormat) *sdk.DocView {
	v := &sdk.DocView{
		Path:    displayPath(req, abs),
		Name:    filepath.Base(abs),
		Format:  format,
		Size:    size,
		ModTime: timeUnix(modTime),
	}
	return v
}

// readCap 读取至多 max 字节,返回是否发生截断。
func readCap(f *os.File, max int64) ([]byte, bool, error) {
	if max <= 0 {
		max = 1 << 20
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > max {
		return data[:max], true, nil
	}
	return data, false, nil
}

// trimPartialRune 去掉尾部被截断的不完整 UTF-8 序列。
func trimPartialRune(b []byte) []byte {
	for i := 0; i < utf8.UTFMax && i < len(b); i++ {
		if utf8.Valid(b) {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}

// normalizeNewlines CRLF/CR → LF。
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// langFor 依扩展名给代码块语言提示(空 = 无提示)。
func langFor(name string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	switch ext {
	case "yml":
		return "yaml"
	case "jsonc", "json5":
		return "json"
	case "sh", "bash":
		return "bash"
	case "tsx":
		return "tsx"
	case "jsx":
		return "jsx"
	case "mdown", "mkd":
		return "markdown"
	}
	if ext == "" {
		return ""
	}
	return ext
}

// extractTextOrCode 纯文本/代码:整体一个 code 块(> 预算截断 + Truncated)。
func extractTextOrCode(_ context.Context, s *Service, abs string, fi os.FileInfo, req sdk.DocRequest, format sdk.DocFormat) (*sdk.DocView, error) {
	max := req.MaxBytes
	if max <= 0 {
		max = s.budget.MaxPreviewBytes
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, truncated, err := readCap(f, max)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", sdk.ErrDocParse, err)
	}
	if truncated {
		data = trimPartialRune(data)
	}
	v := baseView(req, abs, fi.Size(), fi.ModTime().UnixNano(), format)
	text := normalizeNewlines(string(data))
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "\uFFFD")
		v.Warnings = append(v.Warnings, "内容含非 UTF-8 字节,已替换为 U+FFFD")
	}
	v.Blocks = []sdk.DocBlock{{Kind: sdk.DocBlockCode, Text: text, Lang: langFor(abs)}}
	if truncated {
		v.Truncated = append(v.Truncated, fmt.Sprintf("bytes:%d/%d", max, fi.Size()))
	}
	return v, nil
}

// extractBinary 二进制兜底:信息卡 + 前 4KiB hexdump(绝不崩)。
func extractBinary(_ context.Context, s *Service, abs string, fi os.FileInfo, req sdk.DocRequest, format sdk.DocFormat) (*sdk.DocView, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	head, _, err := readCap(f, 4096)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", sdk.ErrDocParse, err)
	}
	v := baseView(req, abs, fi.Size(), fi.ModTime().UnixNano(), format)
	v.Meta = map[string]string{"mime": mimeOf(abs, head), "magic": magicOf(head)}
	v.Blocks = []sdk.DocBlock{
		{Kind: sdk.DocBlockNote, Text: fmt.Sprintf("二进制文件 %s(%d 字节,mime=%s)", v.Name, fi.Size(), v.Meta["mime"])},
		{Kind: sdk.DocBlockCode, Text: hexdump(head, 4096), Lang: "hex"},
	}
	return v, nil
}

// extractImage 图片:元信息 + 尺寸(stdlib 解码器探测;SVG 无尺寸)。
func extractImage(_ context.Context, _ *Service, abs string, fi os.FileInfo, req sdk.DocRequest, format sdk.DocFormat) (*sdk.DocView, error) {
	v := baseView(req, abs, fi.Size(), fi.ModTime().UnixNano(), format)
	mime := mimeOf(abs, nil)
	v.Meta = map[string]string{"mime": mime}
	w, h := imageDims(abs)
	blk := sdk.DocBlock{Kind: sdk.DocBlockImage, Text: v.Name, Meta: map[string]string{"mime": mime}}
	if w > 0 && h > 0 {
		blk.Asset = &sdk.DocAsset{Name: v.Name, Mime: mime, W: w, H: h, Bytes: fi.Size()}
	}
	v.Blocks = []sdk.DocBlock{blk}
	if w > 0 {
		v.Blocks = append(v.Blocks, sdk.DocBlock{Kind: sdk.DocBlockNote, Text: fmt.Sprintf("尺寸 %d×%d,%d 字节", w, h, fi.Size())})
	}
	return v, nil
}

// imageDims 用 stdlib 解码器探测图片尺寸(SVG/未知格式返回 0,0;失败不报错)。
func imageDims(path string) (int, int) {
	if strings.HasSuffix(strings.ToLower(path), ".svg") {
		return 0, 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// extractUnsupported 显式不支持:结构化说明 + 建议(E2 外部转换器),不假装支持。
func extractUnsupported(_ context.Context, _ *Service, abs string, fi os.FileInfo, req sdk.DocRequest, format sdk.DocFormat) (*sdk.DocView, error) {
	v := baseView(req, abs, fi.Size(), fi.ModTime().UnixNano(), format)
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(abs), "."))
	v.Warnings = append(v.Warnings, fmt.Sprintf("格式 .%s 不支持在线预览(旧二进制 Office / 归档 / 音视频等一律不解析)", ext))
	v.Blocks = []sdk.DocBlock{{
		Kind: sdk.DocBlockUnsupported,
		Text: fmt.Sprintf("%s:当前不支持预览(可下载后用本地应用打开;若安装了外部转换器可选高保真预览)", v.Name),
	}}
	return v, nil
}

// magicOf 头部魔数的人类可读描述。
func magicOf(b []byte) string {
	if len(b) >= 4 {
		return hex.EncodeToString(b[:4])
	}
	return hex.EncodeToString(b)
}

// mimeOf 依扩展名猜 MIME;扩展名未知时用头部魔数(spawn 与 web 端点共用)。
func mimeOf(name string, head []byte) string {
	if m := mime.TypeByExtension(filepath.Ext(name)); m != "" {
		return m
	}
	if len(head) >= 8 {
		switch {
		case string(head[:5]) == "%PDF-":
			return "application/pdf"
		case string(head[:4]) == "PK\x03\x04":
			return "application/zip"
		case string(head[:8]) == "\x89PNG\r\n\x1a\n":
			return "image/png"
		case string(head[:3]) == "\xff\xd8\xff":
			return "image/jpeg"
		case string(head[:4]) == "GIF8":
			return "image/gif"
		case string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
			return "image/webp"
		}
	}
	return "application/octet-stream"
}

// hexdump 前 n 字节的经典 hexdump(绝对不 panic:输入不足即止)。
func hexdump(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	var sb strings.Builder
	for off := 0; off < len(b); off += 16 {
		end := off + 16
		if end > len(b) {
			end = len(b)
		}
		chunk := b[off:end]
		fmt.Fprintf(&sb, "%08x  ", off)
		for i := 0; i < 16; i++ {
			if i < len(chunk) {
				fmt.Fprintf(&sb, "%02x ", chunk[i])
			} else {
				sb.WriteString("   ")
			}
			if i == 7 {
				sb.WriteByte(' ')
			}
		}
		sb.WriteString(" |")
		for _, c := range chunk {
			if c >= 0x20 && c < 0x7f {
				sb.WriteByte(c)
			} else {
				sb.WriteByte('.')
			}
		}
		sb.WriteString("|\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
