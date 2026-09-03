package event

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func newBus(t *testing.T) *Bus {
	t.Helper()
	return New(slog.New(slog.DiscardHandler))
}

func TestEmitBroadcast(t *testing.T) {
	// emit:广播通知全部监听器,错误不中断
	b := newBus(t)
	var n atomic.Int32
	fail := func(ctx context.Context, ev *sdk.Event) error { return errors.New("boom") }
	b.Subscribe("t", func(ctx context.Context, ev *sdk.Event) error { n.Add(1); return nil })
	b.Subscribe("t", fail)
	b.Subscribe("t", func(ctx context.Context, ev *sdk.Event) error { n.Add(1); return nil })

	if _, err := b.Emit(context.Background(), "t", nil, sdk.Emit); err != nil {
		t.Fatalf("emit 模式不应返回错误,got %v", err)
	}
	if n.Load() != 2 {
		t.Fatalf("emit 应广播给全部监听器(错误者除外),got %d", n.Load())
	}
}

func TestWaterfallRewriteAndVeto(t *testing.T) {
	b := newBus(t)
	b.Subscribe("t", func(ctx context.Context, ev *sdk.Event) error {
		ev.Payload = ev.Payload.(int) + 1
		return nil
	})
	b.Subscribe("t", func(ctx context.Context, ev *sdk.Event) error {
		ev.Payload = ev.Payload.(int) + 10
		return nil
	})
	got, err := b.Emit(context.Background(), "t", 1, sdk.Waterfall)
	if err != nil {
		t.Fatal(err)
	}
	ev := got.(*sdk.Event)
	if ev.Payload.(int) != 12 {
		t.Fatalf("waterfall 应顺序改写 payload,got %d", ev.Payload)
	}

	// veto:返回错误即拦截,后续不再执行
	var called int
	b2 := newBus(t)
	b2.Subscribe("t", func(ctx context.Context, ev *sdk.Event) error { called++; return errors.New("veto") })
	b2.Subscribe("t", func(ctx context.Context, ev *sdk.Event) error { called++; return nil })
	_, err = b2.Emit(context.Background(), "t", nil, sdk.Waterfall)
	if err == nil || called != 1 {
		t.Fatalf("waterfall veto 应拦截后续,err=%v called=%d", err, called)
	}
}

func TestSerialContinuesOnError(t *testing.T) {
	b := newBus(t)
	var n atomic.Int32
	b.Subscribe("t", func(ctx context.Context, ev *sdk.Event) error { n.Add(1); return errors.New("e1") })
	b.Subscribe("t", func(ctx context.Context, ev *sdk.Event) error { n.Add(1); return nil })
	if _, err := b.Emit(context.Background(), "t", nil, sdk.Serial); err != nil {
		t.Fatal(err) // serial 错误记录但继续,不返回
	}
	if n.Load() != 2 {
		t.Fatalf("serial 应全部执行,got %d", n.Load())
	}
}

func TestBailShortCircuit(t *testing.T) {
	b := newBus(t)
	var called int
	b.Subscribe("t", func(ctx context.Context, ev *sdk.Event) error { called++; return errors.New("stop") })
	b.Subscribe("t", func(ctx context.Context, ev *sdk.Event) error { called++; return nil })
	if _, err := b.Emit(context.Background(), "t", nil, sdk.Bail); err == nil || called != 1 {
		t.Fatalf("bail 应短路,err=%v called=%d", err, called)
	}
}

func TestParallelRunsAllAndReturnsError(t *testing.T) {
	b := newBus(t)
	var slowDone atomic.Bool
	b.Subscribe("t", func(ctx context.Context, ev *sdk.Event) error {
		time.Sleep(30 * time.Millisecond)
		slowDone.Store(true)
		return nil
	})
	b.Subscribe("t", func(ctx context.Context, ev *sdk.Event) error { return errors.New("fail") })
	_, err := b.Emit(context.Background(), "t", nil, sdk.Parallel)
	if err == nil {
		t.Fatal("parallel 应返回错误")
	}
	if !slowDone.Load() {
		t.Fatal("parallel 应并发执行全部监听器(慢者不被跳过)")
	}
}

func TestSubscriberDisposerIdempotent(t *testing.T) {
	b := newBus(t)
	var n atomic.Int32
	d := b.Subscribe("t", func(ctx context.Context, ev *sdk.Event) error { n.Add(1); return nil })
	d()
	d() // 幂等
	b.Emit(context.Background(), "t", nil, sdk.Emit)
	if n.Load() != 0 {
		t.Fatalf("dispose 后不应再收到事件,got %d", n.Load())
	}
}

func TestListenerPanicRecovered(t *testing.T) {
	b := newBus(t)
	var n atomic.Int32
	b.Subscribe("t", func(ctx context.Context, ev *sdk.Event) error { panic("boom") })
	b.Subscribe("t", func(ctx context.Context, ev *sdk.Event) error { n.Add(1); return nil })
	if _, err := b.Emit(context.Background(), "t", nil, sdk.Emit); err != nil {
		t.Fatalf("emit 不应因监听器 panic 返回错误,got %v", err)
	}
	if n.Load() != 1 {
		t.Fatalf("panic 监听器后的监听器仍应执行,got %d", n.Load())
	}
}
