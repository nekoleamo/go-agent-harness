// Package sdk 是插件唯一允许依赖的对外稳定接口层。
//
// 约束(设计红线):
//   - 插件只 import sdk,不 import core/(防循环依赖,保证热重载安全与版本兼容)
//   - core/ 实现本包声明的接口,并对插件隐藏实现细节
//   - 本包语义化版本范围校验见 Manifest.APIVersion(对齐 dsc `>=1.0,<2.0`)
package sdk

import (
	"context"
	"log/slog"
)

// Plugin 描述一个可插拔插件:Start 注册副作用,返回 Disposer 供卸载时按序撤销。
// 注册即副作用,卸载即撤销 —— 不修改宿主代码。
type Plugin interface {
	Name() string

	// Start 挂载插件。c 是插件运行上下文(服务容器 + 事件总线视图)。
	// 在 Start 内做的任何注册(服务/订阅/工具)都必须能被返回的 Disposer 完整撤销。
	Start(c Ctx, m *Manifest) (Disposer, error)
}

// Disposer 撤销一次注册的副作用。必须幂等(多次调用无害)。
type Disposer func()

// Manifest 插件元数据(对齐设计文档 §4.2)。
type Manifest struct {
	ID         string   // plugins/<type>-<name>
	Type       string   // llm/tool/policy/agent/host/ui
	APIVersion string   // 语义化版本范围,如 ">=1.0,<2.0"
	Provides   []string // 提供的 ctx 服务键(拓扑排序依据)
	Requires   []string // 依赖的 ctx 服务键(缺失时显式失败,不静默降级)
	Capabilities []string // 声明的能力(如 "tools:execute", "fs:write")
}

// Ctx 是插件可见的运行上下文:服务注册/注入 + 事件总线。
// core 实现本接口,插件只依赖此视图。
type Ctx interface {
	// —— 服务容器 ——

	// Provide 向容器注册一个具名服务(如 "ctx.tools")。同名重复注册返回错误。
	Provide(key string, svc any) error

	// Inject 按类型取回服务。out 是 *T 指针,服务实现必须是 T。
	// 缺失时返回显式错误(对齐"不静默降级"红线)。
	Inject(key string, out any) error

	// —— 事件总线 ——

	// Subscribe 订阅事件。返回的 Disposer 撤销订阅(必须由订阅方持有并归还)。
	Subscribe(name string, fn AnyListener) Disposer

	// Emit 按指定分发模式发出一个事件。waterfall/bail 的错误会原样返回;
	// emit/serial/parallel 中监听器错误仅记日志(或聚合返回,见实现)。
	Emit(ctx context.Context, name string, payload any, mode DispatchMode) (any, error)

	// —— 环境 ——

	// Logger 返回插件日志(宿主经 fanout 挂接 TUI/外部进程)。
	Logger() *slog.Logger
}

// DispatchMode 事件分发模式(对齐 dsh/cordis 事件体系)。
type DispatchMode int

const (
	// Emit 广播通知:通知所有监听器,错误仅记日志,不中断。
	Emit DispatchMode = iota
	// Waterfall 洋葱拦截:监听器可改写 payload 并传给下一个;
	// 任一听听器返回错误即拦截(veto),停止后续委托。
	Waterfall
	// Serial 顺序:依次执行全部监听器,错误记录但继续。
	Serial
	// Bail 短路:顺序执行,任一监听器返回错误即停止并返回该错误。
	Bail
	// Parallel 并发:并发执行全部监听器,任一错误即取消并返回。
	Parallel
)

// AnyListener 监听器签名。ev.Payload 可被 waterfall 链改写以实现拦截语义。
type AnyListener func(ctx context.Context, ev *Event) error

// Event 分发中携带的事件对象。
type Event struct {
	Name    string
	Payload any
	// Result 留给后续扩展(如 tools/post-execute 的结果传递),M1 不强制使用。
	Result any
}

// Factory 插件构造工厂:由 bundle/配置按 id 关联到具体实现。
type Factory func() Plugin
