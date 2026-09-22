// NOND-N2 系统级通知落点:探测终端能力 + **逐级降级**(不写死支持矩阵)。
//
// 为什么不写支持矩阵:终端对 OSC 通知的支持随版本漂移(公开矩阵里 Windows Terminal /
// VTE 的结论互相矛盾),写死矩阵等于把「配了也不响」变成静默失效。这里的策略是
// 探测环境变量 → 选一个**最可能生效**的落点 → 未知/失败逐级降级到 bell / 仅状态栏,
// 并把「现在到底会怎么发」摆给用户看(`/notify`)。
//
// 分工边界:host-notices 只负责「有这件事要告诉人」(sdk.Notice),**怎么让人注意到**是端的事;
// 本文件就是 TUI 端的那一半。所以这里的失败一律不向用户报错(TUI 已经用状态栏呈现过了),
// 只在 `/notify` 里如实回显探测结果。
package tui

import (
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// NotifyEnv 系统级通知开关:auto(默认,按探测)/ osc(强制 OSC)/ bell(仅响铃)/ off(零输出)。
const NotifyEnv = "GAH_TUI_NOTIFY"

const (
	notifyTitleMax = 120
	notifyBodyMax  = 400
)

// NotifyMode 系统级通知模式。
type NotifyMode string

const (
	NotifyAuto NotifyMode = "auto"
	NotifyOSC  NotifyMode = "osc"
	NotifyBell NotifyMode = "bell"
	NotifyOff  NotifyMode = "off"
)

// ParseNotifyMode 解析模式串(空/未知 → auto:环境变量写错不该把通知静默关掉)。
func ParseNotifyMode(s string) NotifyMode {
	switch NotifyMode(strings.ToLower(strings.TrimSpace(s))) {
	case NotifyOSC:
		return NotifyOSC
	case NotifyBell:
		return NotifyBell
	case NotifyOff:
		return NotifyOff
	default:
		return NotifyAuto
	}
}

// notifyTarget 落点(能力族,不是具体终端 —— 同一族的终端共用一条序列)。
type notifyTarget int

const (
	targetNone   notifyTarget = iota // 完全没有可用落点(见 /notify)
	targetOSC99                      // kitty 协议:标题/正文分块 + 终端侧在场抑制
	targetOSC777                     // WezTerm / Ghostty / foot / VTE 系(urxvt 风格)
	targetOSC9                       // iTerm2 / VS Code / Warp / Cursor / Windows Terminal(候选)
	targetBell                       // 通用兜底(粗但哪都有)
)

func (t notifyTarget) String() string {
	switch t {
	case targetOSC99:
		return "OSC 99(kitty)"
	case targetOSC777:
		return "OSC 777"
	case targetOSC9:
		return "OSC 9"
	case targetBell:
		return "bell"
	default:
		return "无(仅状态栏)"
	}
}

// notifyWrap 多路复用包装:tmux / GNU Screen 会吞掉未包装的 OSC → 需 DCS 包裹。
type notifyWrap int

const (
	wrapNone notifyWrap = iota
	wrapTmux
	wrapScreen
)

func (w notifyWrap) String() string {
	switch w {
	case wrapTmux:
		return "tmux DCS 包裹"
	case wrapScreen:
		return "screen DCS 包裹"
	default:
		return "无"
	}
}

// detectNotifyTarget 纯函数:环境变量 → (落点, 包装)。
// 顺序 = 命中即停,先看最明确的信号(KITTY_WINDOW_ID 这类专用变量)再看宽泛信号(TERM)。
func detectNotifyTarget(env func(string) string) (notifyTarget, notifyWrap) {
	wrap := wrapNone
	switch {
	case env("TMUX") != "":
		wrap = wrapTmux
	case env("STY") != "":
		wrap = wrapScreen
	}
	term := strings.ToLower(env("TERM"))
	prog := strings.ToLower(env("TERM_PROGRAM"))
	target := func() notifyTarget {
		switch {
		case env("KITTY_WINDOW_ID") != "" || strings.Contains(term, "kitty"):
			return targetOSC99
		case env("WEZTERM_PANE") != "" || strings.Contains(term, "wezterm") || prog == "wezterm" ||
			strings.Contains(term, "ghostty") || prog == "ghostty" ||
			strings.Contains(term, "foot") || env("VTE_VERSION") != "":
			return targetOSC777
		case prog == "iterm.app" || prog == "vscode" || prog == "cursor" || prog == "warpterminal" ||
			env("WT_SESSION") != "":
			return targetOSC9
		case prog == "apple_terminal":
			// Terminal.app 没有 OSC 通知能力(公开支持表里确定的一条)→ 直接给 bell,
			// 不先发一串谁也不会看的转义序列。
			return targetBell
		case term != "" && term != "dumb":
			return targetBell
		}
		return targetNone
	}()
	return target, wrap
}

// notifier TUI 侧发射器:模式 + 探测结果 + 输出端(控制终端)。
type notifier struct {
	mode   NotifyMode
	target notifyTarget
	wrap   notifyWrap
	out    io.Writer
	note   string // 装配说明(/notify 回显:未附着终端/写端打开失败原因)
}

// newNotifier 组装(探测与写端都由外部给,便于单测;目标置 nil = 未附着终端)。
func newNotifier(mode NotifyMode, env func(string) string, out io.Writer, attachErr error) *notifier {
	target, wrap := detectNotifyTarget(env)
	n := &notifier{mode: mode, target: target, wrap: wrap, out: out}
	if out == nil {
		n.note = "未附着控制终端(stdout 被重定向或非交互运行)"
		if attachErr != nil {
			n.note += ":" + attachErr.Error()
		}
	}
	return n
}

// newDefaultNotifier 默认装配:模式取 GAH_TUI_NOTIFY,输出写控制终端。
func newDefaultNotifier() *notifier {
	out, err := openNotifyTTY()
	return newNotifier(ParseNotifyMode(os.Getenv(NotifyEnv)), os.Getenv, out, err)
}

// openNotifyTTY 打开控制终端。**必须写 /dev/tty 而不是 stdout**:stdout 可能被重定向到
// 文件、被 hook 捕获(如 `gah ... > log`),那不是终端;写进去只会污染日志。失败 → nil
// (上层降级为「仅状态栏」,不报错:提示的应用内落点本来就还在)。
// Windows 无 /dev/tty,改用 CONOUT$ 拿控制台输出句柄。
func openNotifyTTY() (io.Writer, error) {
	if runtime.GOOS == "windows" {
		f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// setMode 运行期切换模式(`/notify off` 等;不持久化 —— env 是唯一持久开关)。
func (n *notifier) setMode(m NotifyMode) { n.mode = m }

// emit 发一条系统级通知。返回是否真的写出(供 `/notify test` 回显,不静默)。
// suppress = OSC 99 带 o=unfocused(仅终端**未聚焦**时显示,避免打断当前工作):
// 真实通知用 true;`/notify test` 必须用 false —— 否则用户在自己聚焦的窗口里按测试键
// 必然什么都看不到,测试入口自己把自己抑制掉了(2026-09-22 kitty 实测踩到)。
func (n *notifier) emit(title, body string, id uint64, suppress bool) bool {
	if n == nil || n.mode == NotifyOff || n.out == nil {
		return false
	}
	target := n.target
	switch n.mode {
	case NotifyBell:
		target = targetBell
	case NotifyOSC:
		// 强制 OSC:探测不出或探测成 bell 时给 OSC 9(用户自己知道终端支持什么)。
		if target == targetNone || target == targetBell {
			target = targetOSC9
		}
	}
	if target == targetNone {
		return false
	}
	payload := notifyPayload(target, sanitizeNotifyText(title, notifyTitleMax), sanitizeNotifyText(body, notifyBodyMax), id, suppress)
	if payload == "" {
		return false
	}
	// bell 不需要 DCS 包裹(裸 \a 会穿过 tmux/screen 直达成终端)。
	if target != targetBell {
		payload = wrapDCS(payload, n.wrap)
	}
	_, err := io.WriteString(n.out, payload)
	return err == nil
}

// statusText `/notify` 回显:模式 / 落点 / 包装 / 写端,以及「为什么现在可能不响」。
func (n *notifier) statusText() string {
	if n == nil {
		return "系统级通知:未装配(仅状态栏)"
	}
	var b strings.Builder
	b.WriteString("系统级通知: " + string(n.mode) + "(env " + NotifyEnv + " 可改)")
	b.WriteString("\n探测落点: " + n.target.String())
	if n.mode == NotifyBell {
		b.WriteString("(模式 bell:只响铃)")
	} else if n.mode == NotifyOSC && (n.target == targetNone || n.target == targetBell) {
		b.WriteString("(模式 osc:强制 OSC 9)")
	}
	b.WriteString("\n多路复用: " + n.wrap.String())
	b.WriteString("\n输出端: 控制终端 /dev/tty")
	if n.out == nil {
		b.WriteString(" 不可用 —— " + n.note)
	}
	if n.mode == NotifyOff {
		b.WriteString("\n当前模式 off:零输出(状态栏提示仍照常)")
	} else if n.target == targetNone && n.mode == NotifyAuto {
		b.WriteString("\n探测不到可用落点:自动模式只更新状态栏(可用 /notify osc 强制,或用 /notify bell)")
	}
	if n.wrap != wrapNone {
		b.WriteString("\ntmux/screen 需放行转义:tmux 配 `set -g allow-passthrough on`(≥3.3);未放行时只有 bell 能到终端")
	}
	return b.String()
}

// notifyLevelAllows 级别 → 是否值得打扰:只有 warn/error 走系统级;info 只更新状态栏
// (NOND-N1 未引 attention 级,这里就是「系统级」的判据落点)。
func notifyLevelAllows(l sdk.NoticeLevel) bool {
	return l == sdk.NoticeWarn || l == sdk.NoticeError
}

// notifyPayload 落点 → 实际转义序列(纯函数,便于逐字节断言)。
// suppress 只对 OSC 99 有意义(见 emit);其余落点没有在场抑制概念,忽略该参数。
func notifyPayload(t notifyTarget, title, body string, id uint64, suppress bool) string {
	switch t {
	case targetOSC99:
		// kitty 协议:标题与正文**分块**传(d=0 未完 / d=1 完,不置 d=1 终端会一直等下去);
		// o=unfocused 让**终端**做在场抑制 —— 应用判不出自己是否前台,交给终端才不误判;
		// 省略 o 时按协议默认 o=always(必显示),这正是 `/notify test` 要的。
		meta := "i=gah-" + strconv.FormatUint(id, 10) + ":"
		if suppress {
			meta += "o=unfocused:"
		}
		if body == "" {
			return "\x1b]99;" + meta + "d=1;" + title + "\x1b\\"
		}
		return "\x1b]99;" + meta + "d=0;" + title + "\x1b\\" +
			"\x1b]99;" + meta + "d=1:p=body;" + body + "\x1b\\"
	case targetOSC777:
		return "\x1b]777;notify;" + title + ";" + body + "\x07"
	case targetOSC9:
		msg := title
		if body != "" {
			msg += ": " + body
		}
		return "\x1b]9;" + msg + "\x07"
	case targetBell:
		return "\x07"
	}
	return ""
}

// wrapDCS 把序列塞进 DCS 透传:tmux/screen 只在**转义被翻倍**时才承认这是给外层的序列
// (`ESC Ptmux;` + 翻倍后的序列 + `ESC \`)。
func wrapDCS(payload string, w notifyWrap) string {
	if w == wrapNone || payload == "" {
		return payload
	}
	prefix := "\x1bPtmux;"
	if w == wrapScreen {
		prefix = "\x1bP"
	}
	return prefix + strings.ReplaceAll(payload, "\x1b", "\x1b\x1b") + "\x1b\\"
}

// sanitizeNotifyText 清洗进入终端序列的文本:剥掉 C0 控制符与 DEL(否则正文里的 BEL/ESC
// 会**提前终止序列**甚至注入别的转义 —— 提示正文可能来自工具错误信息,不能当可信输入),
// 折成单行、去首尾空白、按 rune 截断(超长显式加省略号)。
func sanitizeNotifyText(s string, max int) string {
	var b strings.Builder
	pendingSpace := false
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			pendingSpace = b.Len() > 0 // 换行/制表/控制符统一折成一个空格
			continue
		}
		if r == ' ' {
			pendingSpace = b.Len() > 0
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	r := []rune(out)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return out
}
