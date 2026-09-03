// Package plugin 实现 PluginRegistry:拓扑加载、依赖管理、热重载。
// 设计对齐 Cordis:注册的副作用经 Disposer 逆序撤销;provides/requires 做拓扑排序,缺失依赖显式失败。
package plugin

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/hashicorp/go-version"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Instance 是一个已装配的插件实例。
type Instance struct {
	Manifest *sdk.Manifest
	Factory  sdk.Factory
	Plugin   sdk.Plugin

	// disposers 按注册顺序累积,Dispose 逆序执行(可逆性):最后注册的副作用最先撤销。
	disposers []sdk.Disposer
}

// Registry 管理插件装配与生命周期。
type Registry struct {
	mu        sync.RWMutex
	factories map[string]sdk.Factory // id → 构造工厂
	manifest  map[string]*sdk.Manifest
	instances map[string]*Instance // 已启动的实例(id →)
	order     []string             // 启动顺序(拓扑序)
}

// New 创建空注册表。
func New() *Registry {
	return &Registry{
		factories: make(map[string]sdk.Factory),
		manifest:  make(map[string]*sdk.Manifest),
		instances: make(map[string]*Instance),
	}
}

// Register 注册插件工厂及其清单(尚未启动)。
func (r *Registry) Register(f sdk.Factory, m *sdk.Manifest) error {
	if f == nil || m == nil {
		return fmt.Errorf("plugin: nil factory or manifest")
	}
	if m.ID == "" {
		return fmt.Errorf("plugin: manifest requires id")
	}
	if err := checkAPIVersion(m); err != nil {
		return fmt.Errorf("plugin: %q: %w", m.ID, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.factories[m.ID]; dup {
		return fmt.Errorf("plugin: %q already registered", m.ID)
	}
	r.factories[m.ID] = f
	r.manifest[m.ID] = m
	return nil
}

// checkAPIVersion SDK 版本兼容校验:清单声明的语义化范围必须包含 sdk.SDKVersion(红线:显式失败不静默)。
func checkAPIVersion(m *sdk.Manifest) error {
	if m.APIVersion == "" {
		return fmt.Errorf("manifest 缺少 apiVersion(必须声明语义化范围,如 >=1.0,<2.0)")
	}
	constraint, err := version.NewConstraint(m.APIVersion)
	if err != nil {
		return fmt.Errorf("apiVersion %q 解析失败: %v", m.APIVersion, err)
	}
	sdkVer, err := version.NewVersion(sdk.SDKVersion)
	if err != nil {
		return err
	}
	if !constraint.Check(sdkVer) {
		return fmt.Errorf("apiVersion %q 不含 SDK %s(插件与 SDK 不兼容)", m.APIVersion, sdk.SDKVersion)
	}
	return nil
}

// AssertAvailable 启动前校验全部 require 有供给方,并返回拓扑序;循环依赖报错。
func (r *Registry) AssertAvailable() ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return topo(r.manifest)
}

// topo 用 Kahn 算法对 plugin 按 provides/requires 排序。
func topo(manifest map[string]*sdk.Manifest) ([]string, error) {
	if len(manifest) == 0 {
		return nil, nil
	}
	provides := make(map[string]string) // serviceKey → pluginID
	for id, m := range manifest {
		for _, k := range m.Provides {
			if prev, ok := provides[k]; ok {
				return nil, fmt.Errorf("plugin: service %q provided by both %q and %q", k, prev, id)
			}
			provides[k] = id
		}
	}

	indegree := make(map[string]int, len(manifest))
	deps := make(map[string][]string) // id → 依赖它的插件
	for id, m := range manifest {
		for _, k := range m.Requires {
			if _, ok := provides[k]; !ok {
				return nil, fmt.Errorf("plugin: %q requires missing service %q", id, k)
			}
			providesBy := provides[k]
			if providesBy == id {
				return nil, fmt.Errorf("plugin: %q depends on its own service %q", id, k)
			}
			deps[providesBy] = append(deps[providesBy], id)
			indegree[id]++
		}
	}

	queue := make([]string, 0, len(manifest))
	for id := range manifest {
		if indegree[id] == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)

	var order []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		order = append(order, cur)
		next := deps[cur]
		sort.Strings(next)
		for _, dep := range next {
			indegree[dep]--
			if indegree[dep] == 0 {
				queue = append(queue, dep)
				sort.Strings(queue)
			}
		}
	}
	if len(order) != len(manifest) {
		return nil, fmt.Errorf("plugin: dependency cycle among %q", cyclic(manifest, order))
	}
	return order, nil
}

