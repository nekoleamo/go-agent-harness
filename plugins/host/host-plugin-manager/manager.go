// Package hostplugmgr 提供 host-plugin-manager 插件:ctx.pluginManager 服务。
// 运行期插拔:List(运行/候选三源)/ Load(启动已注册插件)/ Unload(停止实例,副作用即撤)。
// 候选清单经 system.catalogue 注入(boot 从 plugins/catalogue 构造),避免插件间 import 环。
package hostplugmgr

import (
	"fmt"
	"sort"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-plugin-manager。requires system.registry/system.catalogue。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-plugin-manager" }

// Start 注册 ctx.pluginManager 服务。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var ops sdk.RegistryOps
	if err := c.Inject("system.registry", &ops); err != nil {
		return nil, err
	}
	var candidates map[string]sdk.PluginInfo
	if err := c.Inject("system.catalogue", &candidates); err != nil {
		return nil, err
	}
	m := &Manager{c: c, ops: ops, candidates: candidates}
	if err := c.Provide("ctx.pluginManager", m); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// Manager 实现 sdk.PluginManager。
type Manager struct {
	c          sdk.Ctx
	ops        sdk.RegistryOps
	candidates map[string]sdk.PluginInfo
}

// List 返回候选 + 运行态三源清单。
func (m *Manager) List() []sdk.PluginInfo {
	running := make(map[string]bool)
	for _, id := range m.ops.Snapshot() {
		running[id] = true
	}
	seen := map[string]bool{}
	out := make([]sdk.PluginInfo, 0, len(m.candidates)+len(running))
	for id, info := range m.candidates {
		info.State = "configured"
		if running[id] {
			info.State = "loaded"
		}
		out = append(out, info)
		seen[id] = true
	}
	// 运行中但不在候选的实例(外部插件,M5)也列出
	for _, id := range m.ops.Snapshot() {
		if !seen[id] {
			out = append(out, sdk.PluginInfo{ID: id, State: "loaded(外部)"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Load 启动已配置(或候选)插件。
func (m *Manager) Load(id string) error {
	for _, info := range m.List() {
		if info.ID == id && info.State == "loaded" {
			return nil // 已运行,幂等
		}
	}
	if _, ok := m.candidates[id]; !ok {
		return fmt.Errorf("plugin-manager: 未知插件 %q", id)
	}
	return m.ops.StartOne(m.c, id)
}

// Unload 停止插件实例(副作用随 Disposer 逆序撤销;幂等)。
// 结构性防护:仍被已加载插件依赖时拒绝卸载(显式失败,提示先卸载依赖或走配置切换)。
func (m *Manager) Unload(id string) error {
	if _, ok := m.candidates[id]; !ok {
		return fmt.Errorf("plugin-manager: 未知插件 %q", id)
	}
	blocked := m.ops.BlockedByLoaded(id)
	if len(blocked) > 0 {
		return fmt.Errorf("plugin-manager: %q 正在被已加载插件 %v 依赖,拒绝运行期卸载;请先卸载依赖者或经配置切换(enabled:false,重启生效)", id, blocked)
	}
	m.ops.Dispose(id)
	return nil
}
