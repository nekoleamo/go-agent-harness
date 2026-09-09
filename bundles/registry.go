// Package bundles 汇总全部内置 bundle 的装配器(profile 按声明顺序调用)。
// 对齐设计 §2:config.bundle 名 → 装配器映射。新增 bundle 在此登记。
package bundles

import (
	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	imqqb "github.com/nekoleamo/go-agent-harness/bundles/im-qq"
	imwechatb "github.com/nekoleamo/go-agent-harness/bundles/im-wechat"
	tuib "github.com/nekoleamo/go-agent-harness/bundles/tui"
	webb "github.com/nekoleamo/go-agent-harness/bundles/web"

	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
)

// RegisterFunc 一个 bundle 的装配函数。
type RegisterFunc func(r *plugin.Registry, t *config.Tree) error

// Registry bundle 名 → 装配器。
var Registry = map[string]RegisterFunc{
	"base":      baseb.RegisterAll,
	"tui":       tuib.RegisterAll,
	"web":       webb.RegisterAll,
	"im-wechat": imwechatb.RegisterAll,
	"im-qq":     imqqb.RegisterAll,
}
