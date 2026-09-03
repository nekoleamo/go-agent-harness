// Package tui 是 tui bundle 的插件装配层(对齐设计 §2.2:叠加界面)。
package tui

import (
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/plugins/ui-tui-app"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

type def struct {
	f sdk.Factory
	m *sdk.Manifest
}

// defs 本 bundle 的插件清单。
var defs = map[string]def{
	"ui-tui-app": {func() sdk.Plugin { return &uitui.Plugin{} }, &sdk.Manifest{
		ID: "ui-tui-app", Type: "ui", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.agentLoop", "ctx.llm"}}},
}

// RegisterAll 按配置树注册本 bundle 已启用的插件。
func RegisterAll(r *plugin.Registry, t *config.Tree) error {
	for _, id := range t.List() {
		if !t.Enabled(id) {
			continue
		}
		d, ok := defs[id]
		if !ok {
			continue
		}
		entry, _ := t.Get(id)
		mm := *d.m
		mm.Data = entry.Data
		if err := r.Register(d.f, &mm); err != nil {
			return err
		}
	}
	return nil
}
