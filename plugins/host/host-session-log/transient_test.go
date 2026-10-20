package sessionlog

// 瞬时广播(BroadcastTransient)的用例(2026-10-20 C1)。
//
// 要钉的是「广播但不落盘」的三条不变量:
//  1. 事件**广播**出去了(界面收得到);
//  2. 事件**没进日志**(不落盘、不进内存事件表 ⇒ 重放/投影/导出都看不见);
//  3. 事件**没占序号**(Seq 恒 0;下一条落盘事件的编号不受影响 ——
//     断线续传游标 Last-Event-ID 依赖序号连续)。

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestBroadcastTransientBroadcastsButDoesNotPersist 广播出去,但不进日志。
func TestBroadcastTransientBroadcastsButDoesNotPersist(t *testing.T) {
	c := &captureCtx{}
	lg := newLog("")
	lg.ctx = c

	if err := lg.BroadcastTransient(sdk.SessionEvent{
		Kind:    sdk.EventToolProgress,
		Payload: sdk.ToolProgressEvent{CallID: "call-1", Name: "shell", Chunk: "编译中", Seq: 3},
	}); err != nil {
		t.Fatal(err)
	}

	if len(c.got) != 1 {
		t.Fatalf("应广播 1 条,got %d", len(c.got))
	}
	got := c.got[0]
	if got.Kind != sdk.EventToolProgress {
		t.Fatalf("广播的 Kind 应为 tool/progress,got %q", got.Kind)
	}
	if got.Seq != 0 {
		t.Fatalf("瞬时事件不占序号,Seq 应为 0,got %d", got.Seq)
	}
	if got.TS.IsZero() {
		t.Fatal("瞬时事件应带时间戳(界面按时间显示)")
	}
	if p, ok := got.Payload.(sdk.ToolProgressEvent); !ok || p.Chunk != "编译中" {
		t.Fatalf("载荷应原样广播,got %#v", got.Payload)
	}

	// 不进内存事件表(重放/投影看不见)。
	for _, ev := range lg.Replay() {
		if ev.Kind == sdk.EventToolProgress {
			t.Fatal("瞬时事件不应进事件表")
		}
	}
}

// TestBroadcastTransientDoesNotConsumeSeq 瞬时事件不占序号:下一条落盘事件仍是 Seq 1。
func TestBroadcastTransientDoesNotConsumeSeq(t *testing.T) {
	c := &captureCtx{}
	lg := newLog("")
	lg.ctx = c

	for i := 0; i < 3; i++ {
		if err := lg.BroadcastTransient(sdk.SessionEvent{Kind: sdk.EventToolProgress}); err != nil {
			t.Fatal(err)
		}
	}
	if err := lg.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	replay := lg.Replay()
	if len(replay) != 1 {
		t.Fatalf("日志里只应有 1 条(瞬时的不算),got %d", len(replay))
	}
	if replay[0].Seq != 1 {
		t.Fatalf("落盘事件序号应从 1 起(瞬时事件不占号),got %d", replay[0].Seq)
	}
}

// TestBroadcastTransientNotWrittenToFile 落盘路径下,瞬时事件也不进文件。
func TestBroadcastTransientNotWrittenToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	c := &captureCtx{}
	lg := newLog(path)
	lg.ctx = c

	if err := lg.BroadcastTransient(sdk.SessionEvent{
		Kind:    sdk.EventToolProgress,
		Payload: sdk.ToolProgressEvent{Chunk: "临时增量"},
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return // 文件尚未创建 = 没落盘 ✓
	}
	if strings.Contains(string(raw), "临时增量") {
		t.Fatalf("瞬时事件不应落盘,文件内容:%s", raw)
	}
}

// TestBroadcastTransientOnNamedSession 带会话 id 的实例:广播走**该会话**的事件名。
func TestBroadcastTransientOnNamedSession(t *testing.T) {
	names := []string{}
	c := &captureCtxFunc{fn: func(name string) { names = append(names, name) }}
	lg := newLog("")
	lg.ctx = c
	lg.id = "sess-42"

	if err := lg.BroadcastTransient(sdk.SessionEvent{Kind: sdk.EventToolProgress}); err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != sdk.SessionEventName("sess-42") {
		t.Fatalf("应广播到该会话的事件名 %q,got %v", sdk.SessionEventName("sess-42"), names)
	}
}

// captureCtxFunc 只记录事件名(不关心载荷)。
type captureCtxFunc struct{ fn func(name string) }

func (c *captureCtxFunc) Provide(string, any) error { return nil }
func (c *captureCtxFunc) Inject(string, any) error  { return nil }
func (c *captureCtxFunc) Subscribe(string, sdk.AnyListener) sdk.Disposer {
	return func() {}
}
func (c *captureCtxFunc) Emit(_ context.Context, name string, _ any, _ sdk.DispatchMode) (any, error) {
	c.fn(name)
	return nil, nil
}
func (c *captureCtxFunc) Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }
