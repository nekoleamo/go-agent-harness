// Package confirmfusion 是 confirm-fusion bundle 的插件装配层(多端融合):
// host-confirm-fusion 统一 ctx.confirm 仲裁,使 web/tui 等 UI 通道同进程并存。
// 融合 profile(bundles: base, confirm-fusion, web, tui)装配本层;web/tui 检测到
// ctx.confirmFusion 后注册呈现者、不再各自 Provide(消除同名冲突)。
package confirmfusion

import (
	"github.com/nekoleamo/go-agent-harness/bundles/register"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/plugins/catalogue"
)

// RegisterAll 注册 confirm-fusion bundle 全部插件。
func RegisterAll(r *plugin.Registry, t *config.Tree) error {
	return register.RegisterInto(r, t, catalogue.All, "confirm-fusion")
}
