//go:build !darwin && !(linux && (amd64 || arm64 || loong64 || mips64 || mips64le || ppc64 || ppc64le || riscv64 || s390x || sparc64))

// kernel_other.go:无内核级文件写限制能力的平台(Windows 等),以及 Linux 上无 Landlock
// 系统调用号的架构(386/arm/mips/ppc 32 位)。
//
// 这里**不施加**任何内核约束,但**显式告警**(不静默降级):协作式控制(写目标裁决 + 环境 jail)
// 仍在生效,只是拦不住解释器/构建系统内部的间接写。
package toolshell

import "github.com/nekoleamo/go-agent-harness/sdk"

func platformSupportNote() string {
	return "当前平台无等价的文件写限制能力(内核级沙箱仅支持 macOS seatbelt 与 Linux Landlock)"
}

func platformWrap(sdk.SandboxMode, string) []string {
	warnUnavailable("平台不支持(见提示)")
	return nil
}
