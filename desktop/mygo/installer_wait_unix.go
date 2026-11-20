//go:build !windows

// 等旧进程退出(unix:kill(pid, 0) 探活)。
package main

import (
	"syscall"
	"time"
)

// waitProcessExit 等 pid 退出;最多等 d,超时返回 false。
func waitProcessExit(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return !processAlive(pid)
}

// processAlive 进程是否还活着(信号 0 只做权限/存在性检查,不投递)。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
