//go:build windows

// autostart:开机自启(Windows)。
//
// mygo 0.10.0 没有导出开机自启 API(见 autostart_darwin.go 的说明),这里自己写
// HKCU\Software\Microsoft\Windows\CurrentVersion\Run —— 不需要管理员权限,
// 也不需要计划任务(计划任务的隐蔽启动另有 win-schedule-tasks.ps1,那是 gah 自身的能力)。
package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

// autostartRunKey Run 键位置(当前用户)。
const (
	autostartRunKey  = `Software\Microsoft\Windows\CurrentVersion\Run`
	autostartValName = "gah"
)

// autostartEnabled 读真实自启状态:值存在且指向当前 exe。
func (s *shell) autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer func() { _ = k.Close() }()
	got, _, err := k.GetStringValue(autostartValName)
	if err != nil {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return got == quoteExe(exe)
}

// setAutostart 开/关开机自启。
func (s *shell) setAutostart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开 Run 注册表键失败: %w", err)
	}
	defer func() { _ = k.Close() }()
	if !on {
		if err := k.DeleteValue(autostartValName); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("删除自启项失败: %w", err)
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("取程序路径失败: %w", err)
	}
	if err := k.SetStringValue(autostartValName, quoteExe(exe)); err != nil {
		return fmt.Errorf("写自启项失败: %w", err)
	}
	return nil
}

// quoteExe 路径带空格时必须加引号,否则系统会把第一段当程序名。
func quoteExe(exe string) string { return `"` + exe + `"` }
