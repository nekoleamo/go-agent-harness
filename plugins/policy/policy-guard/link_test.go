package policyguard

import (
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// sync=true 联动:open 档 → 沙箱有效 full-access(read-only 下执行器/写路径也放行)。
func TestLinkOpenBypassesReadOnly(t *testing.T) {
	ap := &ApprovalPolicy{mode: sdk.ApprovalOpen}
	p := &SandboxPolicy{root: t.TempDir(), mode: sdk.SandboxReadOnly, sync: true, approval: ap.Mode}
	if err := p.CheckTool("shell"); err != nil {
		t.Fatalf("open 档联动:read-only 下 shell 应放行,got %v", err)
	}
	if err := p.ValidatePath("out.txt"); err != nil {
		t.Fatalf("open 档联动:写路径应放行,got %v", err)
	}
}

// sync=true 联动:strict 档 → 沙箱有效 read-only(full-access 下执行器/写路径也拒绝)。
func TestLinkStrictForcesReadOnly(t *testing.T) {
	ap := &ApprovalPolicy{mode: sdk.ApprovalStrict}
	p := &SandboxPolicy{root: t.TempDir(), mode: sdk.SandboxFullAccess, sync: true, approval: ap.Mode}
	if err := p.CheckTool("shell"); err == nil {
		t.Fatal("strict 档联动:full-access 下 shell 应被拒")
	}
	if err := p.ValidatePath("x.txt"); err == nil {
		t.Fatal("strict 档联动:写路径应被拒")
	}
}

// sync=true 联动:smart 档不覆盖,沙箱按自身档位(现状零变化)。
func TestLinkSmartIndependent(t *testing.T) {
	ap := &ApprovalPolicy{mode: sdk.ApprovalSmart}
	p := &SandboxPolicy{root: t.TempDir(), mode: sdk.SandboxReadOnly, sync: true, approval: ap.Mode}
	if err := p.CheckTool("shell"); err == nil {
		t.Fatal("smart 档不应覆盖:read-only 仍拒 shell")
	}
}

// sync=false:联动关闭,open 档也不覆盖沙箱(独立档,兼容旧行为)。
func TestLinkDisabledKeepsIndependent(t *testing.T) {
	ap := &ApprovalPolicy{mode: sdk.ApprovalOpen}
	p := &SandboxPolicy{root: t.TempDir(), mode: sdk.SandboxReadOnly, sync: false, approval: ap.Mode}
	if err := p.CheckTool("shell"); err == nil {
		t.Fatal("sync=false 时 open 不应覆盖:read-only 仍拒 shell")
	}
}

// 装配级:guard(approval=open, sandbox=read-only, sync=true)下 shell 危险命令放行且不被沙箱拒。
func TestGuardLinkOpenExecutesShell(t *testing.T) {
	c := build(t, &fakeConfirm{resp: true}, map[string]any{
		"approval": "open", "sandbox": "read-only", "sync": true,
	})
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(t.Context(), "shell", `{"command":"rm -rf /tmp/x"}`)
	if err != nil {
		t.Fatalf("流水线应吞 veto 为结构化结果,got err %v", err)
	}
	if res.Error != "" {
		t.Fatalf("open 档联动:危险命令 + read-only 沙箱下应放行,got %+v", res)
	}
}
