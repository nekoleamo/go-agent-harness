//go:build windows

// pty_windows.go:Windows 侧的 pty = **ConPTY**(伪控制台)。
//
// 为什么 Windows 不用 Git Bash 跑交互(见 pty.go 的 shellArgv):交互场景在 Windows 上
// 就是 cmd/PowerShell 的主场,用 bash 去跑交互 REPL 是绕远路。
//
// Win32 调用序列,每步都标了为什么不能省:
//  1. `CreatePipe` 造匿名管道 —— ConPTY 要求输入输出是**可异步读的句柄**;不能用
//     `GetStdHandle`(服务/无窗口环境下常常是无效句柄)。
//  2. `CreatePseudoConsole(size, hIn, hOut, …)` 建伪控制台。
//  3. `NewProcThreadAttributeList` + `Update(PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE)` 把
//     伪控制台句柄挂进**启动属性**;少了这步子进程认为自己在管道里而不是控制台
//     (没有 TERM、进度条与交互提示的行为全变)。
//  4. `CreateProcessW` 带 `EXTENDED_STARTUPINFO_PRESENT`,走 StartupInfoEx。
//     **不能走 os/exec**:Go 的 `syscall.SysProcAttr` 没有「伪控制台」这个字段;
//     `os.StartProcess` 也不接受外部准备好的属性列表 —— 只能自己调 CreateProcess。
//  5. 采集从输出管道的读端读;ConPTY 做行缓冲并可能吐 VT 序列(与 unix pty 同款,
//     这里不做二次解释,交给上层)。
//  6. 终止顺序:先杀进程再 `ClosePseudoConsole` —— 反了会留孤儿终端缓冲。
//
// 为什么用 x/sys/windows 而不是自己 `syscall.NewLazyDLL`:它已经把整套 ConPTY
// (CreatePseudoConsole / ResizePseudoConsole / StartupInfoEx / 属性列表)封好了,
// 而 `golang.org/x/sys` **本来就是本项目的直接依赖**(internal/xlock 在用)—— 用好
// 已有依赖,不是加新依赖。
//
// **验证边界(必须写清)**:这份代码在本机(macOS)**只能编译,不能运行**。可自动验的部分
// 都在 pty.go(超时/取消/采集/快照/终止,两平台共用同一段编排);这里的风险是 Win32 调用
// 的参数与顺序,已登记进 Windows 真机清单。`data.pty` 开关缺省关 ⇒ 未启用的构建不会走到这段。
package toolshell

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// procThreadAttributePseudoConsole 属性号(winbase.h PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE)。
const procThreadAttributePseudoConsole = 0x00020016

// extendedStartupInfoPresent = EXTENDED_STARTUPINFO_PRESENT(要带属性列表就必须有它)。
const extendedStartupInfoPresent = 0x00080000

// conPTYWidth/conPTYHeight 伪控制台窗口大小(字符):列/行。
// 行数给到 1000 —— 交互输出多时不会被控制台缓冲悄悄截掉(截掉是**静默丢输出**)。
const (
	conPTYWidth  = int16(200)
	conPTYHeight = int16(1000)
)

// conPTYProc 直接持有的进程句柄。
//
// 为什么不用 *os.Process:Go 没有导出「从裸句柄构造 *os.Process」的入口
// (os.NewProcess 未导出),而 ConPTY 必须自己调 CreateProcess 才能带属性列表。
// 于是这里自己管:终止用 TerminateProcess,回收用 WaitForSingleObject,
// 退出码用 GetExitCodeProcess。
type conPTYProc struct {
	h     windows.Handle
	once  sync.Once
	mu    sync.Mutex
	done  bool
	code  uint32
	kiled bool
}

func (p *conPTYProc) kill() {
	if p == nil || p.h == 0 {
		return
	}
	p.mu.Lock()
	killed := p.kiled
	p.kiled = true
	p.mu.Unlock()
	if killed {
		return
	}
	_ = windows.TerminateProcess(p.h, 1)
}

