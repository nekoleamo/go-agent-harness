// 内核级沙箱端到端(第 3 组 ①-E 的结合部验证):
//
// 三个子任务各自的单测覆盖不到「管道 + 消费」的结合部 ——
//
//	host-tools 注入 sdk.SandboxHint(有效档位)→ host-bridge/serve 透传 → tool-shell 施加平台内核限制,
//
// 本文件用**真实管道**(单进程装配 base + policy-guard + host-tools + tool-shell,经 ctx.tools 执行)
// 断言"策略层看不见的写"也被拦住,并做归因(切档后同一命令必须放行/失败)。
//
// 为什么用「解释器内部写」当探针:`python3 -c "open('/tmp/x','w')"` 的写目标不在命令文本里(在程序里),
// policy-guard 的词法级裁决**看不见**它 —— 它是本次交付前文档里明确登记的"诚实边界"(①-F)。
// 若内核层没生效,这条命令会真的写出文件;因此"文件不存在"是最强的端到端断言。
package tests

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// kernelSandboxAvailable 当前平台是否真有内核级沙箱能力(不可用则 skip,不伪造通过)。
// darwin:sandbox-exec 存在;linux:Landlock ABI 探测(与 tool-shell 同源语义,查询返回 >=1 才可用)。
func kernelSandboxAvailable(t *testing.T) (bool, string) {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
			return false, "macOS 无 /usr/bin/sandbox-exec"
		}
		return true, ""
	case "linux":
		if landlockABIVersion() < 1 {
			return false, "内核不支持 Landlock(< 5.13 或未启用)"
		}
		return true, ""
	default:
		return false, runtime.GOOS + " 无等价内核能力(设计取舍:仅 Windows 等平台退回协作式控制)"
	}
}

// runShellTool 经唯一执行入口(ctx.tools,与工具调用同一路径)跑一条 shell 命令。
func runShellTool(t *testing.T, c *ctx.Ctx, command string) *sdk.ToolResult {
	t.Helper()
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatalf("取 ctx.tools 失败: %v", err)
	}
	res, err := tools.Execute(context.Background(), "shell", fmt.Sprintf(`{"command":%q}`, command))
	if err != nil {
		t.Fatalf("流水线不应把工具错误抛给调用方: %v", err)
	}
	return res
}

// setSandboxMode 切换沙箱档位(夹具 sync=true 且 approval=smart → 声明档即有效档,不被联动覆盖)。
func setSandboxMode(t *testing.T, c *ctx.Ctx, mode sdk.SandboxMode) {
	t.Helper()
	var sb sdk.Sandbox
	if err := c.Inject("ctx.sandbox", &sb); err != nil {
		t.Fatalf("取 ctx.sandbox 失败: %v", err)
	}
	sb.SetMode(mode)
	if eff := effectiveMode(sb); eff != mode {
		t.Fatalf("档位未生效:声明 %s 有效 %s", mode, eff)
	}
}

func effectiveMode(sb sdk.Sandbox) sdk.SandboxMode {
	if es, ok := sb.(sdk.EffectiveSandbox); ok {
		return es.EffectiveMode()
	}
	return sb.Mode()
}

