// S1.3 输入增强单测:历史翻页(Ctrl+P/N 含草稿还原)、undo/redo(含合并与清栈)、
// kill(Ctrl+K/U)、按词移动(Alt+←/→)——状态机纯逻辑,不依赖终端。
package tui

import (
	"testing"
	"time"
)

// histState 构造含历史源(user 行 + 命令)的 State。
// TestSelectAllConsume Ctrl+A 全选:Backspace/Delete 一次清空;输入=替换;方向键复位。
func TestSelectAllConsume(t *testing.T) {
	s := &State{}
	s.Input = "hello world"
	s.Cursor = 5
	s.SelectAll()
	s.Backspace()
	if s.Input != "" || s.Cursor != 0 {
		t.Fatalf("全选+Backspace 应清空,got %q cur=%d", s.Input, s.Cursor)
	}
	// 全选替换:输入字符 = 整段替换
	s.Input = "abc"
	s.Cursor = 1
	s.SelectAll()
	s.InsertRune('X')
	if s.Input != "X" || s.Cursor != 1 {
		t.Fatalf("全选+输入应替换为 X,got %q cur=%d", s.Input, s.Cursor)
	}
	// 空输入 SelectAll 不进入全选态(无内容可全选)
	s.Input = ""
	s.SelectAll()
	if s.selectAll {
		t.Fatal("空输入不应进入全选态")
	}
	// 方向键移动退出全选态
	s.Input = "abc"
	s.Cursor = 0
	s.SelectAll()
	if !s.selectAll {
		t.Fatal("应进入全选态")
	}
	s.CursorLeft()
	if s.selectAll {
		t.Fatal("光标移动应退出全选态")
	}
	// Delete 全选清空同样生效
	s.Input = "xyz"
	s.Cursor = 3
	s.SelectAll()
	s.Delete()
	if s.Input != "" {
		t.Fatalf("全选+Delete 应清空,got %q", s.Input)
	}
}

func histState() *State {
	s := &State{}
	s.Lines = append(s.Lines,
		Line{Kind: "user", Text: "第一轮问题"},
		Line{Kind: "assistant", Text: "第一轮回答"},
		Line{Kind: "user", Text: "第二轮问题"},
	)
	s.RecordCmd("/help")
	s.RecordCmd("/sandbox ro")
	return s
}

// TestHistPrevNextCycle 历史翻页:user 行 + 命令合并、P 逐条向上、N 反向、
// 越过最新一条还原草稿。
func TestHistPrevNextCycle(t *testing.T) {
	s := histState()
	// 历史 = [第一轮问题, 第二轮问题, /help, /sandbox ro](命令较新置后)
	s.Input = "草稿文本"
	s.Cursor = 4

	if !s.HistPrev() || s.Input != "/sandbox ro" {
		t.Fatalf("P1 应取最新命令: %q", s.Input)
	}
	if !s.HistPrev() || s.Input != "/help" {
		t.Fatalf("P2 应取 /help: %q", s.Input)
	}
	if !s.HistPrev() || s.Input != "第二轮问题" {
		t.Fatalf("P3 应取第二轮 user: %q", s.Input)
	}
	if !s.HistPrev() || s.Input != "第一轮问题" {
		t.Fatalf("P4 应取第一轮 user: %q", s.Input)
	}
	if s.HistPrev() {
		t.Fatal("已到最老不应再上翻")
	}
	if !s.HistNext() || s.Input != "第二轮问题" {
		t.Fatalf("N1 应回第二轮: %q", s.Input)
	}
	if !s.HistNext() || s.Input != "/help" {
		t.Fatalf("N2 应回 /help: %q", s.Input)
	}
	if !s.HistNext() || s.Input != "/sandbox ro" {
		t.Fatalf("N3 应回 /sandbox ro: %q", s.Input)
	}
	if !s.HistNext() || s.Input != "草稿文本" {
		t.Fatalf("越过最新应还原草稿: %q", s.Input)
	}
	if s.HistNext() {
		t.Fatal("草稿态不应再下翻")
	}
}

// TestHistEmptyAndDedup 无历史不动;相邻重复(user 连续同文本)去重。
func TestHistEmptyAndDedup(t *testing.T) {
	s := &State{}
	if s.HistPrev() || s.HistNext() {
		t.Fatal("无历史不应翻页")
	}
	s.Lines = append(s.Lines, Line{Kind: "user", Text: "重复"}, Line{Kind: "user", Text: "重复"})
	if !s.HistPrev() || s.Input != "重复" {
		t.Fatalf("应取到 user 行: %q", s.Input)
	}
	if s.HistPrev() {
		t.Fatal("去重后只有一条,不应再上翻")
	}
}

