// 凭据隔离(对齐设计 §8):工具子进程 env 白名单化 —
// 滤除 *_API_KEY/*_TOKEN/*_SECRET/AWS_* 等凭据类键(保留 GAH_* 宿主配置与基础键),防模型经 shell 读 key 进会话历史;
// GAH_CB_*(外部插件回调地址/token)只允许出现在宿主直接派生的插件进程里,不再向下一级进程传递。
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

// SanitizedChildEnv 再派生一级子进程的最小环境:等价 SanitizedEnv(nil),只兜底基础键
// (PATH/HOME/SHELL/TMPDIR/LANG/GAH_HOME),不继承调用方进程的其余变量(含凭据与 GAH_CB_* 回调凭据)。
// 用途:由宿主或外部插件再次派生的第三方进程(MCP server、Office/PDF 转换器)显式设置 cmd.Env;
// 这类进程只需基础运行环境,不需要(也不应拿到)宿主配置与凭据。
func SanitizedChildEnv() []string { return SanitizedEnv(nil) }

// isCredentialKey 凭据类键判定(命中即从子进程 env 中滤除):
//   - GAH_CB_*:宿主回调通道凭据,只有宿主直接派生的插件进程可用,不得再向下传;
//   - AWS_*   :AWS 系凭据(ACCESS_KEY_ID/SECRET_ACCESS_KEY/SESSION_TOKEN),非凭据的 AWS_REGION 等一并滤除;
//   - 通用后缀:_API_KEY/_APIKEY/_API_TOKEN/_TOKEN/_AUTH_TOKEN/_SESSION_TOKEN/_SECRET/
//     _PASSWORD/_PASSWD/_CREDENTIALS/_PRIVATE_KEY/_ACCESS_KEY/_ACCESS_KEY_ID/_PAT/_KEY。
//
// 其余键(含 GAH_HOME/GAH_PROFILE 等 GAH_* 宿主配置)保留。
func isCredentialKey(k string) bool {
	up := strings.ToUpper(k)
	if strings.HasPrefix(up, "GAH_CB_") || strings.HasPrefix(up, "AWS_") {
		return true
	}
	for _, suffix := range []string{
		"_API_KEY", "_APIKEY", "_API_TOKEN", "_TOKEN", "_AUTH_TOKEN", "_SESSION_TOKEN",
		"_SECRET", "_PASSWORD", "_PASSWD", "_CREDENTIALS", "_PRIVATE_KEY",
		"_ACCESS_KEY", "_ACCESS_KEY_ID", "_PAT", "_KEY",
	} {
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
