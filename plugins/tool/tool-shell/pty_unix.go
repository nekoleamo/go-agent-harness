//go:build !windows

// pty_unix.go:posix 的 pty 起进程(posix 平台走 creack/pty)。
//
// 编排(超时/采集/快照)在 pty.go;这里只负责「起一个带 pty 的进程」。
//
// 注意**不断设 Setpgid**:pty.Start 内部会设 Setsid(子进程自成会话/进程组组长 ⇒ pgid==pid),
// 两个同设会在部分平台直接 EPERM;超时路径仍可用 killProcessGroup 整组终止。
package toolshell

import (
	"os/exec"

	"github.com/creack/pty"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// shellForPty 交互命令用的 shell(Windows 侧走 cmd.exe,见 pty.go 的 shellArgv)。
func shellForPty() string {
	sh, err := sdk.ResolvePOSIXShell()
	if err != nil {
		// 解析失败不静默换成一个「大概在」的位置:startPTY 里再报错一次带上下文。
		return ""
	}
	return sh
}

// startPTY 起一个带 pty 的进程。
func startPTY(argv []string, dir string, env []string) (*ptySession, error) {
	if len(argv) > 0 && argv[0] == "" {
		// shellForPty 解析失败(缺 Git for Windows 等)
		sh, serr := sdk.ResolvePOSIXShell()
		if serr != nil {
			return nil, serr
		}
		argv = append([]string{sh}, argv[1:]...)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir                   // 本次调用工作根(S-P1-4 隔离运行);pty 与普通路径同语义
	cmd.Env = sdk.ShellExecEnv(env) // MSYS 路径转换开关(Windows;见 sdk/shellpath.go)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	// pty 只给一个双向 master:读与写都用它(写侧关闭 = 给子进程 EOF)。
	return &ptySession{
		out:   ptmx,
		in:    ptmx,
		wait:  func() { _ = cmd.Wait() },
		stop:  func() { killProcessGroup(cmd) },
		close: func() { killProcessGroup(cmd); _ = ptmx.Close(); _ = cmd.Wait() },
	}, nil
}
