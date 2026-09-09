// Package imqq 是 im-qq bundle 的插件装配层(IM 远程控制线 P0-2b-QQ;对齐 im-wechat 先例:
// 叠加 QQ 官方 Bot v2 通道插件;提供 ctx.confirm(IM 审批),与 tui/web/im-wechat profile
// 互斥——profile 层保证)。插件定义来自 plugins/catalogue(单一事实源)。
package imqq

import (
	"github.com/nekoleamo/go-agent-harness/bundles/register"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/plugins/catalogue"
)

// RegisterAll 注册 im-qq bundle 全部插件。
func RegisterAll(r *plugin.Registry, t *config.Tree) error {
	return register.RegisterInto(r, t, catalogue.All, "im-qq")
}
