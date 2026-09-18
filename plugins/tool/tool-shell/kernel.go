// kernel.go:shell 命令的**内核级**沙箱(第 3 组 ①-E:让"判不出的写"也拦得住)。
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
// 与协作层的关系:内核层**只管写**(读与网络不设限),刻意与 policy-guard"只管写目标"的范围对齐 ——
// 不在这里发明第二套权限模型;读凭据类路径仍由 policy-guard 判定。
//
// 平台:darwin 用 seatbelt(sandbox-exec profile);linux 用 Landlock(自举 helper,见 kernel_linux.go);
// 其余平台无等价能力 → 不施加但**显式告警**(不静默降级)。
//
// 开关:GAH_SHELL_KERNEL_SANDBOX=0 显式关闭。与环境 jail(GAH_SHELL_JAIL)相互独立,
// 但 jail 关闭时内核沙箱的临时/缓存白名单失去锚点,故一并跳过(见 kernelWrap,显式告警)。
package toolshell

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// kernelSandboxEnv 内核级沙箱显式关闭开关("0" = 关)。
const kernelSandboxEnv = "GAH_SHELL_KERNEL_SANDBOX"

// sandboxExec darwin 的 seatbelt 前端路径(darwin 的 platformWrap 使用)。
// 之所以是包级变量而不是 darwin 文件内的常量:测试要覆盖"前端缺失 → 降级不施加"分支,
// 且测试文件在所有平台都要能编译(Linux CI 也会跑测试包)。
var sandboxExec = "/usr/bin/sandbox-exec"

var warnOnce sync.Once

// warnUnavailable 一次性告警:内核级沙箱本该生效却无法生效时说明原因与开关。
// 安全边界降级必须**可见**(不静默降级);显式关闭(开关/档位不需要)时静默,不打扰用户。
func warnUnavailable(why string) {
	warnOnce.Do(func() {
		fmt.Fprintf(os.Stderr,
			"gah tool-shell: 内核级沙箱未生效(%s);当前仅工具边界协作式控制(写目标裁决 + 环境 jail)生效。%s;"+
				"如需关闭该提示的触发条件,设置 %s=0。\n",
			why, platformSupportNote(), kernelSandboxEnv)
	})
}

// kernelWrap 按有效档位返回要前置到 argv 的包装(空 = 不施加)。
// ok = 宿主是否注入了沙箱上下文(sdk.SandboxHintOf 的第二返回值)。
func kernelWrap(hint sdk.SandboxHint, ok bool) []string {
	if os.Getenv(kernelSandboxEnv) == "0" {
		return nil // 显式关闭:不施加也不告警
	}
	if !ok {
		return nil // 旧宿主/直连测试:不猜档位
	}
	mode := hint.Mode
	if mode == "" {
		return nil // 只下传了工作根(无沙箱宿主):档位未知 → 不猜测、不施加
	}
	switch mode {
	case sdk.SandboxFullAccess:
		return nil // 全权档:协作层也不拦,内核层无需施加
	case sdk.SandboxReadOnly, sdk.SandboxWorkspace:
		// 继续
	default:
		warnUnavailable("宿主注入的档位无法识别: " + string(mode))
		return nil
	}
	if os.Getenv("GAH_SHELL_JAIL") == "0" {
		// jail 关闭时子进程的 TMPDIR/缓存根回落到系统目录与家目录,内核白名单失去锚点 ——
		// 强行施加会让 go build / npm 之类大面积失败且原因难查。显式跳过并说明,不静默降级。
		warnUnavailable("环境 jail 已关闭(GAH_SHELL_JAIL=0),内核沙箱的临时/缓存白名单失去锚点")
		return nil
	}
	if mode == sdk.SandboxWorkspace && strings.TrimSpace(hint.Root) == "" {
		mode = sdk.SandboxReadOnly // 根未知 → 判不定的写更危险
	}
	return platformWrap(mode, hint.Root)
}

// kernelWrapCtx 从 ctx 读取宿主注入的沙箱上下文并给出包装(shell.go/pty.go 的唯一入口)。
func kernelWrapCtx(ctx context.Context) []string {
	hint, ok := sdk.SandboxHintOf(ctx)
	return kernelWrap(hint, ok)
}

// resolvePath 解析软链;失败时保留原路径。
// 为什么必须解析(darwin 实测踩到):macOS 上 /tmp 是 /private/tmp 的软链,seatbelt 按**真实路径**
// 匹配,不解析则 (subpath "/tmp/x") 形同不设 —— 白名单看起来在、实际全放行。
// 路径**尚不存在**时(全新数据根的 jail、未创建的 workspace 子目录)逐级向上找到最近的存在
// 祖先再拼回剩余部分:直接回退原始路径会把软链前缀(/tmp、/var)交给 seatbelt,白名单同样形同不设。
// 确实解析不出来(整条链都不存在/权限不足)时回退原路径(保守方向:少放行,而不是多放行)。
func resolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil && r != "" {
		return r
	}
	dir := filepath.Clean(p)
	var tail []string
	for {
		parent := filepath.Dir(dir)
		if parent == dir { // 已到根仍解析不出
			return p
		}
		tail = append([]string{filepath.Base(dir)}, tail...)
		dir = parent
		if r, err := filepath.EvalSymlinks(dir); err == nil && r != "" {
			return filepath.Join(append([]string{r}, tail...)...)
		}
	}
}

// prefixedArgv 拼接包装与真实命令(argv 用新底层数组,避免 append 复用原切片导致包装被覆写)。
func prefixedArgv(pre []string, name string, rest ...string) []string {
	argv := make([]string, 0, len(pre)+1+len(rest))
	argv = append(argv, pre...)
	argv = append(argv, name)
	return append(argv, rest...)
}