// TestRecordCmdOnlySlash RecordCmd 只收斜杠命令;普通消息不入 CmdHistory。
func TestRecordCmdOnlySlash(t *testing.T) {
	s := &State{}
	s.RecordCmd("/jobs list")
	s.RecordCmd("普通消息")
	if len(s.CmdHistory) != 1 || s.CmdHistory[0] != "/jobs list" {
		t.Fatalf("CmdHistory: %v", s.CmdHistory)
	}
}

// TestUndoCoalesceAndStack 连续同型编辑合并为一个 undo 步(800ms 窗口);
// 跨窗口后新编辑另起一步。
func TestUndoCoalesceAndStack(t *testing.T) {
	s := &State{}
	for _, r := range "abc" {
		s.InsertRune(r) // 同 800ms 内:合并一步
	}
	if s.Input != "abc" {
		t.Fatalf("输入 abc: %q", s.Input)
	}
	if !s.Undo() {
		t.Fatal("应有一步可撤销")
	}
	if s.Input != "" {
		t.Fatalf("合并撤销应一步回到空: %q", s.Input)
	}
	if s.Undo() {
		t.Fatal("合并后不应再有撤销步")
	}
	// 跨 800ms 窗口再编辑 → 新步
	time.Sleep(850 * time.Millisecond)
	s.InsertRune('x')
	s.InsertRune('y')
	if !s.Undo() || s.Input != "" {
		t.Fatalf("第二段应可撤销到空: %q", s.Input)
	}
}

func TestUndoRedoSequence(t *testing.T) {
	s := &State{}
	for _, r := range "hello" {
		s.InsertRune(r)
	}
	// 隔离:清栈后手工编辑
	s.undo, s.redo, s.lastEdit, s.lastKind = nil, nil, time.Time{}, 0
	s.Input = "hello"
	s.Cursor = 2
	s.Backspace() // hllo, cursor1
	if s.Input != "hllo" || s.Cursor != 1 {
		t.Fatalf("backspace 后 hllo cur1: %q c%d", s.Input, s.Cursor)
	}
	if !s.Undo() || s.Input != "hello" || s.Cursor != 2 {
		t.Fatalf("undo 恢复 hello cur2: %q c%d", s.Input, s.Cursor)
	}
	if !s.Redo() || s.Input != "hllo" || s.Cursor != 1 {
		t.Fatalf("redo 恢复 hllo cur1: %q c%d", s.Input, s.Cursor)
	}
	// 新编辑清空 redo
	s.InsertRune('i')
	if s.Redo() {
		t.Fatal("新编辑后 redo 应失效")
	}
	if !s.Undo() { // 撤掉插入 → 回 hllo
		t.Fatal("应可撤销插入")
	}
}

// TestKillEndAndStart Ctrl+K 删至行尾、Ctrl+U 删至行首,均可 undo 恢复。
func TestKillEndAndStart(t *testing.T) {
	s := &State{}
	for _, r := range "hello world" {
		s.InsertRune(r)
	}
	s.Cursor = 5 // "hello| world"
	if !s.KillToEnd() || s.Input != "hello" || s.Cursor != 5 {
		t.Fatalf("KillToEnd: %q c%d", s.Input, s.Cursor)
	}
	if !s.Undo() || s.Input != "hello world" || s.Cursor != 5 {
		t.Fatalf("undo kill: %q c%d", s.Input, s.Cursor)
	}
	if !s.KillToStart() || s.Input != " world" || s.Cursor != 0 {
		t.Fatalf("KillToStart: %q c%d", s.Input, s.Cursor)
	}
	if s.KillToStart() {
		t.Fatal("光标已到行首,不应再删除")
	}
	s.Cursor = len([]rune(s.Input))
	if s.KillToEnd() {
		t.Fatal("光标已到行尾,不应再删除")
	}
}

