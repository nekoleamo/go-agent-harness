// Package ctx 实现 sdk.Ctx:服务容器 + 事件总线视图。
// 插件持有的 Ctx 本质是宿主上下文的一角:服务注册表提供依赖注入,EventBus 提供扩展点。
package ctx

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Ctx 是宿主的运行上下文实现。
type Ctx struct {
	logger *slog.Logger
	bus    interface {
		Subscribe(name string, fn sdk.AnyListener) sdk.Disposer
		Emit(ctx context.Context, name string, payload any, mode sdk.DispatchMode) (any, error)
	}

	mu       sync.RWMutex
	services map[string]any
}

// New 创建宿主上下文。bus 接受 core/event.Bus(以最小接口隔离实现)。
func New(logger *slog.Logger, bus interface {
	Subscribe(name string, fn sdk.AnyListener) sdk.Disposer
	Emit(ctx context.Context, name string, payload any, mode sdk.DispatchMode) (any, error)
}) *Ctx {
	return &Ctx{logger: logger, bus: bus, services: make(map[string]any)}
}

// Provide 注册具名服务。同名重复注册返回错误(注册即副作用,不允许覆盖)。
func (c *Ctx) Provide(key string, svc any) error {
	if key == "" {
		return fmt.Errorf("ctx: empty service key")
	}
	if svc == nil {
		return fmt.Errorf("ctx: nil service for %q", key)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.services[key]; ok {
		return fmt.Errorf("ctx: service %q already provided", key)
	}
	c.services[key] = svc
	return nil
}

// Inject 类型化取回服务。out 必须是 *T 指针。
func (c *Ctx) Inject(key string, out any) error {
	c.mu.RLock()
	svc, ok := c.services[key]
	c.mu.RUnlock()
	if !ok {
		return fmt.Errorf("ctx: service %q not provided", key)
	}
	return assign(key, svc, out)
}

func assign(key string, svc, out any) error {
	if out == nil {
		return fmt.Errorf("ctx: nil out for %q", key)
	}
	// 反射赋值而非类型断言,使 Inject 能接受任何 *T
	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return fmt.Errorf("ctx: out for %q must be non-nil pointer", key)
	}
	target := rv.Elem()
	if !target.CanSet() {
		return fmt.Errorf("ctx: out for %q is not settable", key)
	}
	sv := reflect.ValueOf(svc)
	if !sv.Type().AssignableTo(target.Type()) {
		return fmt.Errorf("ctx: service %q type %T not assignable to %v", key, svc, target.Type())
	}
	target.Set(sv)
	return nil
}

// Subscribe 订阅事件(透传 EventBus)。
func (c *Ctx) Subscribe(name string, fn sdk.AnyListener) sdk.Disposer {
	return c.bus.Subscribe(name, fn)
}

// Emit 分发事件(透传 EventBus)。
func (c *Ctx) Emit(ctx context.Context, name string, payload any, mode sdk.DispatchMode) (any, error) {
	return c.bus.Emit(ctx, name, payload, mode)
}

// Logger 返回插件日志。
func (c *Ctx) Logger() *slog.Logger {
	return c.logger
}

// ListServiceKeys 返回已注册服务键(调试/--dump 用)。
func (c *Ctx) ListServiceKeys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	keys := make([]string, 0, len(c.services))
	for k := range c.services {
		keys = append(keys, k)
	}
	return keys
}
