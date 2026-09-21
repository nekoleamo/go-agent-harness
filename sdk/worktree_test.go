// worktree_test.go:sdk 侧 worktree 隔离契约的纯逻辑护栏(工作根覆盖 + 隔离运行类型)。
package sdk

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorkRootCarrier(t *testing.T) {
	ctx := context.Background()
	if _, ok := WorkRootOf(ctx); ok {
		t.Fatal("未设置时不应有工作根")
	}
	//lint:ignore SA1012 有意传 nil:WorkRootOf 必须对 nil ctx 容错(不能 panic)
	if _, ok := WorkRootOf(nil); ok {
		t.Fatal("nil ctx 不应有工作根")
	}
	// 空 dir = 未隔离:不打标记(避免空工作根被误当成有效根)
	if got := WithWorkRoot(ctx, ""); got != ctx {
		t.Fatal("空工作根应原样返回 ctx")
	}
	ctx2 := WithWorkRoot(ctx, "/tmp/wt1")
	dir, ok := WorkRootOf(ctx2)
	if !ok || dir != "/tmp/wt1" {
		t.Fatalf("工作根读取不符: %q %v", dir, ok)
	}
	// 叠加 = 末次生效(嵌套隔离运行时内层覆盖外层)
	if d, _ := WorkRootOf(WithWorkRoot(ctx2, "/tmp/wt2")); d != "/tmp/wt2" {
		t.Fatalf("末次工作根应生效,得 %q", d)
	}
	// 上下文值不污染父 ctx
	if _, ok := WorkRootOf(ctx); ok {
		t.Fatal("派生不应回写父 ctx")
	}
}

func TestWorktreeRunJSONRoundTrip(t *testing.T) {
	wt := Worktree{ID: "wt1", Path: "/data/worktrees/repo-wt1", Repo: "/repo",
		Branch: "gah/wt1", Base: "abc123", CreatedAt: time.Unix(1000, 0).UTC()}
	b, err := json.Marshal(WorktreeRunResult{Worktree: wt, Text: "结论",
		Handle: AgentHandle{ID: "ag1", Worktree: &wt}})
	if err != nil {
		t.Fatal(err)
	}
	var back WorktreeRunResult
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Worktree.Path != wt.Path || back.Text != "结论" || back.Handle.Worktree == nil {
		t.Fatalf("往返不符: %+v", back)
	}
	// 非隔离句柄不带 worktree 字段(协议面保持干净)
	b2, _ := json.Marshal(AgentHandle{ID: "ag2"})
	if strings.Contains(string(b2), "worktree") {
		t.Fatalf("非隔离句柄不应带 worktree: %s", b2)
	}
}
