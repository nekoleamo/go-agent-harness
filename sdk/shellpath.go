// shellpath.go:POSIX shell 路径解析(跨插件共享的单一事实源)。
//
// 为什么放在 sdk:tool-shell(shell/pty 执行)与 host-jobs(后台任务 `sh -c`)都需要它,
// 而插件之间不得互相 import(仓库红线)。解析出的必须是 **POSIX shell** ——
// policy-guard 的写目标裁决是 POSIX 词法扫描,只有真跑 POSIX shell 时该裁决才成立;
// 因此这里**不提供 cmd.exe/PowerShell 回退**(两套语义并存会让判决与实际执行脱节,
// 即静默击穿),缺失时显式失败并把解法写进错误信息。
//
// 解析顺序:GAH_SHELL_PATH(显式;不可用即报错,不静默跳到下一步)
//
//	→ Windows:Git for Windows 常见安装位(Program Files / Program Files (x86) /
//	  LOCALAPPDATA\Programs 用户级安装)→ PATH 上的 bash.exe / sh.exe
//	→ 其它平台:PATH 上的 sh → /bin/sh。
//
// 不做缓存:一次探测相对进程启动可忽略,且 GAH_SHELL_PATH 变更即时生效(便于排障)。
package sdk

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ShellPathEnv 显式指定 POSIX shell 路径的配置键(最高优先级;兼容 `-c` 的 sh/bash/zsh/dash 均可)。
const ShellPathEnv = "GAH_SHELL_PATH"

// winSemantics MSYS/Windows 语义开关。变量而非常量:单测需在非 Windows 机器上覆盖该分支。
var winSemantics = runtime.GOOS == "windows"

// shellMissingMsg 找不到 POSIX shell 时的用户指引(显式失败,不静默降级)。
const shellMissingMsg = "未找到 POSIX shell(bash/sh)。Windows 请安装 Git for Windows(自带 bash.exe)," +
	"或设置 " + ShellPathEnv + " 指向 bash 可执行文件;macOS/Linux 请确认 /bin/sh 存在。" +
	"不提供 cmd.exe/PowerShell 回退:命令的写目标裁决按 POSIX 词法进行,换 shell 会让判定与实际执行脱节。"

// ErrPOSIXShellMissing 找不到可用 POSIX shell(调用方应显式失败,不得静默降级到其它 shell)。
var ErrPOSIXShellMissing = errors.New(shellMissingMsg)

// ResolvePOSIXShell 返回本机可用的 POSIX shell 路径(失败即错误,不返回空串)。
func ResolvePOSIXShell() (string, error) {
	if p := strings.TrimSpace(os.Getenv(ShellPathEnv)); p != "" {
		if !isExecutableFile(p) {
			return "", fmt.Errorf("%s=%q 不是可执行文件;%w", ShellPathEnv, p, ErrPOSIXShellMissing)
		}
		return p, nil
	}
	if runtime.GOOS == "windows" {
		for _, c := range winBashCandidates() {
			if isExecutableFile(c) {
				return c, nil
			}
		}
		for _, name := range []string{"bash.exe", "sh.exe"} {
			if p, err := exec.LookPath(name); err == nil {
				return p, nil
			}
		}
		return "", ErrPOSIXShellMissing
	}
	if p, err := exec.LookPath("sh"); err == nil {
		return p, nil
	}
	if isExecutableFile("/bin/sh") {
		return "/bin/sh", nil
	}
	return "", ErrPOSIXShellMissing
}

// winBashCandidates Git for Windows 的常见 bash.exe 安装位(系统级 + 用户级安装)。
// 环境变量缺失时**不产出候选**:否则 filepath.Join("", "Programs", …) 会得到相对路径,
// 被「相对 cwd 探测」意外命中(探测必须是绝对路径)。
func winBashCandidates() []string {
	dirs := []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		dirs = append(dirs, filepath.Join(la, "Programs"))
	}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if strings.TrimSpace(d) == "" {
			continue
		}
		out = append(out, filepath.Join(d, "Git", "bin", "bash.exe"))
	}
	return out
}

// isExecutableFile 判定路径是可执行的普通文件(Windows 无 unix 可执行位,只判非目录)。
func isExecutableFile(p string) bool {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return st.Mode().Perm()&0o111 != 0
}

// ShellExecEnv 在环境链之后追加 shell 运行开关(SanitizedEnv → jailEnv → 本函数)。
//
// Windows/MSYS 必须关闭**参数路径转换**:Git Bash 会把看起来像 unix 路径的参数自动改写成
// Windows 路径(`/tmp/x` → `C:\Users\…\AppData\Local\Temp\x`),而 policy-guard 按命令文本的
// 字面路径裁决 —— 不关掉就出现「裁决说在区内,实际写到区外」的静默击穿。
// 关闭后子进程收到与裁决一致的路径:原生程序拿到 `/` 开头的路径会报错,失败方向是「拒绝」而非「放行」。
func ShellExecEnv(env []string) []string {
	if !winSemantics {
		return env
	}
	return append(env, "MSYS_NO_PATHCONV=1", "MSYS2_ARG_CONV_EXCL=*")
}
