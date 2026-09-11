// 档位联动的可见性契约(R10 ②):/sandbox 与 /approval 必须让「声明档 vs 有效档」的
// 差异显式可见——联动覆盖生效时不再静默失效(此前切档被覆盖也一声不吭)。
package hostintcmd

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// effectiveSandboxStub 声明档 + 活联动的有效档(复刻 policy-guard sync=true 语义:
// approval 为权威档,open → full-access、strict → read-only、smart 不覆盖)。
type effectiveSandboxStub struct {
	mode sdk.SandboxMode
	ap   *stubApproval
}

func (s *effectiveSandboxStub) Mode() sdk.SandboxMode     { return s.mode }
func (s *effectiveSandboxStub) SetMode(m sdk.SandboxMode) { s.mode = m }
func (s *effectiveSandboxStub) Root() string              { return "" }
func (s *effectiveSandboxStub) ValidatePath(string) error { return nil }
func (s *effectiveSandboxStub) EffectiveMode() sdk.SandboxMode {
	switch s.ap.Mode() {
	case sdk.ApprovalOpen:
		return sdk.SandboxFullAccess
	case sdk.ApprovalStrict:
		return sdk.SandboxReadOnly
	}
	return s.mode
}

var _ sdk.EffectiveSandbox = (*effectiveSandboxStub)(nil)

// envWithLink 装配 approval(open)+ 联动沙箱(声明 workspace-write,有效 full-access)。
func envWithLink(t *testing.T, mode sdk.ApprovalMode, sandboxMode sdk.SandboxMode) (*effectiveSandboxStub, sdk.CommandRegistry) {
	t.Helper()
	c, _ := buildEnv(t)
	t.Setenv("GAH_HOME", t.TempDir())
	ap := &stubApproval{mode: mode}
	if err := c.Provide("ctx.approval", ap); err != nil {
		t.Fatal(err)
	}
	sb := &effectiveSandboxStub{mode: sandboxMode, ap: ap}
	if err := c.Provide("ctx.sandbox", sb); err != nil {
		t.Fatal(err)
	}
	return sb, startCmds(t, c)
}

// TestSandboxStatusShowsDerivedMode /sandbox 无参回显声明档 + 有效档 + 联动来源。
func TestSandboxStatusShowsDerivedMode(t *testing.T) {
	_, cmds := envWithLink(t, sdk.ApprovalOpen, sdk.SandboxWorkspace)
	out, err := run(t, cmds, "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"沙箱: workspace-write", "有效: full-access", "approval=open"} {
		if !strings.Contains(out, want) {
			t.Fatalf("派生档回显应含 %q: %q", want, out)
		}
	}
}

// TestSandboxSetWarnsWhenOverridden 切档被联动覆盖时必须显式提示(不静默失效)。
func TestSandboxSetWarnsWhenOverridden(t *testing.T) {
	sb, cmds := envWithLink(t, sdk.ApprovalOpen, sdk.SandboxWorkspace)
	out, err := run(t, cmds, "sandbox", "ws")
	if err != nil {
		t.Fatal(err)
	}
	if string(sb.Mode()) != string(sdk.SandboxWorkspace) {
		t.Fatalf("声明档应落地: %s", sb.Mode())
	}
	for _, want := range []string{"沙箱 -> workspace-write", "注意", "full-access", "暂不生效"} {
		if !strings.Contains(out, want) {
			t.Fatalf("被覆盖的切档应提示 %q: %q", want, out)
		}
	}
	// 声明档与有效档一致时不该喊狼来了(同一链接、approval=smart 时无覆盖)
	sb2, cmds2 := envWithLink(t, sdk.ApprovalSmart, sdk.SandboxWorkspace)
	if sb2.EffectiveMode() != sdk.SandboxWorkspace {
		t.Fatalf("smart 档不应覆盖: %s", sb2.EffectiveMode())
	}
	out2, err := run(t, cmds2, "sandbox", "full")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out2, "注意") || strings.Contains(out2, "暂不生效") {
		t.Fatalf("无覆盖时不应提示被覆盖: %q", out2)
	}
	if out2 != "沙箱 -> full-access" {
		t.Fatalf("无覆盖切档回显: %q", out2)
	}
	// 一致态的无参回显标注"有效一致"
	out3, err := run(t, cmds2, "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out3, "沙箱: full-access(有效一致)") {
		t.Fatalf("一致态应标注有效一致: %q", out3)
	}
}

