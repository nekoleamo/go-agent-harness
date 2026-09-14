// Package testutil 测试辅助:集中收敛跨平台差异。
//
// 约定:平台分支只服务于「测试如何准备环境 / 如何断言」;被测代码的行为差异
// (如 Windows 无内核沙箱、PTY 未实现)由产品侧显式表达,不在本包兜底。
package testutil

import (
	"path/filepath"
	"runtime"
	"testing"
)

// ExeName 给辅助二进制名补当前平台的可执行后缀。
//
// Windows 上必须补 .exe:宿主用 exec.Command(绝对路径) 启动外部插件,而 os/exec
// 在 Windows 上按 PATHEXT 补全,对**无扩展名**的 PE 一律 ErrNotFound(见
// internal/embed.ExtPluginBinary 注释)。测试里 `go build -o` 出的辅助二进制
// (外部插件 / mcpserver / 被测主程序)因此在 Windows 上无法被拉起,
// 现场表现为 "plugin failed to exit gracefully" 或
// `exec: "...\mcpserver": executable file not found in %PATH%`。
//
// 注意:调用方传入的 name 只应是**基名**(如 tool-echo),不要传已含路径的值。
func ExeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// IsWindows 便于测试做出「整段断言在 Windows 上不成立」的平台化处理。
func IsWindows() bool { return runtime.GOOS == "windows" }

// ShellPath 把文件系统路径转成可直接嵌进 shell 命令文本的形态。
//
// Windows 上反斜杠是 shell 的转义符(sh -c "echo x > C:\Users\a" 会把 `\U` 吃掉,
// 实际写到 `C:Usersa`),而正斜杠在 Git Bash 与 Windows API 下同等可用且无需转义。
// POSIX 平台原样返回。
func ShellPath(p string) string {
	if runtime.GOOS == "windows" {
		return filepath.ToSlash(p)
	}
	return p
}

// PosixPerm 报告当前平台是否支持 POSIX 权限位。
//
// Windows 没有 Unix 权限位:os.Chmod 只影响只读位,os.Stat().Mode().Perm() 恒为
// 0666/0777 形态,故「文件应为 0600/0700」一类断言在 Windows 上既不成立也无意义。
// 断言现场应写成 `if testutil.PosixPerm() && perm != 0o600 { ... }`,而不是整段
// t.Skip —— 权限之外的校验(文件存在/内容/归属)在 Windows 上仍然有效。
func PosixPerm() bool { return runtime.GOOS != "windows" }

// SkipNoPTY 跳过依赖 PTY(伪终端)的测试。
//
// Windows 上 shell 工具的 pty 分支未实现(产品侧对 pty 请求显式返回
// "unsupported"),发行矩阵的 windows/arm64 也据此暂缓(见 .goreleaser.yaml 里的
// `# 视 pty 进度启用`)。跳过而非断言失败:这是已知的平台能力缺口,不是回归。
func SkipNoPTY(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows 未实现 PTY(产品侧显式 unsupported;windows/arm64 发行待 PTY 进度)")
	}
}

// SkipNoPosixPath 跳过用 POSIX 绝对路径表达「工作区外」的用例。
//
// 与 SkipNoPTY 的区别:这是**测试无法表达意图**,不是产品缺能力。
// Windows 的 filepath 不把 "/tmp/x" 当绝对路径(没有盘符),而这类用例正是用
// "/tmp/x"(或 "/c/x"、"/usr/bin/x")来构造「根外写目标」。
// withWinSemantics(false) 只能翻转产品侧**显式检查**的语义,翻不动 stdlib 的
// filepath —— 于是同一命令在 Windows 上判定为「相对路径、必在根内」而放行,
// 与用例期望的「绝对路径、越界、拒绝」正好相反。
//
// 这是平台固有差异,不是回归;POSIX 语义的完整覆盖由 ubuntu job 承担,
// Windows 专有语义(MSYS 根相对路径 /c/x、大小写折叠)由
// shellpaths_winsemantics_test.go 与 pathpolicy_win_test.go 在 Windows 上真跑。
func SkipNoPosixPath(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("用例用 POSIX 绝对路径表达「工作区外」,Windows 的 filepath 不认其为绝对路径")
	}
}
