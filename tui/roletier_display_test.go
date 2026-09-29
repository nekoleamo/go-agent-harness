// 角色级权限收紧的显示面(第九十二批,TUI 侧)。
//
// 与第九十一批同一条纪律:**展示与真相同源**。偏离来源问策略器本身(sdk.EffectiveSource),
// 不在展示层写死"联动所致" —— 角色收紧时那句话是假归因,而"换个角色"与"改审批档"
// 是用户完全不同的两个处置动作。
//
// 证伪面:同一批用例里既有"角色收紧"的桩,也有**没有** EffectiveSource 的旧实现桩 ——
// 后者必须逐字回到第九十二批之前的文案(旧插件/测试替身的展示口径零变化)。
package tui

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// roleTightenedSandbox 角色收紧后的沙箱桩:声明 workspace-write,有效 read-only,来源 role。
type roleTightenedSandbox struct{ mode sdk.SandboxMode }

func (s *roleTightenedSandbox) Mode() sdk.SandboxMode     { return s.mode }
func (s *roleTightenedSandbox) SetMode(m sdk.SandboxMode) { s.mode = m }
func (s *roleTightenedSandbox) Root() string              { return "" }
func (s *roleTightenedSandbox) ValidatePath(string) error { return nil }
func (s *roleTightenedSandbox) EffectiveMode() sdk.SandboxMode {
	return sdk.SandboxReadOnly
}
func (s *roleTightenedSandbox) EffectiveFrom() string { return sdk.TierSourceRole }

// roleTightenedApproval 角色收紧后的审批桩:声明 smart,有效 strict,来源 role。
type roleTightenedApproval struct{ mode sdk.ApprovalMode }

func (s *roleTightenedApproval) Mode() sdk.ApprovalMode     { return s.mode }
func (s *roleTightenedApproval) SetMode(m sdk.ApprovalMode) { s.mode = m }
func (s *roleTightenedApproval) EffectiveMode() sdk.ApprovalMode {
	return sdk.ApprovalStrict
}
func (s *roleTightenedApproval) EffectiveFrom() string { return sdk.TierSourceRole }

// TestSandboxDisplayRoleTightened 有效档由角色收紧而来时,状态栏说"角色收紧"。
func TestSandboxDisplayRoleTightened(t *testing.T) {
	sb := &roleTightenedSandbox{mode: sdk.SandboxWorkspace}
	if got := sandboxDisplay(sb); got != "workspace-write→read-only(角色收紧)" {
		t.Fatalf("状态栏文案: %q", got)
	}
	// 未实现 EffectiveSource 的旧实现(联动口径)不受影响:逐字回到旧文案
	linked := &tuiLinkedSandbox{mode: sdk.SandboxWorkspace, ap: &tuiApprovalStub{mode: sdk.ApprovalOpen}}
	if got := sandboxDisplay(linked); got != "workspace-write→full-access(审批联动)" {
		t.Fatalf("无来源能力的旧实现应保持联动口径: %q", got)
	}
}

// TestApprovalDisplayRoleTightened 审批段:有效档 != 声明档时并报,并说清是角色收紧。
func TestApprovalDisplayRoleTightened(t *testing.T) {
	ap := &roleTightenedApproval{mode: sdk.ApprovalSmart}
	eff, from := approvalDeviation(ap)
	if eff != "strict" || from != sdk.TierSourceRole {
		t.Fatalf("approvalDeviation = (%q, %q),期望 (strict, role)", eff, from)
	}
	out := approvalStatusText(ap, nil)
	if !strings.Contains(out, "审批: smart") || !strings.Contains(out, ";有效: strict(角色收紧)") {
		t.Fatalf("/approval 回显: %q", out)
	}
	// 与声明档一致(旧实现 / 无角色):不报冗余的有效档
	plain := &tuiApprovalStub{mode: sdk.ApprovalSmart}
	if out := approvalStatusText(plain, nil); out != "审批: smart" {
		t.Fatalf("无偏离时不该多报有效档: %q", out)
	}
}

// TestStatusLineApprovalTightened 状态栏审批段:角色声明了收紧档就必须显示
// (危险命令此刻已被直接拒,状态栏一字不提就是骗人)。
func TestStatusLineApprovalTightened(t *testing.T) {
	a, _, _ := linkedApp(t)
	a.model.state.Approval, a.model.state.ApprovalFromRole = "smart", true
	a.model.state.ApprovalEff, a.model.state.ApprovalFrom = "strict", sdk.TierSourceRole
	out := stripColor(renderStatusLine(a.model.state, 120))
	if !strings.Contains(out, "审批: 智能→严格(角色收紧)") {
		t.Fatalf("状态栏审批段应显示角色收紧后的有效档: %q", out)
	}
	// 停了角色:该段收回(基线显示与从前一致,不因为"曾经有过角色"而留一行)
	a.model.state.Approval, a.model.state.ApprovalEff, a.model.state.ApprovalFrom = "", "", ""
	a.model.state.ApprovalFromRole = false
	if out := stripColor(renderStatusLine(a.model.state, 120)); strings.Contains(out, "审批:") {
		t.Fatalf("没有档位可报时不该留空段: %q", out)
	}
}
