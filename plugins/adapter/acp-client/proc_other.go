//go:build !unix

package acpclient

import "os/exec"

// setProcessGroup 非 unix 平台没有进程组语义(Windows 需 Job Object,此处不做)。
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup 非 unix 平台退化为终止直接子进程。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
