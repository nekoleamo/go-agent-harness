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

// shellForPty 已收口到 pty.go(两平台统一走 sdk.ResolvePOSIXShell,见那里的决策注释)。
// startPTY 仍接受 argv[0] 为空并在这里显式报出「缺 POSIX shell」的原因(不静默换 shell)。

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
