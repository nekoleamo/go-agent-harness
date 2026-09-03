// Package event 实现 EventBus:五种分发模式(emit/waterfall/serial/bail/parallel)。
// 设计对齐 dsh/cordis 事件体系;监听器 panic 被 recover 并标记故障,不拖垮宿主。
package event

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Bus 是并发安全的事件总线。
type Bus struct {
	logger *slog.Logger

	mu        sync.RWMutex
	listeners map[string][]*entry // 按注册顺序
	seq       uint64
}

type entry struct {
	id uint64
	fn sdk.AnyListener
}

// New 创建事件总线。
func New(logger *slog.Logger) *Bus {
	return &Bus{logger: logger, listeners: make(map[string][]*entry)}
}

// Subscribe 订阅事件,返回幂等 Disposer。
func (b *Bus) Subscribe(name string, fn sdk.AnyListener) sdk.Disposer {
	if fn == nil {
		panic("event: nil listener")
	}
	b.mu.Lock()
	b.seq++
	id := b.seq
	b.listeners[name] = append(b.listeners[name], &entry{id: id, fn: fn})
	b.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			es := b.listeners[name]
			for i, e := range es {
				if e.id == id {
					b.listeners[name] = append(es[:i], es[i+1:]...)
					return
				}
			}
		})
	}
}

// Emit 按模式分发。waterfall/bail/parallel 的错误返回给调用方。
func (b *Bus) Emit(ctx context.Context, name string, payload any, mode sdk.DispatchMode) (any, error) {
	ev := &sdk.Event{Name: name, Payload: payload}

	b.mu.RLock()
	es := make([]*entry, len(b.listeners[name]))
	copy(es, b.listeners[name])
	b.mu.RUnlock()

	if len(es) == 0 {
		return ev, nil
	}

	switch mode {
	case sdk.Emit:
		b.emit(ctx, ev, es) // 广播:错误仅记日志
		return ev, nil
	case sdk.Waterfall:
		for _, e := range es {
			if err := b.invoke(e, ctx, ev); err != nil {
				return ev, err // veto:拦截,停止委托
			}
			// 监听器可改写 ev.Payload,传给下一个
		}
		return ev, nil
	case sdk.Serial:
		for _, e := range es {
			_ = b.invoke(e, ctx, ev) // 错误记录但继续
		}
		return ev, nil
	case sdk.Bail:
		for _, e := range es {
			if err := b.invoke(e, ctx, ev); err != nil {
				return ev, err
			}
		}
		return ev, nil
	case sdk.Parallel:
		return ev, b.parallel(ctx, ev, es)
	default:
		return ev, fmt.Errorf("event: unknown dispatch mode %d", mode)
	}
}

func (b *Bus) emit(ctx context.Context, ev *sdk.Event, es []*entry) {
	for _, e := range es {
		_ = b.invoke(e, ctx, ev)
	}
}

// parallel 并发执行全部监听器;第一个错误取消其余 ctx 并返回。
func (b *Bus) parallel(ctx context.Context, ev *sdk.Event, es []*entry) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg      sync.WaitGroup
		errOnce sync.Once
		first   error
	)
	for _, e := range es {
		wg.Add(1)
		go func(e *entry) {
			defer wg.Done()
			if err := b.invoke(e, ctx, ev); err != nil {
				errOnce.Do(func() {
					first = err
					cancel()
				})
			}
		}(e)
	}
	wg.Wait()
	return first
}

// invoke 执行单个监听器,recover panic 并标记故障(记日志含堆栈)。
func (b *Bus) invoke(e *entry, ctx context.Context, ev *sdk.Event) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("event: listener panicked: %v", r)
			b.logger.Error("event listener panicked",
				"event", ev.Name, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	return e.fn(ctx, ev)
}
