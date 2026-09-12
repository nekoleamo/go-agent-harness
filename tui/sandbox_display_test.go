// 档位联动的可见性(R10 ②,TUI 侧):状态栏与 /sandbox、/approval 回显必须反映
// 联动后的**有效档**，而不是被覆盖掉的声明档。
package tui

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// tuiApprovalStub 审批档桩(可被联动沙箱读取，模拟 policy-guard 的活联动)。
type tuiApprovalStub struct{ mode sdk.ApprovalMode }

func (s *tuiApprovalStub) Mode() sdk.ApprovalMode     { return s.mode }
func (s *tuiApprovalStub) SetMode(m sdk.ApprovalMode) { s.mode = m }

// tuiLinkedSandbox 声明档 + 活联动有效档(open → full-access、strict → read-only)。
type tuiLinkedSandbox struct {
	mode sdk.SandboxMode
	ap   *tuiApprovalStub
}

func (s *tuiLinkedSandbox) Mode() sdk.SandboxMode     { return s.mode }
func (s *tuiLinkedSandbox) SetMode(m sdk.SandboxMode) { s.mode = m }
func (s *tuiLinkedSandbox) Root() string              { return "" }
func (s *tuiLinkedSandbox) ValidatePath(string) error { return nil }
func (s *tuiLinkedSandbox) EffectiveMode() sdk.SandboxMode {
	switch s.ap.Mode() {
	case sdk.ApprovalOpen:
		return sdk.SandboxFullAccess
	case sdk.ApprovalStrict:
		return sdk.SandboxReadOnly
	}
	return s.mode
}

var _ sdk.EffectiveSandbox = (*tuiLinkedSandbox)(nil)

// linkedApp 构造 App：approval=open + 声明 workspace-write 的联动沙箱。
func linkedApp(t *testing.T) (*App, *tuiLinkedSandbox, *tuiApprovalStub) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	ap := &tuiApprovalStub{mode: sdk.ApprovalOpen}
	sb := &tuiLinkedSandbox{mode: sdk.SandboxWorkspace, ap: ap}
	c := &stubCtx{svc: map[string]any{
		"ctx.commands": newMemRegistry(),
		"ctx.sandbox":  sdk.Sandbox(sb),
		"ctx.approval": sdk.ApprovalService(ap),
	}}
	return NewApp(c, stubLoop{}, stubLLM{}, "tui"), sb, ap
}

// TestStatusBarShowsDerivedSandbox 状态栏显示有效档并标注联动来源(启动即反映真实拦截行为)。
func TestStatusBarShowsDerivedSandbox(t *testing.T) {
	a, _, _ := linkedApp(t)
	if got := a.model.state.Sandbox; got != "workspace-write→full-access(审批联动)" {
		t.Fatalf("状态栏应显示派生有效档: %q", got)
	}
	if out := stripColor(renderStatusLine(a.model.state, 120)); !strings.Contains(out, "workspace-write→full-access(审批联动)") {
		t.Fatalf("状态栏渲染应含派生档: %q", out)
	}
}

