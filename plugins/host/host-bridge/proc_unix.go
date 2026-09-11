//go:build unix

package hostbridge

import (
	"os/exec"
	"syscall"
)

// pluginProcAttr 外部插件进程独立进程组(Setsid 语义的轻量替代):
// 只有成为组首才能对其"整组"发信号,从而连带回收插件派生的子进程
// (典型:MCP server —— 此前宿主退出后它们变孤儿并继续占用端口/资源)。
func pluginProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// killPluginGroup 组杀插件进程及其全部后代(进程已退出时静默)。
func killPluginGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
