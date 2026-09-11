//go:build !unix

package hostbridge

import (
	"os/exec"
	"syscall"
)

// pluginProcAttr 非 unix 平台无进程组语义(no-op)。
func pluginProcAttr() *syscall.SysProcAttr { return nil }

// killPluginGroup 非 unix 平台只杀进程本身(无组杀能力)。
func killPluginGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
