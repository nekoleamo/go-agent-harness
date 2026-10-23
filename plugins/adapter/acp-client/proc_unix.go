//go:build unix

package acpclient

import (
	"os/exec"
	"syscall"
)

// setProcessGroup 让外部 agent 自成进程组:超时/取消时整组终止。
// 编码 agent 典型会自己 fork(git、测试进程、语言服务器)—— 只杀直接子进程会留一堆孤儿。
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup 终止整组(进程已退出时静默忽略 ESRCH)。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
