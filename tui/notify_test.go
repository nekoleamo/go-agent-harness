package tui

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// envOf 构造 env 查询函数(测试用:map 即环境,缺失返回空)。
func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// TestDetectNotifyTarget 探测优先级与包装判定(逐条钉死:改探测顺序即失败)。
func TestDetectNotifyTarget(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want notifyTarget
		wrap notifyWrap
	}{
		{"kitty", map[string]string{"KITTY_WINDOW_ID": "1", "TERM": "xterm-kitty"}, targetOSC99, wrapNone},
		{"kitty仅TERM", map[string]string{"TERM": "xterm-kitty"}, targetOSC99, wrapNone},
		{"wezterm", map[string]string{"WEZTERM_PANE": "0", "TERM": "xterm-256color"}, targetOSC777, wrapNone},
		{"ghostty", map[string]string{"TERM": "xterm-ghostty"}, targetOSC777, wrapNone},
		{"foot", map[string]string{"TERM": "foot"}, targetOSC777, wrapNone},
		{"vte", map[string]string{"VTE_VERSION": "7600", "TERM": "xterm-256color"}, targetOSC777, wrapNone},
		{"iterm", map[string]string{"TERM_PROGRAM": "iTerm.app", "TERM": "xterm-256color"}, targetOSC9, wrapNone},
		{"vscode", map[string]string{"TERM_PROGRAM": "vscode", "TERM": "xterm-256color"}, targetOSC9, wrapNone},
		{"cursor", map[string]string{"TERM_PROGRAM": "Cursor", "TERM": "xterm-256color"}, targetOSC9, wrapNone},
		{"windows-terminal", map[string]string{"WT_SESSION": "abc", "TERM": "xterm-256color"}, targetOSC9, wrapNone},
		// Terminal.app 没有 OSC 通知能力(公开支持表里确定的一条)→ 直接 bell,不发没人看的序列。
		{"terminal.app", map[string]string{"TERM_PROGRAM": "Apple_Terminal", "TERM": "xterm-256color"}, targetBell, wrapNone},
		{"未知但TERM可用", map[string]string{"TERM": "xterm-256color"}, targetBell, wrapNone},
		{"无终端", map[string]string{}, targetNone, wrapNone},
		{"dumb", map[string]string{"TERM": "dumb"}, targetNone, wrapNone},
		// 多路复用优先于落点:tmux 里的 kitty 仍是 kitty 协议,但序列必须包 DCS。
		{"tmux里kitty", map[string]string{"TMUX": "/tmp/tmux-501/default,1,0", "KITTY_WINDOW_ID": "1", "TERM": "xterm-kitty"}, targetOSC99, wrapTmux},
		{"screen", map[string]string{"STY": "1234.pts-0.host", "TERM": "screen-256color"}, targetBell, wrapScreen},
		// tmux 与 screen 同时存在时以 tmux 为准(screen 里的 tmux 极罕见,不叠两层包装)。
		{"tmux优先", map[string]string{"TMUX": "x", "STY": "y", "TERM": "screen"}, targetBell, wrapTmux},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, wrap := detectNotifyTarget(envOf(c.env))
			if got != c.want || wrap != c.wrap {
				t.Fatalf("落点/包装 = %v/%v,期望 %v/%v", got, wrap, c.want, c.wrap)
			}
		})
	}
}

