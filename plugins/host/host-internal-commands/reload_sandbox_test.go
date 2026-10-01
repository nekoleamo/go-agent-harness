package hostintcmd

// 补两处**既有**低覆盖路径(cmdReload 72% / sandboxSyncText 75%)。
// 起因:本批给这个包加了 memory/skillpack/rolepack 三组命令(约 500 行新代码),
// 整包覆盖率被稀释到 92.3% —— 按纪律补测而不是降门,而这两处本来就该补。

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// ---- /reload 的三条分支 ----

// noReloadSP 不实现 ReloadableInstructions 的系统提示服务。
type noReloadSP struct{ sdk.SystemPromptService }

func TestCmdReloadWithoutSystemPrompt(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	c, _ := buildEnv(t) // 没装 host-system-prompt ⇒ ctx.systemPrompt 拿不到
	h := &Host{c: c}
	if _, err := h.cmdReload(nil); err == nil {
		t.Fatal("缺 ctx.systemPrompt 时应显式报错")
	}
}

// stubReloadSP 实现 ReloadableInstructions,可控制成败。
type stubReloadSP struct {
	sdk.SystemPromptService
	err error
}

func (s *stubReloadSP) ReloadInstructions() error { return s.err }

func TestCmdReloadFailureKeepsOldValue(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	c, _ := buildEnv(t)
	sp := &stubReloadSP{err: errors.New("文件读不了")}
	_ = c.Provide("ctx.systemPrompt", sp)
	h := &Host{c: c}
	out, err := h.cmdReload(nil)
	if err == nil {
		t.Fatal("重载失败应报错(旧值保留要说清)")
	}
	if !strings.Contains(err.Error(), "旧值保留") {
		t.Fatalf("错误文案应说明旧值保留: %v", err)
	}
	_ = out
}

func TestCmdReloadWithoutReloadableSupport(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	c, _ := buildEnv(t)
	_ = c.Provide("ctx.systemPrompt", noReloadSP{})
	h := &Host{c: c}
	if _, err := h.cmdReload(nil); err == nil {
		t.Fatal("服务不支持 ReloadableInstructions 时应显式报错")
	}
}

// ---- 沙箱联动文案的分支 ----

// plainSB 只声明档位(不实现联动/有效档)。
type plainSB struct {
	sdk.Sandbox
	mode sdk.SandboxMode
}

func (p plainSB) Mode() sdk.SandboxMode { return p.mode }

func TestSandboxSyncTextUnsupported(t *testing.T) {
	out := sandboxSyncText(plainSB{mode: sdk.SandboxWorkspace}, sdk.ApprovalOpen)
	if !strings.Contains(out, "不支持联动开关") {
		t.Fatalf("不支持联动时应如实说明: %s", out)
	}
}

// syncSB 实现联动开关,可开可关;实现有效档用于验证偏离标注。
type syncSB struct {
	plainSB
	on  bool
	eff sdk.SandboxMode
}

func (s syncSB) SyncEnabled() bool              { return s.on }
func (s syncSB) SetSyncEnabled(on bool)         { _ = on } // 联动开关的写侧不在本用例范围
func (s syncSB) EffectiveMode() sdk.SandboxMode { return s.eff }

func TestSandboxSyncTextOff(t *testing.T) {
	sb := syncSB{plainSB: plainSB{mode: sdk.SandboxWorkspace}, on: false, eff: sdk.SandboxWorkspace}
	out := sandboxSyncText(sb, sdk.ApprovalOpen)
	if !strings.Contains(out, "off") || !strings.Contains(out, "独立生效") {
		t.Fatalf("关闭联动的文案应说清档位独立生效: %s", out)
	}
}

func TestSandboxSyncTextOnWithDeviation(t *testing.T) {
	sb := syncSB{plainSB: plainSB{mode: sdk.SandboxReadOnly}, on: true, eff: sdk.SandboxFullAccess}
	out := sandboxSyncText(sb, sdk.ApprovalOpen)
	if !strings.Contains(out, "on") || !strings.Contains(out, "有效档") {
		t.Fatalf("开启联动且档位偏离时应给出有效档: %s", out)
	}
}

// ---- /reload 的角色连带重载(启用了角色插件时,手改 roles/<id>/AGENTS.md 也要生效)----

// stubRoleSvc 实现 RoleService + ReloadableRoles,可控制成败。
type stubRoleSvc struct {
	sdk.RoleService
	err     error
	reloads int
}

func (s *stubRoleSvc) ReloadRoles() error { s.reloads++; return s.err }

