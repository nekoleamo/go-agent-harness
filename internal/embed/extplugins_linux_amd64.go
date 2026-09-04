//go:build linux && amd64

package embed

import "embed"

//go:embed extplugins/linux-amd64
var extPlugins embed.FS

// extPluginDir 本平台外部插件 embed 子目录(P4 平台匹配:每平台文件同名变量,
// build-tag 保证同一构建仅一个定义生效,主包只嵌本平台产物)。
const extPluginDir = "extplugins/linux-amd64"
