// pty.go:tool-shell 交互式命令执行(M6.3)。data.pty 开关启用后,shell 工具
// 支持 {"command", "input"}:命令挂 pseudo-terminal 运行,input 一次性写入,
// 采集终端输出直至进程退出(交互场景:REPL/git 编辑器/询问式脚本)。
// pty 会话无 EOF 时由超时兜底:终止进程并返回已捕获输出(标记 timeout)。
// 纯 syscall 实现,兼容 CGO_ENABLED=0。
package toolshell

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/creack/pty"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// ptyTimeout 交互命令兜底超时(进程不退出则终止,与 shell 普通路径一致)。
const ptyTimeout = 60 * time.Second

// execPty 在 pseudo-terminal 中执行命令并采集输出。
// 返回 (输出, 是否超时)。
func execPty(ctx context.Context, command, input string) (string, bool, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = sdk.SanitizedEnv(os.Environ()) // 凭据隔离:滤除 *_API_KEY/*_TOKEN 等

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return "", false, err
	}
	defer ptmx.Close()

	if input != "" {
		go func() { io.WriteString(ptmx, input) }()
	}

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() { _, err := io.Copy(&buf, ptmx); done <- err }()

	// 上下文取消/超时 → 终止进程(退出无 EOF 的交互进程兜底)
	timeout := ptyTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if left := time.Until(deadline); left < timeout {
			timeout = left
		}
	}
	timedOut := false
	select {
	case <-done:
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		timedOut = true
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		timedOut = true
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	return buf.String(), timedOut, nil
}