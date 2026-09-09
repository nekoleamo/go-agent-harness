// Package web 是 web bundle 的插件装配层(M7 Web UI;对齐 tui bundle 先例:叠加界面)。
// 插件定义来自 plugins/catalogue(单一事实源);与 tui bundle 互斥(profile 层保证)。
package web

import (
	"github.com/nekoleamo/go-agent-harness/bundles/register"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/plugins/catalogue"
)

// RegisterAll 注册 web bundle 全部插件。
func RegisterAll(r *plugin.Registry, t *config.Tree) error {
	return register.RegisterInto(r, t, catalogue.All, "web")
}
