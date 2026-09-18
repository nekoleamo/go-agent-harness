// worktree_isolation_e2e_test.go:S-P1-4 端到端验收(隔离运行真的隔离)。
//
// 装配真实链路:policy-guard(路径裁决)→ host-tools(注入工作根)→ host-worktrees(受管 worktree)
// → host-fanout(隔离运行)→ tool-subagent(isolate=worktree),用**内容驱动**的假适配器
// (不依赖 llm-mock 的全局请求序号:并发下每个子代理各自可判定)让两个子代理写同名相对路径。
//
// 不变量:
//  1. 两个隔离子代理写同一个相对路径 → 各自落各自 worktree,互不覆盖;
//  2. 主工作区**没有**该文件(隔离不是建议性的:相对写与绝对写都进不了主工作区);
//  3. 子代理返回内容含 worktree 路径(父级据此合并/回收);
//  4. 非 git 仓库 → 显式报错,不静默退化为非隔离。
package tests

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/plugins/catalogue"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// markAdapter 内容驱动假适配器:按「任务里的标记」决定写什么,不看请求序号
// (并发子代理共用适配器时仍然各自可判定)。
type markAdapter struct {
	mu   sync.Mutex
	seen []string // 每次请求的最后一条消息(调试用)
}

func (a *markAdapter) Name() string { return "mark" }

func lastContent(req *sdk.LLMRequest) (string, sdk.Role) { // (最后一条消息内容, 角色)
	if len(req.Messages) == 0 {
		return "", ""
	}
	m := req.Messages[len(req.Messages)-1]
	return m.Content, m.Role
}

func (a *markAdapter) Complete(_ context.Context, req *sdk.LLMRequest, onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	content, role := lastContent(req)
	a.mu.Lock()
	a.seen = append(a.seen, string(role)+":"+content)
	a.mu.Unlock()
	if role == sdk.RoleTool { // 工具已执行 → 收尾
		return emitText(onChunk, "已写入 shared.txt")
	}
	// 首轮:按任务标记决定写入内容,写**相对路径**(基准必须是本次调用的工作根)
	mark := "A"
	if strings.Contains(content, "标记B") {
		mark = "B"
	}
	args := `{"path":"shared.txt","content":"` + mark + `"}`
	return emitCall(onChunk, "file_write", args)
}

// emitCall 发一次工具调用事件(done=true)。
func emitCall(onChunk func(ev sdk.LLMStreamEvent) error, name, args string) (*sdk.LLMResponse, error) {
	id := "call_" + name
	call := sdk.ToolCall{ID: id, Name: name, Arguments: args}
	if err := onChunk(sdk.LLMStreamEvent{ToolCallID: id, ToolCallName: name, ToolCallArgs: args}); err != nil {
		return nil, err
	}
	done := sdk.LLMStreamEvent{Done: true, FinishReason: sdk.FinishReasonToolCalls}
	done.Message = sdk.LLMMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{call}}
	if err := onChunk(done); err != nil {
		return nil, err
	}
	return &sdk.LLMResponse{Message: done.Message, FinishReason: sdk.FinishReasonToolCalls}, nil
}

// emitText 发一次文本回复(done=true)。
func emitText(onChunk func(ev sdk.LLMStreamEvent) error, text string) (*sdk.LLMResponse, error) {
	if err := onChunk(sdk.LLMStreamEvent{Delta: text}); err != nil {
		return nil, err
	}
	done := sdk.LLMStreamEvent{Done: true, FinishReason: sdk.FinishReasonStop}
	done.Message = sdk.LLMMessage{Role: sdk.RoleAssistant, Content: text}
	if err := onChunk(done); err != nil {
		return nil, err
	}
	return &sdk.LLMResponse{Message: done.Message, FinishReason: sdk.FinishReasonStop}, nil
}

// buildIsolationEnv 装配隔离 e2e 环境(工作区 = ws;返回全部相关服务)。
func buildIsolationEnv(t *testing.T, ws string) sdk.Ctx {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-system-prompt"},
		{ID: "host-fanout"},
		{ID: "host-worktrees"},
		{ID: "policy-guard", Data: map[string]any{"approval": "smart", "sandbox": "workspace-write", "sync": true}},
		{ID: "tool-files"},
		{ID: "tool-subagent"},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := baseb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })

	// 假适配器 + 当前模型
	var llm sdk.LLMService
	if err := c.Inject("ctx.llm", &llm); err != nil {
		t.Fatal(err)
	}
	llm.RegisterAdapter(&markAdapter{})
	llm.SetModel("mark-model")

	// 工作区切换(与真实切换同路径:host-cwd-sessions 广播事件 → 沙箱 root 同步)
	if _, err := bus.Emit(context.Background(), "cwd/workspace-switched", ws, sdk.Emit); err != nil {
		t.Fatal(err)
	}
	return c
}

// runSubagent 经**工具执行入口**调用 subagent(走 pre-execute 路径裁决 + 工作根注入的完整管线)。
func runSubagent(t *testing.T, c sdk.Ctx, args string) (*sdk.ToolResult, error) {
	t.Helper()
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	if _, ok := tools.Get("subagent"); !ok {
		t.Fatal("应注册 subagent 工具(tool-subagent)")
	}
	return tools.Execute(context.Background(), "subagent", args)
}

