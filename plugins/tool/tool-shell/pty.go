// pty.go:tool-shell 交互式命令执行(M6.3)。data.pty 开关启用后,shell 工具
// 支持 {"command", "input"}:命令挂 pseudo-terminal 运行,input 一次性写入,
// 采集终端输出直至进程退出(交互场景:REPL/git 编辑器/询问式脚本)。
// pty 会话无 EOF 时由超时兜底:终止进程并返回已捕获输出(标记 timeout)。
// 纯 syscall 实现,兼容 CGO_ENABLED=0。
package toolshell

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// ptyTimeout 交互命令兜底超时(进程不退出则终止,与 shell 普通路径一致)。
const ptyTimeout = 60 * time.Second

// lockedWriter 让超时路径读缓冲区时与 io.Copy 写入互斥(消数据竞争)。
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// execPty 在 pseudo-terminal 中执行命令并采集输出。
// 返回 (输出, 是否超时)。
func execPty(ctx context.Context, command, input string) (string, bool, error) {
	// 内核级沙箱(第 3 组 ①-E):与普通路径同一套包装(见 kernel.go);pty 不改变 argv 语义。
	pre := kernelWrapCtx(ctx)
	argv := prefixedArgv(pre, "sh", "-c", command)
	cmd := exec.Command(argv[0], argv[1:]...)
	// 凭据隔离在前、环境 jail 覆盖缓存根/临时根在后(见 jail.go);jail 建不起来→显式失败。
	env, jerr := jailEnv(sdk.SanitizedEnv(os.Environ()))
	if jerr != nil {
		return "", false, fmt.Errorf("环境 jail 初始化失败: %w", jerr)
	}
	cmd.Env = env
	// 不断设 Setpgid:pty.Start 内部会设 Setsid(子进程自成会话/进程组组长→ pgid==pid),
	// 两个同设会在部分平台直接 EPERM;下面仍可用 killProcessGroup 整组终止。

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return "", false, err
	}
	// defer 逆序执行:先关 ptmx(唤醒接收 goroutine),再回收子进程(防僵尸)并杀组内残留。
	defer func() {
		killProcessGroup(cmd)
		_ = cmd.Wait()
	}()
	defer ptmx.Close()

	if input != "" {
		go func() { _, _ = io.WriteString(ptmx, input) }()
	}

	var mu sync.Mutex
	var buf bytes.Buffer
	snapshot := func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(&lockedWriter{mu: &mu, w: &buf}, ptmx)
		done <- err
	}()

	// 上下文取消/超时 → 终止整组(退出无 EOF 的交互进程兜底)
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
		killProcessGroup(cmd)
		timedOut = true
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	case <-time.After(timeout):
		killProcessGroup(cmd)
		timedOut = true
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	return snapshot(), timedOut, nil
}
