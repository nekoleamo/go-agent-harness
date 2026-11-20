//go:build windows

// 等旧进程退出(Windows:OpenProcess(SYNCHRONIZE) + WaitForSingleObject)。
package main

import (
	"time"

	"golang.org/x/sys/windows"
)

// waitProcessExit 等 pid 退出;最多等 d,超时返回 false。
func waitProcessExit(pid int, d time.Duration) bool {
	if pid <= 0 {
		return true
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true // 打不开 = 已经不在了(或不属于我们,后面 rename 会给出真实错误)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	// 带超时等待:安装器不能无限期挂在一个不响应的壳上。
	ev, werr := windows.WaitForSingleObject(h, uint32(d/time.Millisecond))
	return werr == nil && ev == windows.WAIT_OBJECT_0
}