// cyclic 返回未排入拓扑序的插件 id(近似定位环)。
func cyclic(manifest map[string]*sdk.Manifest, order []string) []string {
	in := make(map[string]bool, len(order))
	for _, id := range order {
		in[id] = true
	}
	var out []string
	for id := range manifest {
		if !in[id] {
			out = append(out, id)
		}
	}
	return out
}

// StartAll 按拓扑序启动全部插件。任一个启动失败则逆序回滚已启动插件,返回错误。
func (r *Registry) StartAll(c sdk.Ctx) error {
	return r.StartSubset(c, nil)
}

// StartSubset 按拓扑序启动 enabled 子集(nil/空 = 全部;配置树启停的装配入口)。
// 未启用的已注册插件保持 stopped 态,可经 plugin-manager 运行期 Load。
func (r *Registry) StartSubset(c sdk.Ctx, enabled map[string]bool) error {
	order, err := r.AssertAvailable()
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.order = append([]string(nil), order...)
	r.mu.Unlock()

	var started []string
	for _, id := range order {
		if enabled != nil && !enabled[id] {
			continue
		}
		if err := r.startOne(c, id); err != nil {
			// 回滚:已启动的逆序 dispose
			for i := len(started) - 1; i >= 0; i-- {
				r.Dispose(started[i])
			}
			return fmt.Errorf("plugin: %q failed to start: %w", id, err)
		}
		started = append(started, id)
	}
	return nil
}

func (r *Registry) startOne(c sdk.Ctx, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.instances[id]; ok {
		return fmt.Errorf("plugin: %q already started", id)
	}
	f := r.factories[id]
	p := f()
	d, err := p.Start(c, r.manifest[id])
	if err != nil {
		return err
	}
	r.instances[id] = &Instance{Manifest: r.manifest[id], Factory: f, Plugin: p, disposers: []sdk.Disposer{d}}
	return nil
}

// BlockedByLoaded 返回仍加载且依赖 id 所提供服务键的插件(运行期卸载防护)。
func (r *Registry) BlockedByLoaded(id string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.manifest[id]
	if !ok {
		return nil
	}
	if _, running := r.instances[id]; !running {
		return nil // 未加载,卸载无影响
	}
	provided := map[string]bool{}
	for _, k := range m.Provides {
		provided[k] = true
	}
	var blocked []string
	for oid, om := range r.manifest {
		if oid == id {
			continue
		}
		if _, running := r.instances[oid]; !running {
			continue
		}
		for _, need := range om.Requires {
			if provided[need] {
				blocked = append(blocked, oid)
				break
			}
		}
	}
	sort.Strings(blocked)
	return blocked
}

// Dispose 逆序执行某插件的全部 disposers(幂等)。
func (r *Registry) Dispose(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.disposeOne(id)
}

func (r *Registry) disposeOne(id string) {
	inst, ok := r.instances[id]
	if !ok {
		return
	}
	delete(r.instances, id)
	for i := len(inst.disposers) - 1; i >= 0; i-- {
		inst.disposers[i]() // 幂等性由各 disposer 自行保证
	}
}

// DisposeAll 逆序 dispose 全部已启动插件。
func (r *Registry) DisposeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.instances))
	for id := range r.instances {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for i := len(ids) - 1; i >= 0; i-- {
		r.disposeOne(ids[i])
	}
}

// Reload 热重载单个插件:dispose 旧实例后以新工厂启动。
// (M1 同步语义:进行中 turn 的排空等待在 M2 引入,见设计 §8)
func (r *Registry) Reload(c sdk.Ctx, id string) error {
	r.mu.RLock()
	if _, ok := r.factories[id]; !ok {
		r.mu.RUnlock()
		return fmt.Errorf("plugin: %q not registered", id)
	}
	r.mu.RUnlock()

	r.Dispose(id)
	// 旧插件注销的服务/订阅已在 dispose 前被其 disposer 撤销
	return r.startOne(c, id)
}

// StartOne 启动单个插件(供测试与后期桥接用)。
func (r *Registry) StartOne(c sdk.Ctx, id string) error {
	return r.startOne(c, id)
}

// Snapshot 返回当前已启动实例清单(调试/--dump 用)。
func (r *Registry) Snapshot() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.instances))
	for id := range r.instances {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Order 返回最近一次拓扑序。
func (r *Registry) Order() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
}

// Describe 汇总已注册插件信息。
func (r *Registry) Describe() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var sb strings.Builder
	ids := make([]string, 0, len(r.manifest))
	for id := range r.manifest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		m := r.manifest[id]
		state := "stopped"
		if _, ok := r.instances[id]; ok {
			state = "started"
		}
		fmt.Fprintf(&sb, "%s [%s] %s  provides=%v requires=%v\n",
			id, m.Type, state, m.Provides, m.Requires)
	}
	return strings.TrimSuffix(sb.String(), "\n")
}
