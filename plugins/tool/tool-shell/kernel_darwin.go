//go:build darwin

// kernel_darwin.go:macOS 内核级沙箱 = seatbelt(sandbox-exec profile)。
// sandbox-exec 是系统自带前端(10.5+;Apple 标记 deprecated 但仍是唯一可用的用户态入口):
// 进程及其**所有后代**都受 profile 约束 —— 这正是协作式控制拦不住的间接写所需要的。
package toolshell

import (
	"os"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func platformSupportNote() string { return "macOS 需 " + sandboxExec + " 可用" }

// platformWrap 生成 seatbelt 包装 argv(空 = 不施加)。
// profile 语义:默认全放行,只 deny 文件写,再按档位放行白名单 —— 与协作层"只管写"的范围一致
// (读与网络不设限),避免在内核层发明第二套权限模型。
func platformWrap(mode sdk.SandboxMode, root string) []string {
	if _, err := os.Stat(sandboxExec); err != nil {
		warnUnavailable("找不到 " + sandboxExec + ": " + err.Error())
		return nil
	}
	return []string{sandboxExec, "-p", darwinProfile(mode, root)}
}

// darwinProfile 组装 seatbelt profile。
func darwinProfile(mode sdk.SandboxMode, root string) string {
	var b strings.Builder
	b.WriteString("(version 1)(allow default)(deny file-write*)(allow file-write*")
	// 白名单路径必须**按解析后的真实路径**给出:macOS 上 /tmp 是 /private/tmp 的软链,
	// seatbelt 匹配的是真实路径 —— 不解析则 (subpath "/tmp/x") 形同不设(实测踩到)。
	// 顺序无关,但 workspace 在前便于阅读。
	if mode == sdk.SandboxWorkspace && strings.TrimSpace(root) != "" {
		b.WriteString(" (subpath \"" + sandboxQuote(resolvePath(root)) + "\")")
	}
	// jail 是 gah 自己的临时/缓存区(数据根内):两档都放行,否则 TMPDIR/GOCACHE 写不通,
	// 命令会大面积失败。read-only 也只放行到 jail 为止 —— 用户文件一律不可写。
	b.WriteString(" (subpath \"" + sandboxQuote(resolvePath(jailRoot())) + "\")")
	for _, lit := range []string{"/dev/null", "/dev/stdout", "/dev/stderr", "/dev/tty", "/dev/ptmx"} {
		b.WriteString(" (literal \"" + lit + "\")")
	}
	b.WriteString(" (subpath \"/dev/fd\")")
	// pty 模式:子进程的 stdout/stderr 是 pty slave(/dev/ttysNNN);不放行则 pty 全线失败。
	b.WriteString(" (regex #\"^/dev/ttys[0-9]+\"))")
	return b.String()
}

// sandboxQuote 转义 profile 字符串字面量中的 " 与 \(防路径含引号时 profile 语法被打破)。
func sandboxQuote(p string) string {
	return strings.ReplaceAll(strings.ReplaceAll(p, "\\", "\\\\"), "\"", "\\\"")
}
