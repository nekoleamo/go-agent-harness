//go:build darwin

// darwin.go:macOS 内核沙箱 = seatbelt(`sandbox-exec` profile)。
// `sandbox-exec` 是系统自带前端(10.5+;Apple 标记 deprecated 但仍是唯一可用的用户态入口):
// 进程及其**所有后代**都受 profile 约束 —— 这正是协作式控制拦不住的间接写所需要的
// (spike 实证:profile 生效后 node 的 fs.writeFileSync 到区外得 EPERM)。
package kernelsandbox

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// sandboxExecDefault seatbelt 前端默认路径。
const sandboxExecDefault = "/usr/bin/sandbox-exec"

var readDenyNoteOnce sync.Once

// noteReadDeny 一次性说明:内核层读拒绝已生效(只在本文件调用 —— 即 darwin 分支)。
// 不说清楚的话,`git push`/`aws` 因读不到 key 而失败时无从归因(错误来自 ssh/aws 自己,
// 不是 gah 的拒绝文案)。与告警区分措辞:这是边界生效,不是降级。
func noteReadDeny(spec Spec, dirs []string) {
	if len(dirs) == 0 {
		return
	}
	readDenyNoteOnce.Do(func() {
		fmt.Fprintf(os.Stderr, "gah %s: 内核层已拒绝读取凭据目录(%s);如需直读密钥,设 %s=0。\n",
			spec.label(), strings.Join(dirs, ", "), spec.ReadDenySwitch)
	})
}

func platformSupportNote() string { return "macOS 需 " + sandboxExecDefault + " 可用" }

// platformWrap 生成 seatbelt 包装 argv(空 = 不施加)。
// profile 语义:默认全放行(读 + 网络),只 deny 文件写,再按档位放行白名单
// —— 与协作层"只管写目标"的范围一致。读只多拒 ReadDeny 里的目录(shell 专用)。
func platformWrap(spec Spec) []string {
	exe := spec.SandboxExec
	if strings.TrimSpace(exe) == "" {
		exe = sandboxExecDefault
	}
	if _, err := os.Stat(exe); err != nil {
		WarnUnavailable(spec, "找不到 "+exe+": "+err.Error())
		return nil
	}
	return []string{exe, "-p", darwinProfile(spec)}
}

// darwinProfile 组装 seatbelt profile。
func darwinProfile(spec Spec) string {
	var b strings.Builder
	b.WriteString("(version 1)(allow default)(deny file-write*)(allow file-write*")
	// 白名单路径必须**按解析后的真实路径**给出(见 ResolvePath 注释)。顺序无关,
	// workspace 在前便于阅读。
	if spec.Mode == sdk.SandboxWorkspace && strings.TrimSpace(spec.Root) != "" {
		b.WriteString(" (subpath \"" + sandboxQuote(ResolvePath(spec.Root)) + "\")")
	}
	// jail(临时/缓存锚点):两档都放行,否则 TMPDIR/GOCACHE 写不通,命令会大面积失败。
	b.WriteString(" (subpath \"" + sandboxQuote(ResolvePath(spec.Jail)) + "\")")
	// 额外白名单(包管理器缓存等):非存在路径也无害(seatbelt 对不存在的 subpath 不报错)。
	for _, p := range spec.RW {
		if strings.TrimSpace(p) == "" {
			continue
		}
		b.WriteString(" (subpath \"" + sandboxQuote(ResolvePath(p)) + "\")")
	}
	for _, lit := range []string{"/dev/null", "/dev/stdout", "/dev/stderr", "/dev/tty", "/dev/ptmx"} {
		b.WriteString(" (literal \"" + lit + "\")")
	}
	b.WriteString(" (subpath \"/dev/fd\")")
	// pty 模式:子进程的 stdout/stderr 是 pty slave(/dev/ttysNNN);不放行则 pty 全线失败。
	// 此处闭合 (allow file-write* …) 分组 —— 下面的 deny 必须是独立形态,放进 allow 表单里是非法参数。
	b.WriteString(" (regex #\"^/dev/ttys[0-9]+\"))")
	// 内核层**读**拒绝(shell 专用,默认空):协作层的凭据判定只看得见命令文本里的字面路径,
	// 解释器内动态构造的读(`python3 -c "open('~/.ss'+'h/id_'+'rsa')"`)完全绕过它。
	// 放在 (allow default) 之后:写侧"先 deny 后 allow"的实证说明具体规则晚于 default 生效,读侧同理。
	dirs := spec.ReadDeny
	for _, d := range dirs {
		b.WriteString(" (deny file-read* (subpath \"" + sandboxQuote(ResolvePath(d)) + "\"))")
	}
	noteReadDeny(spec, dirs)
	return b.String()
}

// sandboxQuote 转义 profile 字符串字面量中的 " 与 \(防路径含引号时 profile 语法被打破)。
func sandboxQuote(p string) string {
	return strings.ReplaceAll(strings.ReplaceAll(p, "\\", "\\\\"), "\"", "\\\"")
}