func TestCmdReloadWithRoles(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	c, _ := buildEnv(t)
	sp := &stubReloadSP{}
	_ = c.Provide("ctx.systemPrompt", sp)
	rs := &stubRoleSvc{}
	_ = c.Provide("ctx.roles", rs)
	h := &Host{c: c}
	out, err := h.cmdReload(nil)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if rs.reloads != 1 {
		t.Fatalf("应连带重载角色一次,got %d", rs.reloads)
	}
	if !strings.Contains(out, "角色定义") {
		t.Fatalf("回执应说明角色定义也重载了: %s", out)
	}
}

func TestCmdReloadRolesFailureIsLoud(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	c, _ := buildEnv(t)
	_ = c.Provide("ctx.systemPrompt", &stubReloadSP{})
	_ = c.Provide("ctx.roles", &stubRoleSvc{err: errors.New("角色定义坏了")})
	h := &Host{c: c}
	_, err := h.cmdReload(nil)
	if err == nil {
		t.Fatal("角色重载失败必须报错(指令已重载但角色没重载 = 半失败,不能静默)")
	}
	if !strings.Contains(err.Error(), "指令已重载") {
		t.Fatalf("错误文案应说清「指令已重载、角色没重载」: %v", err)
	}
}

// ---- /export 的 .html 分支 ----

func TestCmdExportHTMLBranch(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	c, _ := buildEnv(t)
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Skip("该测试环境未装会话日志,跳过 html 导出分支")
	}
	h := &Host{c: c}
	p := filepath.Join(t.TempDir(), "s.html")
	if _, err := h.cmdExport([]string{p}); err != nil {
		t.Fatalf("导出 html: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("没落盘: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(raw)), "<html") {
		t.Fatalf(".html 应是自包含网页,got %q", string(raw[:min(120, len(raw))]))
	}
}

// TestCmdExportRejectsURLPath /export 收到网址形态路径必须显式失败:
// 真机事故是模型把网页地址当文件路径,filepath.Clean 把 "//" 折成 "/",
// 于是在工作区里长出 `https:/host/...` 垃圾目录树。
func TestCmdExportRejectsURLPath(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	c, _ := buildEnv(t)
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Skip("该测试环境未装会话日志")
	}
	h := &Host{c: c}
	_, err := h.cmdExport([]string{"https://example.com/x.html"})
	if err == nil {
		t.Fatal("网址形态路径应显式失败")
	}
	if !strings.Contains(err.Error(), "网址形态") {
		t.Fatalf("错误文案应点明原因: %v", err)
	}
}

func TestCmdExportJSONLBranch(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	c, _ := buildEnv(t)
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Skip("该测试环境未装会话日志,跳过 jsonl 分支")
	}
	h := &Host{c: c}
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if _, err := h.cmdExport([]string{p}); err != nil {
		t.Fatalf("导出 jsonl: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("没落盘: %v", err)
	}
	// 空会话导出 0 字节是合法结果(没有事件);关键是文件存在且不是 HTML
	if strings.Contains(strings.ToLower(string(raw)), "<html") {
		t.Fatalf("非 .html 结尾应导 jsonl,got %q", string(raw))
	}
}

// ---- 档位偏离来源:角色收紧要标出来(第九十二批口径)----

// roleTightSB 有效档偏离且来源 = 角色(不是审批联动)。
type roleTightSB struct {
	syncSB
}

func (r roleTightSB) EffectiveFrom() string { return "role" }

func TestSandboxSyncTextMarksRoleTightening(t *testing.T) {
	// 声明 full-access、实际有效 read-only,且来源是角色收紧 ⇒ 走"偏离"分支并标注角色
	sb := roleTightSB{syncSB{plainSB: plainSB{mode: sdk.SandboxFullAccess}, on: true, eff: sdk.SandboxReadOnly}}
	out := sandboxSyncText(sb, sdk.ApprovalSmart)
	if !strings.Contains(out, "角色收紧") {
		t.Fatalf("偏离来自角色时应标注来源: %s", out)
	}
}

func TestSandboxSyncTextOffMarksRoleTightening(t *testing.T) {
	// 关闭联动 ≠ 档位就是声明档:角色收紧仍在作用(第九十二批的关键区分)
	sb := roleTightSB{syncSB{plainSB: plainSB{mode: sdk.SandboxFullAccess}, on: false, eff: sdk.SandboxReadOnly}}
	out := sandboxSyncText(sb, sdk.ApprovalSmart)
	if !strings.Contains(out, "off") || !strings.Contains(out, "角色收紧") {
		t.Fatalf("关闭联动且角色收紧时应同时说明: %s", out)
	}
}

func TestTierDeviationSourceWithoutCapability(t *testing.T) {
	if got := tierDeviationSource(plainSB{mode: sdk.SandboxReadOnly}); got != "" {
		t.Fatalf("不实现 EffectiveSource 时应返回空,got %q", got)
	}
}
