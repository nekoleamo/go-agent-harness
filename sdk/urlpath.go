package sdk

import "strings"

// knownURLSchemes URL 形态路径的前缀表(见 LooksLikeURLPath 的由来)。
// 只收 `//` 形态的 scheme(它们后面总跟 `/`,与本地路径不可能撞);`mailto:`/`data:` 这类
// 不带斜杠的形态不在此列 —— 它们构不成 `host/path` 的目录树,也不在真机事故里。
var knownURLSchemes = map[string]bool{
	"http": true, "https": true, "ftp": true, "ftps": true, "file": true,
	"ws": true, "wss": true, "sftp": true, "ssh": true, "smb": true, "gopher": true,
}

// LooksLikeURLPath 判定字符串是"URL 形态"而不是本地路径。
//
// 由来(2026-09-22 真机):模型偶尔把网页地址当文件路径交给写工具,于是在工作目录里长出
// `https:/host/docs/...` 这样的空目录树 —— 路径在到达写盘前通常已被 filepath.Clean 把 `//`
// 折成 `/`,所以两种形态都要判。
//
// 判据:首个 `:` 之前是已知 scheme(RFC 3986 小写字母集),且其后紧跟 `/`(或为空)。
// Windows 盘符(`C:/…`)天然不匹配(scheme 是单字母且不在表内),`file.txt:…` 也不匹配。
func LooksLikeURLPath(p string) bool {
	s := strings.TrimSpace(p)
	if s == "" {
		return false
	}
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return false
	}
	if !knownURLSchemes[strings.ToLower(s[:i])] {
		return false
	}
	rest := s[i+1:]
	return rest == "" || strings.HasPrefix(rest, "/")
}
