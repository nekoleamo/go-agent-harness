// 角色级权限收紧(第九十二批):角色只能把审批/沙箱**往里收**。
//
// 钉四件事:
//  1. 合成顺序「声明档 → sync 联动 → 角色收紧」:全局 open + sync 把沙箱放大到
//     full-access 时,角色的 read-only 必须仍然生效(角色档是下限,任何路径不得更松);
//  2. **每次裁决现算**:改角色定义/切角色下一毫秒就生效,不需要重装插件
//     (缓存会重犯第八十七批 P1-3:`/plugins off|on host-roles` 后策略冻结到重启);
//  3. 审批收紧到 strict = 危险命令**直接拒**,不再弹一次确认框;
//  4. 角色档绝不放宽全局(方向坑:沙箱档的自然序与松紧序相反)。
package policyguard

import (
	"context"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// roleStub 只要 Current/Get 的角色服务桩(其余方法由内嵌接口兜底:本测试不走那些路径)。
// spec 可运行期改 —— 这正是"现算"要钉的场景(改角色定义不必重装插件)。
type roleStub struct {
	sdk.RoleService
	cur  string
	spec sdk.RoleSpec
}

func (s *roleStub) Current() string { return s.cur }

func (s *roleStub) Get(id string) (sdk.RoleSpec, bool) {
	if id == "" || id != s.spec.ID {
		return sdk.RoleSpec{}, false // 偏好里的角色读不出来:按"不收紧"处理(坏角色在别处显式可见)
	}
	return s.spec, true
}

func sandboxOf(t *testing.T, c sdk.Ctx) sdk.Sandbox {
	t.Helper()
	var sb sdk.Sandbox
	if err := c.Inject("ctx.sandbox", &sb); err != nil {
		t.Fatal(err)
	}
	return sb
}

func approvalOf(t *testing.T, c sdk.Ctx) sdk.ApprovalService {
	t.Helper()
	var ap sdk.ApprovalService
	if err := c.Inject("ctx.approval", &ap); err != nil {
		t.Fatal(err)
	}
	return ap
}

// TestRoleTightensSandboxOverLinkage 全局 open + sync=true(沙箱被联动放大到 full-access)时,
// 角色的 read-only 仍然生效;角色一撤销立刻回到联动结果 —— 一次装配覆盖"收紧""现算""撤销"。
func TestRoleTightensSandboxOverLinkage(t *testing.T) {
	c := build(t, &fakeConfirm{resp: true}, map[string]any{"approval": "open", "sandbox": "workspace-write", "sync": true})
	rs := &roleStub{spec: sdk.RoleSpec{ID: "audit", Sandbox: "read-only"}}
	if err := c.Provide("ctx.roles", sdk.RoleService(rs)); err != nil {
		t.Fatal(err)
	}
	sb := sandboxOf(t, c)
	es, ok := sb.(sdk.EffectiveSandbox)
	if !ok {
		t.Fatal("SandboxPolicy 应实现 sdk.EffectiveSandbox")
	}
	if got := es.EffectiveMode(); got != sdk.SandboxFullAccess {
		t.Fatalf("未启用角色时:open + sync 应联动成 full-access,got %s", got)
	}

	rs.cur = "audit"
	if got := es.EffectiveMode(); got != sdk.SandboxReadOnly {
		t.Fatalf("角色 read-only 必须盖过联动放大,got %s", got)
	}
	if got := sb.(sdk.EffectiveSource).EffectiveFrom(); got != sdk.TierSourceRole {
		t.Fatalf("偏离来源应报角色收紧,got %q", got)
	}
	if err := sb.ValidatePath("out.txt"); err == nil {
		t.Fatal("角色收紧到 read-only 后写路径应被拒")
	}

	// 撤销角色 = 立刻回到联动档(现算,不留缓存)
	rs.cur = ""
	if got := es.EffectiveMode(); got != sdk.SandboxFullAccess {
		t.Fatalf("撤销角色后应立刻回到联动档,got %s", got)
	}

	// 改角色定义(不重装插件)同样立刻生效
	rs.cur = "audit"
	rs.spec.Sandbox = "" // 角色改成"不收紧"
	if got := es.EffectiveMode(); got != sdk.SandboxFullAccess {
		t.Fatalf("改角色定义后应立刻生效(不得缓存),got %s", got)
	}
}

// TestRoleCannotRelaxSandbox 全局比角色更严时,角色档不得把权限放大。
// 方向坑:沙箱档的自然序(read-only < workspace-write < full-access)与松紧序相反,
// 写反了就是"角色把全局 read-only 放宽成 workspace-write"—— 静默提权。
func TestRoleCannotRelaxSandbox(t *testing.T) {
	c := build(t, nil, map[string]any{"sandbox": "read-only", "sync": false})
	rs := &roleStub{cur: "dev", spec: sdk.RoleSpec{ID: "dev", Sandbox: "workspace-write"}}
	if err := c.Provide("ctx.roles", sdk.RoleService(rs)); err != nil {
		t.Fatal(err)
	}
	sb := sandboxOf(t, c)
	es := sb.(sdk.EffectiveSandbox)
	if got := es.EffectiveMode(); got != sdk.SandboxReadOnly {
		t.Fatalf("角色档不得放宽全局:期望 read-only,got %s", got)
	}
	if err := sb.ValidatePath("out.txt"); err == nil {
		t.Fatal("全局 read-only 时写路径仍应被拒(角色声明 workspace-write 不算数)")
	}
	if got := sb.(sdk.EffectiveSource).EffectiveFrom(); got == sdk.TierSourceRole {
		t.Fatalf("角色档没起作用时不该报\"角色收紧\"(来源 %q)", got)
	}
}

// TestRoleTightensApproval 角色 strict 审批 = 危险命令**直接拒**,不弹确认框。
// 对照组(未启用角色)按全局 smart 弹一次 —— 证明"没弹"来自角色档,而不是确认服务没接上。
func TestRoleTightensApproval(t *testing.T) {
	cf := &countingConfirm{resp: true}
	c := build(t, cf, map[string]any{"approval": "smart", "sandbox": "full-access"})
	rs := &roleStub{spec: sdk.RoleSpec{ID: "audit", Approval: "strict"}}
	if err := c.Provide("ctx.roles", sdk.RoleService(rs)); err != nil {
		t.Fatal(err)
	}
	ap := approvalOf(t, c)
	ea, ok := ap.(sdk.EffectiveApproval)
	if !ok {
		t.Fatal("ApprovalPolicy 应实现 sdk.EffectiveApproval")
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	danger := `{"command":"rm -rf /tmp/x"}`

	// 对照组:未启用角色 → 全局 smart → 弹确认(允许) → 执行
	res, err := tools.Execute(context.Background(), "shell", danger)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("smart 档用户允许后应执行,got %+v", res)
	}
	if cf.count() != 1 {
		t.Fatalf("smart 档应弹一次确认,got %d", cf.count())
	}

	// 角色收紧到 strict → 直接拒,且**一次都不问**
	rs.cur = "audit"
	if got := ea.EffectiveMode(); got != sdk.ApprovalStrict {
		t.Fatalf("角色收紧后有效审批档应为 strict,got %s", got)
	}
	res, err = tools.Execute(context.Background(), "shell", danger)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == "" {
		t.Fatal("角色收紧到 strict 后危险命令应被直接拒")
	}
	if cf.count() != 1 {
		t.Fatalf("strict 档不该再弹确认框(calls=%d)", cf.count())
	}
	if got := ap.Mode(); got != sdk.ApprovalSmart {
		t.Fatalf("Mode() 应仍是声明档 smart(展示/持久化原义),got %s", got)
	}
	if got := ap.(sdk.EffectiveSource).EffectiveFrom(); got != sdk.TierSourceRole {
		t.Fatalf("审批偏离来源应报角色收紧,got %q", got)
	}
}

// TestRoleTightenWithoutRoleService 未装配 ctx.roles(极简宿主/headless 无角色插件):
// 行为与第九十二批之前逐字一致 —— 不收紧、不报错、不缓存 nil。
func TestRoleTightenWithoutRoleService(t *testing.T) {
	c := build(t, nil, map[string]any{"approval": "open", "sandbox": "workspace-write", "sync": true})
	sb := sandboxOf(t, c)
	if got := sb.(sdk.EffectiveSandbox).EffectiveMode(); got != sdk.SandboxFullAccess {
		t.Fatalf("无角色服务时应只走联动:期望 full-access,got %s", got)
	}
	if got := sb.(sdk.EffectiveSource).EffectiveFrom(); got != sdk.TierSourceApproval {
		t.Fatalf("无角色时偏离来源应报联动,got %q", got)
	}
	if got := approvalOf(t, c).(sdk.EffectiveApproval).EffectiveMode(); got != sdk.ApprovalOpen {
		t.Fatalf("无角色时审批有效档 = 声明档,got %s", got)
	}
}
