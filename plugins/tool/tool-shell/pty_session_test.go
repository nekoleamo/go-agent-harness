package toolshell

// 交互执行编排(runPty)的契约测试(**平台无关**)。
//
// 为什么能在 mac 上验这一段:2026-10-02 把 pty 拆成了「编排(本文件所在)+ 起进程
// (pty_unix.go / pty_windows.go)」之后,超时/取消/采集/快照/终止这套**语义**不再依赖
// 具体 pty 实现 —— 用一对假管道就能把它跑起来。
//
// 意义:Windows 的 ConPTY 那段样板只能靠真机验,但它**之上**的语义(超时兜底、终止后
// 仍能取到已捕获输出、并发下不数据竞争)现在有 CI 护栏 —— 那些语义以前只在 unix 上
// 有覆盖(靠真 pty),现在两平台共测同一段。

import (
	"context"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// isWindows 平台判定(测试内用;生产侧用 runtime.GOOS 的 build tag 切文件)。
func isWindows() bool { return runtime.GOOS == "windows" }

// fakePTY 一条假 pty:子进程往 outW 写、父进程从 outR 读;输入方向相反。
// 字段用具体类型(*io.PipeReader/Writer)而不是接口:测试自己要 Write/Close 它们。
type fakePTY struct {
	outR         *io.PipeReader
	outW         *io.PipeWriter
	inR          *io.PipeReader
	inW          *io.PipeWriter
	mu           sync.Mutex
	stopCalls    int
	waitCalls    int
	closedCalled int
}

func newFakePTY() *fakePTY {
	outR, outW := io.Pipe()
	inR, inW := io.Pipe()
	return &fakePTY{outR: outR, outW: outW, inR: inR, inW: inW}
}

func (f *fakePTY) session() *ptySession {
	return &ptySession{
		out: f.outR,
		in:  f.inW,
		stop: func() {
			f.mu.Lock()
			f.stopCalls++
			f.mu.Unlock()
		},
		wait: func() {
			f.mu.Lock()
			f.waitCalls++
			f.mu.Unlock()
		},
		close: func() {
			f.mu.Lock()
			f.closedCalled++
			f.mu.Unlock()
			f.outW.Close()
			f.inW.Close()
		},
	}
}

func (f *fakePTY) counts() (stop, wait, closed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopCalls, f.waitCalls, f.closedCalled
}

// TestRunPtyCollectsOutputUntilEOF 正常路径:子进程写完退出 → 采集到完整输出,不误判超时。
func TestRunPtyCollectsOutputUntilEOF(t *testing.T) {
	f := newFakePTY()
	defer f.outR.Close()
	go func() {
		_, _ = io.WriteString(f.outW, "hello-from-interactive\n")
		_ = f.outW.Close() // EOF = 进程退出
	}()
	out, timedOut, err := runPty(context.Background(), f.session(), "")
	if err != nil {
		t.Fatal(err)
	}
	if timedOut {
		t.Error("正常退出不该报超时")
	}
	if !strings.Contains(out, "hello-from-interactive") {
		t.Fatalf("输出没采到:%q", out)
	}
	if stop, _, _ := f.counts(); stop != 0 {
		t.Errorf("正常退出不该调 stop(调了说明有人在超时路径上乱发信号):%d", stop)
	}
}

// TestRunPtyWritesInput 交互的另一半:input 会被写进 pty(REPL/git 编辑器就靠这个)。
func TestRunPtyWritesInput(t *testing.T) {
	f := newFakePTY()
	defer f.outR.Close()
	// **只读 input 的那几字节**就返回(不能 io.ReadAll:写端一直开着,ReadAll 会一直
	// 等到超时 —— 这个坑我先踩了一次:用例跑了 64s 才失败)。
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 3) // "ls\n"
		_, _ = io.ReadFull(f.inR, buf)
		got <- string(buf)
		f.outW.Close()
	}()
	_, timedOut, err := runPty(context.Background(), f.session(), "ls\n")
	if err != nil {
		t.Fatal(err)
	}
	if timedOut {
		t.Error("不该超时")
	}
	select {
	case s := <-got:
		if s != "ls\n" {
			t.Errorf("input 应原样写入 pty,got %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("没收到写入 pty 的 input")
	}
}

// TestRunPtyTimeoutReturnsCapturedOutput 超时兜底:无 EOF 的交互进程(REPL 等着输入)必须
// 被终止,**且仍返回已捕获的输出** —— 这是本段最关键的一条:终止后返回空输出等于让用户
// 丢掉已经看到的内容。
func TestRunPtyTimeoutReturnsCapturedOutput(t *testing.T) {
	f := newFakePTY()
	defer f.outR.Close()
	// 写一点输出后**不关闭**(模拟 REPL 挂着等输入)
	go func() { _, _ = io.WriteString(f.outW, "prompt$ ") }()
	// 短超时:用 ctx 的 deadline(runPty 会取更小者)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	out, timedOut, err := runPty(ctx, f.session(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !timedOut {
		t.Error("无 EOF 的会话应报超时")
	}
	if !strings.Contains(out, "prompt$") {
		t.Fatalf("超时后必须仍返回已捕获输出,got %q", out)
	}
	if stop, _, _ := f.counts(); stop == 0 {
		t.Error("超时必须调 stop(终止整组)")
	}
}

// TestRunPtyContextCancel 取消(用户按 Esc / 回合被中断)与超时同处置。
func TestRunPtyContextCancel(t *testing.T) {
	f := newFakePTY()
	defer f.outR.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, timedOut, err := runPty(ctx, f.session(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !timedOut {
		t.Error("取消后应标记 timedOut(上层据此告诉模型「进程被终止」)")
	}
	if stop, _, _ := f.counts(); stop == 0 {
		t.Error("取消必须调 stop")
	}
}

// TestShellArgvPerPlatform 交互命令选哪个 shell:Windows 走 cmd.exe,其它走 POSIX shell
// (缺 Git for Windows 时返回空串 —— 由 startPTY 显式报错,不静默换个「大概在」的位置)。
func TestShellArgvPerPlatform(t *testing.T) {
	argv := shellArgv("echo hi")
	if len(argv) == 0 {
		t.Fatal("argv 为空")
	}
	if isWindows() {
		if argv[0] != "cmd.exe" || argv[1] != "/c" {
			t.Errorf("Windows 侧应是 cmd.exe /c,got %v", argv)
		}
	} else {
		sh := shellForPty()
		if sh == "" {
			t.Skip("本机没有 POSIX shell(缺 Git for Windows?);Windows 侧由真机清单验")
		}
		if argv[0] != sh || argv[1] != "-c" {
			t.Errorf("posix 侧应是 <shell> -c,got %v", argv)
		}
	}
	if argv[len(argv)-1] != "echo hi" {
		t.Errorf("命令应在最后一段,got %v", argv)
	}
}
