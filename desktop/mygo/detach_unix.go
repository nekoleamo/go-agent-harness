//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detach 让子进程脱离本进程的会话/进程组:壳退出(或被一起收掉)时它继续活着。
// 「新实例」的语义就是另一个独立进程,不能因为父进程退出而被带走。
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
