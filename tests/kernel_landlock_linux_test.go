//go:build linux

// Linux 侧 Landlock 可用性探测(供内核沙箱端到端测试决定 skip 或真跑)。
// 与 plugins/tool/tool-shell/kernel_linux.go 同源语义:查询 ABI 版本,返回 >=1 才可用。
package tests

import "golang.org/x/sys/unix"

// landlockCreateRulesetVersion = LANDLOCK_CREATE_RULESET_VERSION(内核 include/uapi/linux/landlock.h)。
const landlockCreateRulesetVersion = 1

func landlockABIVersion() int {
	r, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, landlockCreateRulesetVersion)
	if errno != 0 || int(r) < 1 {
		return -1
	}
	return int(r)
}
