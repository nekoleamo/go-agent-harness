//go:build !windows

package hostjobs

import (
	"os/exec"
	"syscall"
)

// setupCmdGroup 命令入独立进程组(unix;组杀可连带 sh -c 的子进程)。
func setupCmdGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killCmdGroup 杀整个进程组(含子进程;sh 未 exec 替换时 sleep 等子进程不致孤儿)。
func killCmdGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
