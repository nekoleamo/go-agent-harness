// 入站媒体落盘与类型判定(共享:微信 iLink 与 QQ 附件入站)。
// 便携纪律:落盘 $GAH_HOME/im-media/ 单根派生(空 GAH_HOME 兜底 TempDir,不落 cwd)。
package im

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MediaDir 媒体落盘根($GAH_HOME/im-media)。
func MediaDir() string {
	home := os.Getenv("GAH_HOME")
	if home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, "im-media")
}

// SaveMedia 媒体字节落盘(<MediaDir>/<base>.<ext>);base 建议 "<user>-<idx>"。
func SaveMedia(base, ext string, data []byte) (string, error) {
	dir := MediaDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if ext == "" {
		ext = "bin"
	}
	path := filepath.Join(dir, base+"."+ext)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// MediaExtFromContentType contentType/MIME → 扩展名(image/jpeg→jpg;voice→mp3;空 → 空)。
func MediaExtFromContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ct == "" {
		return ""
	}
	if i := strings.Index(ct, ";"); i > 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "voice":
		return "mp3"
	case "image/jpeg", "image/jpg":
		return "jpg"
	}
	if i := strings.Index(ct, "/"); i > 0 {
		sub := ct[i+1:]
		switch sub {
		case "plain":
			return "txt"
		case "mpeg":
			return "mp3"
		case "quicktime":
			return "mov"
		}
		if len(sub) <= 5 && sub != "" {
			return sub
		}
	}
	return ""
}

// IsTextExt 文本类扩展名(内容可安全并入 IM 正文)。
func IsTextExt(ext string) bool {
	switch strings.ToLower(ext) {
	case "txt", "md", "markdown", "json", "jsonl", "yaml", "yml", "csv", "log",
		"go", "py", "js", "ts", "html", "css", "sh", "toml", "xml", "ini", "conf", "env", "sql":
		return true
	}
	return false
}

// MediaNote 生成附件说明行(供 transport 拼入正文)。
func MediaNote(kind, name string, err error) string {
	if err != nil {
		return fmt.Sprintf("(收到%s %s,下载失败)", kind, name)
	}
	return fmt.Sprintf("(收到%s %s,已保存;如需读取请告知路径)", kind, name)
}
