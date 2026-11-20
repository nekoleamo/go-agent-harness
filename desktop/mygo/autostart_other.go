//go:build !darwin && !windows

// autostart:Linux 等其他平台(2026-11-20 起 Linux 已退出发行矩阵,这里只为能编译出
// 明确的"不支持"语义,不静默成功)。
package main

import "fmt"

// autostartEnabled 其他平台一律报未启用。
func (s *shell) autostartEnabled() bool { return false }

// setAutostart 其他平台明确失败(不静默降级)。
func (s *shell) setAutostart(bool) error {
	return fmt.Errorf("当前平台不支持开机自启(gah 只发 macOS / Windows)")
}
