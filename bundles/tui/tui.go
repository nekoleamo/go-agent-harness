// Package tui 是 tui bundle 的插件装配层(对齐设计 §2.2:叠加界面)。
// 插件定义来自 plugins/catalogue(单一事实源)。
package tui

import (
	"github.com/nekoleamo/go-agent-harness/bundles/register"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/plugins/catalogue"
)

// RegisterAll 注册 tui bundle 全部插件。
func RegisterAll(r *plugin.Registry, t *config.Tree) error {
	return register.RegisterInto(r, t, catalogue.All, "tui")
}
