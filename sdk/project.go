package sdk

import (
	"os"
	"path/filepath"
	"strings"
)

// ProjectKeyFromCwd 当前工作目录 → 项目 key。纯函数,宿主与工具共享:
// 会话(todos/memory/plans)按项目隔离时派生 key 用本函数——tool 插件不能
// import host 插件包(红线),统一收口到 sdk 避免各包复制漂移。
func ProjectKeyFromCwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "default"
	}
	return ProjectKey(wd)
}

// ProjectKey 绝对路径 → 项目 key:分隔符/冒号统一转 -,去除首尾 -;
// 空/根/. → default。与 plugins/host/host-cwd-sessions 内同款派生保持同步
// (host 包保留本地实现以免扩大宿主改动面;规则变更需两处一致)。
func ProjectKey(abs string) string {
	clean := filepath.Clean(abs)
	if clean == "." || clean == "" || clean == string(filepath.Separator) {
		return "default"
	}
	r := strings.NewReplacer("/", "-", `\`, "-", ":", "-")
	out := strings.Trim(r.Replace(clean), "-")
	if out == "" {
		return "default"
	}
	return out
}
