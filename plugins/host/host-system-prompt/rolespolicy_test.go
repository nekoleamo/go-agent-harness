// 角色策略可选能力的查询语义单测:host-roles 可被用户卸载/重载,判定不能冻结在卸载前。
package hostsystemprompt

import (
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubRoleSvc 只实现本用例用得到的那两个方法;其余经嵌入的接口兜底
// (嵌入 nil 接口:若被调到即 panic,正好暴露用例越界)。
type stubRoleSvc struct {
	sdk.RoleService
	excludeGlobal bool
}

func (s *stubRoleSvc) InheritGlobalInstructions() bool { return !s.excludeGlobal }

// TestRolesPolicyNotCachedAcrossUnload 卸载 host-roles 后不得再沿用旧指针:
// 旧实例不再 Refresh(角色态是内存快照)却仍可注入,缓存住它就会把
// 「当前角色要不要注入全局指令」永久冻结在卸载那一刻,两种方向都错且只能重启恢复。
func TestRolesPolicyNotCachedAcrossUnload(t *testing.T) {
	c := newFakeCtx()
	if err := c.Provide("ctx.roles", &stubRoleSvc{excludeGlobal: true}); err != nil {
		t.Fatal(err)
	}
	s := &Service{ctx: c}
	p := s.rolesPolicy()
	if p == nil || p.InheritGlobalInstructions() {
		t.Fatalf("应查到角色策略且为 exclude_global(不注入全局指令): %v", p)
	}
	// 卸载 host-roles(= ctx.roles 消失)
	c.mu.Lock()
	delete(c.svcs, "ctx.roles")
	c.mu.Unlock()
	if p := s.rolesPolicy(); p != nil {
		t.Fatal("角色服务卸载后不应再沿用旧指针(会把 exclude_global 判定冻结在卸载前)")
	}
	// 重载新实例 → 必须改用新实例的答案
	if err := c.Provide("ctx.roles", &stubRoleSvc{excludeGlobal: false}); err != nil {
		t.Fatal(err)
	}
	if p := s.rolesPolicy(); p == nil || !p.InheritGlobalInstructions() {
		t.Fatalf("重载后应改用新实例(exclude_global=false → 全局指令照常注入): %v", p)
	}
}
