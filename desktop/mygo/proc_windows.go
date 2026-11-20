//go:build windows

// Windows 上的子进程启动细节。
//
// 为什么不设就会出问题:壳是 GUI 进程(windows_subsystem),而 `gah.exe` 是**控制台程序**;
// 从 GUI 进程启动控制台程序时,Windows 会给它开一个新控制台窗口 —— 用户会看到一个黑框
// 一闪(或一直挂着,取决于它往 stdout 写多少)。CREATE_NO_WINDOW(0x08000000) 让子进程
// 不带控制台启动;HideWindow 兜住某些杀软/兼容层下的变体。
package main

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
