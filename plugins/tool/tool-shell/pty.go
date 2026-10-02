// pty.go:tool-shell 交互式命令执行的**平台无关**部分(M6.3)。
//
// data.pty 开关启用后,shell 工具支持 {"command", "input"}:命令挂 pseudo-terminal 运行,
// input 一次性写入,采集终端输出直至进程退出(交互场景:REPL/git 编辑器/询问式脚本)。
// pty 会话无 EOF 时由超时兜底:终止进程并返回已捕获输出(标记 timeout)。
//
// **按平台切分(2026-10-02)**:能在这个文件里验的东西都留在这里 —— 超时与取消的处置、
// 输出采集与缓冲的并发安全、输入写入的时机、终止后取快照。「怎么起一个带 pty 的进程」
// 是平台专属,放在 pty_unix.go(posix:creack/pty)与 pty_windows.go(win:ConPTY)。
// 切分的收益:交互执行的语义(超时/终止/快照)在 macOS 上也有用例,
// 而 ConPTY 那段样板只剩「起进程 + 读两个管道」需要真机验。
package toolshell

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

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

// ptyTimeout 交互命令兜底超时(进程不退出则终止,与 shell 普通路径一致)。
// 放在平台无关层:这是**策略**(交互命令等多久算太久),不是平台能力。
const ptyTimeout = 60 * time.Second

// ptySession 一次交互执行。**平台无关的编排**都在 runPty 里,平台文件只负责「起进程」。
type ptySession struct {
	out   io.ReadCloser  // 读侧(pty master / ConPTY 输出管道)
	in    io.WriteCloser // 写侧(pty master / ConPTY 输入管道)
	wait  func()         // 回收子进程
	stop  func()         // 终止整组(超时/取消共用)
	close func()         // 关管道 + 终止 + 回收
}

// execPty 在 pseudo-terminal 中执行命令并采集输出。
// dir = 本次调用工作根(空 = 继承宿主 cwd;见 workdirOf/shell.go)。
// 返回 (输出, 是否超时)。
func execPty(ctx context.Context, dir, command, input string) (string, bool, error) {
	// 环境 jail **先**建出,再算内核白名单(pty 与普通路径同一顺序理由,见 shell.go/jail.go):
	// 否则全新数据根的首条命令会因 jailRoot() 未存在而拿到未解析的 seatbelt 白名单。
	// 凭据隔离在前、环境 jail 覆盖缓存根/临时根在后(见 jail.go);jail 建不起来→显式失败。
	env, jerr := jailEnv(sdk.SanitizedEnv(os.Environ()))
	if jerr != nil {
		return "", false, fmt.Errorf("环境 jail 初始化失败: %w", jerr)
	}
	// 内核级沙箱(第 3 组 ①-E):与普通路径同一套包装(见 kernel.go);pty 不改变 argv 语义。
	pre := kernelWrapCtx(ctx)
	argv := prefixedArgv(pre, shellArgv(command)[0], shellArgv(command)[1:]...)
	s, err := startPTY(argv, dir, env)
	if err != nil {
		return "", false, err
	}
	// 关闭顺序:先关管道(唤醒采集 goroutine),再回收子进程(防僵尸)并杀组内残留。
	defer s.close()

	return runPty(ctx, s, input)
}

// shellArgv 交互命令的 argv(平台无关的那一半:选 shell + 传 -c)。
//
// Windows 侧**不用** POSIX shell:交互场景在 Windows 上就是 cmd/PowerShell 的主场
// (用 Git Bash 跑交互 REPL 是绕远路)。所以这半也不放在 unix 文件里 ——
// 但「选哪个 shell」是**策略**,两处各写一遍早晚会不一致。
func shellArgv(command string) []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe", "/c", command}
	}
	return []string{shellForPty(), "-c", command}
}

// runPty 会话驱动:写输入 → 采集输出 → 等退出 / 超时 / 取消 → 取快照。
// **这段是平台无关的**,两套实现共用(见文件头注)。
func runPty(ctx context.Context, s *ptySession, input string) (string, bool, error) {
	if input != "" {
		go func() { _, _ = io.WriteString(s.in, input) }()
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
		_, err := io.Copy(&lockedWriter{mu: &mu, w: &buf}, s.out)
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
		s.stop()
		timedOut = true
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	case <-time.After(timeout):
		s.stop()
		timedOut = true
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	return snapshot(), timedOut, nil
}
