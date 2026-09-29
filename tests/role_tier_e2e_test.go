// 角色级权限收紧端到端(第九十二批,跨插件):
//
// 装配**真实的** policy-guard + host-roles(不是桩),钉住三件事:
//  1. 改角色定义 / 切角色后**裁决行为**立刻变(不是只改显示):角色声明 read-only 时,
//     全局 open + sync 放大出来的 full-access 被压回只读,写路径真被拦;
//  2. 审批收紧到 strict = 危险命令**直接拒**,连确认框都不弹(计数确认服务为证);
//  3. 命令回显说清来源(「(角色收紧)」而不是写死「联动覆盖生效」)。
//
// 证伪面:同一条探针在**未启用角色**时是放行的(全局 open + sync) —— 拒绝只可能来自角色档。
package tests

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// tierConfirm 计数确认服务:证明 strict 档"没弹窗"来自角色档,而不是确认服务没接上。
type tierConfirm struct {
	mu    sync.Mutex
	calls int
}

func (c *tierConfirm) Confirm(context.Context, string) (bool, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return true, nil // 一律允许:探针被拒只可能是因为档位,而不是"用户点了拒绝"
}

func (c *tierConfirm) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// buildRoleTierEnv 装配角色链 + policy-guard(approval=open, sandbox=workspace-write, sync=true)。
// 这套组合下"未启用角色"= full-access(联动放大),于是角色档一收紧就看得出来。
func buildRoleTierEnv(t *testing.T) (sdk.Ctx, *tierConfirm) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-tools"},
		{ID: "tool-shell"},
		{ID: "host-system-prompt"},
		{ID: "host-llm"},
		{ID: "host-skills"},
		{ID: "host-commands"},
		{ID: "host-internal-commands"},
		{ID: "host-roles"},
		{ID: "policy-guard", Data: map[string]any{"approval": "open", "sandbox": "workspace-write", "sync": true}},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	confirm := &tierConfirm{}
	if err := c.Provide("ctx.confirm", confirm); err != nil {
		t.Fatal(err)
	}
	if err := baseb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c, confirm
}

// probeWrite 工作区之外的删除命令:审批层(open 不拦)与沙箱层(档位)判它各一次,
// 正好把"角色收紧的是哪一层"分开看。路径形态与 sandbox_sync_e2e_test.go 同款
// (Windows: `/c/...`;反斜杠会被 git-bash 当转义吃掉)。
func probeWrite(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "gah-role-tier-probe")
	if runtime.GOOS == "windows" {
		vol := filepath.VolumeName(dir)
		dir = "/" + strings.ToLower(strings.TrimSuffix(vol, ":")) + strings.TrimPrefix(filepath.ToSlash(dir), vol)
	}
	b, err := json.Marshal(map[string]string{"command": "rm -rf " + dir})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRoleTierTightensLayersE2E 角色收紧沙箱/审批后:两层各自的拦截都跟着变,来源可查。
