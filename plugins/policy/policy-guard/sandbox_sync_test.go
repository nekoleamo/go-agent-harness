// 档位联动开关(R10 ②-2):声明档与行为不一致的**可控性**部分 ——
// 可见性已由 EffectiveSandbox 解决(如实显示有效档),这里验"关掉联动后沙箱档独立生效"。
package policyguard

import (
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestSyncToggleChangesEffectiveMode 开关切换即时改变有效档(不必重建策略器),
// 且**拦截行为**同步改变(否则只是显示一致、行为仍被覆盖)。
// 取最极端组合:approval=open + 声明 read-only —— 联动开着时连 shell/写都放行,
// 关掉后必须回到 read-only 的拒绝语义。
func TestSyncToggleChangesEffectiveMode(t *testing.T) {
	ap := &ApprovalPolicy{mode: sdk.ApprovalOpen}
	p := &SandboxPolicy{root: t.TempDir(), mode: sdk.SandboxReadOnly, sync: true, approval: ap.Mode}
	if got := p.EffectiveMode(); got != sdk.SandboxFullAccess {
		t.Fatalf("联动开启:read-only 有效档应为 full-access,got %s", got)
	}
	if !p.SyncEnabled() {
		t.Fatal("SyncEnabled 应与构造一致")
	}
	if err := p.CheckTool("shell"); err != nil {
		t.Fatalf("联动开启:open 档下 shell 应放行,got %v", err)
	}
	if err := p.ValidatePath("out.txt"); err != nil {
		t.Fatalf("联动开启:open 档下写应放行,got %v", err)
	}
	p.SetSyncEnabled(false)
	if got := p.EffectiveMode(); got != sdk.SandboxReadOnly {
		t.Fatalf("关闭联动:有效档应回声明档,got %s", got)
	}
	if err := p.CheckTool("shell"); err == nil {
		t.Fatal("关闭联动后 read-only 语义应复位:open 档不再放行 shell")
	}
	if err := p.ValidatePath("out.txt"); err == nil {
		t.Fatal("关闭联动后 read-only 应拒写")
	}
	// 声明档本身不受开关影响(开关只改"是否被覆盖")
	if got := p.Mode(); got != sdk.SandboxReadOnly {
		t.Fatalf("开关不得改声明档,got %s", got)
	}
	p.SetSyncEnabled(true)
	if got := p.EffectiveMode(); got != sdk.SandboxFullAccess {
		t.Fatalf("重新开启应恢复覆盖,got %s", got)
	}
	if err := p.CheckTool("shell"); err != nil {
		t.Fatalf("重新开启后应恢复放行,got %v", err)
	}
}

var _ sdk.SandboxSync = (*SandboxPolicy)(nil)

// TestStartAppliesPrefsOverride 启动时 prefs 里的用户选择覆盖 config 默认。
// 放在插件 Start(而非各端启动):headless/定时任务也走同一路径,否则关掉联动这一安全
// 相关选择会在无人值守场景静默失效。
func TestStartAppliesPrefsOverride(t *testing.T) {
	cases := []struct {
		name    string
		config  any
		prefs   *bool
		wantEff sdk.SandboxMode
		wantOn  bool
	}{
		{"prefs 未设置:用 config 默认 true", true, nil, sdk.SandboxFullAccess, true},
		{"prefs 关:覆盖 config true", true, boolPtr(false), sdk.SandboxWorkspace, false},
		{"prefs 开:覆盖 config false", false, boolPtr(true), sdk.SandboxFullAccess, true},
		{"config false + prefs 未设置:保持 false", false, nil, sdk.SandboxWorkspace, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GAH_HOME", t.TempDir())
			if tc.prefs != nil {
				prefs.SetSandboxSync(*tc.prefs)
			}
			c := build(t, &fakeConfirm{resp: true}, map[string]any{
				"approval": "open", "sandbox": "workspace-write", "sync": tc.config,
			})
			var sb sdk.Sandbox
			if err := c.Inject("ctx.sandbox", &sb); err != nil {
				t.Fatal(err)
			}
			sc, ok := sb.(sdk.SandboxSync)
			if !ok {
				t.Fatalf("policy-guard 沙箱应实现 sdk.SandboxSync: %T", sb)
			}
			if sc.SyncEnabled() != tc.wantOn {
				t.Fatalf("开关应恢复为 %v,got %v", tc.wantOn, sc.SyncEnabled())
			}
			es := sb.(sdk.EffectiveSandbox)
			if got := es.EffectiveMode(); got != tc.wantEff {
				t.Fatalf("有效档应为 %s,got %s", tc.wantEff, got)
			}
		})
	}
}

// TestStartPrefsOnlyTouchesSync:prefs 只覆写联动开关,不碰审批/沙箱档位(那些走 SetMode 链)。
func TestStartPrefsOnlyTouchesSync(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	prefs.SetSandboxSync(false)
	prefs.SetSandbox(string(sdk.SandboxReadOnly)) // 另一条链:由 web/tui 的 ApplyPrefs 应用
	c := build(t, &fakeConfirm{resp: true}, map[string]any{
		"approval": "strict", "sandbox": "full-access", "sync": true,
	})
	var sb sdk.Sandbox
	if err := c.Inject("ctx.sandbox", &sb); err != nil {
		t.Fatal(err)
	}
	if got := sb.Mode(); got != sdk.SandboxFullAccess {
		t.Fatalf("插件 Start 不读 prefs.Sandbox(归 ApplyPrefs),应保持 config 档,got %s", got)
	}
	if sb.(sdk.SandboxSync).SyncEnabled() {
		t.Fatal("prefs.SandboxSync=false 应已生效")
	}
}

func boolPtr(v bool) *bool { return &v }
