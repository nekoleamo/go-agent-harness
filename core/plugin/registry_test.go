package plugin

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func newTestCtx(t *testing.T) sdk.Ctx {
	t.Helper()
	return ctx.New(slog.New(slog.DiscardHandler), event.New(slog.New(slog.DiscardHandler)))
}

func m(id string, provides, requires []string) *sdk.Manifest {
	return &sdk.Manifest{ID: id, Type: "host", APIVersion: ">=1.0,<2.0", Provides: provides, Requires: requires}
}

// counter 插件:Start 时注册副作用(track 计数 +1)并订阅事件;dispose 撤销(-1)。
type counter struct {
	id    string
	track *atomic.Int32
}

func (p *counter) Name() string { return p.id }
func (p *counter) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	p.track.Add(1)
	_ = c.Provide("svc."+p.id, p)
	sub := c.Subscribe("ev."+p.id, func(ctx context.Context, ev *sdk.Event) error { return nil })
	return func() {
		sub()
		p.track.Add(-1)
	}, nil
}

// broken 插件:Start 必然失败(用于回滚测试)。
type broken struct{}

func (b *broken) Name() string { return "bad" }
func (b *broken) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	return nil, errors.New("broken start")
}

func TestAPIVersionEnforced(t *testing.T) {
	r := New()
	// 无 apiVersion → 拒绝
	bad := &sdk.Manifest{ID: "no-ver", Type: "host", Provides: []string{"ctx.x"}}
	if err := r.Register(func() sdk.Plugin { return &counter{id: "no-ver"} }, bad); err == nil {
		t.Fatal("缺 apiVersion 应拒绝")
	}
	// 不含 SDK 1.0 的范围 → 拒绝
	incompat := &sdk.Manifest{ID: "old", Type: "host", APIVersion: ">=0.5,<1.0"}
	if err := r.Register(func() sdk.Plugin { return &counter{id: "old"} }, incompat); err == nil {
		t.Fatal("不兼容的 apiVersion 应拒绝")
	}
	// 合法范围 → 通过
	ok := &sdk.Manifest{ID: "ok", Type: "host", APIVersion: ">=1.0,<2.0"}
	if err := r.Register(func() sdk.Plugin { return &counter{id: "ok"} }, ok); err != nil {
		t.Fatalf("合法 apiVersion 应通过: %v", err)
	}
}

func TestTopoOrder(t *testing.T) {
	r := New()
	r.Register(func() sdk.Plugin { return &counter{id: "b"} }, m("b", []string{"ctx.tools"}, []string{"ctx.llm"}))
	r.Register(func() sdk.Plugin { return &counter{id: "a"} }, m("a", []string{"ctx.llm"}, nil))
	order, err := r.AssertAvailable()
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("拓扑序应先提供者后依赖者,got %v", order)
	}
}

func TestMissingRequireFailsExplicitly(t *testing.T) {
	r := New()
	r.Register(func() sdk.Plugin { return &counter{id: "b"} }, m("b", nil, []string{"ctx.missing"}))
	if _, err := r.AssertAvailable(); err == nil {
		t.Fatal("缺失依赖应显式失败,不静默降级")
	}
}

func TestDuplicateProvideFails(t *testing.T) {
	r := New()
	r.Register(func() sdk.Plugin { return &counter{id: "a"} }, m("a", []string{"ctx.x"}, nil))
	r.Register(func() sdk.Plugin { return &counter{id: "b"} }, m("b", []string{"ctx.x"}, nil))
	if _, err := r.AssertAvailable(); err == nil {
		t.Fatal("重复提供同一服务应报错")
	}
}

func TestCycleDetected(t *testing.T) {
	r := New()
	r.Register(func() sdk.Plugin { return &counter{id: "a"} }, m("a", []string{"s1"}, []string{"s2"}))
	r.Register(func() sdk.Plugin { return &counter{id: "b"} }, m("b", []string{"s2"}, []string{"s1"}))
	if _, err := r.AssertAvailable(); err == nil {
		t.Fatal("循环依赖应报错")
	}
}

func TestStartAllRollbackOnFailure(t *testing.T) {
	r := New()
	var goodTrack atomic.Int32
	r.Register(func() sdk.Plugin { return &counter{id: "good", track: &goodTrack} }, m("good", []string{"ctx.g"}, nil))
	r.Register(func() sdk.Plugin { return &broken{} }, m("bad", nil, nil))

	if err := r.StartAll(newTestCtx(t)); err == nil {
		t.Fatal("bad 启动失败应使 StartAll 报错")
	}
	if goodTrack.Load() != 0 {
		t.Fatalf("bad 失败应回滚 good 的副作用,track=%d", goodTrack.Load())
	}
}

func TestDisposeReversibleAndIdempotent(t *testing.T) {
	r := New()
	var track atomic.Int32
	r.Register(func() sdk.Plugin { return &counter{id: "p", track: &track} }, m("p", []string{"ctx.p"}, nil))
	c := newTestCtx(t)
	if err := r.StartAll(c); err != nil {
		t.Fatal(err)
	}
	if track.Load() != 1 {
		t.Fatalf("启动后副作用应生效,track=%d", track.Load())
	}
	r.Dispose("p")
	r.Dispose("p") // 幂等
	if track.Load() != 0 {
		t.Fatalf("dispose 后副作用应撤销,track=%d", track.Load())
	}
}

func TestReloadDisposesOldInstance(t *testing.T) {
	r := New()
	var track atomic.Int32
	r.Register(func() sdk.Plugin { return &counter{id: "p", track: &track} }, m("p", []string{"ctx.p"}, nil))
	c := newTestCtx(t)
	if err := r.StartAll(c); err != nil {
		t.Fatal(err)
	}
	if err := r.Reload(c, "p"); err != nil {
		t.Fatalf("reload 失败: %v", err)
	}
	// reload = dispose 旧 + start 新 → 计数回到 1,实例数仍 1
	if track.Load() != 1 {
		t.Fatalf("reload 应 dispose 旧实例并 start 新实例,track=%d", track.Load())
	}
	if len(r.Snapshot()) != 1 {
		t.Fatalf("reload 后实例数应仍为 1,got %v", r.Snapshot())
	}
}

// disposeOrder 插件:记录 dispose 顺序。
type disposeOrder struct {
	id  string
	seq *[]string
	mu  *sync.Mutex
}

func (p *disposeOrder) Name() string { return p.id }
func (p *disposeOrder) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	_ = c.Provide("svc."+p.id, p)
	return func() {
		p.mu.Lock()
		*p.seq = append(*p.seq, p.id)
		p.mu.Unlock()
	}, nil
}

func TestDisposeAllReverseOrder(t *testing.T) {
	r := New()
	var order []string
	var mu sync.Mutex
	mk := func(id string, req []string) *disposeOrder {
		return &disposeOrder{id: id, seq: &order, mu: &mu}
	}
	r.Register(func() sdk.Plugin { return mk("b", nil) }, m("b", []string{"ctx.b"}, []string{"ctx.a"}))
	r.Register(func() sdk.Plugin { return mk("a", nil) }, m("a", []string{"ctx.a"}, nil))
	if err := r.StartAll(newTestCtx(t)); err != nil {
		t.Fatal(err)
	}
	r.DisposeAll()
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "b" || order[1] != "a" {
		t.Fatalf("dispose 应逆拓扑序(b→a),got %v", order)
	}
}
