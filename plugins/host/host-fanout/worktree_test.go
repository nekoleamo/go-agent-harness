// worktree_test.go:S-P1-4 隔离运行的 fanout 侧契约(工作根下传/句柄带 worktree/失败显式报错)。
package hostfanout

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/adapter/llm-mock"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-llm"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// probeTool 记录每次调用看到的工作根(替代 shell:不依赖真实 git/文件系统)。
type probeTool struct {
	mu   sync.Mutex
	dirs []string
}

func (p *probeTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "probe", Description: "测试用探针工具",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}
}

func (p *probeTool) Execute(c context.Context, _ string) (any, error) {
	dir, _ := sdk.WorkRootOf(c)
	p.mu.Lock()
	p.dirs = append(p.dirs, dir)
	p.mu.Unlock()
	return map[string]any{"ok": true}, nil
}

func (p *probeTool) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.dirs...)
}

// stubWorktrees 替身:不碰真实 git,只按序发号(隔离语义由 host-worktrees 单测覆盖)。
type stubWorktrees struct {
	mu   sync.Mutex
	n    int
	base string
	err  error // 非空 = Create 显式失败(模拟非 git 仓库)
}

func (s *stubWorktrees) Create(_ context.Context, _ string) (sdk.Worktree, error) {
	if s.err != nil {
		return sdk.Worktree{}, s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	id := "wt" + string(rune('0'+s.n))
	return sdk.Worktree{ID: id, Path: s.base + "/" + id, Repo: s.base, Branch: "gah/" + id, Base: "deadbeef"}, nil
}
func (s *stubWorktrees) List() []sdk.Worktree { return nil }
func (s *stubWorktrees) Remove(string, bool) error {
	return nil
}

// isoEnv 隔离运行测试环境。
type isoEnv struct {
	svc   sdk.FanoutService
	iso   sdk.IsolatedFanout
	probe *probeTool
}

// buildIsoEnv 装配隔离运行环境(probe 工具 + mock llm 两轮:调 probe → 文本收尾)。
func buildIsoEnv(t *testing.T, wts sdk.WorktreeService) isoEnv {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	probe := &probeTool{}
	tools.Register(probe)
	if wts != nil {
		if err := c.Provide("ctx.worktrees", wts); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&hostllm.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&llmmock.Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{
		"script": `[{"tool":{"name":"probe","args":"{}"}},{"text":"完成","finish":"stop"}]`,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var svc sdk.FanoutService
	if err := c.Inject("ctx.fanout", &svc); err != nil {
		t.Fatal(err)
	}
	iso, ok := svc.(sdk.IsolatedFanout)
	if !ok {
		t.Fatal("Fanout 应实现 sdk.IsolatedFanout")
	}
	return isoEnv{svc: svc, iso: iso, probe: probe}
}

// TestRunInWorktreeSync 同步隔离运行:工具调用看到 worktree 工作根,结果回传路径/分支。
func TestRunInWorktreeSync(t *testing.T) {
	env := buildIsoEnv(t, &stubWorktrees{base: t.TempDir()})
	res, err := env.iso.RunInWorktree(context.Background(), sdk.WorktreeRun{Input: "改一下文件", Sync: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Worktree.Path == "" || res.Worktree.Branch == "" {
		t.Fatalf("结果必须含 worktree 路径与分支: %+v", res.Worktree)
	}
	dirs := env.probe.seen()
	if len(dirs) == 0 {
		t.Fatal("子代理应至少调用一次工具")
	}
	for _, d := range dirs {
		if d != res.Worktree.Path {
			t.Fatalf("工具调用的工作根应为 worktree: got %q want %q", d, res.Worktree.Path)
		}
	}
	if !strings.Contains(res.Text, "完成") {
		t.Fatalf("应回传子代理文本: %q", res.Text)
	}
}

// TestRunInWorktreeBackground 后台隔离运行:句柄带 worktree(父级可直接回报路径)。
func TestRunInWorktreeBackground(t *testing.T) {
	env := buildIsoEnv(t, &stubWorktrees{base: t.TempDir()})
	res, err := env.iso.RunInWorktree(context.Background(), sdk.WorktreeRun{Input: "后台任务"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Handle.ID == "" || res.Handle.Worktree == nil {
		t.Fatalf("后台句柄必须带 id 与 worktree: %+v", res.Handle)
	}
	if res.Handle.Worktree.Path != res.Worktree.Path {
		t.Fatalf("句柄与结果 worktree 应一致: %+v vs %+v", res.Handle.Worktree, res.Worktree)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		h, ok := env.svc.AgentStatus(res.Handle.ID)
		if ok && h.State != sdk.AgentRunning {
			if h.State != sdk.AgentDone {
				t.Fatalf("子代理应完成: %+v", h)
			}
			if h.Worktree == nil || h.Worktree.Path != res.Worktree.Path {
				t.Fatalf("句柄状态应保留 worktree: %+v", h)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("等待子代理完成超时")
		}
		time.Sleep(2 * time.Millisecond)
	}
	for _, d := range env.probe.seen() {
		if d != res.Worktree.Path {
			t.Fatalf("后台子代理工具工作根应为 worktree: %q", d)
		}
	}
}

// TestWorktreeNote 目录说明必须含路径/分支/基线(子代理不知道就会写错地方)。
func TestWorktreeNote(t *testing.T) {
	note := worktreeNote(sdk.Worktree{Path: "/data/wt/1", Branch: "gah/1", Base: "0123456789abcdef"})
	for _, want := range []string{"/data/wt/1", "gah/1", "01234567", "隔离运行"} {
		if !strings.Contains(note, want) {
			t.Fatalf("说明应含 %q: %s", want, note)
		}
	}
	if !strings.HasSuffix(note, "\n\n") {
		t.Fatalf("说明后应空行分隔任务: %q", note)
	}
}

// TestRunInWorktreeNonGitExplicit 建 worktree 失败(非 git 仓库)= 显式错误,不静默退化。
func TestRunInWorktreeNonGitExplicit(t *testing.T) {
	env := buildIsoEnv(t, &stubWorktrees{base: t.TempDir(), err: errors.New("工作目录不是 git 仓库(/tmp/x)")})
	_, err := env.iso.RunInWorktree(context.Background(), sdk.WorktreeRun{Input: "x", Sync: true})
	if err == nil || !strings.Contains(err.Error(), "不是 git 仓库") {
		t.Fatalf("应显式回错: %v", err)
	}
	if len(env.probe.seen()) != 0 {
		t.Fatalf("建 worktree 失败后不得运行子代理: %v", env.probe.seen())
	}
}

// TestRunInWorktreeMissingService 未装配 ctx.worktrees:显式报错(不静默退回非隔离)。
func TestRunInWorktreeMissingService(t *testing.T) {
	env := buildIsoEnv(t, nil)
	_, err := env.iso.RunInWorktree(context.Background(), sdk.WorktreeRun{Input: "x", Sync: true})
	if err == nil || !strings.Contains(err.Error(), "ctx.worktrees") {
		t.Fatalf("应提示 ctx.worktrees 未装配: %v", err)
	}
	// 空任务 = 显式报错(不进 worktree 创建)
	if _, err := env.iso.RunInWorktree(context.Background(), sdk.WorktreeRun{Input: "  "}); err == nil {
		t.Fatal("空任务应报错")
	}
}
