// /sandbox sync 子命令(R10 ②-2):联动开关的查看与切换(含"沙箱不支持该能力"的显式回错)。
package hostintcmd

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// syncStubSandbox 声明档 + 活联动 + 联动开关(复刻 policy-guard 的三件套)。
type syncStubSandbox struct {
	mode sdk.SandboxMode
	sync bool
	ap   *stubApproval
}

func (s *syncStubSandbox) Mode() sdk.SandboxMode     { return s.mode }
func (s *syncStubSandbox) SetMode(m sdk.SandboxMode) { s.mode = m }
func (s *syncStubSandbox) Root() string              { return "" }
func (s *syncStubSandbox) ValidatePath(string) error { return nil }
func (s *syncStubSandbox) SyncEnabled() bool         { return s.sync }
func (s *syncStubSandbox) SetSyncEnabled(on bool)    { s.sync = on }
func (s *syncStubSandbox) EffectiveMode() sdk.SandboxMode {
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
	_ sdk.SandboxSync      = (*syncStubSandbox)(nil)
	_ sdk.EffectiveSandbox = (*syncStubSandbox)(nil)
)

// envWithSync 装配 approval(open)+ 可切联动的沙箱(声明 workspace-write)。
func envWithSync(t *testing.T) (*syncStubSandbox, sdk.CommandRegistry) {
	t.Helper()
	c, _ := buildEnv(t)
	t.Setenv("GAH_HOME", t.TempDir())
	ap := &stubApproval{mode: sdk.ApprovalOpen}
	if err := c.Provide("ctx.approval", ap); err != nil {
		t.Fatal(err)
	}
	sb := &syncStubSandbox{mode: sdk.SandboxWorkspace, sync: true, ap: ap}
	if err := c.Provide("ctx.sandbox", sb); err != nil {
		t.Fatal(err)
	}
	return sb, startCmds(t, c)
}

// TestSandboxSyncViewAndToggle 无参回显开关与当前有效档;切换后即时生效并落偏好。
func TestSandboxSyncViewAndToggle(t *testing.T) {
	sb, cmds := envWithSync(t)
	out, err := run(t, cmds, "sandbox", "sync")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"沙箱联动: on", "当前有效档 full-access", "approval=open"} {
		if !strings.Contains(out, want) {
			t.Fatalf("联动回显应含 %q: %q", want, out)
		}
	}
	out, err = run(t, cmds, "sandbox", "sync", "off")
	if err != nil {
		t.Fatal(err)
	}
	if sb.SyncEnabled() {
		t.Fatal("切换后开关应为 off")
	}
	if sb.EffectiveMode() != sdk.SandboxWorkspace {
		t.Fatalf("关闭联动后有效档应回声明档: %s", sb.EffectiveMode())
	}
	for _, want := range []string{"沙箱联动 -> off", "独立生效", "workspace-write"} {
		if !strings.Contains(out, want) {
			t.Fatalf("切换回显应含 %q: %q", want, out)
		}
	}
	if got := prefs.Load().SandboxSync; got == nil || *got {
		t.Fatalf("用户选择应落偏好(重启恢复),got %v", got)
	}
	// 关掉后无参回显不该再宣称"被覆盖"
	out, err = run(t, cmds, "sandbox", "sync")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "沙箱联动: off") || !strings.Contains(out, "不被审批档覆盖") {
		t.Fatalf("off 态回显: %q", out)
	}
	// 重新打开:恢复覆盖 + 偏好回写 true
	out, err = run(t, cmds, "sandbox", "sync", "on")
	if err != nil {
		t.Fatal(err)
	}
	if !sb.SyncEnabled() || !strings.Contains(out, "沙箱联动 -> on") {
		t.Fatalf("重新开启: %q enabled=%v", out, sb.SyncEnabled())
	}
	if got := prefs.Load().SandboxSync; got == nil || !*got {
		t.Fatalf("偏好应回写 true,got %v", got)
	}
	// 切档后再看/再切:声明档与开关互相独立(开关不重置声明档)
	if _, err := run(t, cmds, "sandbox", "ws"); err != nil {
		t.Fatal(err)
	}
	if sb.Mode() != sdk.SandboxWorkspace || !sb.SyncEnabled() {
		t.Fatalf("切档不应改开关: mode=%s sync=%v", sb.Mode(), sb.SyncEnabled())
	}
}

