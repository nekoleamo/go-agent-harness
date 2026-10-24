package hostcwdsessions

// C4 写侧:从分支切回祖先会话时,把分支的一句话注入目标会话;只注一次,反方向不注。

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// branchNotes 从 fake 会话日志里挑出分支备注事件。
func branchNotes(evs []sdk.SessionEvent) []sdk.BranchNote {
	var out []sdk.BranchNote
	for _, ev := range evs {
		if ev.Kind != sdk.EventBranchNote {
			continue
		}
		if n, ok := branchNotePayload(ev.Payload); ok {
			out = append(out, n)
		}
	}
	return out
}

func TestBranchNoteInjectedOnReturnToAncestor(t *testing.T) {
	svc, fs := forkSetup(t)
	// 从主会话 fork 出一条分支(当前会话 = 分支)
	id, err := svc.ForkAt(3)
	if err != nil {
		t.Fatal(err)
	}
	// 分支有概述缓存(真实路径:host-session-summary 写 meta.json)
	if err := svc.SetSummary(id, sdk.SessionSummary{Text: "重构导出逻辑:改成流式写入"}); err != nil {
		t.Fatal(err)
	}
	// 切回主会话 ⇒ 注入一条 branch/note
	if err := svc.Open(""); err != nil {
		t.Fatal(err)
	}
	notes := branchNotes(fs.Replay())
	if len(notes) != 1 {
		t.Fatalf("切回主会话应注入 1 条分支备注,得 %d: %+v", len(notes), notes)
	}
	if notes[0].From != id || notes[0].Text != "重构导出逻辑:改成流式写入" {
		t.Fatalf("备注内容不对: %+v", notes[0])
	}
	if notes[0].FromName == "" {
		t.Fatalf("应带分支显示名(展示端不该反查): %+v", notes[0])
	}

	// 再切出去、再切回来:同一句话不重复注入(否则上下文里排满同一段话)
	if err := svc.Open(id); err != nil {
		t.Fatal(err)
	}
	if err := svc.Open(""); err != nil {
		t.Fatal(err)
	}
	if got := len(branchNotes(fs.Replay())); got != 1 {
		t.Fatalf("重复切回不应再注入(得 %d 条)", got)
	}
}

// TestBranchNoteNotInjectedTowardDescendant 反方向(父 → 子)不注入:
// 子会话本来就继承着父的历史,再补一句是噪音。
func TestBranchNoteNotInjectedTowardDescendant(t *testing.T) {
	svc, fs := forkSetup(t)
	id, err := svc.ForkAt(3)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Open(""); err != nil {
		t.Fatal(err)
	}
	before := len(branchNotes(fs.Replay()))
	if err := svc.Open(id); err != nil { // 父 → 子
		t.Fatal(err)
	}
	if got := len(branchNotes(fs.Replay())); got != before {
		t.Fatalf("父 → 子 方向不应注入(得 %d 条,之前 %d 条)", got, before)
	}
}

// TestBranchNoteNotInjectedWithoutForkRecord 无关会话之间不注入
// (没有派生记录 = 不是从这条会话分出去的,手工拷来的文件同理)。
func TestBranchNoteNotInjectedWithoutForkRecord(t *testing.T) {
	svc, fs := forkSetup(t)
	// 造一个"会话文件存在但不是本会话的分支"的场景:直接切过去,树里没有记录
	if err := svc.Open("20260101-000000"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Open(""); err != nil {
		t.Fatal(err)
	}
	if got := len(branchNotes(fs.Replay())); got != 0 {
		t.Fatalf("无派生记录的会话不应注入(得 %d 条)", got)
	}
}

// TestBranchNoteFallsBackToPreview 概述缺失时退回首条用户消息(零模型调用)。
func TestBranchNoteFallsBackToPreview(t *testing.T) {
	svc, fs := forkSetup(t)
	if _, err := svc.ForkAt(3); err != nil {
		t.Fatal(err)
	}
	// 不做 SetSummary:分支文件里已有继承来的用户消息 ⇒ 预览非空
	if err := svc.Open(""); err != nil {
		t.Fatal(err)
	}
	notes := branchNotes(fs.Replay())
	if len(notes) != 1 {
		t.Fatalf("应注入 1 条(退回首条提问),得 %d", len(notes))
	}
	if notes[0].Text == "" {
		t.Fatalf("退回到预览也不该是空文本: %+v", notes[0])
	}
}

// oneLine 是注入口径的守门人:一行、压空白、超长截断、全空 = 空串(不注入)。
func TestOneLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"\n\n  \n", ""},
		{"\n  第一行  \n第二行", "第一行"},
		{"  多余   空白\t混排  ", "多余 空白 混排"},
	}
	for _, c := range cases {
		if got := oneLine(c.in); got != c.want {
			t.Fatalf("oneLine(%q)=%q,想要 %q", c.in, got, c.want)
		}
	}
	long := strings.Repeat("字", branchNoteMaxRunes+20)
	got := oneLine(long)
	if r := []rune(got); len(r) != branchNoteMaxRunes+1 || r[len(r)-1] != '…' {
		t.Fatalf("超长应截断到 %d 字 + 省略号,得 %d 字", branchNoteMaxRunes, len(r))
	}
}

// branchNotePayload 兼容未还原的 map;认不出的类型返回 ok=false。
func TestBranchNotePayloadShapes(t *testing.T) {
	if n, ok := branchNotePayload(map[string]any{"from": "b2", "text": "x"}); !ok || n.From != "b2" || n.Text != "x" {
		t.Fatalf("map 载荷应识别: ok=%v %+v", ok, n)
	}
	if _, ok := branchNotePayload(42); ok {
		t.Fatal("非备注载荷应报 ok=false")
	}
}

// 深层派生链(fork 的 fork)切回根:仍要注入,显示名取该分支自己的。
func TestBranchNoteDeepChain(t *testing.T) {
	svc, fs := forkSetup(t)
	first, err := svc.ForkAt(3)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.ForkAt(3) // 从 first 再派生一层
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSummary(second, sdk.SessionSummary{Text: "第二层分支的工作"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Open(""); err != nil { // 直接切回根
		t.Fatal(err)
	}
	notes := branchNotes(fs.Replay())
	if len(notes) != 1 || notes[0].From != second {
		t.Fatalf("深层分支切回根应注入 1 条(来自第二层),得 %+v", notes)
	}
	_ = first
}

// 注入失败不能卡住会话切换(会话已经切过去了):只报一行日志。
func TestBranchNoteInjectFailureDoesNotBlockSwitch(t *testing.T) {
	svc, fs := forkSetup(t)
	if _, err := svc.ForkAt(3); err != nil {
		t.Fatal(err)
	}
	failSessions := &failingAppend{forkSessions: fs}
	svc.sessions = failSessions
	svc.logger = slog.New(slog.DiscardHandler)
	if err := svc.Open(""); err != nil {
		t.Fatalf("备注写入失败不应阻断切换: %v", err)
	}
	if svc.CurrentSession() != "" {
		t.Fatalf("应已切回主会话,得 %q", svc.CurrentSession())
	}
}

// failingAppend 让分支备注的写入失败(其余照旧)。
type failingAppend struct{ *forkSessions }

func (f *failingAppend) Append(ev sdk.SessionEvent) error {
	if ev.Kind == sdk.EventBranchNote {
		return errors.New("磁盘满")
	}
	return f.forkSessions.Append(ev)
}