// TestCmdSandboxApprovalVisibility 命令回显与状态栏随 /sandbox、/approval 同步更新。
func TestCmdSandboxApprovalVisibility(t *testing.T) {
	a, sb, _ := linkedApp(t)

	// 无参 = 查看声明档 + 有效档 + 来源
	out, err := a.cmdSandbox(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"沙箱: workspace-write", "有效: full-access", "approval=open"} {
		if !strings.Contains(out, want) {
			t.Fatalf("无参回显应含 %q: %q", want, out)
		}
	}
	// 切档被覆盖 → 显式提示，且状态栏仍显示有效档
	out, err = a.cmdSandbox([]string{"ws"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "暂不生效") {
		t.Fatalf("被覆盖的切档应提示: %q", out)
	}
	if a.model.state.Sandbox != "workspace-write→full-access(审批联动)" {
		t.Fatalf("状态栏应维持有效档: %q", a.model.state.Sandbox)
	}
	// 审批切 strict → 有效档翻转为 read-only(状态栏与回显同步)
	out, err = a.cmdApproval([]string{"strict"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "沙箱有效: read-only") {
		t.Fatalf("strict 回显应含有效档: %q", out)
	}
	if a.model.state.Sandbox != "workspace-write→read-only(审批联动)" {
		t.Fatalf("状态栏应随审批档翻转: %q", a.model.state.Sandbox)
	}
	if sb.EffectiveMode() != sdk.SandboxReadOnly {
		t.Fatalf("联动应只改有效档: %s", sb.EffectiveMode())
	}
	// 审批切 smart → 不再覆盖，状态栏回到声明档
	if _, err := a.cmdApproval([]string{"smart"}); err != nil {
		t.Fatal(err)
	}
	if a.model.state.Sandbox != "workspace-write" {
		t.Fatalf("smart 档应回到声明档: %q", a.model.state.Sandbox)
	}
	// 非法参数保持既有错误串(不因本次改动漂移)
	if _, err := a.cmdSandbox([]string{"bogus"}); err == nil || !strings.Contains(err.Error(), "/sandbox ro|ws|full") {
		t.Fatalf("非法档位应给用法: %v", err)
	}
	if _, err := a.cmdApproval([]string{"bogus"}); err == nil || !strings.Contains(err.Error(), "/approval open|smart|strict") {
		t.Fatalf("非法审批档应给用法: %v", err)
	}
}

// TestCmdSandboxWithoutEffectiveCapability 沙箱未实现 sdk.EffectiveSandbox 时只报声明档
// (不凭空声称"有效一致");服务未装配仍显式报错。
func TestCmdSandboxWithoutEffectiveCapability(t *testing.T) {
	a, _, _ := linkedApp(t)
	plain := &plainSandboxStub{mode: sdk.SandboxReadOnly}
	a.c = &stubCtx{svc: map[string]any{"ctx.commands": newMemRegistry(), "ctx.sandbox": sdk.Sandbox(plain)}}
	a.model.state.Sandbox = sandboxDisplay(plain)
	if a.model.state.Sandbox != "read-only" {
		t.Fatalf("无有效档能力时状态栏应显示声明档: %q", a.model.state.Sandbox)
	}
	out, err := a.cmdSandbox(nil)
	if err != nil || out != "沙箱: read-only" {
		t.Fatalf("无有效档能力时回显: %q %v", out, err)
	}
	// 沙箱未装配:显式报错(不静默)
	a.c = &stubCtx{svc: map[string]any{"ctx.commands": newMemRegistry()}}
	if _, err := a.cmdSandbox(nil); err == nil || !strings.Contains(err.Error(), "ctx.sandbox 未装配") {
		t.Fatalf("未装配沙箱应显式报错: %v", err)
	}
	if _, err := a.cmdApproval([]string{"open"}); err == nil || !strings.Contains(err.Error(), "ctx.approval 未装配") {
		t.Fatalf("未装配审批应显式报错: %v", err)
	}
}

// plainSandboxStub 仅实现 sdk.Sandbox(无有效档能力)。
type plainSandboxStub struct{ mode sdk.SandboxMode }

func (s *plainSandboxStub) Mode() sdk.SandboxMode     { return s.mode }
func (s *plainSandboxStub) SetMode(m sdk.SandboxMode) { s.mode = m }
func (s *plainSandboxStub) Root() string              { return "" }
func (s *plainSandboxStub) ValidatePath(string) error { return nil }

// tuiAlwaysDerivedSandbox 声明档与有效档恒定不一致(等价"被联动覆盖"的最简形态),
// 且不依赖审批服务:用于覆盖"来源未知"分支。
type tuiAlwaysDerivedSandbox struct{ mode sdk.SandboxMode }

func (s *tuiAlwaysDerivedSandbox) Mode() sdk.SandboxMode     { return s.mode }
func (s *tuiAlwaysDerivedSandbox) SetMode(m sdk.SandboxMode) { s.mode = m }
func (s *tuiAlwaysDerivedSandbox) Root() string              { return "" }
func (s *tuiAlwaysDerivedSandbox) ValidatePath(string) error { return nil }
func (s *tuiAlwaysDerivedSandbox) EffectiveMode() sdk.SandboxMode {
	return sdk.SandboxFullAccess
}

// TestCmdApprovalStatusReportsSandboxEffect 无参 /approval 必须连带报出它对沙箱有效档的影响:
// 有有效档能力时实报,否则给固定说明(open/strict),smart 档不覆盖故只回档位。
func TestCmdApprovalStatusReportsSandboxEffect(t *testing.T) {
	a, _, ap := linkedApp(t)
	out, err := a.cmdApproval(nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "审批: open;沙箱有效: full-access" {
		t.Fatalf("无参 /approval 应连报沙箱有效档: %q", out)
	}
	// 无有效档能力:按档位给固定说明(不凭空读有效档)
	plain := &plainSandboxStub{mode: sdk.SandboxWorkspace}
	a.c = &stubCtx{svc: map[string]any{"ctx.commands": newMemRegistry(), "ctx.sandbox": sdk.Sandbox(plain), "ctx.approval": sdk.ApprovalService(ap)}}
	// 顺序固定(不用 map 迭代:Go 的 map 遍历顺序随机,曾导致此处 2/3 概率随机失败;
	// smart 必须落最后,下方「smart 不覆盖」的断言才成立)
	for _, tc := range []struct{ mode, want string }{
		{"open", "(联动开启时沙箱有效档 = full-access)"},
		{"strict", "(联动开启时沙箱有效档 = read-only)"},
		{"smart", ""},
	} {
		if _, err := a.cmdApproval([]string{tc.mode}); err != nil {
			t.Fatal(err)
		}
		got, err := a.cmdApproval(nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "审批: "+tc.mode) || (tc.want != "" && !strings.Contains(got, tc.want)) {
			t.Fatalf("approval=%s 回显: %q", tc.mode, got)
		}
		if tc.mode == "smart" && got != "审批: smart" {
			t.Fatalf("smart 不覆盖,应只回档位: %q", got)
		}
	}
	// 沙箱未装配:只回审批档(不因缺沙箱而报错/漏报)
	a.c = &stubCtx{svc: map[string]any{"ctx.commands": newMemRegistry(), "ctx.approval": sdk.ApprovalService(ap)}}
	if got, err := a.cmdApproval(nil); err != nil || got != "审批: smart" {
		t.Fatalf("无沙箱时的审批档回显: %q %v", got, err)
	}
}

// TestSandboxTextsWithoutApprovalSource 来源未知(审批服务未装配)与"有效一致"两种文案,
// 以及无有效档能力时的切档回显。
func TestSandboxTextsWithoutApprovalSource(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	derived := &tuiAlwaysDerivedSandbox{mode: sdk.SandboxWorkspace}
	a := newTestApp(&stubCtx{svc: map[string]any{"ctx.commands": newMemRegistry(), "ctx.sandbox": sdk.Sandbox(derived)}})
	// 未装配审批:来源只能标"审批档联动",不得编造档位名
	out, err := a.cmdSandbox(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "(联动来源 审批档联动)") {
		t.Fatalf("审批未装配时的来源标注: %q", out)
	}
	// 切档仍被覆盖 → 提示到位
	if out, err = a.cmdSandbox([]string{"ws"}); err != nil || !strings.Contains(out, "注意:联动覆盖生效") {
		t.Fatalf("覆盖提示: %q %v", out, err)
	}
	// 无有效档能力 + 一致档:只报"沙箱 -> X"(不虚报覆盖)
	plain := &plainSandboxStub{mode: sdk.SandboxWorkspace}
	a.c = &stubCtx{svc: map[string]any{"ctx.commands": newMemRegistry(), "ctx.sandbox": sdk.Sandbox(plain)}}
	if out, err = a.cmdSandbox([]string{"ro"}); err != nil || out != "沙箱 -> read-only" {
		t.Fatalf("无有效档能力的切档回显: %q %v", out, err)
	}
	// 有效一致:smart 档下联动沙箱的有效档 == 声明档
	ap := &tuiApprovalStub{mode: sdk.ApprovalSmart}
	linked := &tuiLinkedSandbox{mode: sdk.SandboxWorkspace, ap: ap}
	a.c = &stubCtx{svc: map[string]any{"ctx.commands": newMemRegistry(), "ctx.sandbox": sdk.Sandbox(linked), "ctx.approval": sdk.ApprovalService(ap)}}
	out, err = a.cmdSandbox(nil)
	if err != nil || out != "沙箱: workspace-write(有效一致)" {
		t.Fatalf("有效一致文案: %q %v", out, err)
	}
	// 有效档 == 声明档时切档不虚报覆盖;full 档走完三档分支
	if out, err = a.cmdSandbox([]string{"ro"}); err != nil || out != "沙箱 -> read-only" {
		t.Fatalf("一致档切档回显: %q %v", out, err)
	}
	if out, err = a.cmdSandbox([]string{"full"}); err != nil || out != "沙箱 -> full-access" {
		t.Fatalf("full 档切档回显: %q %v", out, err)
	}
}

// newTestApp 以给定服务集构造 App(共用 harness:stubLoop/stubLLM)。
func newTestApp(c sdk.Ctx) *App { return NewApp(c, stubLoop{}, stubLLM{}, "tui") }
