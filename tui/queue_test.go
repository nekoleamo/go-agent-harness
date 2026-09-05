// P4-1 消息队列单测:state 队列方法、运行中 Enter 排队 / 空闲直接发、
// 回合成功自动续发(取消/失败暂停)、Alt+Up/Esc 取回、状态栏待发提示、会话切换清空。
package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestQueueMethods(t *testing.T) {
	s := &State{}
	if s.Dequeue() != "" || s.PopQueued() != "" {
		t.Fatal("空队列取应返回空串")
	}
	s.Enqueue("a")
	s.Enqueue("b")
	s.Enqueue("c")
	if s.Dequeue() != "a" || s.Dequeue() != "b" {
		t.Fatal("Dequeue 应先发队头")
	}
	s.Enqueue("d")
	if s.PopQueued() != "d" {
		t.Fatal("PopQueued 应取队尾(最新)")
	}
	s.ClearQueue()
	if len(s.Queue) != 0 {
		t.Fatal("清空应移除全部")
	}
}

func TestRunningEnterQueuesIdleSubmits(t *testing.T) {
	var sent []string
	m := &Model{state: &State{Running: true}}
	m.onSubmit = func(s string) { sent = append(sent, s) }
	m.onCommand = func(s string) error { return nil }

	// 运行中提交普通消息 → 入队,onSubmit 不调用
	for _, r := range "等等" {
		m.state.InsertRune(r)
	}
	if _, _ = m.Update(keyPress(tea.KeyEnter, 0)); len(sent) != 0 {
		t.Fatal("运行中 Enter 不应直接提交")
	}
	if len(m.state.Queue) != 1 || m.state.Queue[0] != "等等" {
		t.Fatalf("应入队: %v", m.state.Queue)
	}
	if m.state.Input != "" {
		t.Fatalf("排队后输入应清空: %q", m.state.Input)
	}
	// 命令不排队(即时执行)
	for _, r := range "/jobs list" {
		m.state.InsertRune(r)
	}
	if _, _ = m.Update(keyPress(tea.KeyEnter, 0)); len(sent) != 0 {
		t.Fatal("命令不应进 onSubmit")
	}
	if len(m.state.Queue) != 1 {
		t.Fatal("命令不应入队")
	}
	// 回合结束成功 → 自动发队列头(每次一条)
	if _, _ = m.Update(agentDoneMsg{}); len(sent) != 1 || sent[0] != "等等" {
		t.Fatalf("回合结束应自动发队列: %v", sent)
	}
	if len(m.state.Queue) != 0 {
		t.Fatal("发送后队列应空")
	}
	// 空闲 Enter 直接提交(不进队列)
	for _, r := range "空闲消息" {
		m.state.InsertRune(r)
	}
	m.state.Running = false
	if _, _ = m.Update(keyPress(tea.KeyEnter, 0)); len(sent) != 2 || sent[1] != "空闲消息" {
		t.Fatalf("空闲 Enter 应直接发: %v", sent)
	}
}

func TestAgentDoneCancelPausesQueue(t *testing.T) {
	var sent []string
	m := &Model{state: &State{Running: true}}
	m.onSubmit = func(s string) { sent = append(sent, s) }
	m.state.Enqueue("排队内容")

	// 回合失败(非取消):不续发
	if _, _ = m.Update(agentDoneMsg{err: errors.New("boom")}); len(sent) != 0 {
		t.Fatal("失败回合不应续发")
	}
	if len(m.state.Queue) != 1 {
		t.Fatal("失败后队列应保留")
	}
	// 用户取回(Esc,空闲态)→ 回填编辑区并出队
	m.state.Running = false
	if _, _ = m.Update(keyPress(tea.KeyEscape, 0)); m.state.Input != "排队内容" {
		t.Fatalf("Esc 应取回: %q", m.state.Input)
	}
	if len(m.state.Queue) != 0 {
		t.Fatal("取回后队列应空")
	}
	// 取消回合(Esc 运行中)也不续发
	m.state.Enqueue("第二条")
	m.state.Running = true
	if _, _ = m.Update(agentDoneMsg{err: context.Canceled}); len(sent) != 0 {
		t.Fatal("取消回合不应续发")
	}
}

func TestAltUpPopsQueued(t *testing.T) {
	m := &Model{state: &State{Running: true}}
	m.state.Enqueue("a")
	m.state.Enqueue("b")
	// Alt+Up 运行中取回最新一条
	if _, _ = m.Update(keyPress(tea.KeyUp, tea.ModAlt)); m.state.Input != "b" {
		t.Fatalf("Alt+Up 应取回最新: %q", m.state.Input)
	}
	if len(m.state.Queue) != 1 || m.state.Queue[0] != "a" {
		t.Fatalf("队列应剩 a: %v", m.state.Queue)
	}
	// 队列只剩 a 时再 Alt+Up:取回 a(取空队列)
	if _, _ = m.Update(keyPress(tea.KeyUp, tea.ModAlt)); m.state.Input != "a" {
		t.Fatalf("再次 Alt+Up 应取回 a: %q", m.state.Input)
	}
	if len(m.state.Queue) != 0 {
		t.Fatal("取空后队列应为 0")
	}
}

func TestStatusBarQueueHint(t *testing.T) {
	s := &State{Running: true, Queue: []string{"q1", "q2"}}
	out := stripColor(renderStatusLine(s, 80))
	if !strings.Contains(out, "待发 2") || !strings.Contains(out, "Alt+Up") {
		t.Fatalf("状态栏应显示待发数: %q", out)
	}
	// 空闲 + 队列(回合结束后保留)同样提示
	s2 := &State{Queue: []string{"x"}}
	if out := stripColor(renderStatusLine(s2, 80)); !strings.Contains(out, "待发 1") {
		t.Fatalf("空闲待发提示: %q", out)
	}
	// 无队列不显示
	if out := stripColor(renderStatusLine(&State{}, 80)); strings.Contains(out, "待发") {
		t.Fatalf("无队列不提示: %q", out)
	}
}

func TestClearQueueOnSessionSwitch(t *testing.T) {
	s := &State{Queue: []string{"遗留"}}
	// afterSessionSwitch 在 App 层清空;此处直接验证 ClearQueue 经 UI 语义可达
	s.ClearQueue()
	if len(s.Queue) != 0 {
		t.Fatal("会话切换应清空队列")
	}
}