// wait 等待退出并取退出码(可重复调用;第二次直接返回上次结果)。
func (p *conPTYProc) wait() uint32 {
	if p == nil || p.h == 0 {
		return 0
	}
	p.once.Do(func() {
		_, _ = windows.WaitForSingleObject(p.h, windows.INFINITE)
		var code uint32
		if err := windows.GetExitCodeProcess(p.h, &code); err == nil {
			p.code = code
		}
		p.done = true
	})
	return p.code
}

func (p *conPTYProc) close() {
	if p == nil || p.h == 0 {
		return
	}
	p.kill()
	p.wait()
	_ = windows.CloseHandle(p.h)
	p.h = 0
}

// startPTY 用 ConPTY 起一个带伪控制台的进程。
func startPTY(argv []string, dir string, env []string) (*ptySession, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("pty: argv 为空")
	}
	// ① 管道:子进程**读** inR、**写** outW;父进程写 inW、读 outR。
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("pty: 建输入管道失败:%w", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, fmt.Errorf("pty: 建输出管道失败:%w", err)
	}
	// ConPTY 持有它需要的写端/读端副本;父进程这两端随即关闭(否则句柄泄漏)。
	defer inW.Close()
	defer outW.Close()

	// ② 伪控制台
	var hPC windows.Handle
	size := windows.Coord{X: conPTYWidth, Y: conPTYHeight}
	if err := windows.CreatePseudoConsole(size,
		windows.Handle(inW.Fd()), windows.Handle(outW.Fd()), 0, &hPC); err != nil {
		inR.Close()
		outR.Close()
		return nil, fmt.Errorf("pty: CreatePseudoConsole 失败:%w", err)
	}
	// ③ 启动属性
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		windows.ClosePseudoConsole(hPC)
		inR.Close()
		outR.Close()
		return nil, fmt.Errorf("pty: 建启动属性列表失败:%w", err)
	}
	defer attrs.Delete()
	// 取**局部变量的地址**而不是把句柄本身转成 unsafe.Pointer:后者是 uintptr→Pointer
	// 转换,`go vet` 的 unsafeptr 检查会拦(CI 的 test-windows job 跑 vet ⇒ 会红)。
	// Update 只需要一个「装着句柄值的内存地址」,给 &h 完全够,且语义更清楚。
	ph := hPC
	if err := attrs.Update(procThreadAttributePseudoConsole, unsafe.Pointer(&ph), unsafe.Sizeof(ph)); err != nil {
		windows.ClosePseudoConsole(hPC)
		inR.Close()
		outR.Close()
		return nil, fmt.Errorf("pty: 挂伪控制台属性失败:%w", err)
	}

	// ④ CreateProcessW
	si := &windows.StartupInfoEx{
		ProcThreadAttributeList: (*windows.ProcThreadAttributeList)(unsafe.Pointer(attrs.List())),
	}
	si.Cb = uint32(unsafe.Sizeof(*si))
	// 子进程的标准句柄:它从 inR 读输入、往 outW 写输出。
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput = windows.Handle(inR.Fd())
	si.StdOutput = windows.Handle(outW.Fd())
	si.StdErr = si.StdOutput

	utf16Env, err := utf16EnvBlock(env)
	if err != nil {
		windows.ClosePseudoConsole(hPC)
		inR.Close()
		outR.Close()
		return nil, err
	}
	// 命令行:Windows 的命令行是**单个字符串**,引号规则与 posix 不同(CreateProcess 自己
	// 解析),所以必须自己把 argv 拼成一条,并保证整体是 UTF-16。
	cmdLine, err := windows.UTF16PtrFromString(joinArgv(argv))
	if err != nil {
		windows.ClosePseudoConsole(hPC)
		inR.Close()
		outR.Close()
		return nil, fmt.Errorf("pty: 命令行转 UTF-16 失败:%w", err)
	}
	var dirPtr *uint16
	if dir != "" {
		if p, err := windows.UTF16PtrFromString(dir); err == nil {
			dirPtr = p
		}
	}
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(nil, cmdLine, nil, nil, false,
		extendedStartupInfoPresent|windows.CREATE_UNICODE_ENVIRONMENT,
		utf16Env, dirPtr, (*windows.StartupInfo)(unsafe.Pointer(si)), &pi); err != nil {
		windows.ClosePseudoConsole(hPC)
		inR.Close()
		outR.Close()
		return nil, fmt.Errorf("pty: CreateProcessW 失败:%w", err)
	}
	// 主线程句柄用不上(不等它)
	_ = windows.CloseHandle(pi.Thread)
	// 父进程这侧不再需要伪控制台句柄(子进程已持有);留着会一直占着管道缓冲。
	windows.ClosePseudoConsole(hPC)

	proc := &conPTYProc{h: pi.Process}
	return &ptySession{
		out:   outR,
		in:    inR,
		wait:  func() { proc.wait() },
		stop:  func() { proc.kill() },
		close: func() { proc.close(); inR.Close(); outR.Close() },
	}, nil
}

