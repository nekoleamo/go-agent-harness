// 凭据隔离(对齐设计 §8):工具子进程 env 白名单化 —
// 滤除 *_API_KEY/*_TOKEN/*_SECRET 等凭据类键(保留 GAH_* 宿主配置与基础键),防模型经 shell 读 key 进会话历史。
package sdk

import (
	"os"
	"strings"
)

// SanitizedEnv 从 base(通常 os.Environ())过滤凭据类键,返回工具子进程可用 env。
func SanitizedEnv(base []string) []string {
	var out []string
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if isCredentialKey(k) {
			continue
		}
		out = append(out, kv)
	}
	// 基础键兜底(确保子进程可用环境基础能力)
	for _, k := range []string{"PATH", "HOME", "SHELL", "TMPDIR", "LANG", "GAH_HOME"} {
		if hasKey(out, k) {
			continue
		}
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// isCredentialKey 凭据类键判定:以 _API_KEY/_TOKEN/_SECRET 结尾或以 GAH_ 开头者保留。
func isCredentialKey(k string) bool {
	up := strings.ToUpper(k)
	for _, suffix := range []string{"_API_KEY", "_TOKEN", "_SECRET", "_PASSWORD", "_CREDENTIALS"} {
		if strings.HasSuffix(up, suffix) {
			return true
		}
	}
	return false
}

func hasKey(env []string, key string) bool {
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k == key {
			return true
		}
	}
	return false
}