// TestKernelSandboxBlocksInvisibleWrite 端到端:workspace-write 档下,策略层看不见的越界写也必须被拦,
// 且区内同类写不受影响;再切 full-access 做归因(同一命令必须成功)。
func TestKernelSandboxBlocksInvisibleWrite(t *testing.T) {
	if ok, why := kernelSandboxAvailable(t); !ok {
		t.Skip("跳过内核级沙箱端到端:" + why)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("跳过:无 python3 可用于构造「解释器内部写」探针")
	}
	c, _ := buildTestEnv(t)
	setSandboxMode(t, c, sdk.SandboxWorkspace)

	outside := filepath.Join(t.TempDir(), "escaped.txt")
	inside := "gah-kernel-probe-inside.txt" // 相对路径 → workspace 根(= cwd)内
	t.Cleanup(func() { os.Remove(inside) })

	// 1) 区内写(策略层与内核层都允许):不得误拦
	res := runShellTool(t, c, fmt.Sprintf(`python3 -c "open('%s','w').write('ok')"`, inside))
	if res.Error != "" || strings.Contains(res.Content, "exit_error") {
		t.Fatalf("workspace 内写应成功(内核白名单未覆盖 workspace?):%+v", res)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Fatalf("workspace 内写未落盘: %v", err)
	}

	// 2) 越界写(策略层看不见写目标):必须被内核层拦住,且文件不得存在
	res = runShellTool(t, c, fmt.Sprintf(`python3 -c "open('%s','w').write('escaped')"`, outside))
	if _, err := os.Stat(outside); err == nil {
		os.Remove(outside)
		t.Fatalf("越界写真的落盘了 —— 内核级沙箱未生效(hint 管道断在内核层之前?):%+v", res)
	}
	low := strings.ToLower(res.Content)
	if !strings.Contains(low, "operation not permitted") && !strings.Contains(low, "permission denied") {
		t.Fatalf("越界写虽未落盘,但不是内核层拒绝(错误形态:%+v)", res)
	}
	// 归因:策略层对这条命令**没有**裁决能力(写目标不在文本里),故不得出现路径裁决的措辞
	if strings.Contains(res.Content, "写目标被拒") {
		t.Fatalf("该探针本应只有内核层拦得住,却出现策略层措辞(探针选错了):%+v", res)
	}

	// 3) 归因对照:切 full-access(内核层不施加)后,同一越界写必须成功
	setSandboxMode(t, c, sdk.SandboxFullAccess)
	res = runShellTool(t, c, fmt.Sprintf(`python3 -c "open('%s','w').write('escaped')"`, outside))
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("full-access 档下越界写应成功(证明第 2 步的拦截来自沙箱层而非环境问题):%+v", res)
	}
	os.Remove(outside)
}

// TestKernelSandboxFollowsEffectiveMode 端到端:管道必须下发**有效**档位(联动后),而不是声明档位。
//
// 只改审批档、不动沙箱声明档(workspace-write),看内核层是否随之收手:
//
//	approval=smart → 有效档 workspace-write → 越界写被内核层拦
//	approval=open  → 有效档 full-access    → 同一命令必须成功(内核层不施加)
//
// 若 host-tools 漏用 EffectiveSandbox(用声明档),第二段会被拦 —— 该测试即失败。
// 这条断言同时钉住 R10 ②-1(档位联动)与第 3 组(内核层消费有效档)两处不变式。
func TestKernelSandboxFollowsEffectiveMode(t *testing.T) {
	if ok, why := kernelSandboxAvailable(t); !ok {
		t.Skip("跳过内核级沙箱端到端:" + why)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("跳过:无 python3 可用于构造「解释器内部写」探针")
	}
	c, _ := buildTestEnv(t)
	outside := filepath.Join(t.TempDir(), "escaped.txt")
	cmd := fmt.Sprintf(`python3 -c "open('%s','w').write('x')"`, outside)

	var ap sdk.ApprovalService
	if err := c.Inject("ctx.approval", &ap); err != nil {
		t.Fatalf("取 ctx.approval 失败: %v", err)
	}
	t.Cleanup(func() { ap.SetMode(sdk.ApprovalSmart) })
	var sb sdk.Sandbox
	if err := c.Inject("ctx.sandbox", &sb); err != nil {
		t.Fatalf("取 ctx.sandbox 失败: %v", err)
	}

	// 第一段:smart → 有效 workspace-write → 必须被拦
	if eff := effectiveMode(sb); eff != sdk.SandboxWorkspace {
		t.Fatalf("前置:smart 下有效档应为 workspace-write,实为 %s", eff)
	}
	runShellTool(t, c, cmd)
	if _, err := os.Stat(outside); err == nil {
		os.Remove(outside)
		t.Fatal("workspace-write 档下越界写竟落盘")
	}

	// 第二段:open → 有效 full-access(声明档未变)→ 必须放行
	ap.SetMode(sdk.ApprovalOpen)
	if eff := effectiveMode(sb); eff != sdk.SandboxFullAccess {
		t.Fatalf("open 联动后有效档应为 full-access,实为 %s(沙箱声明档仍为 %s)", eff, sb.Mode())
	}
	res := runShellTool(t, c, cmd)
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("有效档 full-access 下越界写应成功(内核层错按声明档施加了限制?):%+v", res)
	}
	os.Remove(outside)
}
