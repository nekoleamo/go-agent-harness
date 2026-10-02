//go:build windows

// Windows 侧的真实执行(powershell_windows.go)。
//
// 单独成文件的原因:这段逻辑在 darwin/linux 上**永远跑不到**,放在平台无关文件里会被
// 编进那两个平台的二进制并计入覆盖率,却永远是 0%(tool-shell 因此掉 15pp)。切开之后,
// 这段代码的覆盖率由 **Windows 平台**的 CI 负责,而 Windows runner 上
// `powershell.exe` 确实存在 —— `TestPowerShellExecOnWindows` 会在那里真跑一条命令。

package toolshell

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// execPowerShell 跑一条命令(结构化错误回传模型,不中断 turn)。
func execPowerShell(ctx context.Context, sh, command string, timeout time.Duration, workdir string) any {
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 凭据隔离 → 环境 jail(顺序与 shell 相同:jail 建在沙箱包装之前,见 shell.go 注释)
	env, jerr := jailEnv(sdk.SanitizedEnv(os.Environ()))
	if jerr != nil {
		return map[string]any{"error": "powershell: 环境 jail 初始化失败: " + jerr.Error()}
	}

	// 内核沙箱包装:Windows 上无等价原语 ⇒ 包装为空(内核层缺失这件事已在别处如实告警,
	// 这里不假装有防护)。参数顺序与 shell 相同。
	pre := kernelWrapCtx(ctx)
	argv := prefixedArgv(pre, sh, "-NoProfile", "-NonInteractive", "-Command", command)
	cmd := exec.CommandContext(dctx, argv[0], argv[1:]...)
	cmd.Dir = workdir
	cmd.Env = sdk.ShellExecEnv(env)
	// -NoProfile:不加载用户 profile(否则 profile 里的别名/函数会改变命令语义,
	// 而且那也是一处「用户环境决定行为」的不可控面);
	// -NonInteractive:不弹交互提示(无人值守的回合里弹窗 = 永远等下去)。
	setProcessGroup(cmd)
	cmd.WaitDelay = 3 * time.Second
	defer killProcessGroup(cmd)
	out, rerr := cmd.CombinedOutput()
	if rerr != nil {
		return map[string]any{"exit_error": rerr.Error(), "output": string(out)}
	}
	return map[string]any{"output": string(out)}
}

// runPowerShell 解析可执行文件并执行(Windows 侧)。
//
// 「解析 + 执行」整体放在平台文件里,而不是留在平台无关的那份:解析的**成功**分支只在
// 真的装了 PowerShell 的机器上可达,留在外面就是一段非 Windows 平台永远 0% 的代码。
func runPowerShell(ctx context.Context, command string, timeout time.Duration, workdir string) any {
	sh, err := ResolvePowerShell()
	if err != nil {
		return map[string]any{"error": "powershell: " + err.Error()}
	}
	return execPowerShell(ctx, sh, command, timeout, workdir)
}

// ResolvePowerShell 找 PowerShell 可执行文件(Windows PowerShell 优先,其次 PowerShell 7)。
//
// 找两个是因为它们都可能只有其一(Win10/11 自带 5.1 powershell.exe;装了
// PowerShell 7 的是 pwsh.exe)。**都不在时显式报错**,并把两个名字都写进错误里 ——
// 用户看到「没有 PowerShell」才知道要装什么。
func ResolvePowerShell() (string, error) {
	for _, name := range []string{"powershell.exe", "pwsh.exe", "powershell", "pwsh"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("本机找不到 PowerShell(试过 powershell.exe / pwsh.exe);" +
		"装 PowerShell 7(pwsh.exe,winget install Microsoft.PowerShell)或用 shell 工具走 Git Bash")
}
