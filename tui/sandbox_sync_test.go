// /sandbox sync 的 TUI 侧(R10 ②-2):开关切换后状态栏必须立刻反映新的有效档
// (开关与"有效档"是一个语义的两面,状态栏落后一步就等于又说了一遍假话)。
package tui

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// tuiSyncSandbox 声明档 + 活联动 + 联动开关(等价 policy-guard 三件套的 TUI 桩)。
type tuiSyncSandbox struct {
	mode sdk.SandboxMode
	sync bool
	ap   *tuiApprovalStub
}

func (s *tuiSyncSandbox) Mode() sdk.SandboxMode     { return s.mode }
func (s *tuiSyncSandbox) SetMode(m sdk.SandboxMode) { s.mode = m }
func (s *tuiSyncSandbox) Root() string              { return "" }
func (s *tuiSyncSandbox) ValidatePath(string) error { return nil }
func (s *tuiSyncSandbox) SyncEnabled() bool         { return s.sync }
func (s *tuiSyncSandbox) SetSyncEnabled(on bool)    { s.sync = on }
func (s *tuiSyncSandbox) EffectiveMode() sdk.SandboxMode {
	if !s.sync {
		return s.mode
	}
	switch s.ap.Mode() {
	case sdk.ApprovalOpen:
		return sdk.SandboxFullAccess
	case sdk.ApprovalStrict:
		return sdk.SandboxReadOnly
	}
	return s.mode
}

var (
	_ sdk.SandboxSync      = (*tuiSyncSandbox)(nil)
	_ sdk.EffectiveSandbox = (*tuiSyncSandbox)(nil)
)

// syncApp 构造 App:approval=open + 可切联动的沙箱(声明 workspace-write)。
func syncApp(t *testing.T) (*App, *tuiSyncSandbox) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	ap := &tuiApprovalStub{mode: sdk.ApprovalOpen}
	sb := &tuiSyncSandbox{mode: sdk.SandboxWorkspace, sync: true, ap: ap}
	c := &stubCtx{svc: map[string]any{
		"ctx.commands": newMemRegistry(),
		"ctx.sandbox":  sdk.Sandbox(sb),
		"ctx.approval": sdk.ApprovalService(ap),
	}}
	return NewApp(c, stubLoop{}, stubLLM{}, "tui"), sb
}

// TestCmdSandboxSyncTogglesAndRefreshesStatus 切换开关后状态栏立刻改口径,偏好落盘。
func TestCmdSandboxSyncTogglesAndRefreshesStatus(t *testing.T) {
	a, sb := syncApp(t)
	if got := a.model.state.Sandbox; got != "workspace-write→full-access(审批联动)" {
		t.Fatalf("初始状态栏: %q", got)
	}
	out, err := a.cmdSandbox([]string{"sync"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "沙箱联动: on") || !strings.Contains(out, "full-access") {
		t.Fatalf("查看回显: %q", out)
	}
	out, err = a.cmdSandbox([]string{"sync", "off"})
	if err != nil {
		t.Fatal(err)
	}
	if sb.sync {
		t.Fatalf("切换应关掉联动: sync=%v out=%q", sb.sync, out)
	}
	if !strings.Contains(out, "沙箱联动 -> off") {
		t.Fatalf("切换回显应报 off: %q", out)
	}
	if got := a.model.state.Sandbox; got != "workspace-write" {
		t.Fatalf("关掉联动后状态栏应只报声明档: %q", got)
	}
	if v := prefs.Load().SandboxSync; v == nil || *v {
		t.Fatalf("偏好应记为 off,got %v", v)
	}
	// 非法参数:报用法且不动开关
	if _, err := a.cmdSandbox([]string{"sync", "maybe"}); err == nil || !strings.Contains(err.Error(), "/sandbox sync on|off") {
		t.Fatalf("非法参数应报用法: %v", err)
	}
	if sb.sync {
		t.Fatal("非法参数不得改动开关")
	}
	// 沙箱无该能力:显式报错(不假装成功、不改状态栏)
	a2, _, _ := linkedApp(t) // 该桩未实现 sdk.SandboxSync
	if _, err := a2.cmdSandbox([]string{"sync"}); err == nil || !strings.Contains(err.Error(), "不支持联动开关") {
		t.Fatalf("不支持能力应报错: %v", err)
	}
}
