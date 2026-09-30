package sessionlog

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 造一个能 Emit 的最小 ctx(只用于验证事件名;不做其它注入)。
type nopCtx struct{ sdk.Ctx }

func (nopCtx) Emit(_ context.Context, _ string, _ any, _ sdk.DispatchMode) (any, error) {
	return nil, nil
}

// 记录广播到的事件名(验证「每个会话一个事件流」)。
type recCtx struct {
	sdk.Ctx
	mu    sync.Mutex
	names []string
}

func (r *recCtx) Emit(_ context.Context, name string, _ any, _ sdk.DispatchMode) (any, error) {
	r.mu.Lock()
	r.names = append(r.names, name)
	r.mu.Unlock()
	return nil, nil
}

func (r *recCtx) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.names...)
}

func TestSessionEventNamePerStream(t *testing.T) {
	rc := &recCtx{}
	r := NewLogs(rc)
	dir := t.TempDir()
	la, err := r.Acquire(filepath.Join(dir, "a.jsonl"), "20260930-1")
	if err != nil {
		t.Fatal(err)
	}
	_ = la.Append(sdk.SessionEvent{Kind: sdk.EventTurnStart})
	lb, _ := r.Acquire(filepath.Join(dir, "b.jsonl"), "20260930-2")
	_ = lb.Append(sdk.SessionEvent{Kind: sdk.EventTurnStart})
	seen := rc.seen()
	if len(seen) != 2 {
		t.Fatalf("应广播 2 次,got %v", seen)
	}
	if seen[0] != "session/event/20260930-1" || seen[1] != "session/event/20260930-2" {
		t.Fatalf("事件名应带会话 id,got %v", seen)
	}
	// 主会话(id 空)仍是 session/event(既有订阅方零改动)。
	if got := sdk.SessionEventName(""); got != sdk.EventSession {
		t.Fatalf("id 空应回落 EventSession,got %q", got)
	}
}

func TestLogsAcquireSamePathReusesInstance(t *testing.T) {
	r := NewLogs(nopCtx{})
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	a, err := r.Acquire(p, "s1")
	if err != nil {
		t.Fatalf("首次 Acquire: %v", err)
	}
	b, err := r.Acquire(p, "s1")
	if err != nil {
		t.Fatalf("二次 Acquire: %v", err)
	}
	if a != b {
		t.Fatalf("同一路径应复用同一实例,got %p vs %p", a, b)
	}
	if got := r.Active(); len(got) != 1 || got[0] != p {
		t.Fatalf("Active 应含该路径,got %v", got)
	}
	// 引用归零才移出表:第二次 Acquire 后 refs=2,Release 一次仍在。
	r.Release(p)
	if len(r.Active()) != 1 {
		t.Fatalf("refs 未归零时不应移出表,got %v", r.Active())
	}
	r.Release(p)
	if len(r.Active()) != 0 {
		t.Fatalf("refs 归零后应移出表,got %v", r.Active())
	}
	// Release 未持有的路径 = no-op(幂等)。
	r.Release(p)
}

func TestLogsInstancesAreIndependent(t *testing.T) {
	r := NewLogs(nopCtx{})
	dir := t.TempDir()
	pa := filepath.Join(dir, "a.jsonl")
	pb := filepath.Join(dir, "b.jsonl")
	la, _ := r.Acquire(pa, "a1")
	lb, _ := r.Acquire(pb, "b1")
	if la == lb {
		t.Fatal("不同路径应是不同实例")
	}
	if err := la.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: map[string]string{"text": "A"}}); err != nil {
		t.Fatalf("append a: %v", err)
	}
	if err := lb.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: map[string]string{"text": "B"}}); err != nil {
		t.Fatalf("append b: %v", err)
	}
	// 各自只看到自己的事件(并行不串)。
	if n := len(la.Replay()); n != 1 {
		t.Fatalf("实例 a 应只有 1 条,got %d", n)
	}
	if n := len(lb.Replay()); n != 1 {
		t.Fatalf("实例 b 应只有 1 条,got %d", n)
	}
	// 各自 seq 独立从 1 起(不是共享计数器)。
	if evs := la.Replay(); len(evs) == 1 && evs[0].Seq != 1 {
		t.Fatalf("实例 a 的 seq 应从 1 起,got %d", evs[0].Seq)
	}
}

func TestLogsAcquireLoadsExistingHistory(t *testing.T) {
	r := NewLogs(nopCtx{})
	dir := t.TempDir()
	p := filepath.Join(dir, "h.jsonl")
	// 先写一条,再 Acquire —— 应读到历史(不是空会话)。
	if err := os.WriteFile(p, []byte(`{"kind":"user/message","seq":7,"ts":"2026-09-30T10:00:00Z","payload":{"text":"旧"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lg, err := r.Acquire(p, "h1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	evs := lg.Replay()
	if len(evs) != 1 {
		t.Fatalf("应恢复 1 条历史,got %d", len(evs))
	}
	// seq 接续:新追加应为 8。
	if err := lg.Append(sdk.SessionEvent{Kind: sdk.EventTurnEnd}); err != nil {
		t.Fatal(err)
	}
	evs = lg.Replay()
	if got := evs[len(evs)-1].Seq; got != 8 {
		t.Fatalf("seq 应接续为 8,got %d", got)
	}
}

func TestLogsAcquireEmptyPathErrors(t *testing.T) {
	r := NewLogs(nopCtx{})
	if _, err := r.Acquire("", "x"); err == nil {
		t.Fatal("空路径应显式报错(不静默回落)")
	}
}

func TestLogsConcurrentAcquireSamePath(t *testing.T) {
	r := NewLogs(nopCtx{})
	p := filepath.Join(t.TempDir(), "c.jsonl")
	var wg sync.WaitGroup
	got := make([]sdk.SessionLog, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lg, err := r.Acquire(p, "c1")
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			got[i] = lg
		}(i)
	}
	wg.Wait()
	for i := 1; i < len(got); i++ {
		if got[i] != got[0] {
			t.Fatal("并发 Acquire 同路径应返回同一实例")
		}
	}
}