// TestNotifyPayloads 逐字节断言各落点的转义序列(含 kitty 分块与 d=1 收尾)。
func TestNotifyPayloads(t *testing.T) {
	cases := []struct {
		target   notifyTarget
		title    string
		body     string
		want     string
		suppress bool
	}{
		// 只有标题时必须 d=1(否则 kitty 一直等后续分块,永远不弹)。
		{targetOSC99, "标题", "", "\x1b]99;i=gah-7:o=unfocused:d=1;标题\x1b\\", true},
		// 有正文 = 两块:标题 d=0(未完)→ 正文 d=1:p=body(完)。
		{targetOSC99, "标题", "正文", "\x1b]99;i=gah-7:o=unfocused:d=0;标题\x1b\\" +
			"\x1b]99;i=gah-7:o=unfocused:d=1:p=body;正文\x1b\\", true},
		{targetOSC777, "标题", "正文", "\x1b]777;notify;标题;正文\x07", true},
		{targetOSC9, "标题", "正文", "\x1b]9;标题: 正文\x07", true},
		{targetBell, "标题", "正文", "\x07", true},
		// suppress=false = `/notify test`:不带 o=unfocused,按协议默认 o=always 必显示。
		{targetOSC99, "标题", "正文", "\x1b]99;i=gah-7:d=0;标题\x1b\\" +
			"\x1b]99;i=gah-7:d=1:p=body;正文\x1b\\", false},
		{targetNone, "标题", "正文", "", true},
	}
	for _, c := range cases {
		if got := notifyPayload(c.target, c.title, c.body, 7, c.suppress); got != c.want {
			t.Fatalf("%v 载荷 = %q,期望 %q", c.target, got, c.want)
		}
	}
}

// TestSanitizeNotifyText 清洗:控制符剥离(否则正文里的 BEL/ESC 会提前终止序列甚至注入转义)、
// 折单行、截断显式加省略号。
func TestSanitizeNotifyText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"普通文本", "普通文本"},
		{"两行\n内容", "两行 内容"},
		{"制表\t分隔", "制表 分隔"},
		{"注入\x07响铃", "注入 响铃"},
		{"逃逸\x1b]9;假\x07", "逃逸 ]9;假"},
		{"多余   空格", "多余 空格"},
		{"  首尾  ", "首尾"},
		{"\x00\x7f", ""},
	}
	for _, c := range cases {
		if got := sanitizeNotifyText(c.in, 100); got != c.want {
			t.Fatalf("清洗 %q = %q,期望 %q", c.in, got, c.want)
		}
	}
	if got := sanitizeNotifyText(strings.Repeat("字", 10), 4); got != "字字字字…" {
		t.Fatalf("超长应截断并显式省略: %q", got)
	}
}

// TestWrapDCS tmux/screen 的 DCS 透传包装:ESC 必须翻倍(不翻倍外层只当普通字符吞掉)。
func TestWrapDCS(t *testing.T) {
	if got := wrapDCS("\x1b]9;hi\x07", wrapNone); got != "\x1b]9;hi\x07" {
		t.Fatalf("未包装时不应改动: %q", got)
	}
	want := "\x1bPtmux;\x1b\x1b]9;hi\x07\x1b\\"
	if got := wrapDCS("\x1b]9;hi\x07", wrapTmux); got != want {
		t.Fatalf("tmux 包装 = %q,期望 %q", got, want)
	}
	if got := wrapDCS("\x1b]9;hi\x07", wrapScreen); !strings.HasPrefix(got, "\x1bP\x1b\x1b]9;") {
		t.Fatalf("screen 包装应以 DCS 开头且 ESC 翻倍: %q", got)
	}
}