// TestSandboxSyncBadArgs 非法参数与不支持能力都必须显式回错(不静默当作 on/off)。
func TestSandboxSyncBadArgs(t *testing.T) {
	_, cmds := envWithSync(t)
	if out, err := run(t, cmds, "sandbox", "sync", "yes"); err == nil || !strings.Contains(err.Error(), "/sandbox sync on|off") {
		t.Fatalf("非法参数应提示用法: %q %v", out, err)
	}
	// /sandbox ro 这类既有用法不能被新分支吞掉
	if out, err := run(t, cmds, "sandbox", "ro"); err != nil || !strings.Contains(out, "沙箱 -> read-only") {
		t.Fatalf("既有切档应不受影响: %q %v", out, err)
	}
	if out, err := run(t, cmds, "sandbox", "bogus"); err == nil || !strings.Contains(err.Error(), "/sandbox ro|ws|full|sync") {
		t.Fatalf("未知子命令应列出含 sync 的用法: %q %v", out, err)
	}
}

// TestSandboxSyncUnsupported 沙箱未实现 sdk.SandboxSync 时显式报不支持(不假装成功)。
func TestSandboxSyncUnsupported(t *testing.T) {
	c, _ := buildEnv(t)
	t.Setenv("GAH_HOME", t.TempDir())
	if err := c.Provide("ctx.sandbox", &stubSandbox{mode: sdk.SandboxWorkspace}); err != nil {
		t.Fatal(err)
	}
	cmds := startCmds(t, c)
	if _, err := run(t, cmds, "sandbox", "sync"); err == nil || !strings.Contains(err.Error(), "不支持联动开关") {
		t.Fatalf("不支持能力应报错: %v", err)
	}
	if _, err := run(t, cmds, "sandbox", "sync", "off"); err == nil {
		t.Fatal("不支持能力时切换也应报错")
	}
	// 沙箱整个未装配:报可操作的错误(不是空表)
	c2, _ := buildEnv(t)
	cmds2 := startCmds(t, c2)
	if _, err := run(t, cmds2, "sandbox", "sync"); err == nil || !strings.Contains(err.Error(), "ctx.sandbox 未装配") {
		t.Fatalf("未装配沙箱应报可操作错误: %v", err)
	}
}

// TestSandboxSyncLevelDeclared 逐级确认:sync 才给二级 on/off,其余子命令无二级(选完即执行)。
func TestSandboxSyncLevelDeclared(t *testing.T) {
	_, cmds := envWithSync(t)
	var spec sdk.CommandSpec
	for _, s := range cmds.List() {
		if s.Name == "sandbox" {
			spec = s
		}
	}
	if len(spec.Args) != 2 {
		t.Fatalf("/sandbox 应声明两级参数: %d", len(spec.Args))
	}
	lv1 := spec.Args[0].Options([]string{"sandbox"})
	hasSync := false
	for _, o := range lv1 {
		if o.Value == "sync" {
			hasSync = true
		}
	}
	if !hasSync {
		t.Fatalf("一级应含 sync: %+v", lv1)
	}
	if got := spec.Args[1].Options([]string{"sandbox", "ro"}); got != nil {
		t.Fatalf("非 sync 子命令不该有二级: %+v", got)
	}
	lv2 := spec.Args[1].Options([]string{"sandbox", "sync"})
	if len(lv2) != 2 || lv2[0].Value != "on" || lv2[1].Value != "off" {
		t.Fatalf("sync 二级应为 on|off: %+v", lv2)
	}
	if !strings.Contains(spec.Usage, "sync [on|off]") {
		t.Fatalf("Usage 应含 sync: %q", spec.Usage)
	}
}
