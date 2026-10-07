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
	// ctxFilter 按会话的可见性判定(可选;ctx.tools 实现 sdk.ContextualToolCatalogue)。
	ctxFilter func(context.Context, sdk.ToolDefinition) bool
	// filter 可见性判定(nil = 不过滤;实现 sdk.ToolCatalogue,第九十一批)。
	// 由 host-roles 按“当前角色排除清单”安装;每次 List/Execute 现算 ⇒ 切角色即生效。
	// 用指针包装是为了让 Disposer 能识别“当前装的还是不是自己那一个”(函数值不可比)。
	filter *toolFilter
}

// toolFilter 可见性判定函数的包装体(只为取得可比较的身份)。
type toolFilter struct{ visible func(sdk.ToolDefinition) bool }

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

// SetFilter 安装可见性判定函数(nil = 恢复全量);返回 Disposer 幂等撤销(实现 sdk.ToolCatalogue)。
// 为什么记当前**有一个** filter 而不是叠一叠:过滤语义是“当前角色决定什么可见” ——
// 多源叠加会让“卸载 host-roles 后过滤还在”这种残留成为可能(注册即副作用/卸载即撤销)。
func (r *reg) SetFilter(visible func(sdk.ToolDefinition) bool) sdk.Disposer {
	if visible == nil {
		r.mu.Lock()
		r.filter = nil
		r.mu.Unlock()
		return func() {}
	}
	f := &toolFilter{visible: visible}
	r.mu.Lock()
	r.filter = f
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			// 仅当当前 filter 还是自己装的那个才清(后装的 filter 不该被先装的 disposer 抹掉)
			if r.filter == f {
				r.filter = nil
			}
			r.mu.Unlock()
		})
	}
}

// SetContextFilter 实现 sdk.ContextualToolCatalogue:安装按会话的可见性判定。
func (r *reg) SetContextFilter(visible func(context.Context, sdk.ToolDefinition) bool) sdk.Disposer {
	r.mu.Lock()
	r.ctxFilter = visible
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		// 仅当当前还是自己装的那个才清(后装的 filter 不该被先装的 disposer 抹掉)
		if r.ctxFilter == nil {
			r.mu.Unlock()
			return
		}
		r.ctxFilter = nil
		r.mu.Unlock()
	}
}

// ListFor 实现 sdk.ContextualToolCatalogue:该会话下模型可见的工具。
//
// 没装按会话判定时与 List() 逐字一致(单会话路径零变化)。
func (r *reg) ListFor(ctx context.Context) []sdk.ToolDefinition {
	r.mu.RLock()
	f := r.ctxFilter
	r.mu.RUnlock()
	if f == nil {
		return r.List()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]sdk.ToolDefinition, 0, len(r.order))
	for _, n := range r.order {
		def := r.tools[n].Definition()
		if r.filter != nil && !r.filter.visible(def) {
			continue
		}
		if !f(ctx, def) {
			continue
		}
		out = append(out, def)
	}
	return out
}

// List 返回模型可见的工具定义(已应用可见性过滤)。
func (r *reg) List() []sdk.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]sdk.ToolDefinition, 0, len(r.order))
	for _, n := range r.order {
		def := r.tools[n].Definition()
		if r.filter != nil && !r.filter.visible(def) {
			continue
		}
		out = append(out, def)
	}
	return out
}

// ListAll 全部已注册工具(不过滤):管理面(注册状态/安装列表)用 ——
// “注册了什么”与“模型看得见什么”是两件事。
func (r *reg) ListAll() []sdk.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]sdk.ToolDefinition, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.tools[n].Definition())
	}
	return out
}

