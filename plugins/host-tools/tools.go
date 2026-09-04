// Package hosttools 提供 host-tools 插件:ctx.tools 工具注册表 + 执行流水线。
// 流水线对齐 dsh:tools/pre-execute(veto) → 执行 → tools/post-execute → tool/result(广播)。
package hosttools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-tools。requires ctx(事件总线随 Ctx 提供)。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-tools" }

// Start 注册 ctx.tools 服务。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	r := &reg{c: c, tools: make(map[string]sdk.Tool), logger: c.Logger()}
	if err := c.Provide("ctx.tools", r); err != nil {
		return nil, err
	}
	return func() {}, nil
}

type reg struct {
	c      sdk.Ctx
	mu     sync.RWMutex
	order  []string
	tools  map[string]sdk.Tool
	logger *slog.Logger
}

// Register 注册工具(返回 Disposer)。
func (r *reg) Register(t sdk.Tool) sdk.Disposer {
	if t == nil {
		return func() {}
	}
	def := t.Definition()
	r.mu.Lock()
	if _, ok := r.tools[def.Name]; ok {
		r.mu.Unlock()
		// 重名注册非静默:显式提示(替换同名工具应关闭旧工具插件后再启用新插件)
		if r.logger != nil {
			r.logger.Warn("tool 注册冲突已忽略", "tool", def.Name, "hint", "先关闭提供同名工具的插件,再启用新插件")
		}
		return func() {}
	}
	r.tools[def.Name] = t
	r.order = append(r.order, def.Name)
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			delete(r.tools, def.Name)
			for i, n := range r.order {
				if n == def.Name {
					r.order = append(r.order[:i], r.order[i+1:]...)
					break
				}
			}
		})
	}
}

// List 返回模型可见的工具定义。
func (r *reg) List() []sdk.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]sdk.ToolDefinition, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.tools[n].Definition())
	}
	return out
}

// Get 取回单个工具定义。
func (r *reg) Get(name string) (sdk.ToolDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if !ok {
		return sdk.ToolDefinition{}, false
	}
	return t.Definition(), true
}

// Execute 经流水线执行工具。
func (r *reg) Execute(ctx context.Context, name, args string) (*sdk.ToolResult, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		res := &sdk.ToolResult{Error: fmt.Sprintf("工具 %q 不存在 (对应插件可能已卸载/未启用;可经 /plugins list 排查)", name), Content: "{}"}
		r.broadcastResult(ctx, name, res)
		return res, nil
	}

	// 1. tools/pre-execute:waterfall veto 拦截
	call := &sdk.ToolCallEvent{ID: newCallID(), Name: name, Arguments: args}
	if _, err := r.c.Emit(ctx, "tools/pre-execute", call, sdk.Waterfall); err != nil {
		res := &sdk.ToolResult{Error: "blocked: " + err.Error(), Content: "{}"}
		r.broadcastResult(ctx, name, res)
		return res, nil
	}

	// 2. 执行(包裹/超时策略在 M4 tools/execute 瀑布引入)
	out, err := t.Execute(ctx, args)
	var content string
	var merr string
	if err != nil {
		merr = err.Error() // 结构化错误回传模型,不中断 turn(设计 §11)
		content = "{}"
	} else {
		b, jerr := json.Marshal(out)
		if jerr != nil {
			content = fmt.Sprint(out)
		} else {
			content = string(b)
		}
		// 结构化错误契约:结果对象含 error 键时提升为 Error 字段
		if m, isMap := out.(map[string]any); isMap {
			if e, ok := m["error"].(string); ok && e != "" {
				merr = e
			}
		}
	}
	res := &sdk.ToolResult{Content: content, Error: merr}

	// 3. tools/post-execute:waterfall(可改写结果)
	if _, err := r.c.Emit(ctx, "tools/post-execute", res, sdk.Waterfall); err != nil {
		res = &sdk.ToolResult{Error: "post-execute blocked: " + err.Error(), Content: "{}"}
	}

	// 4. tool/result:emit 广播结果
	r.broadcastResult(ctx, name, res)
	return res, nil
}

// broadcastResult 广播工具结果(供 UI/日志/策略监听)。
func (r *reg) broadcastResult(ctx context.Context, name string, res *sdk.ToolResult) {
	r.c.Emit(ctx, "tool/result", &sdk.ToolResultEvent{Name: name, Content: res.Content, Error: res.Error}, sdk.Emit)
}

var callSeq uint64

func newCallID() string {
	return fmt.Sprintf("call_%d", atomic.AddUint64(&callSeq, 1))
}
