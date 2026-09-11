//go:build !unix

package hostdocview

import "os/exec"

// setProcessGroup 非 unix 平台(Windows):无进程组概念,退化为单进程终止
// (子进程树回收需 Job Object,此处不做;runExternal 侧仍有 WaitDelay 兜底)。
func setProcessGroup(_ *exec.Cmd) {}

// killProcessGroup 非 unix:仅终止直接子进程(已退出时静默)。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
