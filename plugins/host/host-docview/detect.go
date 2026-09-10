// 格式探测:只按扩展名判定(对齐 DOC_PREVIEW_PLAN §5.1)——
// CSV 无签名、Office 是 zip 容器、markdown 无魔数,嗅探只会引入不确定性。
// 仅两种兜底:扩展名未知时读前 512 字节判 %PDF- 魔数,再不行按 UTF-8 合法性分 text/binary。
package hostdocview

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// extFormats 扩展名 → 格式(小写,不含点)。
var extFormats = map[string]sdk.DocFormat{
	// markdown
	"md": sdk.DocFormatMarkdown, "markdown": sdk.DocFormatMarkdown, "mdown": sdk.DocFormatMarkdown,
	"mkd": sdk.DocFormatMarkdown, "mdx": sdk.DocFormatMarkdown,
	// 纯文本
	"txt": sdk.DocFormatText, "text": sdk.DocFormatText, "log": sdk.DocFormatText,
	// 表格文本
	"csv": sdk.DocFormatCSV, "tsv": sdk.DocFormatCSV,
	// notebook
	"ipynb": sdk.DocFormatNotebook,
	// 代码
	"json": sdk.DocFormatCode, "json5": sdk.DocFormatCode, "jsonc": sdk.DocFormatCode,
	"yaml": sdk.DocFormatCode, "yml": sdk.DocFormatCode, "toml": sdk.DocFormatCode,
	"ini": sdk.DocFormatCode, "conf": sdk.DocFormatCode, "env": sdk.DocFormatCode,
	"go": sdk.DocFormatCode, "rs": sdk.DocFormatCode, "py": sdk.DocFormatCode,
	"js": sdk.DocFormatCode, "mjs": sdk.DocFormatCode, "cjs": sdk.DocFormatCode,
	"ts": sdk.DocFormatCode, "tsx": sdk.DocFormatCode, "jsx": sdk.DocFormatCode,
	"vue": sdk.DocFormatCode, "svelte": sdk.DocFormatCode, "java": sdk.DocFormatCode,
	"kt": sdk.DocFormatCode, "c": sdk.DocFormatCode, "h": sdk.DocFormatCode,
	"cc": sdk.DocFormatCode, "cpp": sdk.DocFormatCode, "hpp": sdk.DocFormatCode,
	"cs": sdk.DocFormatCode, "rb": sdk.DocFormatCode, "php": sdk.DocFormatCode,
	"swift": sdk.DocFormatCode, "sh": sdk.DocFormatCode, "bash": sdk.DocFormatCode,
	"zsh": sdk.DocFormatCode, "fish": sdk.DocFormatCode, "ps1": sdk.DocFormatCode,
	"sql": sdk.DocFormatCode, "lua": sdk.DocFormatCode, "pl": sdk.DocFormatCode,
	"r": sdk.DocFormatCode, "scala": sdk.DocFormatCode, "dart": sdk.DocFormatCode,
	"ex": sdk.DocFormatCode, "exs": sdk.DocFormatCode, "erl": sdk.DocFormatCode,
	"hs": sdk.DocFormatCode, "clj": sdk.DocFormatCode, "proto": sdk.DocFormatCode,
	"gradle": sdk.DocFormatCode, "cmake": sdk.DocFormatCode, "mk": sdk.DocFormatCode,
	"dockerfile": sdk.DocFormatCode, "gitignore": sdk.DocFormatCode, "patch": sdk.DocFormatCode,
	"diff": sdk.DocFormatCode, "starlark": sdk.DocFormatCode,
	// OOXML
	"docx": sdk.DocFormatDOCX, "docm": sdk.DocFormatDOCX,
	"xlsx": sdk.DocFormatXLSX, "xlsm": sdk.DocFormatXLSX,
	"pptx": sdk.DocFormatPPTX, "pptm": sdk.DocFormatPPTX,
	// PDF / HTML
	"pdf":  sdk.DocFormatPDF,
	"html": sdk.DocFormatHTML, "htm": sdk.DocFormatHTML, "xhtml": sdk.DocFormatHTML,
	// 图片
	"png": sdk.DocFormatImage, "jpg": sdk.DocFormatImage, "jpeg": sdk.DocFormatImage,
	"gif": sdk.DocFormatImage, "webp": sdk.DocFormatImage, "bmp": sdk.DocFormatImage,
	"svg": sdk.DocFormatImage, "ico": sdk.DocFormatImage, "avif": sdk.DocFormatImage,
}

// unsupportedExts 显式不支持的扩展名(明确拒绝,不回退到 binary 的模糊语义)。
// 旧二进制 Office / iWork / 压缩包 / 音视频 / 可执行:不做解析(可选 E2 外部转换器)。
var unsupportedExts = map[string]bool{
	"doc": true, "xls": true, "ppt": true,
	"pages": true, "numbers": true, "key": true,
	"zip": true, "tar": true, "gz": true, "tgz": true, "bz2": true, "xz": true, "7z": true, "rar": true,
	"mp3": true, "wav": true, "flac": true, "ogg": true, "m4a": true, "aac": true,
	"mp4": true, "mov": true, "avi": true, "mkv": true, "webm": true,
	"exe": true, "dll": true, "so": true, "dylib": true, "bin": true, "o": true, "a": true,
	"psd": true, "ai": true, "sketch": true, "fig": true,
	"dwg": true, "dxf": true, "stl": true, "obj": true,
	"ttf": true, "otf": true, "woff": true, "woff2": true,
	"sqlite": true, "db": true, "mdb": true,
}

// formatByExt 扩展名映射;ok=false 表示无映射(需兜底探测)。
func formatByExt(name string) (sdk.DocFormat, bool) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if ext == "" {
		return "", false
	}
	if unsupportedExts[ext] {
		return sdk.DocFormatUnsupported, true
	}
	if f, ok := extFormats[ext]; ok {
		return f, true
	}
	return "", false
}

// sniffFormat 无扩展名(或未知扩展名)时的兜底:读前 512 字节。
// 唯一接受的魔数是 %PDF-(其余一律按 UTF-8 合法性分 text/binary)。
func sniffFormat(r io.Reader) sdk.DocFormat {
	buf := make([]byte, 512)
	n, _ := io.ReadFull(r, buf)
	if n <= 0 {
		return sdk.DocFormatText
	}
	b := buf[:n]
	if bytes.HasPrefix(b, []byte("%PDF-")) {
		return sdk.DocFormatPDF
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return sdk.DocFormatBinary
	}
	if !utf8.Valid(b) {
		return sdk.DocFormatBinary
	}
	return sdk.DocFormatText
}
