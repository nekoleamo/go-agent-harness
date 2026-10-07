package policyguard

// 沙箱/审批档按会话生效(第一百一十六批)。
//
// 要钉的是两件事:
//  ① **拦截真的按会话**(不是只有"有效档位"显示变了、实际照写 —— 那是最坏的假象:
//     界面写着只读、文件却写进去了);
//  ② 会话级与角色级都是**只更严**:角色 strict 时,会话开到 full-access 仍是 strict。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// prefsSrcStub 会话偏好桩:按会话 id 返回偏好。
type prefsSrcStub map[string]sdk.SessionPrefs

func (p prefsSrcStub) SessionPrefsOf(id string) sdk.SessionPrefs { return p[id] }

// withSessionPrefs 临时装一份会话偏好(返回还原函数)。
func withSessionPrefs(t *testing.T, by map[string]sdk.SessionPrefs) {
	t.Helper()
	old := currentSessionPrefsSrc
	currentSessionPrefsSrc = prefsSrcStub(by)
	t.Cleanup(func() { currentSessionPrefsSrc = old })
}

func TestEffectiveSandboxTierPerSession(t *testing.T) {
	withSessionPrefs(t, map[string]sdk.SessionPrefs{
		"strictA": {Sandbox: "read-only"},
		"looseB":  {}, // 没设 ⇒ 跟随全局
	})
	root := t.TempDir()
	sp := &SandboxPolicy{mode: sdk.SandboxWorkspace, root: root}

	if got := sp.EffectiveModeFor(sdk.WithSessionContext(context.Background(), "strictA")); got != sdk.SandboxReadOnly {
		t.Fatalf("会话 A 设了 read-only,有效档应是 read-only,得 %q", got)
	}
	if got := sp.EffectiveModeFor(sdk.WithSessionContext(context.Background(), "looseB")); got != sdk.SandboxWorkspace {
		t.Fatalf("没设过的会话应跟随全局,得 %q", got)
	}
}

// 关键:写路径校验也按会话(A 只读就该真被拒,B 不受影响)。
func TestValidatePathForPerSession(t *testing.T) {
	withSessionPrefs(t, map[string]sdk.SessionPrefs{
		"ro": {Sandbox: "read-only"},
		"rw": {Sandbox: "workspace-write"},
	})
	root := t.TempDir()
	sp := &SandboxPolicy{mode: sdk.SandboxFullAccess, root: root} // 全局很松,会话才是决定
	target := filepath.Join(root, "a.txt")

	if err := sp.ValidatePathFor(sdk.WithSessionContext(context.Background(), "ro"), target); err == nil {
		t.Fatal("会话 A 设了 read-only,写必须被拒(否则就是'界面说只读、实际照写')")
	}
	if err := sp.ValidatePathFor(sdk.WithSessionContext(context.Background(), "rw"), target); err != nil {
		t.Fatalf("会话 B 是 workspace-write,写工作区内应放行: %v", err)
	}
	// 无参路径(展示/单会话)取全局档:与从前一致
	if err := sp.ValidatePath(target); err != nil {
		t.Fatalf("无参校验取全局档(full-access),应放行: %v", err)
	}
}

// 会话只能更严,不能比角色更松。
func TestSessionTierCannotLoosenRole(t *testing.T) {
	withSessionPrefs(t, map[string]sdk.SessionPrefs{"x": {Sandbox: "full-access"}})
	sp := &SandboxPolicy{
		mode: sdk.SandboxWorkspace,
		root: t.TempDir(),
		role: func(context.Context) (string, string) { return "strict", "read-only" },
	}
	if got := sp.EffectiveModeFor(sdk.WithSessionContext(context.Background(), "x")); got != sdk.SandboxReadOnly {
		t.Fatalf("角色 read-only 是下限,会话开到 full-access 也应是 read-only,得 %q", got)
	}
}