func TestRoleTierTightensLayersE2E(t *testing.T) {
	c, confirm := buildRoleTierEnv(t)
	var svc sdk.RoleService
	if err := c.Inject("ctx.roles", &svc); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	sb := sandboxOf(t, c)
	var ap sdk.ApprovalService
	if err := c.Inject("ctx.approval", &ap); err != nil {
		t.Fatal(err)
	}
	probe := probeWrite(t)
	exec := func() string {
		t.Helper()
		res, err := tools.Execute(t.Context(), "shell", probe)
		if err != nil {
			t.Fatalf("Execute 不应返回传输错误(业务失败在 res.Error 里): %v", err)
		}
		return res.Error
	}

	// 未启用角色:全局 open + sync ⇒ 有效 full-access,同一条探针放行(证伪面)
	if got := sb.(sdk.EffectiveSandbox).EffectiveMode(); got != sdk.SandboxFullAccess {
		t.Fatalf("基线有效档应为 full-access: %s", got)
	}
	if msg := exec(); msg != "" {
		t.Fatalf("基线(无角色)应放行,got %q", msg)
	}

	// 角色①:只收紧沙箱(审批不声明)
	if _, err := svc.Create(sdk.RoleSpec{ID: "audit", Name: "审计", Sandbox: "read-only"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.Use("audit"); err != nil {
		t.Fatal(err)
	}
	if got := sb.(sdk.EffectiveSandbox).EffectiveMode(); got != sdk.SandboxReadOnly {
		t.Fatalf("角色 read-only 应压过联动放大: %s", got)
	}
	if got := sb.(sdk.EffectiveSource).EffectiveFrom(); got != sdk.TierSourceRole {
		t.Fatalf("沙箱偏离来源应为 role,got %q", got)
	}
	if msg := exec(); msg == "" {
		t.Fatal("角色收紧到 read-only 后写路径应被拦")
	} else if !strings.Contains(msg, "sandbox") && !strings.Contains(msg, "沙箱") {
		t.Fatalf("拦截应来自沙箱层: %q", msg)
	}
	if got := ap.(sdk.EffectiveApproval).EffectiveMode(); got != sdk.ApprovalOpen {
		t.Fatalf("角色没声明审批时有效档应保持 open,got %s", got)
	}
	// 命令回显:来源要写成"角色收紧",不能写死"联动覆盖生效"
	if out, err := runCmd(t, cmds, "sandbox"); err != nil || !strings.Contains(out, "(角色收紧)") {
		t.Fatalf("/sandbox 应报角色收紧: %q %v", out, err)
	}
	// 联动关掉也不能把角色收紧说没了
	if out, err := runCmd(t, cmds, "sandbox", "sync", "off"); err != nil || !strings.Contains(out, "当前有效档 read-only(角色收紧)") {
		t.Fatalf("关联动后仍应报角色收紧: %q %v", out, err)
	}

	// 角色②:只收紧审批(strict)⇒ 危险命令直接拒,且**一次都不弹确认**
	if _, err := svc.Update("audit", sdk.RoleSpec{ID: "audit", Name: "审计", Approval: "strict"}); err != nil {
		t.Fatal(err)
	}
	if got := ap.(sdk.EffectiveApproval).EffectiveMode(); got != sdk.ApprovalStrict {
		t.Fatalf("改角色定义后审批有效档应立刻变 strict(不得缓存),got %s", got)
	}
	// 沙箱:角色档撤掉后回自己的档位(联动已在上面关掉 ⇒ 声明档 workspace-write 生效)
	if got := sb.(sdk.EffectiveSandbox).EffectiveMode(); got != sdk.SandboxWorkspace {
		t.Fatalf("沙箱档撤掉后应回声明档 workspace-write,got %s", got)
	}
	before := confirm.count()
	if msg := exec(); msg == "" {
		t.Fatal("角色收紧到 strict 后危险命令应被直接拒")
	} else if !strings.Contains(msg, "审批") && !strings.Contains(msg, "approval") {
		t.Fatalf("拒绝应来自审批层: %q", msg)
	}
	if confirm.count() != before {
		t.Fatalf("strict 档不该弹确认框(calls %d → %d)", before, confirm.count())
	}
	if out, err := runCmd(t, cmds, "approval"); err != nil || !strings.Contains(out, ";有效: strict(角色收紧)") {
		t.Fatalf("/approval 应报角色收紧后的有效档: %q %v", out, err)
	}

	// 停用角色:两层都回全局档(角色档不是"粘住"的)
	if err := svc.Use(""); err != nil {
		t.Fatal(err)
	}
	if got := ap.(sdk.EffectiveApproval).EffectiveMode(); got != sdk.ApprovalOpen {
		t.Fatalf("停用角色后审批应回 open,got %s", got)
	}
	if got := sb.(sdk.EffectiveSandbox).EffectiveMode(); got != sdk.SandboxWorkspace {
		t.Fatalf("停用角色后沙箱应回全局档 workspace-write,got %s", got)
	}
	if out, err := runCmd(t, cmds, "approval"); err != nil || strings.Contains(out, "角色收紧") {
		t.Fatalf("停用角色后不该再提角色收紧: %q %v", out, err)
	}
}