// TestSandboxStatusWithoutEffectiveCapability 沙箱未实现 sdk.EffectiveSandbox 时只报声明档
// (不能凭空断言"有效一致"),且不报错。
func TestSandboxStatusWithoutEffectiveCapability(t *testing.T) {
	c, _ := buildEnv(t)
	t.Setenv("GAH_HOME", t.TempDir())
	if err := c.Provide("ctx.sandbox", &stubSandbox{mode: sdk.SandboxReadOnly}); err != nil {
		t.Fatal(err)
	}
	cmds := startCmds(t, c)
	out, err := run(t, cmds, "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if out != "沙箱: read-only" {
		t.Fatalf("无有效档能力时只报声明档: %q", out)
	}
	if out2, err := run(t, cmds, "sandbox", "ro"); err != nil || out2 != "沙箱 -> read-only" {
		t.Fatalf("无有效档能力时切档回显: %q %v", out2, err)
	}
}

// TestApprovalStatusShowsSandboxEffect /approval 无参与带参都回显对沙箱有效档的影响,
// 且换档后立即反映新联动(可见性与行为同步)。
func TestApprovalStatusShowsSandboxEffect(t *testing.T) {
	sb, cmds := envWithLink(t, sdk.ApprovalOpen, sdk.SandboxWorkspace)
	out, err := run(t, cmds, "approval")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "审批: open") || !strings.Contains(out, "沙箱有效: full-access") {
		t.Fatalf("审批档回显应带沙箱有效档: %q", out)
	}
	// 切到 strict:声明档不变,有效档翻转为 read-only(与 link.go 语义一致)
	out, err = run(t, cmds, "approval", "strict")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "沙箱有效: read-only") {
		t.Fatalf("切 strict 后应回显 read-only: %q", out)
	}
	if sb.EffectiveMode() != sdk.SandboxReadOnly || sb.Mode() != sdk.SandboxWorkspace {
		t.Fatalf("联动应只改有效档: declared=%s effective=%s", sb.Mode(), sb.EffectiveMode())
	}
	// smart:不再覆盖,有效档回到声明档
	out, err = run(t, cmds, "approval", "smart")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "沙箱有效: workspace-write") {
		t.Fatalf("smart 档应回到声明档: %q", out)
	}
}

// TestApprovalStatusWithoutEffectiveCapability:沙箱存在但无有效档能力时给固定语义说明,
// 不猜 sync 开关(被测端读不到该开关);沙箱也未装配时仅回显档位。
func TestApprovalStatusWithoutEffectiveCapability(t *testing.T) {
	c, _ := buildEnv(t)
	t.Setenv("GAH_HOME", t.TempDir())
	if err := c.Provide("ctx.approval", &stubApproval{}); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.sandbox", &stubSandbox{mode: sdk.SandboxWorkspace}); err != nil {
		t.Fatal(err)
	}
	cmds := startCmds(t, c)
	out, err := run(t, cmds, "approval", "open")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "联动开启时沙箱有效档 = full-access") {
		t.Fatalf("无沙箱有效档能力时应给固定说明: %q", out)
	}
	if out, err := run(t, cmds, "approval", "strict"); err != nil || !strings.Contains(out, "联动开启时沙箱有效档 = read-only") {
		t.Fatalf("strict 固定说明: %q %v", out, err)
	}
	// 沙箱服务也未装配:仅回显档位(不报错,不编造联动结果)
	c2, _ := buildEnv(t)
	if err := c2.Provide("ctx.approval", &stubApproval{}); err != nil {
		t.Fatal(err)
	}
	cmds2 := startCmds(t, c2)
	if out, err := run(t, cmds2, "approval"); err != nil || out != "审批: " {
		t.Fatalf("沙箱缺失时应仅回显档位: %q %v", out, err)
	}
	if out, err := run(t, cmds2, "approval", "open"); err != nil || !strings.Contains(out, "审批: open") {
		t.Fatalf("沙箱缺失时应仅回显档位: %q %v", out, err)
	}
}