// TestYankKillBuf P5:kill-ring 单槽——Ctrl+K/U 删除记录,killBuf 循环覆盖,Alt+P 粘贴可撤销。
func TestYankKillBuf(t *testing.T) {
	s := &State{}
	for _, r := range "one two" {
		s.InsertRune(r)
	}
	// Ctrl+K 杀 "two"(光标 4 后)
	s.Cursor = 4
	if !s.KillToEnd() || s.killBuf != "two" || s.Input != "one " {
		t.Fatalf("kill to end 应记录 killBuf: %q buf %q", s.Input, s.killBuf)
	}
	// 光标回 0,Alt+P yank 粘贴
	s.Cursor = 0
	if !s.Yank() || s.Input != "twoone " || s.Cursor != 3 {
		t.Fatalf("yank 应粘贴到光标: %q c%d", s.Input, s.Cursor)
	}
	// yank 可撤销
	if !s.Undo() || s.Input != "one " || s.Cursor != 0 {
		t.Fatalf("undo yank: %q c%d", s.Input, s.Cursor)
	}
	// Ctrl+U 覆盖 killBuf(单槽最近):光标 2 杀 "on"
	s.Cursor = 2
	if !s.KillToStart() || s.killBuf != "on" || s.Input != "e " {
		t.Fatalf("kill to start 应覆盖 killBuf: %q input %q", s.killBuf, s.Input)
	}
	// 连续 kill 后 yank 用最新 buf
	s.Cursor = 0
	if !s.Yank() || s.Input != "one " || s.Cursor != 2 {
		t.Fatalf("yank 应用最近 killBuf: %q c%d", s.Input, s.Cursor)
	}
	// 空 buf no-op
	s2 := &State{}
	if s2.Yank() {
		t.Fatal("空 killBuf 不应 yank")
	}
}

// TestWordMove Alt+←/→ 按词移动。串 "go build 测试 完成":
// 0g 1o 2␣ 3b..7d 8␣ 9测 10试 11␣ 12完 13成。
func TestWordMove(t *testing.T) {
	s := &State{}
	for _, r := range "go build 测试 完成" {
		s.InsertRune(r)
	}
	// 右移:2(空格)→ 3(build 词首);词中 3 → 9(测试词首);9 → 12(完成词首)
	s.Cursor = 2
	s.WordRight()
	if s.Cursor != 3 {
		t.Fatalf("空格后右移应到下一词首: c%d", s.Cursor)
	}
	s.WordRight()
	if s.Cursor != 9 {
		t.Fatalf("词中右移应到下一词首(测试): c%d", s.Cursor)
	}
	s.WordRight()
	if s.Cursor != 12 {
		t.Fatalf("再右移应到 完成 词首: c%d", s.Cursor)
	}
	s.WordRight() // 行尾钳制
	if s.Cursor != 14 {
		t.Fatalf("行尾右移应停在行尾: c%d", s.Cursor)
	}
	// 左移:12(完成词首)→ 9(测试词首);8(空格)→ 3(build 词首);3 → 0(行首)
	s.Cursor = 12
	s.WordLeft()
	if s.Cursor != 9 {
		t.Fatalf("词首左移应到前词首(测试): c%d", s.Cursor)
	}
	s.Cursor = 8
	s.WordLeft()
	if s.Cursor != 3 {
		t.Fatalf("空格左移应到前词首(build): c%d", s.Cursor)
	}
	s.WordLeft()
	if s.Cursor != 0 {
		t.Fatalf("继续左移应回行首: c%d", s.Cursor)
	}
	s.WordLeft() // 行首钳制
	if s.Cursor != 0 {
		t.Fatal("行首再左移应钳制")
	}
	// 双空格跨空白:串 "a␣␣b":1(空格)右移 → 3(b 词首)
	s2 := &State{}
	for _, r := range "a  b" {
		s2.InsertRune(r)
	}
	s2.Cursor = 1
	s2.WordRight()
	if s2.Cursor != 3 {
		t.Fatalf("双空格后右移应到 b: c%d", s2.Cursor)
	}
}

// TestClearInputReset 提交/清空:undo/redo 清栈、历史指针复位。
func TestClearInputReset(t *testing.T) {
	s := &State{}
	s.RecordCmd("/help")
	s.InsertRune('x')
	s.HistPrev()
	if !s.histActive || len(s.undo) == 0 {
		t.Fatalf("前置态: histActive=%v undo=%d", s.histActive, len(s.undo))
	}
	s.ClearInput()
	if len(s.undo) != 0 || len(s.redo) != 0 || s.histActive || s.Input != "" {
		t.Fatalf("ClearInput 应清空栈与指针: undo=%d redo=%d histActive=%v", len(s.undo), len(s.redo), s.histActive)
	}
}

// TestHistNoActivatePicker 历史填入以 / 开头的命令后置 PickDismissed(不自动激活选择器)。
func TestHistNoActivatePicker(t *testing.T) {
	s := &State{}
	s.RecordCmd("/search")
	s.PickDismissed = false
	s.HistPrev()
	if s.Input != "/search" {
		t.Fatalf("应填入 /search: %q", s.Input)
	}
	if !s.PickDismissed {
		t.Fatal("填入 / 历史不应自动激活选择器(PickDismissed)")
	}
	if s.Cursor != len([]rune(s.Input)) {
		t.Fatal("填入后光标应到行尾")
	}
}
