//go:build !windows

// 非 Windows:没有「控制台窗口」这回事,无需处理。
package main

import "os/exec"

func hideConsole(*exec.Cmd) {}
