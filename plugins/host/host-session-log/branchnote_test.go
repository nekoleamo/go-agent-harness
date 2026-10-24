package sessionlog

// C4 写侧:分支备注(branch/note)必须进**模型上下文**(一条 system 说明),
// 且重启/切会话后仍在 —— 否则"主线记得那次尝试"只在本次进程内成立。

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func branchNoteMsg(msgs []sdk.LLMMessage) (sdk.LLMMessage, bool) {
	for _, m := range msgs {
		if m.Role == sdk.RoleSystem && strings.Contains(m.Content, "[分支说明]") {
			return m, true
		}
	}
	return sdk.LLMMessage{}, false
}

func TestBranchNoteProjectedAsSystem(t *testing.T) {
	l := newTestLog(t, "")
	if err := l.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "主线问题"}}); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(sdk.SessionEvent{Kind: sdk.EventBranchNote, Payload: sdk.BranchNote{
		From: "20261024-101500", FromName: "fork@3 ← 主", Text: "把导出改成流式",
	}}); err != nil {
		t.Fatal(err)
	}
	msgs := l.DeriveMessages()
	m, ok := branchNoteMsg(msgs)
	if !ok {
		t.Fatalf("分支备注应进模型上下文(投影里要有 system 说明): %+v", msgs)
	}
	if !strings.Contains(m.Content, "fork@3 ← 主") || !strings.Contains(m.Content, "流式") {
		t.Fatalf("说明里要有分支名与那句话: %q", m.Content)
	}
	// 边界要写明:分支历史不在本会话(否则模型会把别人的对话当自己的记忆)
	if !strings.Contains(m.Content, "不在本会话") {
		t.Fatalf("必须写清“该分支对话不在本会话”: %q", m.Content)
	}
	// 备注不是用户消息:不能混成 user 角色(否则模型以为用户刚说了这句)
	for _, mm := range msgs {
		if mm.Role == sdk.RoleUser && strings.Contains(mm.Content, "[分支说明]") {
			t.Fatal("分支备注必须是 system,不能伪造成用户消息")
		}
	}
}

func TestBranchNoteSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	l := newLog(dir)
	l.SetPath(path)
	if err := l.Append(sdk.SessionEvent{Kind: sdk.EventBranchNote, Payload: sdk.BranchNote{
		From: "b1", FromName: "分支一", Text: "试了 A 方案,失败",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}

	l2 := newLog(dir)
	t.Cleanup(l2.Close)
	l2.SetPath(path)
	if err := l2.Load(path); err != nil {
		t.Fatal(err)
	}
	// 反向验证:internal/sessionevents 不还原 BranchNote ⇒ 载荷停在 map ⇒ 本用例红
	// (投影本身有 map 兜底,所以必须额外钉类型 —— 否则“还原”这件事没有任何用例看得见)
	var typed bool
	for _, ev := range l2.Replay() {
		if ev.Kind != sdk.EventBranchNote {
			continue
		}
		if _, ok := ev.Payload.(sdk.BranchNote); ok {
			typed = true
		}
	}
	if !typed {
		t.Fatalf("Load 后载荷应还原为 sdk.BranchNote(得 %T)", payloadOf(l2.Replay(), sdk.EventBranchNote))
	}
	if m, ok := branchNoteMsg(l2.DeriveMessages()); !ok {
		t.Fatal("重启后分支备注仍应进模型上下文(载荷必须还原为具体类型)")
	} else if !strings.Contains(m.Content, "A 方案") {
		t.Fatalf("文本丢了: %q", m.Content)
	}
}

// 没有文本的空备注不产生空消息(防御:假装说了什么比不说更坏)。
func TestBranchNoteEmptyTextIgnored(t *testing.T) {
	l := newTestLog(t, "")
	if err := l.Append(sdk.SessionEvent{Kind: sdk.EventBranchNote, Payload: sdk.BranchNote{From: "b1"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := branchNoteMsg(l.DeriveMessages()); ok {
		t.Fatal("空文本的备注不应进投影")
	}
}

// payloadOf 取某类事件的第一个载荷(断言类型用)。
func payloadOf(evs []sdk.SessionEvent, kind string) any {
	for _, ev := range evs {
		if ev.Kind == kind {
			return ev.Payload
		}
	}
	return nil
}

// 载荷没被还原成具体类型(旧日志 / 外部写入的 map)也必须进投影:
// 认不出类型就静默丢一条本该给模型的说明 —— 那正是这条护栏要防的。
func TestBranchNoteMapPayloadStillProjected(t *testing.T) {
	l := newTestLog(t, "")
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventBranchNote, Payload: map[string]any{
		"from": "b9", "from_name": "分支九", "text": "换了缓存实现",
	}})
	m, ok := branchNoteMsg(l.DeriveMessages())
	if !ok || !strings.Contains(m.Content, "分支九") || !strings.Contains(m.Content, "缓存") {
		t.Fatalf("map 载荷也应投影出来: ok=%v %+v", ok, m)
	}
}

// 认不出的载荷(这里是字符串)不产生消息,也不 panic。
func TestBranchNoteUnknownPayloadIgnored(t *testing.T) {
	l := newTestLog(t, "")
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventBranchNote, Payload: "just-a-string"})
	if _, ok := branchNoteMsg(l.DeriveMessages()); ok {
		t.Fatal("无法识别的载荷不应产生投影消息")
	}
}

// FromName 缺失时用 id 顶上(展示端不该显示空名字)。
func TestBranchNoteTextFallsBackToID(t *testing.T) {
	got := branchNoteText(sdk.BranchNote{From: "20261024-093000", Text: "试了新方案"})
	if !strings.Contains(got, "20261024-093000") {
		t.Fatalf("没有显示名时应退回 id: %q", got)
	}
}

// 切会话必须清掉上一个会话的**应急状态**(2026-10-24 review 的 P1):
// emergencyTrim 只在"下一条 usage 事件"里被清 ⇒ 不清就粘到新会话,新会话第一次投影
// 会按旧会话的应急阈值截断最旧工具结果(静默丢内容);autoSummary 会让 HasAutoSummary 说谎。
//
// **反向验证**:删掉 Load 里 `l.emergencyTrim = 0` / `l.autoSummary = false` ⇒ 本用例红。
func TestLoadResetsEmergencyState(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.jsonl")
	b := filepath.Join(dir, "b.jsonl")
	l := newLog(dir)
	l.SetPath(a)
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: strings.Repeat("问", 400)}})
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "c1", Name: "shell", Content: strings.Repeat("o", 800)}})
	// 人为置上应急状态(等价于刚刚溢出过一次)
	l.emergencyTrim = 120
	l.autoSummary = true

	// 切到另一个空会话
	if err := l.Load(b); err != nil {
		t.Fatal(err)
	}
	if l.emergencyTrim != 0 {
		t.Fatalf("切会话后应急截断必须清零,得 %d(否则新会话首投影被旧阈值截断)", l.emergencyTrim)
	}
	if l.HasAutoSummary() {
		t.Fatal("切会话后 HasAutoSummary 应为 false")
	}
	// 再切回去:旧会话自己的内容不受影响(未被应急截断改写)
	if err := l.Load(a); err != nil {
		t.Fatal(err)
	}
	for _, m := range l.DeriveMessages() {
		if strings.Contains(m.Content, "[截断") {
			t.Fatalf("旧会话内容不应被截断标记改写: %q", m.Content)
		}
	}
}
