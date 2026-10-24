package sdk

// 委派档位快照(pin)的契约:只能更严,永不放宽;没有快照 = 行为零变化。

import (
	"context"
	"testing"
)

// stubApproval 声明档 + 有效档分开给(模拟"会话/角色把档收紧了")。
type stubApproval struct {
	mode ApprovalMode
	eff  ApprovalMode
}

func (s stubApproval) Mode() ApprovalMode          { return s.mode }
func (s stubApproval) SetMode(ApprovalMode)        {} // 测试替身不改状态(staticcheck SA4005)
func (s stubApproval) EffectiveMode() ApprovalMode { return s.eff }

// stubApprovalFor 带会话上下文的有效档(优先于 EffectiveApproval 被取用)。
type stubApprovalFor struct {
	stubApproval
	seen string
}

func (s *stubApprovalFor) EffectiveModeFor(ctx context.Context) ApprovalMode {
	s.seen = SessionFromContext(ctx)
	return s.eff
}

type stubSandbox struct {
	mode SandboxMode
	eff  SandboxMode
}

func (s stubSandbox) Mode() SandboxMode          { return s.mode }
func (s stubSandbox) SetMode(SandboxMode)        {} // 测试替身不改状态(staticcheck SA4005)
func (s stubSandbox) Root() string               { return "" }
func (s stubSandbox) ValidatePath(string) error  { return nil }
func (s stubSandbox) EffectiveMode() SandboxMode { return s.eff }

func TestTierSnapshotFromMissing(t *testing.T) {
	if _, ok := TierSnapshotFrom(context.Background()); ok {
		t.Fatal("没有快照时 ok 必须是 false(旧路径行为零变化)")
	}
	var nilCtx context.Context
	if _, ok := TierSnapshotFrom(nilCtx); ok {
		t.Fatal("nil ctx 不应 panic,且必须报 ok=false")
	}
}

func TestApprovalAtMostOnlyTightens(t *testing.T) {
	cases := []struct {
		name string
		snap ApprovalMode
		cur  ApprovalMode
		want ApprovalMode
	}{
		{"快照更严 ⇒ 取快照", ApprovalStrict, ApprovalOpen, ApprovalStrict},
		{"快照更严(smart)", ApprovalSmart, ApprovalOpen, ApprovalSmart},
		{"快照更松 ⇒ 保持现算(不能放宽)", ApprovalOpen, ApprovalStrict, ApprovalStrict},
		{"等档 ⇒ 不变", ApprovalSmart, ApprovalSmart, ApprovalSmart},
		{"空快照 ⇒ 不发生收紧", "", ApprovalOpen, ApprovalOpen},
	}
	for _, c := range cases {
		got, bySnap := (TierSnapshot{Approval: c.snap}).ApprovalAtMost(c.cur)
		if got != c.want {
			t.Fatalf("%s:得 %q,想要 %q", c.name, got, c.want)
		}
		if c.snap == "" && bySnap {
			t.Fatalf("%s:空快照不应报 bySnapshot", c.name)
		}
	}
}

func TestSandboxAtMostOnlyTightens(t *testing.T) {
	// 沙箱严度:read-only > workspace-write > full-access
	if got, _ := (TierSnapshot{Sandbox: SandboxReadOnly}).SandboxAtMost(SandboxFullAccess); got != SandboxReadOnly {
		t.Fatalf("快照只读应把 full-access 收成只读,得 %q", got)
	}
	if got, _ := (TierSnapshot{Sandbox: SandboxFullAccess}).SandboxAtMost(SandboxReadOnly); got != SandboxReadOnly {
		t.Fatalf("快照 full-access 不得放宽只读,得 %q", got)
	}
	if got, _ := (TierSnapshot{Sandbox: SandboxWorkspace}).SandboxAtMost(SandboxWorkspace); got != SandboxWorkspace {
		t.Fatalf("等档应保持,得 %q", got)
	}
	// 非法档故障闭合:快照里塞了不认识的档 ⇒ 当最严(不是"未设置")
	if got, _ := (TierSnapshot{Sandbox: "bogus"}).SandboxAtMost(SandboxFullAccess); got != SandboxReadOnly {
		t.Fatalf("非法快照档应故障闭合为只读,得 %q", got)
	}
}

func TestSnapshotTiersPrefersPerSession(t *testing.T) {
	ap := &stubApprovalFor{stubApproval: stubApproval{mode: ApprovalOpen, eff: ApprovalStrict}}
	sb := stubSandbox{mode: SandboxWorkspace, eff: SandboxReadOnly}
	ctx := WithSessionContext(context.Background(), "sess-X")

	snap, ok := SnapshotTiers(ctx, ap, sb)
	if !ok {
		t.Fatal("两个服务都在,应当拿得到快照")
	}
	if ap.seen != "sess-X" {
		t.Fatalf("取有效档必须带上会话归属(实得 %q)", ap.seen)
	}
	if snap.Approval != ApprovalStrict || snap.Sandbox != SandboxReadOnly {
		t.Fatalf("快照应是有效档(strict/read-only),得 %+v", snap)
	}
}

func TestSnapshotTiersWithoutServices(t *testing.T) {
	if _, ok := SnapshotTiers(context.Background(), nil, nil); ok {
		t.Fatal("两个服务都拿不到时不应注入空快照(那会让读端以为有人在管)")
	}
	// 只有审批服务:沙箱字段留空(不收紧沙箱)
	snap, ok := SnapshotTiers(context.Background(), stubApproval{mode: ApprovalOpen, eff: ApprovalSmart}, nil)
	if !ok || snap.Approval != ApprovalSmart || snap.Sandbox != "" {
		t.Fatalf("只给审批服务时应只有审批字段: ok=%v %+v", ok, snap)
	}
}