// 工作区之外的落点:会话 full-access 时允许(显式放宽),全局仍只读时不允许。
func TestSessionFullAccessAllowsOutsideWorkspace(t *testing.T) {
	withSessionPrefs(t, map[string]sdk.SessionPrefs{"open": {Sandbox: "full-access"}})
	root := t.TempDir()
	sp := &SandboxPolicy{mode: sdk.SandboxReadOnly, root: root}
	outside := filepath.Join(t.TempDir(), "b.txt")

	if err := sp.ValidatePathFor(sdk.WithSessionContext(context.Background(), "open"), outside); err != nil {
		t.Fatalf("会话显式放宽到 full-access,工作区外写应放行: %v", err)
	}
	if err := sp.ValidatePath(outside); err == nil {
		t.Fatal("全局仍只读时,工作区外写应被拒")
	}
	_ = os.Remove(outside)
}

// 审批档同样按会话,且会话 open 也不能被角色 strict 盖住。
func TestApprovalTierPerSession(t *testing.T) {
	withSessionPrefs(t, map[string]sdk.SessionPrefs{"p": {Approval: "open"}})
	ap := &ApprovalPolicy{mode: sdk.ApprovalSmart}
	if got := ap.EffectiveModeFor(sdk.WithSessionContext(context.Background(), "p")); got != sdk.ApprovalOpen {
		t.Fatalf("会话设了 open,应生效,得 %q", got)
	}
	ap2 := &ApprovalPolicy{mode: sdk.ApprovalSmart, role: func(context.Context) (string, string) { return "strict", "" }}
	if got := ap2.EffectiveModeFor(sdk.WithSessionContext(context.Background(), "p")); got != sdk.ApprovalStrict {
		t.Fatalf("角色 strict 是下限,会话 open 也应是 strict,得 %q", got)
	}
}

// 联动与角色收紧的组合(第一百一十六批把会话档插进这条链之后,各分支都要有人走)。
func TestLinkedAndRoleTiersWithSession(t *testing.T) {
	root := t.TempDir()
	// sync=true + 审批 strict ⇒ 沙箱被联动压成只读;会话想放宽也没用(审批是最高防线)
	ap := &ApprovalPolicy{mode: sdk.ApprovalStrict}
	sp := &SandboxPolicy{mode: sdk.SandboxWorkspace, root: root, sync: true, approval: ap.Mode}
	if got := sp.EffectiveModeFor(sdk.WithSessionContext(context.Background(), "x")); got != sdk.SandboxReadOnly {
		t.Fatalf("审批 strict 时有效档应是只读,得 %q", got)
	}
	// 联动关闭时,会话档说了算
	withSessionPrefs(t, map[string]sdk.SessionPrefs{"y": {Sandbox: "full-access"}})
	sp2 := &SandboxPolicy{mode: sdk.SandboxReadOnly, root: root, sync: false, approval: ap.Mode}
	if got := sp2.EffectiveModeFor(sdk.WithSessionContext(context.Background(), "y")); got != sdk.SandboxFullAccess {
		t.Fatalf("联动关闭时会话档应生效,得 %q", got)
	}
	// 审批联动开启 + 审批 open ⇒ 全权(不受会话只读影响 —— 联动方向相反,见 link.go 注释)
	apOpen := &ApprovalPolicy{mode: sdk.ApprovalOpen}
	sp3 := &SandboxPolicy{mode: sdk.SandboxReadOnly, root: root, sync: true, approval: apOpen.Mode,
		role: func(context.Context) (string, string) { return "", "read-only" }} // 第二个返回值才是沙箱档
	if got := sp3.EffectiveModeFor(sdk.WithSessionContext(context.Background(), "y")); got != sdk.SandboxReadOnly {
		t.Fatalf("审批 open 的全权又被角色只读压回来,应得 %q", got)
	}
	// 读路径按会话:会话 full-access 时读不受 workspace 限制
	sp4 := &SandboxPolicy{mode: sdk.SandboxReadOnly, root: root}
	if err := sp4.ValidateReadFor(sdk.WithSessionContext(context.Background(), "y"), "/etc/hosts"); err != nil {
		t.Fatalf("会话 full-access 时读应放行: %v", err)
	}
	// 未装配会话偏好(读数 nil)⇒ 全部跟随全局,行为与从前一致
	old := currentSessionPrefsSrc
	currentSessionPrefsSrc = nil
	if got := sp4.EffectiveModeFor(sdk.WithSessionContext(context.Background(), "y")); got != sdk.SandboxReadOnly {
		t.Fatalf("未装配会话偏好时应回落全局,得 %q", got)
	}
	currentSessionPrefsSrc = old
}
