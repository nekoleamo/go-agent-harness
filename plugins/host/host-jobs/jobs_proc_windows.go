//go:build windows

package hostjobs

import "os/exec"

// setupCmdGroup windows:无进程组语义,no-op。
func setupCmdGroup(cmd *exec.Cmd) {}

// killCmdGroup windows:仅杀直接进程(无组信号)。
func killCmdGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
