// Package register 提供 bundle→registry 的通用装配逻辑(独立包避免 import 环)。
package register

import (
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/plugins/catalogue"
)

// RegisterInto 注册某 bundle 全部插件:data 从配置条目注入,未实现条目跳过。
func RegisterInto(r *plugin.Registry, t *config.Tree, all map[string]catalogue.Def, name string) error {
	for id, d := range all {
		if d.Bundle != name {
			continue
		}
		mm := *d.Manifest
		if entry, ok := t.Get(id); ok {
			mm.Data = entry.Data
		}
		if err := r.Register(d.Factory, &mm); err != nil {
			return err
		}
	}
	return nil
}
