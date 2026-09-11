// catalogue 注册路径测试:RegisterAll 的真装配行为(注册整 bundle、未知 bundle 空注册、
// 错误上抛不继续、清单副本隔离)与「工厂↔登记 id 一致」不变量。
// 既有 catalogue_test.go 只做声明一致性守卫(不执行 RegisterAll),这里补执行路径。
package catalogue

import (
	"fmt"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeRegistry 记录注册调用的假注册表(实现 RegisterAll 注入接口,零 core 依赖)。
type fakeRegistry struct {
	calls  []string // 已注册的清单 ID(按调用序)
	shared []bool   // 对应调用收到的清单是否就是 catalogue 共享指针(必须全 false)
	mutate func(m *sdk.Manifest)
	failAt int // 第 N 次调用返回错误(0 = 不失败)
}

func (r *fakeRegistry) Register(f sdk.Factory, m *sdk.Manifest) error {
	if f == nil {
		return fmt.Errorf("fakeRegistry: 工厂为 nil(id=%s)", m.ID)
	}
	if d, ok := All[m.ID]; ok {
		r.shared = append(r.shared, m == d.Manifest)
	}
	r.calls = append(r.calls, m.ID)
	if r.mutate != nil {
		r.mutate(m)
	}
	if r.failAt > 0 && len(r.calls) == r.failAt {
		return fmt.Errorf("fakeRegistry: 第 %d 次注册被拒绝", r.failAt)
	}
	return nil
}

// bundleDefs 返回指定 bundle 的登记条目数(期望值的独立来源)。
func bundleDefs(name string) int {
	n := 0
	for _, d := range All {
		if d.Bundle == name {
			n++
		}
	}
	return n
}

// TestRegisterAllRegistersWholeBundle 单一 bundle 全量注册:条数吻合、ID 非空且互不重复、
// 且不串入其它 bundle 的插件(装配层据此按 profile 起停)。
func TestRegisterAllRegistersWholeBundle(t *testing.T) {
	for _, bundle := range []string{"base", "tui", "web", "confirm-fusion"} {
		r := &fakeRegistry{}
		if err := RegisterAll(r, bundle); err != nil {
			t.Fatalf("RegisterAll(%s) 应成功: %v", bundle, err)
		}
		if want := bundleDefs(bundle); len(r.calls) != want {
			t.Fatalf("bundle %s 应注册 %d 个插件,实际 %d", bundle, want, len(r.calls))
		}
		seen := map[string]bool{}
		for _, id := range r.calls {
			if id == "" {
				t.Fatalf("bundle %s 注册了空 ID(清单未填 ID)", bundle)
			}
			if seen[id] {
				t.Fatalf("bundle %s 重复注册 %s", bundle, id)
			}
			if d, ok := All[id]; !ok || d.Bundle != bundle {
				t.Fatalf("bundle %s 注册了不属于它的插件 %s(串 bundle)", bundle, id)
			}
			seen[id] = true
		}
	}
}

// TestRegisterAllUnknownBundleIsNoop 未知 bundle 名 = 空注册(不报错也不误注册):
// 配置写错 bundle 名时不会偷偷装上 base 插件。
func TestRegisterAllUnknownBundleIsNoop(t *testing.T) {
	r := &fakeRegistry{}
	if err := RegisterAll(r, "does-not-exist"); err != nil {
		t.Fatalf("未知 bundle 不应报错: %v", err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("未知 bundle 不应注册任何插件: %v", r.calls)
	}
}

// TestRegisterAllPropagatesError 注册失败必须立即上抛并停止(不静默吞掉半套装配)。
func TestRegisterAllPropagatesError(t *testing.T) {
	r := &fakeRegistry{failAt: 2}
	err := RegisterAll(r, "base")
	if err == nil {
		t.Fatal("注册表拒绝时应上抛错误")
	}
	if len(r.calls) != 2 {
		t.Fatalf("失败后不应继续注册(期望停在 2,实际 %d)", len(r.calls))
	}
}

// TestRegisterAllManifestIsolatedCopy 交给注册表的清单必须是副本:
// 装配层会往清单里写 data(条目配置),若共享 catalogue 的清单指针,
// 一次装配的 data 会泄漏到后续装配/其它 bundle(同 id 复用时行为不可预测)。
func TestRegisterAllManifestIsolatedCopy(t *testing.T) {
	r := &fakeRegistry{mutate: func(m *sdk.Manifest) {
		m.Data = map[string]any{"injected": true}
		m.ID = "被改写"
	}}
	if err := RegisterAll(r, "base"); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) == 0 {
		t.Fatal("应至少注册一个 base 插件")
	}
	if len(r.shared) != len(r.calls) {
		t.Fatalf("有注册调用未在 All 中匹配到共享清单(共 %d 次,比对 %d 次)", len(r.calls), len(r.shared))
	}
	for i, same := range r.shared {
		if same {
			t.Fatalf("第 %d 次注册拿到的是共享清单指针(应传副本): %s", i+1, r.calls[i])
		}
	}
	// 装配层改写副本后,共享清单必须原封不动
	for id, d := range All {
		if d.Manifest.Data != nil {
			t.Fatalf("共享清单 %s 被装配层注入污染: %v", id, d.Manifest.Data)
		}
		if d.Manifest.ID != id {
			t.Fatalf("共享清单 %s 的 ID 被改写为 %q", id, d.Manifest.ID)
		}
	}
}

// TestFactoryMatchesID 每个登记的工厂可实例化,且 Name() 与登记 id 一致、清单 ID 与键一致。
// 防「复制粘贴条目忘改 Name/ID」——此类错误到运行期才炸(加载/卸载 id 不匹配)。
func TestFactoryMatchesID(t *testing.T) {
	for id, d := range All {
		if d.Factory == nil {
			t.Fatalf("%s 未提供工厂(装配必 panic)", id)
		}
		pl := d.Factory()
		if pl == nil {
			t.Fatalf("%s 的工厂返回 nil", id)
		}
		if got := pl.Name(); got != id {
			t.Fatalf("%s 的工厂产出 Name()=%q,与登记 id 不符", id, got)
		}
		if d.Manifest == nil {
			t.Fatalf("%s 未提供清单(RegisterAll 会 panic)", id)
		}
		if d.Manifest.ID != id {
			t.Fatalf("%s 的清单 ID=%q 与键不符", id, d.Manifest.ID)
		}
		if d.Manifest.Type == "" {
			t.Fatalf("%s 的清单缺 Type(管理域/展示派生依赖它)", id)
		}
	}
}
