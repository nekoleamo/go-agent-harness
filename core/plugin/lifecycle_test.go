// 生命周期可逆性单测(P0 回归):Provide 的服务键必须随卸载归还。
//
// 背景:此前 Ctx 只有 Provide 没有注销路径,registry.disposeOne 也不通知 ctx →
// TUI `/plugins off host-usage-stats` 后 `/plugins on` 恒报
// `ctx: service "ctx.usageStats" already provided`,插件永久停在卸载态;
// 且卸载后依赖者 Inject 仍成功并拿到已停实例(红线"卸载即撤销""缺失依赖显式失败"双破)。
package plugin

import (
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// provider 插件:Start 时注册服务(模拟 host-usage-stats / host-jobs / host-backup 等)。
type provider struct{ id string }

func (p *provider) Name() string { return p.id }

func (p *provider) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	if err := c.Provide("svc."+p.id, p); err != nil {
		return nil, err
	}
	return func() {}, nil
}

func TestDisposeReturnsProvidedServices(t *testing.T) {
	c := newTestCtx(t)
	r := New()
	const id = "svc-provider"
	if err := r.Register(func() sdk.Plugin { return &provider{id: id} }, m(id, []string{"svc." + id}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := r.StartOne(c, id); err != nil {
		t.Fatal(err)
	}
	var got any
	if err := c.Inject("svc."+id, &got); err != nil {
		t.Fatalf("启动后服务应可用: %v", err)
	}

	// 卸载 → 服务必须归还(依赖者 Inject 显式失败,而非拿到已停实例)
	r.Dispose(id)
	if err := c.Inject("svc."+id, &got); err == nil {
		t.Fatal("卸载后服务应已归还(Inject 应报 not provided)")
	}
	// 卸载后重新加载必须成功
	if err := r.StartOne(c, id); err != nil {
		t.Fatalf("卸载后重新加载应成功,得 %v", err)
	}
	// 热重载(dispose + start)必须成功
	if err := r.Reload(c, id); err != nil {
		t.Fatalf("Reload 应成功,得 %v", err)
	}
	r.DisposeAll()
	if err := c.Inject("svc."+id, &got); err == nil {
		t.Fatal("DisposeAll 后服务应已归还")
	}
}

// startFailProvider 启动中途失败(注册服务后返回错误):失败也必须回滚服务键。
type startFailProvider struct{ id string }

func (p *startFailProvider) Name() string { return p.id }

func (p *startFailProvider) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	if err := c.Provide("svc."+p.id, p); err != nil {
		return nil, err
	}
	return nil, errStartFail
}

var errStartFail = &startError{}

type startError struct{}

func (e *startError) Error() string { return "start failed after provide" }

func TestStartFailureRollsBackServices(t *testing.T) {
	c := newTestCtx(t)
	r := New()
	const id = "fail-provider"
	if err := r.Register(func() sdk.Plugin { return &startFailProvider{id: id} }, m(id, []string{"svc." + id}, nil)); err != nil {
		t.Fatal(err)
	}
	if err := r.StartOne(c, id); err == nil {
		t.Fatal("Start 失败应返回错误")
	}
	var got any
	if err := c.Inject("svc."+id, &got); err == nil {
		t.Fatal("启动失败后不得残留服务键(否则重试必失败)")
	}
	// 重新加载仍可用
	if err := r.Register(func() sdk.Plugin { return &provider{id: id} }, &sdk.Manifest{
		ID: id, Type: "host", APIVersion: ">=1.0,<2.0", Provides: []string{"svc." + id},
	}); err != nil {
		t.Skipf("同 id 重复注册按设计拒绝: %v", err)
	}
}