// Get 取回单个工具定义。
// **刻意不应用 filter**:本方法是裁决面(policy-guard 拿真实目标工具定义做路径/审批裁决)
// 与插件自查面 —— 过滤它会让被排除的工具连裁决都拿不到定义(见 sdk.ToolCatalogue 注释)。
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
	filter := r.filter
	ctxFilter := r.ctxFilter
	r.mu.RUnlock()
	if ok && ctxFilter != nil && !ctxFilter(ctx, t.Definition()) {
		res := &sdk.ToolResult{
			Error:   fmt.Sprintf("工具 %q 在**本会话**的角色下未被授权:该会话用的角色把它排除了(可在「设置 → 角色 → 工具」里恢复,或给这个会话换一个角色)", name),
			Content: "{}",
		}
		r.broadcastResult(ctx, name, res)
		return res, nil
	}
	if !ok {
		res := &sdk.ToolResult{Error: fmt.Sprintf("工具 %q 不存在 (对应插件可能已卸载/未启用;可经 /plugins list 排查)", name), Content: "{}"}
		r.broadcastResult(ctx, name, res)
		return res, nil
	}
	// 可见性过滤:只过 List 不够 —— 模型会把历史上下文里出现过的工具名再叫一次。
	// 文案必须与“不存在”**区分开**(否则用户会把“角色没授权”当成插件坏了去查安装),
	// 并且**在 tools/pre-execute 之前**拒:被排除的工具不应进入审批/沙箱裁决
	// (否则会弹一次毫无意义的确认框)。
	if filter != nil && !filter.visible(t.Definition()) {
		res := &sdk.ToolResult{
			Error:   fmt.Sprintf("工具 %q 已被当前角色排除(未授权):可在「设置 → 角色 → 工具」里恢复,或切换到其它角色", name),
			Content: "{}",
		}
		r.broadcastResult(ctx, name, res)
		return res, nil
	}

	// 1. tools/pre-execute:waterfall veto 拦截
	call := &sdk.ToolCallEvent{ID: newCallID(), Name: name, Arguments: args}
	if r.c != nil {
		// 同样 nil 守卫:裸注册表(单测/嵌入)没有总线,不该在 pre-execute 上崩。
		if _, err := r.c.Emit(ctx, "tools/pre-execute", call, sdk.Waterfall); err != nil {
			// 「用户按了停止」不是策略拒绝:blocked 的潜台词是「换个写法重试」,
			// 而中止的潜台词是「这轮结束了」—— 混用会让每次停止都跳一条红色错误(见 sdk/aborted.go)。
			text := "blocked: " + err.Error()
			if sdk.IsAborted(err) {
				text = err.Error()
			}
			res := &sdk.ToolResult{Error: text, Content: "{}"}
			r.broadcastResult(ctx, name, res)
			return res, nil
		}
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

	// 3. tools/post-execute:waterfall(可改写结果);裸注册表无总线 ⇒ 跳过(同上)
	if r.c != nil {
		if _, err := r.c.Emit(ctx, "tools/post-execute", res, sdk.Waterfall); err != nil {
			res = &sdk.ToolResult{Error: "post-execute blocked: " + err.Error(), Content: "{}"}
		}
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
	// nil 守卫:裸注册表(单测/嵌入)没有 ctx,拿不到沙箱 —— 走到"无沙箱"分支即可,
	// 不该 panic(拒绝路径已经 return,只有真正放行的工具才会到这里)。
	if r.c == nil {
		return sdk.WithSandboxHint(ctx, sdk.SandboxHint{Root: root})
	}
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
	// ctx 为 nil 时不广播:裸构造的注册表(单测/嵌入)没有总线,不能因此 panic
	// —— 被排除的工具/不存在的工具都要走这里(拒绝路径同样该让前端看到)。
	if r.c == nil {
		return
	}
	_, _ = r.c.Emit(ctx, "tool/result", &sdk.ToolResultEvent{Name: name, Content: res.Content, Error: res.Error}, sdk.Emit)
}

var callSeq uint64

func newCallID() string {
	return fmt.Sprintf("call_%d", atomic.AddUint64(&callSeq, 1))
}