// TestNotifierEmit 发射与模式:off 零输出、bell 只响铃、osc 强制 OSC9、未附着终端不发。
func TestNotifierEmit(t *testing.T) {
	kitty := map[string]string{"KITTY_WINDOW_ID": "1", "TERM": "xterm-kitty"}
	var buf bytes.Buffer
	n := newNotifier(NotifyAuto, envOf(kitty), &buf, nil)
	if !n.emit("标题", "正文", 3, true) {
		t.Fatal("kitty 环境应能发出")
	}
	if got := buf.String(); got != notifyPayload(targetOSC99, "标题", "正文", 3, true) {
		t.Fatalf("写入内容与载荷不符: %q", got)
	}

	// off:零输出(可断言:一个字节都不写)。
	buf.Reset()
	n.setMode(NotifyOff)
	if n.emit("标题", "", 4, true) || buf.Len() != 0 {
		t.Fatalf("off 模式必须零输出: %q", buf.String())
	}

	// bell:无条件响铃(模式优先于探测)。
	buf.Reset()
	n.setMode(NotifyBell)
	if !n.emit("标题", "", 5, true) || buf.String() != "\x07" {
		t.Fatalf("bell 模式应只写 \\a: %q", buf.String())
	}

	// osc:探测为 bell(Terminal.app)时强制升级成 OSC 9(用户的显式逃生口)。
	buf.Reset()
	n2 := newNotifier(NotifyOSC, envOf(map[string]string{"TERM_PROGRAM": "Apple_Terminal", "TERM": "xterm-256color"}), &buf, nil)
	if !n2.emit("标题", "", 6, true) || !strings.HasPrefix(buf.String(), "\x1b]9;") {
		t.Fatalf("osc 模式应强制 OSC 9: %q", buf.String())
	}

	// 探测不到且非强制 → 不发(仅状态栏),不报错。
	buf.Reset()
	n3 := newNotifier(NotifyAuto, envOf(map[string]string{}), &buf, nil)
	if n3.emit("标题", "", 7, true) || buf.Len() != 0 {
		t.Fatalf("无落点时应不发: %q", buf.String())
	}

	// 未附着控制终端(stdout 被重定向)→ 不发,但状态行里说明原因。
	buf.Reset()
	n4 := newNotifier(NotifyAuto, envOf(kitty), nil, nil)
	if n4.emit("标题", "", 8, true) {
		t.Fatal("无写端时不应发")
	}
	if !strings.Contains(n4.statusText(), "未附着") {
		t.Fatalf("状态行应说明未附着终端: %q", n4.statusText())
	}

	// tmux 里发:包装生效。
	buf.Reset()
	n5 := newNotifier(NotifyAuto, envOf(map[string]string{"TMUX": "x", "KITTY_WINDOW_ID": "1", "TERM": "xterm-kitty"}), &buf, nil)
	n5.emit("标题", "", 9, true)
	if !strings.HasPrefix(buf.String(), "\x1bPtmux;\x1b\x1b]99;") {
		t.Fatalf("tmux 内应包 DCS: %q", buf.String())
	}
}

// TestNotifyLevelAllows 只有 warn/error 打扰人;info 只更新状态栏。
func TestNotifyLevelAllows(t *testing.T) {
	if notifyLevelAllows(sdk.NoticeInfo) {
		t.Fatal("info 不应触发系统级通知")
	}
	if !notifyLevelAllows(sdk.NoticeWarn) || !notifyLevelAllows(sdk.NoticeError) {
		t.Fatal("warn/error 应触发系统级通知")
	}
}

// TestParseNotifyMode 环境变量写错不该把通知静默关掉(未知 → auto)。
func TestParseNotifyMode(t *testing.T) {
	cases := map[string]NotifyMode{
		"": NotifyAuto, "auto": NotifyAuto, "AUTO": NotifyAuto, " off ": NotifyOff,
		"osc": NotifyOSC, "bell": NotifyBell, "nonsense": NotifyAuto,
	}
	for in, want := range cases {
		if got := ParseNotifyMode(in); got != want {
			t.Fatalf("ParseNotifyMode(%q) = %q,期望 %q", in, got, want)
		}
	}
}

// TestCmdNotify /notify 命令:无参回显、test 真发、切模式、非法参数显式报错。
func TestCmdNotify(t *testing.T) {
	a := commandTestApp()
	var buf bytes.Buffer
	a.notifier = newNotifier(NotifyAuto, envOf(map[string]string{"KITTY_WINDOW_ID": "1", "TERM": "xterm-kitty"}), &buf, nil)

	out, err := a.cmdNotify(nil)
	if err != nil || !strings.Contains(out, "OSC 99") || !strings.Contains(out, "输出端") {
		t.Fatalf("/notify 应回显探测结果: %q %v", out, err)
	}

	out, err = a.cmdNotify([]string{"test"})
	if err != nil || !strings.Contains(out, "已发出") || buf.Len() == 0 {
		t.Fatalf("/notify test 应真发一条: %q %v (%q)", out, err, buf.String())
	}

	if out, err = a.cmdNotify([]string{"off"}); err != nil || !strings.Contains(out, "off") {
		t.Fatalf("/notify off 应切模式: %q %v", out, err)
	}
	buf.Reset()
	if out, err = a.cmdNotify([]string{"test"}); err != nil || !strings.Contains(out, "未发出") || buf.Len() != 0 {
		t.Fatalf("off 后 test 应如实回显未发出: %q %v (%q)", out, err, buf.String())
	}

	if _, err = a.cmdNotify([]string{"nonsense"}); err == nil {
		t.Fatal("非法参数应显式报错")
	}
}

