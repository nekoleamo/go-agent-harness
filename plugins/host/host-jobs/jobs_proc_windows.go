//go:build windows

package hostjobs

import (
	"os/exec"
	"strconv"
)

// setupCmdGroup windows:无进程组语义,no-op。
func setupCmdGroup(cmd *exec.Cmd) {}

// killCmdGroup windows:taskkill /T 终止整棵进程树。
//
// 只用 Process.Kill() 会留下孤儿:命令任务经 sh -c 启动(`sleep 30` 之类),
// 杀掉的只是 sh 本体,真正的子进程仍在跑 —— Jobs.Kill 会一直等到 8 秒超时后
// 报「终止超时」。POSIX 侧靠进程组负 PID 组杀解决同一问题(jobs_proc_unix.go)。
func killCmdGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	if err := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run(); err == nil {
		return nil
	}
	return cmd.Process.Kill() // taskkill 不可用(极端自制环境)时退回单进程终止
}
