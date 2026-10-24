package policyguard

// 委派档位快照的**裁决面**(pin):快照只能收紧,永不放宽;
// 没有快照时行为与从前逐字一致。

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestApprovalSnapshotOnlyTightens 审批裁决:快照是上限,不是替换。
func TestApprovalSnapshotOnlyTightens(t *testing.T) {
	open := &ApprovalPolicy{mode: sdk.ApprovalOpen}
	// 基线:没有快照 ⇒ 开放档直接放行(零行为变化)
	if err := open.decide(context.Background(), nil, "删除操作"); err != nil {
		t.Fatalf("无快照的开放档应放行: %v", err)
	}
	// 快照 strict ⇒ 即使现算 open 也必须拒(委派发起时是严格档)
	strictSnap := sdk.WithTierSnapshot(context.Background(), sdk.TierSnapshot{Approval: sdk.ApprovalStrict})
	if err := open.decide(strictSnap, nil, "删除操作"); err == nil {
		t.Fatal("快照 strict 时开放档也必须拒绝 —— 否则委派成了放宽通道")
	}

	// 反方向:现算 strict + 快照 open ⇒ 仍然拒(快照不许放宽)
	strict := &ApprovalPolicy{mode: sdk.ApprovalStrict}
	openSnap := sdk.WithTierSnapshot(context.Background(), sdk.TierSnapshot{Approval: sdk.ApprovalOpen})
	if err := strict.decide(openSnap, nil, "删除操作"); err == nil {
		t.Fatal("快照比现算更松时不得放宽 —— 快照是上限不是替换")
	}
}

// TestSandboxSnapshotOnlyTightens 写路径裁决:快照 read-only 时必须真的拦住写。
func TestSandboxSnapshotOnlyTightens(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	loose := &SandboxPolicy{mode: sdk.SandboxFullAccess, root: root}
	if err := loose.ValidatePathFor(context.Background(), target); err != nil {
		t.Fatalf("无快照的 full-access 应放行: %v", err)
	}
	roSnap := sdk.WithTierSnapshot(context.Background(), sdk.TierSnapshot{Sandbox: sdk.SandboxReadOnly})
	if err := loose.ValidatePathFor(roSnap, target); err == nil {
		t.Fatal("快照 read-only 时写必须被拒(显示档与实际拦截不能不一致)")
	}

	// 反方向:现算只读 + 快照 full-access ⇒ 仍然拒
	ro := &SandboxPolicy{mode: sdk.SandboxReadOnly, root: root}
	faSnap := sdk.WithTierSnapshot(context.Background(), sdk.TierSnapshot{Sandbox: sdk.SandboxFullAccess})
	if err := ro.ValidatePathFor(faSnap, target); err == nil {
		t.Fatal("快照比现算更松时不得放宽")
	}
}

// TestEffectiveModesReflectSnapshot 展示路径也要如实:EffectiveModeFor 返回被快照收紧后的档。
func TestEffectiveModesReflectSnapshot(t *testing.T) {
	sp := &SandboxPolicy{mode: sdk.SandboxFullAccess, root: t.TempDir()}
	ctx := sdk.WithTierSnapshot(context.Background(), sdk.TierSnapshot{Sandbox: sdk.SandboxWorkspace})
	if got := sp.EffectiveModeFor(ctx); got != sdk.SandboxWorkspace {
		t.Fatalf("有效档应被快照收成 workspace-write,得 %q", got)
	}
	ap := &ApprovalPolicy{mode: sdk.ApprovalOpen}
	actx := sdk.WithTierSnapshot(context.Background(), sdk.TierSnapshot{Approval: sdk.ApprovalSmart})
	if got := ap.EffectiveModeFor(actx); got != sdk.ApprovalSmart {
		t.Fatalf("有效审批档应被快照收成 smart,得 %q", got)
	}
}
