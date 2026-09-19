// 档位联动开关的端到端契约(R10 ②-2,跨插件):
//
//  1. 启动恢复走**插件 Start**(而不是各端 UI 启动钩子)—— 因为无人值守(定时任务/headless)
//     不经过 web/tui 的 ApplyPrefs,只在那里恢复会让「关掉联动」这个安全相关选择静默失效;
//  2. 关掉联动后**拦截行为**跟着变(不只是显示):approval=open + 声明 workspace-write 时,
//     full-access 该有的放行消失;
//  3. 命令路径(/sandbox sync)写入的用户选择能在下次启动被读回(prefs 往返)。
package tests

import (
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildSyncEnv 装配含 policy-guard(approval=open, sandbox=workspace-write)+ 命令面的环境。
// syncInConfig 为插件 config 的 data.sync 默认值;GAH_HOME 由调用方先设好(prefs 需在其下)。
func buildSyncEnv(t *testing.T, syncInConfig bool) sdk.Ctx {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "tool-shell"},
		{ID: "host-system-prompt"},
		{ID: "host-commands"},
		{ID: "host-internal-commands"},
		{ID: "llm-mock", Data: map[string]any{"script": []any{map[string]any{"text": "ok", "finish": "stop"}}}},
		{ID: "policy-guard", Data: map[string]any{"approval": "open", "sandbox": "workspace-write", "sync": syncInConfig}},
		{ID: "host-agent-loop"},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := baseb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c
}

// runCmd 按注册表取命令并执行(与 TUI/Web 同一分发路径:spec.Run(除命令名外的参数))。
func runCmd(t *testing.T, cmds sdk.CommandRegistry, name string, args ...string) (string, error) {
	t.Helper()
	spec, ok := cmds.Get(name)
	if !ok {
		t.Fatalf("命令 /%s 未注册", name)
	}
	return spec.Run(args)
}

// sandboxOf 取装配后的沙箱服务。
func sandboxOf(t *testing.T, c sdk.Ctx) sdk.Sandbox {
	t.Helper()
	var sb sdk.Sandbox
	if err := c.Inject("ctx.sandbox", &sb); err != nil {
		t.Fatal(err)
	}
	return sb
}

// TestSandboxSyncE2EStartFromPrefs 启动即按 prefs 恢复开关(与 config 默认相反也要生效)。
func TestSandboxSyncE2EStartFromPrefs(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	off := false
	prefs.SetSandboxSync(off)
	c := buildSyncEnv(t, true) // config 默认开
	sb := sandboxOf(t, c)
	sc, ok := sb.(sdk.SandboxSync)
	if !ok {
		t.Fatalf("policy-guard 沙箱应实现 sdk.SandboxSync: %T", sb)
	}
	if sc.SyncEnabled() {
		t.Fatal("prefs 里的 off 应在 Start 时生效(否则无人值守场景静默回退)")
	}
	if got := sb.(sdk.EffectiveSandbox).EffectiveMode(); got != sdk.SandboxWorkspace {
		t.Fatalf("关掉联动后有效档应回声明档,got %s", got)
	}
	// 声明档本身不受影响
	if got := sb.Mode(); got != sdk.SandboxWorkspace {
		t.Fatalf("声明档应保持 config 值,got %s", got)
	}
}

// TestSandboxSyncE2ECommandRoundTrip 命令切换 → 拦截行为变化 → 偏好落盘 → 下次启动读回。
func TestSandboxSyncE2ECommandRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	c := buildSyncEnv(t, true)
	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	sb := sandboxOf(t, c)
	// 写工作区之外的命令:两条路径的判别都靠它 —— 联动开着(有效 full-access)放行,
	// 关掉后(workspace-write)必须被路径裁决拦下。
	probe := `{"command":"rm -rf /tmp/gah-sync-probe"}`
	resOf := func() string {
		res, err := tools.Execute(t.Context(), "shell", probe)
		if err != nil {
			t.Fatalf("Execute 不应返回传输错误(业务失败在 res.Error 里): %v", err)
		}
		return res.Error
	}
	// 联动开着:approval=open → 有效 full-access,工作区外写也放行(open 档语义)
	if got := sb.(sdk.EffectiveSandbox).EffectiveMode(); got != sdk.SandboxFullAccess {
		t.Fatalf("初始有效档应为 full-access: %s", got)
	}
	if msg := resOf(); msg != "" {
		t.Fatalf("联动开启(有效 full-access)下应放行,got %q", msg)
	}
	// 关掉联动:有效档回 workspace-write + open 档不再放行该命令
	out, err := runCmd(t, cmds, "sandbox", "sync", "off")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "沙箱联动 -> off") {
		t.Fatalf("切换回显: %q", out)
	}
	if got := sb.(sdk.EffectiveSandbox).EffectiveMode(); got != sdk.SandboxWorkspace {
		t.Fatalf("关掉联动后有效档应为 workspace-write: %s", got)
	}
	if msg := resOf(); !strings.Contains(msg, "被拒") {
		t.Fatalf("关掉联动后 open 档不得再放行(行为必须跟着开关变,不能只改显示),got %q", msg)
	}
	// 偏好落盘:文件里能看到用户的显式选择
	raw, err := os.ReadFile(home + "/config/gah-state.json")
	if err != nil {
		t.Fatalf("偏好应落盘: %v", err)
	}
	var p struct {
		SandboxSync *bool `json:"sandbox_sync"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.SandboxSync == nil || *p.SandboxSync {
		t.Fatalf("偏好文件应记录 sandbox_sync=false: %s", raw)
	}
	// 下次启动读回:config 仍是 true,但开关保持 off
	c2 := buildSyncEnv(t, true)
	sb2 := sandboxOf(t, c2)
	if sb2.(sdk.SandboxSync).SyncEnabled() {
		t.Fatal("重启应读回用户的 off 选择")
	}
	// 回写 on:重开联动 + 偏好 true
	c3 := buildSyncEnv(t, true)
	var cmds3 sdk.CommandRegistry
	if err := c3.Inject("ctx.commands", &cmds3); err != nil {
		t.Fatal(err)
	}
	if _, err := runCmd(t, cmds3, "sandbox", "sync", "on"); err != nil {
		t.Fatal(err)
	}
	if !sandboxOf(t, c3).(sdk.SandboxSync).SyncEnabled() {
		t.Fatal("on 应重新开启联动")
	}
	if v := prefs.Load().SandboxSync; v == nil || !*v {
		t.Fatalf("偏好应回写 true,got %v", v)
	}
}
