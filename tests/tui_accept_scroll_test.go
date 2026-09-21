// A-1 条目 28(滚动条/PgUp 之外的:滚轮 / 划选 / 搜索 / @ 共存)的 pty 真机验收。
// 鼠标用 SGR 编码(`ESC [ < Cb ; Cx ; Cy M/m`)直接写进 pty —— 与真实终端同一条通路。
package tests

import (
	"strings"
	"testing"
	"time"
)

const (
	wheelUp   = "\x1b[<64;10;10M"
	mouseDown = "\x1b[<0;5;5M"
	mouseDrag = "\x1b[<32;5;12M"
	mouseUp   = "\x1b[<0;5;12m"
)

func TestTUIAcceptScrollSearchSelectMention(t *testing.T) {
	bin, env, _, _ := tuiAcceptSetup(t)
	s := newTuiSess(t, bin, env, "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	// 造可滚动内容(/help 长输出进会话流)
	s.send("/help\r")
	time.Sleep(900 * time.Millisecond)
	s.send("/help\r")
	// 长内容会触发会话流滚动 —— 最小屏模型对"区域滚动"覆盖不全,故内容类判定一律走原始流
	if !s.waitRaw("命令帮助", 20*time.Second) {
		t.Fatalf("条目 28:帮助输出未出现;尾段 %q", stripANSI(tailS(s.rawText(), 300)))
	}
	n0 := len(s.rawText())
	for i := 0; i < 3; i++ {
		s.send(wheelUp)
		time.Sleep(250 * time.Millisecond)
	}
	t.Logf("条目 28:滚轮上滚输出增量 %d 字节", len(s.rawText())-n0)
	if len(s.rawText()) == n0 {
		t.Errorf("条目 28:鼠标滚轮未生效(无重绘)")
	}

	// 搜索激活(/search 命令面)+ 搜索态下滚轮仍可用
	s.send("/search 命令帮助\r")
	if !s.waitRaw("命中", 15*time.Second) {
		t.Errorf("条目 28:搜索未激活")
	}
	n1 := len(s.rawText())
	for i := 0; i < 2; i++ {
		s.send(wheelUp)
		time.Sleep(250 * time.Millisecond)
	}
	if len(s.rawText()) == n1 {
		t.Errorf("条目 28:搜索态下滚轮失效(搜索与滚轮不能共存)")
	}

	// 划选(按下 → 拖动 → 释放):有位移 → 走 OSC52 复制
	s.send(mouseDown)
	time.Sleep(200 * time.Millisecond)
	s.send(mouseDrag)
	time.Sleep(250 * time.Millisecond)
	s.send(mouseUp)
	time.Sleep(700 * time.Millisecond)
	if !strings.Contains(s.rawText(), "\x1b]52;") {
		t.Errorf("条目 28:划选拖动未写出 OSC52 剪贴板")
	}

	// 清场后 @ 引用候选与滚轮并存(不崩、候选仍在)
	s.send("\x1b")
	time.Sleep(400 * time.Millisecond)
	s.send("@")
	s.send("@")
	time.Sleep(300 * time.Millisecond)
	s.send(wheelUp)
	time.Sleep(400 * time.Millisecond)
	if strings.Contains(s.rawText(), "panic") {
		t.Errorf("条目 28:滚轮与 @ 候选并存时 panic")
	}
	if !strings.Contains(s.rawText(), "@") {
		t.Errorf("条目 28:@ 引用输入未回显")
	}
}
