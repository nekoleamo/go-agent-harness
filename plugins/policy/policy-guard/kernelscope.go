// kernelscope.go:协作层与内核层的**共享写入面**(2026-10-03)。
//
// 动因:「哪里能写」此前被推导两遍且两遍不等价 ——
//
//	内核层(darwinProfile / landlock 规则):工作区根(仅 workspace 档)+ jail + RW(包缓存/临时区);
//	协作层(ValidatePathAt,workspace 档):**只有工作区根**。
//
// 于是「内核放行、协作拒绝」成为常态:$GAH_HOME/jail(shell 侧的 TMPDIR)与包缓存/
// 系统临时区(外部插件侧的 RW)都被内核放行,却被协作层按「工作区之外」拒掉。用户看到的
// 就是「做出操作后再判断、被回绝后再重新操作」—— 模型在同一堵墙上反复换路径,而两次
// 裁决说的根本不是同一件事。
//
// 收敛口径(不是放宽安全边界):
//   - 安全边界**仍然只由内核层守** —— profile 一行都没变,没有多放行任何落点;
//   - 协作层改为**按同一份清单**裁决,不再比内核更严;
//   - **内核层不在场时(返回 nil)** 协作层退回「只在工作区根内」的窄口径 ——
//     那时它是唯一的边界(Windows / 无 seatbelt / 开关关),放宽等于失守。
//
// 即「把边界前置到内核层」的落法:由内核定义边界,协作层与之对齐,而不是两层各写一套。
package policyguard

import (
	"os"

	"github.com/nekoleamo/go-agent-harness/internal/kernelsandbox"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 开关名与各执行面的内核 spec 一一对应(同一个字面量,读错就是「以为对齐了,其实没有」——
// 那比不同源更糟)。
const (
	// 工具面(默认形态下由外部插件 tool-basic 提供 file_*/memory/todo):host-bridge 施加。
	kernelToolSwitch   = "GAH_EXT_PLUGIN_SANDBOX"
	kernelToolRWPaths  = "GAH_EXT_PLUGIN_RW_PATHS"
	kernelShellSwitch  = "GAH_SHELL_KERNEL_SANDBOX"
	kernelShellJailEnv = "GAH_SHELL_JAIL"
)

// kernelWriteScope 指定执行面当前内核层**真正会施加**的写入面目录;不在场时返回 nil。
//
// 为什么按面分:tool-shell 把子进程 TMPDIR 重定向进 jail(spec.RW 为空),外部插件则额外
// 放行包管理器缓存与系统临时区。两份 spec 一样宽或一样窄都会说谎。
//
// 取「偏严一侧」的失败方向:判错成不在场 → 协作层退回今天的口径(只是多弹一次 blocked);
// 判错成在场 → 协作层放宽到与内核相同的落点(内核仍是最后一道,实际风险不变)。
func kernelWriteScope(sp *SandboxPolicy, surface kernelSurface) []string {
	if os.Getenv(kernelShellJailEnv) == "0" {
		// shell 侧临时区锚点被关:其白名单不成立;外部插件面的 jail 也来自同一目录,
		// 一起不放宽更保守(而不是只退一半)。
		return nil
	}
	mode := ""
	root := ""
	if sp != nil {
		mode = string(sp.EffectiveMode()) // 无会话上下文(内核写入面是全局的) —— 见下方注释
		root = sp.Root()
	}
	spec := kernelsandbox.Spec{
		Mode:  sdk.SandboxMode(mode),
		Root:  root,
		Jail:  kernelsandbox.EnsureJailDir(),
		Label: "policy-guard 共享写入面",
	}
	switch surface {
	case surfaceShell:
		// 与 tool-shell 的 kernelWrap 同款:TMPDIR 已重定向进 jail,故不放系统临时区。
		spec.Switch, spec.RequireJailSwitch = kernelShellSwitch, kernelShellJailEnv
	default:
		// 与 host-bridge 的 pluginSandboxSpec 同款(减去插件各自自报的数据目录 ——
		// 那是**每个插件一份**的自报项,协作层拿不到也不该猜)。
		spec.RW = kernelsandbox.DefaultRWPaths()
		spec.RW = append(spec.RW, kernelsandbox.RWPathsFromEnv(kernelToolRWPaths)...)
		spec.Switch, spec.RequireJailSwitch = kernelToolSwitch, kernelShellJailEnv
	}
	// 与 Wrap/WouldApply 走**同一份**修正(spec.Normalized):workspace 根缺失时内核按只读
	// 生效,协作层也必须按只读那份清单对齐 —— 否则会出现「内核按只读拒、协作层按工作区放」
	// 的错位,比不同源更难查。
	spec, _ = kernelsandbox.Normalized(spec)
	if ok, _ := kernelsandbox.WouldApply(spec); !ok {
		return nil
	}
	return kernelsandbox.WritablePaths(spec)
}
