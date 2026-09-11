//go:build !unix

package toolshell

import "os/exec"

// setProcessGroup 非 unix 平台(Windows):无进程组概念,退化为单进程终止
// (子进程树回收需 Job Object,此处不做;execute 侧仍有 WaitDelay 兜底)。
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup 非 unix 平台:终止直接子进程。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
