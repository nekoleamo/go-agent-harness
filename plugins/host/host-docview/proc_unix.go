//go:build unix

package hostdocview

import (
	"os/exec"
	"syscall"
)

// setProcessGroup 让外部转换器自成进程组:超时/取消时可整组终止,避免转换器
// 派生(shell 包装)的孙进程逃逸为孤儿并握住 stdout 管道。
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
