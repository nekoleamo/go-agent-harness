//go:build unix

package toolshell

import (
	"os/exec"
	"syscall"
)

// setProcessGroup 让命令自成进程组:超时/取消时可整组终止,
// 避免 `sleep 300 &` 这类孙进程逃逸为孤儿并握住 stdout 管道。
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