// derivedStubSandbox 声明档与有效档恒定不一致(等价于"被联动覆盖"的最简形态),
// 用于覆盖联动来源标注的各分支(审批服务缺失 / smart 档 / 非 full-access 来源)。
type derivedStubSandbox struct{ mode sdk.SandboxMode }

func (s *derivedStubSandbox) Mode() sdk.SandboxMode     { return s.mode }
func (s *derivedStubSandbox) SetMode(m sdk.SandboxMode) { s.mode = m }
func (s *derivedStubSandbox) Root() string              { return "" }
func (s *derivedStubSandbox) ValidatePath(string) error { return nil }
func (s *derivedStubSandbox) EffectiveMode() sdk.SandboxMode {
	return sdk.SandboxFullAccess
}

// TestSandboxStatusDerivedSources 来源标注覆盖三种形态:审批服务缺失(不知道来源)、
// smart 档(非覆盖档,却被外部实现判为不一致)、strict 档(read-only 来源)。
func TestSandboxStatusDerivedSources(t *testing.T) {
	// 审批服务缺失:来源只能标注"审批档联动覆盖",不能编造档位名
	c, _ := buildEnv(t)
	t.Setenv("GAH_HOME", t.TempDir())
	if err := c.Provide("ctx.sandbox", &derivedStubSandbox{mode: sdk.SandboxWorkspace}); err != nil {
		t.Fatal(err)
	}
	cmds := startCmds(t, c)
	out, err := run(t, cmds, "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "沙箱: workspace-write") || !strings.Contains(out, "(联动来源 审批档联动)") {
		t.Fatalf("审批服务缺失时的来源标注: %q", out)
	}
	// smart 档:非覆盖档位,来源标注按档位名给(不冒充 open/strict 语义)
	c2, _ := buildEnv(t)
	ap := &stubApproval{mode: sdk.ApprovalSmart}
	if err := c2.Provide("ctx.approval", ap); err != nil {
		t.Fatal(err)
	}
	if err := c2.Provide("ctx.sandbox", &derivedStubSandbox{mode: sdk.SandboxWorkspace}); err != nil {
		t.Fatal(err)
	}
	cmds2 := startCmds(t, c2)
	if out, err := run(t, cmds2, "sandbox"); err != nil || !strings.Contains(out, "联动来源 approval=smart") {
		t.Fatalf("smart 档来源标注: %q %v", out, err)
	}
	// strict 档 + 切档提示:提示里必须点名来源档与实报有效档(不按 approval 推测结果)
	if out, err := run(t, cmds2, "approval", "strict"); err != nil || !strings.Contains(out, "沙箱有效: full-access") {
		t.Fatalf("审批档回显(有效档由实现决定): %q %v", out, err)
	}
	out, err = run(t, cmds2, "sandbox", "ws")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"注意", "当前有效档 full-access", "approval=strict", "暂不生效"} {
		if !strings.Contains(out, want) {
			t.Fatalf("覆盖提示应含 %q: %q", want, out)
		}
	}
}

// TestApprovalStatusSmartWithoutEffectiveCapability smart 档 + 无有效档能力:只回档位
// (smart 不覆盖,给固定说明反而是误导)。
func TestApprovalStatusSmartWithoutEffectiveCapability(t *testing.T) {
	c, _ := buildEnv(t)
	t.Setenv("GAH_HOME", t.TempDir())
	ap := &stubApproval{mode: sdk.ApprovalSmart}
	if err := c.Provide("ctx.approval", ap); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.sandbox", &stubSandbox{mode: sdk.SandboxWorkspace}); err != nil {
		t.Fatal(err)
	}
	cmds := startCmds(t, c)
	out, err := run(t, cmds, "approval")
	if err != nil {
		t.Fatal(err)
	}
	if out != "审批: smart" {
		t.Fatalf("smart 档应只回档位: %q", out)
	}
}
