//go:build !windows

// 非 Windows 上的占位(powershell_other.go):只回一句结构化说明。
// 真实执行在 powershell_windows.go —— 见那里的理由(平台专属代码必须放平台专属文件,
// 否则 darwin/linux 会编进一段永远 0% 的 Windows 代码)。

package toolshell

import (
	"context"
	"fmt"
	"runtime"
	"time"
)

// runPowerShell 非 Windows 上的分派点:回同一句结构化说明(不执行任何东西)。
func runPowerShell(ctx context.Context, command string, timeout time.Duration, workdir string) any {
	return notWindows()
}

// notWindows 非 Windows 上的回执(结构化,不中断回合)。
func notWindows() map[string]any {
	return map[string]any{"error": "powershell: 该工具只在 Windows 上注册(本机是 " + runtime.GOOS + ")"}
}

// ResolvePowerShell 非 Windows 上恒为「找不到」—— 它**只**用于 Windows 的执行路径,
// 所以这里不该返回成功(返回成功会让一段 Windows 逻辑在 macOS 上被当成可用)。
func ResolvePowerShell() (string, error) {
	return "", fmt.Errorf("PowerShell 工具只在 Windows 上可用(本机是 %s)", runtime.GOOS)
}
