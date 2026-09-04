// Package hostcommands 提供 host-commands 插件(M5 后补):ctx.commands 斜杠命令
// 注册表服务。零业务能力,仅提供注册表——命令语义归各能力插件自身(如
// host-jobs 注册 /jobs);TUI 提示列表/分发均来自本注册表,插件命令自动进入
// 提示,卸载随 Disposer 撤销。装配于 base bundle(先于依赖者启动,可插拔)。
package hostcommands

import (
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-commands。provides ctx.commands。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-commands" }

// Start 提供 ctx.commands 注册表。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	reg := NewRegistry()
	if err := c.Provide("ctx.commands", reg); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// Registry 实现 sdk.CommandRegistry(注册顺序稳定,同名冲突非静默)。
type Registry struct {
	mu    sync.RWMutex
	specs map[string]sdk.CommandSpec
	order []string
}

func NewRegistry() *Registry {
	return &Registry{specs: map[string]sdk.CommandSpec{}}
}

// Register 注册命令;同名冲突返回错误(先到先得,不覆盖)。
func (r *Registry) Register(spec sdk.CommandSpec) (sdk.Disposer, error) {
	if spec.Name == "" {
		return nil, errString("命令名不能为空")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.specs[spec.Name]; exists {
		return nil, errString("命令 /" + spec.Name + " 已注册(先到先得;先卸载原提供者)")
	}
	r.specs[spec.Name] = spec
	r.order = append(r.order, spec.Name)
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.specs, spec.Name)
		for i, n := range r.order {
			if n == spec.Name {
				r.order = append(r.order[:i], r.order[i+1:]...)
				break
			}
		}
	}, nil
}

// List 全部已注册命令(注册顺序)。
func (r *Registry) List() []sdk.CommandSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]sdk.CommandSpec, 0, len(r.order))
	for _, n := range r.order {
		if s, ok := r.specs[n]; ok {
			out = append(out, s)
		}
	}
	return out
}

// Get 取回单个命令。
func (r *Registry) Get(name string) (sdk.CommandSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.specs[name]
	return s, ok
}

type errString string

func (e errString) Error() string { return string(e) }
