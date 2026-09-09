// Package imwechat 是 im-wechat bundle 的插件装配层(IM 远程控制线 P0-2b;对齐 web/tui 先例:
// 叠加 IM 微信通道插件;提供 ctx.confirm(IM 审批),与 tui/web profile 互斥——profile 层保证)。
// 插件定义来自 plugins/catalogue(单一事实源)。
package imwechat

import (
	"github.com/nekoleamo/go-agent-harness/bundles/register"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/plugins/catalogue"
)

// RegisterAll 注册 im-wechat bundle 全部插件。
func RegisterAll(r *plugin.Registry, t *config.Tree) error {
	return register.RegisterInto(r, t, catalogue.All, "im-wechat")
}
