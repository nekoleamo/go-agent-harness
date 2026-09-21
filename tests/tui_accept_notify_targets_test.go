// A-1 条目 88(通知矩阵)的可自动化半边:落点**选路**在真 pty 里端到端验 ——
// 同一份二进制,只换环境变量(TERM_PROGRAM / TMUX / KITTY_WINDOW_ID),断言实际写出的 OSC 序列。
// 真机是否"弹出来"属终端行为,归人眼(见 docs/VERIFY.md 剩余清单)。
package tests

import (
	"strings"
	"testing"
	"time"
)

// envOver 覆盖指定键:probeEnv 把 os.Environ() 放在前面,而进程取 env 是「首个匹配生效」,
// 故必须把我方取值**放在最前**,并从继承环境里删掉同名键(否则本机终端的 TERM=…kitty 会盖住用例设定)。
func envOver(base, over []string) []string {
	keys := map[string]bool{}
	for _, kv := range over {
		if i := strings.IndexByte(kv, '='); i > 0 {
			keys[kv[:i]] = true
		}
	}
	out := append([]string{}, over...)
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i > 0 && keys[kv[:i]] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func notifyProbe(t *testing.T, extraEnv []string, seq string) {
	t.Helper()
	bin, env, _, _ := tuiAcceptSetup(t)
	env = envOver(env, extraEnv)
	s := newTuiSess(t, bin, env, "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("/notify auto\r") // 清掉历史偏好(共享数据根,前序用例可能已把落点写进 gah-state.json)
	time.Sleep(400 * time.Millisecond)
	s.send("/notify test\r")
	if !s.waitRaw(seq, 15*time.Second) {
		t.Fatalf("未写出序列 %q;尾段 %q", seq, stripANSI(tailS(s.rawText(), 220)))
	}
}

// iTerm2 族 → OSC 9(未包装)
func TestTUIAcceptNotifyITerm2OSC9(t *testing.T) {
	notifyProbe(t, []string{"TERM_PROGRAM=iTerm.app", "TERM=xterm-256color", "TMUX=", "STY=", "KITTY_WINDOW_ID="}, "\x1b]9;")
}

// tmux + kitty → OSC 99 且被 tmux DCS 包裹(不包裹会被吞)
func TestTUIAcceptNotifyTmuxWrappedOSC99(t *testing.T) {
	notifyProbe(t, []string{"TMUX=/tmp/tmux-501/default,1,0", "KITTY_WINDOW_ID=1", "TERM=xterm-kitty", "TERM_PROGRAM=kitty", "STY="}, "\x1bPtmux;")
}

// GNU Screen + screen-256color:无 OSC 族匹配 → bell 兜底(裸 OSC 会被 screen 吞,兜底才是正确行为);
// 落点族矩阵由单测钉住(notify_test.go),此处验端到端报告与行为一致。
func TestTUIAcceptNotifyScreenWrapped(t *testing.T) {
	bin, env, _, _ := tuiAcceptSetup(t)
	env = envOver(env, []string{"STY=1234.pts-0.host", "TERM=screen-256color", "TERM_PROGRAM=", "TMUX=", "KITTY_WINDOW_ID="})
	s := newTuiSess(t, bin, env, "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("/notify auto\r")
	time.Sleep(400 * time.Millisecond)
	s.send("/notify test\r")
	if !s.waitRaw("落点 ", 15*time.Second) {
		t.Fatalf("未报告落点;尾段 %q", stripANSI(tailS(s.rawText(), 220)))
	}
	// screen-256color 无族匹配 → 落点 bell(与单测矩阵一致):此时**不需要** DCS 包裹,
	// 裸 OSC 在 screen 里会被吞,故"兜底 bell"就是正确行为。
	if out := squashSpace(stripANSI(s.rawText())); !strings.Contains(out, "落点bell") {
		t.Errorf("screen 环境落点应为 bell 兜底")
	}
}

// 无任何信号 → bell 兜底(粗但哪都有)
func TestTUIAcceptNotifyBellFallback(t *testing.T) {
	bin, env, _, _ := tuiAcceptSetup(t)
	env = envOver(env, []string{"TERM_PROGRAM=", "TERM=dumb", "TMUX=", "STY=", "KITTY_WINDOW_ID="})
	s := newTuiSess(t, bin, env, "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("/notify auto\r")
	time.Sleep(400 * time.Millisecond)
	s.send("/notify test\r")
	if !s.waitRaw("bell", 15*time.Second) {
		t.Fatalf("bell 兜底未生效(应显式告知落点);尾段 %q", stripANSI(tailS(s.rawText(), 220)))
	}
	if strings.Contains(s.rawText(), "\x1b]9;") || strings.Contains(s.rawText(), "\x1b]777;") {
		t.Errorf("无信号环境不应走 OSC 落点")
	}
}