// joinArgv 把 argv 拼成 Windows 命令行字符串(整体一个字符串,CreateProcess 自己解析)。
//
// 引号规则:含空白或引号的项用**双引号**包起来,内部 `"` 按 Windows 规则转义为 `\"`。
// 反斜杠结尾要加倍(否则收尾的引号会被吃掉)—— 这是 Windows 命令行最经典的坑。
func joinArgv(argv []string) string {
	var b strings.Builder
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(quoteWinArg(a))
	}
	return b.String()
}

func quoteWinArg(a string) string {
	if !strings.ContainsAny(a, " \t\"") {
		return a
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for i := 0; i < len(a); i++ {
		c := a[i]
		if c == '\\' {
			slashes++
			continue
		}
		if c == '"' {
			// 2n+1 个反斜杠 + 转义引号
			b.WriteString(strings.Repeat("\\", slashes*2+1))
			b.WriteByte('"')
			slashes = 0
			continue
		}
		b.WriteString(strings.Repeat("\\", slashes))
		slashes = 0
		b.WriteByte(c)
	}
	b.WriteString(strings.Repeat("\\", slashes*2)) // 收尾:反斜杠加倍(保护封口引号)
	b.WriteByte('"')
	return b.String()
}

// utf16EnvBlock 编 UTF-16 环境块(每个变量 "K=V\0",整块再补一个 \0)。
// 必须配 CREATE_UNICODE_ENVIRONMENT —— 否则 Windows 按 ANSI 解析,非 ASCII 变量值会乱。
func utf16EnvBlock(env []string) (*uint16, error) {
	if len(env) == 0 {
		return nil, nil // nil = 继承父进程环境
	}
	// 排序保证可复现(CreateProcess 不在乎顺序,但「同样输入 → 同样块」便于排查)。
	sorted := append([]string(nil), env...)
	sortStrings(sorted)
	var b []uint16
	for _, kv := range sorted {
		if kv == "" {
			continue
		}
		u, err := windows.UTF16FromString(kv)
		if err != nil {
			return nil, fmt.Errorf("pty: 环境变量转 UTF-16 失败(%q):%w", kv, err)
		}
		b = append(b, u...) // UTF16FromString 已含结尾 \0
	}
	b = append(b, 0) // 整块终止
	return &b[0], nil
}

// sortStrings 小工具(避免为一处排序引 sort 包到本文件的 import 组)。
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// shellForPty 交互命令用的 shell(Windows 侧恒为 cmd.exe,见 pty.go 的 shellArgv)。
// 保留这个函数是为了让「选哪个 shell」这件事在两个平台文件里形状一致(pty.go 直接调它)。
func shellForPty() string { return "cmd.exe" }

var _ = sdk.SandboxWorkspace // 保持 sdk 引用(环境/沙箱口径与 unix 侧同源,见 pty.go)
