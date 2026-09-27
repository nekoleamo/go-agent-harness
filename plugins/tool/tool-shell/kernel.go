// kernel.go:shell 命令的内核级沙箱包装(实现已抽到 internal/kernelsandbox,与
// MCP server / 外部插件共用同一份平台代码;2026-09-27 审计发现外部进程原本完全在裁决面之外)。
//
// 为什么需要:policy-guard 的写目标裁决是**工具边界的协作式控制** —— 它只看得见命令文本里的写目标。
// 编译器/构建系统/解释器/包装器内部的写(`python3 -c "open('/x','w')"`、`ccache` 自选落点、
// `make install DESTDIR=…`)对它不可见;这类写只有内核层拦得住(实测:seatbelt 生效后
// 解释器内部写同样被拒)。
//
// 档位从哪来:本工具常运行在外部插件进程(tool-basic)里,只经回调通道与宿主通信,**拿不到**
// 宿主的 ctx.sandbox 服务;宿主因此在执行入口(host-tools → host-bridge)把**有效档位**
// 经 sdk.SandboxHint 注入 ctx,这里只消费其语义:
//   - hint 未注入(旧宿主/直连测试)→ 不施加(不得假定档位);
//   - full-access / GAH_SHELL_KERNEL_SANDBOX=0 → 不施加;
//   - read-only → 只放行 jail(数据根内的临时/缓存区);
//   - workspace-write → 放行 workspace 根 + jail;
//   - workspace-write 但 workspace 根未知 → 按 read-only 处理(判不定的写更危险)。
//
// 与协作层的关系:内核层**只管写**,刻意与 policy-guard"只管写目标"的范围对齐 ——
// 不在这里发明第二套权限模型。唯一例外是**凭据目录的读**:读侧协作层只看得见命令文本里的
// 字面路径(解释器内动态拼路径可绕过,2026-09-27 审计实测),故取同一份名单在更底层再拒一次
// (见 credentialReadDenyDirs;darwin 有等价能力,Linux 无 —— 见 kernelsandbox/linux.go 登记)。
//
// 平台:darwin 用 seatbelt(profile);linux 用 Landlock(自举 helper);其余平台无等价能力
// → 不施加但**显式告警**(不静默降级)。
//
// 开关:GAH_SHELL_KERNEL_SANDBOX=0 显式关闭。与环境 jail(GAH_SHELL_JAIL)相互独立,
// 但 jail 关闭时内核沙箱的临时/缓存白名单失去锚点,故一并跳过(见 kernelWrap,显式告警)。
package toolshell

import (
	"context"
	"os"

	"github.com/nekoleamo/go-agent-harness/internal/kernelsandbox"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// kernelSandboxEnv 内核级沙箱显式关闭开关("0" = 关)。
const kernelSandboxEnv = "GAH_SHELL_KERNEL_SANDBOX"

// credReadKernelEnv 内核层**凭据读拒绝**的显式关闭开关("0" = 关)。
//
// 为何单独一个开关:文本层的凭据判定只看得见命令文本里的字面路径(解释器内动态拼路径可绕过,
// 见 credentialReadDenyDirs),内核层能兜住这一类;但它同样会拒掉"直读 keyfile"的
// ssh/git push 与 aws/gcloud CLI(用 agent/keychain 时不受影响) —— 那是合法工作流,
// 故给一条显式的退路:关掉即退回纯文本层判定(降级可见,不静默)。
const credReadKernelEnv = "GAH_SHELL_CRED_READ_KERNEL"

// credReadDenyEnv 内部名(与 credReadKernelEnv 同值):调用方语义上是"读拒绝开关"。
const credReadDenyEnv = credReadKernelEnv

// jailSwitchEnv 环境 jail 的关闭开关(内核白名单锚点依赖它)。
const jailSwitchEnv = "GAH_SHELL_JAIL"

// sandboxExec darwin 的 seatbelt 前端路径。
//
// 之所以是包级变量而不是常量:测试要覆盖"前端缺失 → 降级不施加"分支,且测试文件在所有平台
// 都要能编译(Linux CI 也会跑测试包)。值经 Spec 传给 internal/kernelsandbox。
var sandboxExec = "/usr/bin/sandbox-exec"

// credentialReadDenyDirs 内核层读拒绝的目录(已解析真实路径;开关关闭 = nil)。
//
// 为何是跨平台变量而只有 darwin 消费:名单与开关是**跨平台契约**(README 环境变量表),
// 且测试文件在所有平台都要能编译;Linux 分支无等价能力的具体原因见 kernelsandbox/linux.go。
//
// 只拒**目录**(sdk.CredentialHomeDirs + $GAH_HOME/config),不拒 .npmrc/.netrc 之类凭据文件:
// 后者是 npm/git/curl 等常规工具自己会读的,内核层一拒就是大面积工作流失败;
// 目录(.ssh/.aws/.gnupg/gcloud/数据根 config)才是“被解释器读出来”的高价值目标。
func credentialReadDenyDirs() []string {
	if os.Getenv(credReadKernelEnv) == "0" {
		return nil
	}
	dirs := sdk.CredentialDenyDirs() // 与外部进程白名单同源(见 sdk/credentialpath.go)
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, resolvePath(d))
	}
	return out
}

// kernelWrap 按有效档位返回要前置到 argv 的包装(空 = 不施加)。
// ok = 宿主是否注入了沙箱上下文(sdk.SandboxHintOf 的第二返回值)。
func kernelWrap(hint sdk.SandboxHint, ok bool) []string {
	if !ok {
		return nil // 旧宿主/直连测试:不猜档位
	}
	if hint.Mode == "" {
		return nil // 只下传了工作根(无沙箱宿主):档位未知 → 不猜测、不施加
	}
	return kernelsandbox.Wrap(kernelsandbox.Spec{
		Mode:           hint.Mode,
		Root:           hint.Root,
		Jail:           jailRoot(),
		ReadDeny:       credentialReadDenyDirs(),
		ReadDenySwitch: credReadDenyEnv,
		Switch:         kernelSandboxEnv,
		Label:          "tool-shell",
		SandboxExec:    sandboxExec,
		// jail 关闭时子进程的 TMPDIR/缓存根回落到系统目录与家目录,内核白名单失去锚点 ——
		// 强行施加会让 go build / npm 之类大面积失败且原因难查。显式跳过并说明,不静默降级。
		RequireJailSwitch: jailSwitchEnv,
	})
}

// kernelWrapCtx 从 ctx 读取宿主注入的沙箱上下文并给出包装(shell.go/pty.go 的唯一入口)。
func kernelWrapCtx(ctx context.Context) []string {
	hint, ok := sdk.SandboxHintOf(ctx)
	return kernelWrap(hint, ok)
}

// resolvePath 解析软链(见 kernelsandbox.ResolvePath;保留此名供既有测试与调用点使用)。
func resolvePath(p string) string { return kernelsandbox.ResolvePath(p) }

// prefixedArgv 拼接包装与真实命令(见 kernelsandbox.PrefixedArgv)。
func prefixedArgv(pre []string, name string, rest ...string) []string {
	return kernelsandbox.PrefixedArgv(pre, name, rest...)
}
