//go:build !(darwin && (amd64 || arm64)) && !(linux && (amd64 || arm64)) && !(windows && amd64)

package embed

import "embed"

// 发行矩阵外的平台(darwin/linux 的 386、windows/arm64 等):**允许构建**(此前这些目标
// 因缺少 extPlugins/extPluginDir 直接编译失败,报错是 "undefined: extPlugins",看不出原因),
// 但不内置任何外部插件 —— 相关调用经 extPluginDir == "" 显式失败(不静默降级成"没有插件")。
// 矩阵定义在 scripts/gen-extplugins.sh(gen 侧按同一矩阵生成,漂移即报错)。
var extPlugins embed.FS

// extPluginDir 空 = 本平台无内置产物(embed.go 据此给出可诊断的显式错误)。
const extPluginDir = ""
