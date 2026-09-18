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
	// 有效沙箱档位随 ctx 下传(见 sdk.SandboxHint):工具侧——尤其是外部进程插件里的
	// 工具,它们拿不到 ctx.sandbox 服务——据此施加与档位一致的内核级约束。
	out, err := t.Execute(r.withSandboxHint(ctx), args)
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

// withSandboxHint 把当前**有效**沙箱档位挂到 ctx(唯一执行入口注入,见 sdk.SandboxHint)。
// 有效档位 = 审批联动后的档位:审批 open/strict 会覆盖沙箱声明档,只读 Mode() 会与
// 实际拦截行为不一致;故优先取 sdk.EffectiveSandbox.EffectiveMode()。
// 每次调用重新 Inject(档位运行期可变:/sandbox 切档、审批联动),不缓存。
// 未装配沙箱 / 取不到 → 原样返回(不挂 hint:工具不得假定任何档位)。
// 工作根(S-P1-4):ctx 上的调用级覆盖(sdk.WithWorkRoot,隔离子代理 = 受管 worktree)优先于
// 沙箱自身 root —— 合并到同一个 hint.Root 但不改档位(调用方不能借覆盖放宽档位)。
func (r *reg) withSandboxHint(ctx context.Context) context.Context {
	root := ""
	if dir, ok := sdk.WorkRootOf(ctx); ok {
		root = dir
	}
	var sb sdk.Sandbox
	if err := r.c.Inject("ctx.sandbox", &sb); err != nil || sb == nil {
		if root == "" {
			return ctx
		}
		// 无沙箱但有工作根:只下传路径基准(Mode 留空 = 工具不得假定档位)
		return sdk.WithSandboxHint(ctx, sdk.SandboxHint{Root: root})
	}
	mode := sb.Mode()
	if es, ok := sb.(sdk.EffectiveSandbox); ok {
		mode = es.EffectiveMode()
	}
	if mode == "" {
		if root == "" {
			return ctx // 档位未知:不挂(不给工具可乘之机)
		}
		return sdk.WithSandboxHint(ctx, sdk.SandboxHint{Root: root})
	}
	if root == "" {
		root = sb.Root()
	}
	return sdk.WithSandboxHint(ctx, sdk.SandboxHint{Mode: mode, Root: root})
}

// broadcastResult 广播工具结果(供 UI/日志/策略监听)。
func (r *reg) broadcastResult(ctx context.Context, name string, res *sdk.ToolResult) {
	r.c.Emit(ctx, "tool/result", &sdk.ToolResultEvent{Name: name, Content: res.Content, Error: res.Error}, sdk.Emit)
}

var callSeq uint64

func newCallID() string {
	return fmt.Sprintf("call_%d", atomic.AddUint64(&callSeq, 1))
}
