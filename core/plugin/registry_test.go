package plugin

import (
	"context"
	"errors"
	"log/slog"
	"strings"
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

// TestBlockedByLoaded 卸载防护:仍加载且依赖「我提供的服务键」的插件要被点名。
//
// 为什么这条必须有(而不是"顺手补覆盖"):插拔安全的红线就是「运行期卸载不得破坏
// 依赖者」—— 之前 BlockedByLoaded 是**零覆盖**,也就是那条保证一直只有注释撑着。
// 反向验证:把 BlockedByLoaded 改成恒返回 nil ⇒ 本用例红。
func TestBlockedByLoaded(t *testing.T) {
	r := New()
	var track atomic.Int32
	a := &counter{id: "base", track: &track}
	b := &counter{id: "user", track: &track}
	c := &counter{id: "bystander", track: &track}
	if err := r.Register(func() sdk.Plugin { return a }, m("base", []string{"svc.base"}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(func() sdk.Plugin { return b }, m("user", []string{"svc.user"}, []string{"svc.base"})); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(func() sdk.Plugin { return c }, m("bystander", []string{"svc.by"}, nil)); err != nil {
		t.Fatal(err)
	}
	// 未加载时:卸载没有影响 ⇒ 不算阻塞(否则"注册了就永远卸不掉")
	if got := r.BlockedByLoaded("base"); len(got) != 0 {
		t.Fatalf("未加载的插件不该阻塞任何人,got %v", got)
	}
	// 未注册的 id:空(不 panic)
	if got := r.BlockedByLoaded("nope"); len(got) != 0 {
		t.Fatalf("未知 id 应返回空,got %v", got)
	}
	if err := r.StartAll(newTestCtx(t)); err != nil {
		t.Fatal(err)
	}
	// 全加载:只有「真的 requires 我提供的键」的那个被点名;旁观者不算
	if got := r.BlockedByLoaded("base"); len(got) != 1 || got[0] != "user" {
		t.Fatalf("应只阻塞依赖 svc.base 的 user,got %v", got)
	}
	if got := r.BlockedByLoaded("user"); len(got) != 0 {
		t.Fatalf("没人依赖 user 提供的键,不该被阻塞,got %v", got)
	}
	// 依赖者卸载后就不再阻塞(防护要随状态变,不能一次性冻结)
	r.Dispose("user")
	if got := r.BlockedByLoaded("base"); len(got) != 0 {
		t.Fatalf("依赖者已卸载,不该再阻塞,got %v", got)
	}
}

// TestOrderAndDescribe 拓扑序快照与 describe 汇总(运维面:/plugins 与状态栏读它们)。
func TestOrderAndDescribe(t *testing.T) {
	r := New()
	var track atomic.Int32
	// 刻意乱序注册:b 依赖 a ⇒ 拓扑序必须把 a 排前
	for _, id := range []string{"b", "c", "a"} {
		pl := &counter{id: id, track: &track}
		var req []string
		if id == "b" {
			req = []string{"svc.a"}
		}
		if err := r.Register(func() sdk.Plugin { return pl }, m(id, []string{"svc." + id}, req)); err != nil {
			t.Fatal(err)
		}
	}
	// 启动前:order 为空(Order 报的是"最近一次拓扑序")
	if got := r.Order(); len(got) != 0 {
		t.Fatalf("未启动时 order 应为空,got %v", got)
	}
	if err := r.StartAll(newTestCtx(t)); err != nil {
		t.Fatal(err)
	}
	order := r.Order()
	if len(order) != 3 {
		t.Fatalf("order 应含 3 项,got %v", order)
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	if pos["a"] > pos["b"] {
		t.Fatalf("拓扑序应把 a 排在 b 前,got %v", order)
	}
	// Order 返回**副本**:调用方改它不得影响注册表(否则运维面板一次打印就把顺序搞乱)
	if len(order) > 0 {
		order[0] = "hacked"
		if r.Order()[0] == "hacked" {
			t.Fatal("Order 必须返回副本")
		}
	}
	desc := r.Describe()
	for _, id := range []string{"a", "b", "c"} {
		if !strings.Contains(desc, id+" [host] started") {
			t.Fatalf("describe 缺 %s 的加载态条目:\n%s", id, desc)
		}
	}
	if !strings.Contains(desc, "requires=[svc.a]") {
		t.Fatalf("describe 应列出 requires(运维靠它看依赖):\n%s", desc)
	}
	if strings.HasSuffix(desc, "\n") {
		t.Fatalf("describe 不应带尾换行(调用方自己决定排版):%q", desc)
	}
	// 卸载后同一份 describe 要变 stopped(运维面不能报旧状态)
	r.Dispose("c")
	if d := r.Describe(); !strings.Contains(d, "c [host] stopped") {
		t.Fatalf("卸载后 describe 应显示 stopped:\n%s", d)
	}
}