// decodeResult 解析 subagent 回包(host-tools 把工具返回值 JSON 序列化进 Content)。
func decodeResult(t *testing.T, res *sdk.ToolResult) map[string]any {
	t.Helper()
	if res == nil {
		t.Fatal("工具应返回结果")
	}
	if res.Error != "" {
		t.Fatalf("工具报错: %s", res.Error)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(res.Content), &m); err != nil {
		t.Fatalf("回包不是 JSON: %v (%s)", err, res.Content)
	}
	return m
}

// gitRepo 建一个提交过的真实仓库。
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("环境无 git")
	}
	dir := t.TempDir()
	for _, a := range [][]string{{"init", "-q"}, {"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"}, {"config", "commit.gpgsign", "false"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, a := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	return dir
}

// TestWorktreeIsolationParallelWrites 两个并发隔离子代理写同名相对路径:互不覆盖且不落主工作区。
func TestWorktreeIsolationParallelWrites(t *testing.T) {
	repo := gitRepo(t)
	c := buildIsolationEnv(t, repo)

	type outcome struct {
		out *sdk.ToolResult
		err error
	}
	results := make([]outcome, 2)
	tasks := []string{"写文件,标记A", "写文件,标记B"}
	var wg sync.WaitGroup
	for i := range tasks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := `{"action":"delegate","task":"` + tasks[i] + `","isolate":"worktree"}`
			results[i].out, results[i].err = runSubagent(t, c, args)
		}(i)
	}
	wg.Wait()

	var dirs []string
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("第 %d 路子代理失败: %v", i, r.err)
		}
		m := decodeResult(t, r.out)
		if m["isolated"] != true {
			t.Fatalf("回包应标注 isolated: %+v", m)
		}
		dir, _ := m["worktree"].(string)
		if dir == "" {
			t.Fatalf("回包必须含 worktree 路径(父级据此合并/回收): %+v", m)
		}
		if !strings.Contains(dir, string(filepath.Separator)+"worktrees"+string(filepath.Separator)) {
			t.Fatalf("worktree 应落数据根 worktrees/ 下: %s", dir)
		}
		dirs = append(dirs, dir)
	}
	if dirs[0] == dirs[1] {
		t.Fatalf("两个子代理必须拿到不同 worktree: %s", dirs[0])
	}
	// 1) 各 worktree 内写的是自己的内容(隔离生效,无互相覆盖)
	want := []string{"A", "B"}
	for i, d := range dirs {
		b, err := os.ReadFile(filepath.Join(d, "shared.txt"))
		if err != nil {
			t.Fatalf("第 %d 路 worktree 内应落 shared.txt: %v", i, err)
		}
		if strings.TrimSpace(string(b)) != want[i] {
			t.Fatalf("第 %d 路内容被覆盖: got %q want %q", i, string(b), want[i])
		}
	}
	// 2) 主工作区完全没被碰(相对路径基准 = 本次调用的工作根)
	if _, err := os.Stat(filepath.Join(repo, "shared.txt")); !os.IsNotExist(err) {
		t.Fatalf("主工作区不得出现子代理写的文件: %v", err)
	}
	out, err := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("主工作区应保持干净: %q", out)
	}
}

// TestWorktreeIsolationNonGitExplicit 非 git 工作区:显式报错,不静默退化为非隔离运行。
func TestWorktreeIsolationNonGitExplicit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("环境无 git")
	}
	plain := t.TempDir() // 不是 git 仓库
	c := buildIsolationEnv(t, plain)
	res, err := runSubagent(t, c, `{"action":"delegate","task":"写文件","isolate":"worktree"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !strings.Contains(res.Error, "不是 git 仓库") {
		t.Fatalf("应显式说明非 git 仓库: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(plain, "shared.txt")); !os.IsNotExist(err) {
		t.Fatalf("失败时不得降级为主工作区执行: %v", err)
	}
}

// TestShippedBundleEnablesWorktrees 发行态契约:随包 bundle 样板必须默认启用 host-worktrees
// (S-P1-4 的落地入口 —— 只登记 catalogue 而样板漏配,老用户升级后 isolate 就会报“未装配”)。
func TestShippedBundleEnablesWorktrees(t *testing.T) {
	b, err := config.ReadBundle("../config/bundle-base.yaml")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range b.Entries {
		if e.ID != "host-worktrees" {
			continue
		}
		found = true
		if e.Enabled != nil && !*e.Enabled {
			t.Fatal("host-worktrees 随包样板应默认启用")
		}
	}
	if !found {
		t.Fatal("config/bundle-base.yaml 缺 host-worktrees 条目(隔离运行会报未装配)")
	}
	// catalogue 单一事实源:归属 base 且提供 ctx.worktrees
	def, ok := catalogue.All["host-worktrees"]
	if !ok {
		t.Fatal("catalogue 未登记 host-worktrees")
	}
	if def.Bundle != "base" {
		t.Fatalf("host-worktrees 应归 base bundle: %q", def.Bundle)
	}
	var provides bool
	for _, p := range def.Manifest.Provides {
		if p == "ctx.worktrees" {
			provides = true
		}
	}
	if !provides {
		t.Fatalf("host-worktrees 应声明 provides ctx.worktrees: %v", def.Manifest.Provides)
	}
}
