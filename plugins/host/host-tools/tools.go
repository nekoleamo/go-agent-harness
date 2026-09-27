// Package hosttools 提供 host-tools 插件:ctx.tools 工具注册表 + 执行流水线。
// 流水线对齐 dsh:tools/pre-execute(veto) → 执行 → tools/post-execute → tool/result(广播)。
package hosttools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
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
	c       sdk.Ctx
	mu      sync.RWMutex
	order   []string
	tools   map[string]sdk.Tool
	ignored []sdk.ToolConflict // 重名被忽略者(可见性面:B3)
	logger  *slog.Logger
}

// Register 注册工具(返回 Disposer)。
func (r *reg) Register(t sdk.Tool) sdk.Disposer {
	if t == nil {
		return func() {}
	}
	def := t.Definition()
	// 未声明路径参数的工具:宿主裁决时会按 schema/参数值推断(见 sdk.InferPathParams 与
	// policy-guard CheckToolCallAt)—— 从 fail-open 变成"有裁决"是行为变化,作者该知道,
	// 故这里点名一次(不静默降级;声明 PathParams 即可覆盖推断)。
	if len(def.PathParams) == 0 && !def.PathParamsDeclared && r.logger != nil {
		if inferred := sdk.InferPathParams(def); len(inferred) > 0 {
			r.logger.Info("工具未声明路径参数,宿主按推断裁决",
				"tool", def.Name, "inferred", sdk.DescribePathParams(inferred),
				"hint", "显式声明 PathParams 可覆盖推断(docs/PLUGIN_DEV.md §2.6);确无路径参数请设 PathParamsDeclared")
		}
	}
	r.mu.Lock()
	if _, ok := r.tools[def.Name]; ok {
		// 重名注册非静默,且**可见**:first-wins 的语义不变(后到者不顶掉前者 ——
		// 静默替换更难排查),但被忽略者记进 ToolConflicts(日志/`/plugins list`/API 三面可见)。
		ignored := sdk.ToolConflict{Name: def.Name, Ignored: briefDesc(def)}
		r.ignored = append(r.ignored, ignored)
		r.mu.Unlock()
		if r.logger != nil {
			r.logger.Error("tool 注册冲突:同名工具已存在,本次注册被忽略",
				"tool", def.Name, "ignored", ignored.Ignored,
				"hint", "先关闭提供同名工具的插件,再启用新插件(启用成功≠工具可用)")
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

// ToolConflicts 返回被忽略的同名工具注册(稳定顺序;实现 sdk.ToolConflictReporter)。
func (r *reg) ToolConflicts() []sdk.ToolConflict {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]sdk.ToolConflict, len(r.ignored))
	copy(out, r.ignored)
	return out
}

// briefDesc 把定义压缩成一行描述(有界),供冲突日志/状态面定位来源插件。
func briefDesc(def sdk.ToolDefinition) string {
	d := strings.TrimSpace(def.Description)
	if d == "" {
		return "(无描述)"
	}
	r := []rune(d)
	if len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return d
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