// TestSystemNotifyOnlyWarnError 端到端桥接:warn/error 写终端,info 不写;同一 id 不重复打扰。
func TestSystemNotifyOnlyWarnError(t *testing.T) {
	a := commandTestApp()
	var buf bytes.Buffer
	a.notifier = newNotifier(NotifyAuto, envOf(map[string]string{"KITTY_WINDOW_ID": "1", "TERM": "xterm-kitty"}), &buf, nil)

	a.systemNotify(&sdk.Notice{ID: 1, Level: sdk.NoticeInfo, Title: "只是状态"})
	if buf.Len() != 0 {
		t.Fatalf("info 不应写终端: %q", buf.String())
	}
	a.systemNotify(&sdk.Notice{ID: 2, Level: sdk.NoticeError, Title: "任务失败", Body: "原因"})
	if !strings.Contains(buf.String(), "任务失败") {
		t.Fatalf("error 应写终端: %q", buf.String())
	}
}

// TestApplyNoticeReportsAcceptance 接纳语义:新 id 才算接纳(NOND-N2 靠它避免重复打扰)。
func TestApplyNoticeReportsAcceptance(t *testing.T) {
	s := &State{}
	if !s.ApplyNotice(&sdk.Notice{ID: 3, Title: "a"}) {
		t.Fatal("新提示应被接纳")
	}
	if s.ApplyNotice(&sdk.Notice{ID: 3, Title: "a-重复"}) || s.ApplyNotice(&sdk.Notice{ID: 2, Title: "旧"}) {
		t.Fatal("同 id 或更旧 id 不应被接纳")
	}
	if s.ApplyNotice(nil) || s.ApplyNotice(&sdk.Notice{}) {
		t.Fatal("nil / ID=0 不应被接纳")
	}
	if s.Notice == nil || s.Notice.Title != "a" {
		t.Fatalf("接纳的更旧 id 不得覆盖当前: %+v", s.Notice)
	}
}

// TestNotifyRealTTY 真机冒烟(默认跳过):附着控制终端时把序列真写进终端。
// 为什么默认跳过 —— 它会在跑测试的那个终端上真弹出通知;真机矩阵逐环境跑时显式打开:
//
//	GAH_TUI_NOTIFY_SMOKE=1 script -q /tmp/tty.log go test ./tui/ -run TestNotifyRealTTY -v
//
// 断言的是「写到真实 pty 且不报错」;观察到弹出与否由人确认(产物 /tmp/tty.log 里有转义序列)。
func TestNotifyRealTTY(t *testing.T) {
	if os.Getenv("GAH_TUI_NOTIFY_SMOKE") == "" {
		t.Skip("真机冒烟:需 GAH_TUI_NOTIFY_SMOKE=1(会在当前终端真发通知)")
	}
	out, err := openNotifyTTY()
	if err != nil {
		t.Skipf("未附着控制终端(%v):/dev/tty 不可用时的降级路径由 TestNotifierEmit 覆盖", err)
	}
	mode := NotifyAuto
	if m := os.Getenv(NotifyEnv); m != "" {
		mode = ParseNotifyMode(m)
	}
	n := newNotifier(mode, os.Getenv, out, nil)
	t.Log("探测结果:", n.statusText())
	if !n.emit("gah 真机冒烟", "若你看到这条通知,说明 "+n.target.String()+" 在本终端生效", 1, true) {
		t.Fatalf("附着终端时应发出(模式 %s,落点 %s)", n.mode, n.target)
	}
}
