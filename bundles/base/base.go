// Package base 是 base bundle 的插件装配层(对齐设计 §2.2:base = 共享第一层)。
// 插件定义来自 plugins/catalogue(单一事实源);本层仅按配置树注入 data 并注册。
package base

import (
	"github.com/nekoleamo/go-agent-harness/bundles/register"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/plugins/catalogue"
)

// RegisterAll 注册 base bundle 全部插件(启动过滤由 boot StartSubset 按 enabled 执行)。
func RegisterAll(r *plugin.Registry, t *config.Tree) error {
	return register.RegisterInto(r, t, catalogue.All, "base")
}
