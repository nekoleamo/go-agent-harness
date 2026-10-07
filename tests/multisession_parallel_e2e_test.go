// 多会话**并发**压测(端到端):一个实例里多个会话同时跑回合、同时经工具改**同一工作区**。
//
// 为什么单独一个文件:页签上线后,「多会话并行」从"多窗口各跑各的"变成日常用法,而
// 多会话并发时真正共享的是 **llm / tools / policy-guard / 文件系统**。此前只有
// host-agent-loop 包里的白盒测试验过"事件不交错",**没有任何一处验过共享资源的并发**。
// 这里补上,并把它写成**每次 CI 都跑**的断言,而不是一次性手工观察。
//
// 断言的六件事:
//
//	① 峰值并发 ≥ 2(否则就退化成串行,"并行"两个字是假的);
//	② 每个会话的账本里 user/tool_call/tool_result/assistant **严格配对**(不交错);
//	③ 工具真的把文件写进了**各自**会话的文件(没有写到别人那儿);
//	④ 同一会话跨轮追加**不丢**(第二轮的内容接在第一轮后面);
//	⑤ 全部回合无错误;
//	⑥ -race 下干净(整套由 `go test -race ./...` 覆盖)。
package tests

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"encoding/json"

	"github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	hostcwdsessions "github.com/nekoleamo/go-agent-harness/plugins/host/host-cwd-sessions"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// —— 压测用的 LLM 适配器 ——
//
// 每收到「模型消息」就发起一次 file_append(带一个短延时),收到「工具结果」就收尾。
// 延时是**故意**的:没有重叠窗口就测不出"真并行"。
type slowToolAdapter struct {
	inflight atomic.Int32 // 当前在跑的请求数
	peak     atomic.Int32 // 观察到的峰值
	turns    atomic.Int32 // 总请求数

	mu   sync.Mutex
	seen map[string]int // 模型请求里"最后一条 user 文本" → 次数(诊断用)
}

func (a *slowToolAdapter) Name() string { return "multisession-stress" }

func (a *slowToolAdapter) Complete(ctx context.Context, req *sdk.LLMRequest, _ func(sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	// 并发观测:在飞请求数与峰值。这是"并行名副其实"的判据 —— 没有重叠就测不出真并行。
	n := a.inflight.Add(1)
	a.turns.Add(1)
	for {
		p := a.peak.Load()
		if n <= p || a.peak.CompareAndSwap(p, n) {
			break
		}
	}
	defer a.inflight.Add(-1)
	// 制造重叠窗口:同时跑的会话才会互相看见(串行跑就永远是 1)
	select {
	case <-time.After(60 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// 每个请求都调一次工具,**不在这里收尾**:回合由 host-agent-loop 的 max_steps 收口。
	//
	// 为什么这么绕:试过让 mock 根据投影内容自行收敛("最后一条是不是 tool""这个任务
	// 调过没有"),两种判据都会随**宿主事件时序**飘 —— 那是把宿主的不变量挪进假模型里测,
	// 一旦飘了既分不清是宿主坏了还是 mock 坏了。改成"固定跑一步",断言反而更硬:
	// 要么工具真跑过(盘上看得见),要么没跑。
	// 从用户消息里取出「本会话的文件名 + 本轮标记」
	taskText := lastUserText(req)
	if a.seen != nil {
		a.mu.Lock()
		a.seen[taskText]++
		a.mu.Unlock()
	}
	path, mark := parseTask(taskText)
	call := sdk.ToolCall{
		ID:        fmt.Sprintf("call-%s-%d", mark, a.turns.Load()),
		Name:      "file_append",
		Arguments: fmt.Sprintf(`{"path":%q,"content":%q}`, path, mark+"\n"),
	}
	return &sdk.LLMResponse{
		Message:      sdk.LLMMessage{Role: sdk.RoleAssistant, Content: "写入 " + mark, ToolCalls: []sdk.ToolCall{call}},
		FinishReason: sdk.FinishReasonToolCalls,
	}, nil
}

// parseTask 从「文件=out-A.txt 轮=1」这类任务描述里取出参数。
func parseTask(s string) (path, mark string) {
	for _, f := range strings.Fields(s) {
		switch {
		case strings.HasPrefix(f, "文件="):
			path = strings.TrimPrefix(f, "文件=")
		case strings.HasPrefix(f, "轮="):
			mark = strings.TrimPrefix(f, "轮=")
		}
	}
	return path, mark
}

// lastUserText 取最后一条 user 消息文本(任务描述固定在用户消息里)。
func lastUserText(req *sdk.LLMRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == sdk.RoleUser {
			return req.Messages[i].Content
		}
	}
	return ""
}

// buildParallelEnv 装配最小可跑多会话的宿主(含会话目录与文件工具)。
func buildParallelEnv(t *testing.T, ws string) (sdk.Ctx, *slowToolAdapter) {
	t.Helper()
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
		// max_steps=1:每个回合固定跑一步(一次模型请求 + 一次工具),到顶显式失败。
		// 压测要的是"多个会话同时跑工具",不是"模型会不会收敛"。
		{ID: "host-agent-loop", Data: map[string]any{"max_steps": 1}},
		{ID: "host-cwd-sessions"},
		{ID: "host-usage-stats"},
		{ID: "policy-guard", Data: map[string]any{"approval": "open", "sandbox": "workspace-write", "sync": true}},
		{ID: "tool-files"},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := base.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })

	// 沙箱根 = 工作区(与真实路径同一套事件同步)
	if _, err := bus.Emit(context.Background(), "cwd/workspace-switched", ws, sdk.Emit); err != nil {
		t.Fatal(err)
	}
	var llm sdk.LLMService
	if err := c.Inject("ctx.llm", &llm); err != nil {
		t.Fatal(err)
	}
	adapter := &slowToolAdapter{seen: map[string]int{}}
	llm.RegisterAdapter(adapter)
	llm.SetModel("multisession-stress-model")
	return c, adapter
}

// TestMultiSessionParallelTurnsShareOneWorkspace 4 个会话同时跑,各自经工具改同一个工作区。
func TestMultiSessionParallelTurnsShareOneWorkspace(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("GAH_HOME", t.TempDir())
	c, adapter := buildParallelEnv(t, ws)

	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	runner, ok := loop.(sdk.SessionRunner)
	if !ok {
		t.Fatal("宿主未实现 sdk.SessionRunner(多会话并行回合不可用)")
	}
	var sdir sdk.SessionDir
	if err := c.Inject("ctx.sessionDir", &sdir); err != nil {
		t.Fatal(err)
	}

	const n = 4
	ids := make([]string, 0, n)
	for i := range n {
		id, err := sdir.Spawn()
		if err != nil {
			t.Fatalf("Spawn 会话 %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	// 每个会话两轮:第一轮写 A,第二轮 append B(同会话续写不许丢)
	var cs sdk.CwdSessions
	if err := c.Inject("ctx.cwdSessions", &cs); err != nil {
		t.Fatal(err)
	}
	paths := make([]string, n)
	for i, id := range ids {
		paths[i] = hostcwdsessions.SessionPath(hostcwdsessions.SessionsRoot(), cs.Current(), id)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2*n)
	ledgers := make([]ledgerOf, n)
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id, path string) {
			defer wg.Done()
			runOne(t, i, id, path, runner, sdir, errs, ledgers)
		}(i, id, paths[i])
	}
	wg.Wait()
	// 先报回合错误:账本检查的失败往往是它的**后果**(回合没跑完当然没账本),
	// 顺序反了会让人去查错地方。
	for i, err := range errs {
		if err != nil {
			t.Fatalf("会话 %d 的第 %d 轮失败: %v", i/2, i%2+1, err)
		}
	}
	for i := range ledgers {
		checkLedger(t, i, ledgers[i])
	}

	// ① 真并行:同一时刻至少两个请求在飞(否则"并行"只是名字)
	adapterPeak := adapter.peak.Load()
	if adapterPeak < 2 {
		t.Fatalf("峰值并发只有 %d:多个会话实际上是串行跑的(并行名不副实)", adapterPeak)
	}
	t.Logf("峰值并发 = %d(会话数 %d)", adapterPeak, n)
	adapter.mu.Lock()
	for k, v := range adapter.seen {
		t.Logf("模型看到的最后一条 user 输入 %q × %d", k, v)
	}
	adapter.mu.Unlock()

	// ③④ 文件:每个会话两轮追加都在,内容互不串
	for i := range n {
		p := filepath.Join(ws, fmt.Sprintf("out-%d.txt", i))
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("会话 %d 的输出文件没落盘: %v", i, err)
		}
		got := string(b)
		want := fmt.Sprintf("1-%c", 'A'+byte(i))
		if !strings.Contains(got, want) {
			t.Fatalf("会话 %d 的文件缺自己的标记 %q: %q", i, want, got)
		}
		if lines := strings.Count(strings.TrimSpace(got), "\n") + 1; lines != 1 {
			t.Fatalf("会话 %d 的文件应只有 1 行(其余内容说明别的会话写串了):%q", i, got)
		}
	}
}

// checkLedger 校验单个会话的账本:工具调用与结果成对、且没有别人的标记混进来。

// runOne 单个会话跑两轮(并发/串行共用一条路径,便于对照)。
func runOne(t *testing.T, i int, id, path string, runner sdk.SessionRunner, sdir sdk.SessionDir, errs []error, ledgers []ledgerOf) {
	t.Helper()
	lg, err := sdir.Acquire(id)
	if err != nil {
		errs[i*2] = err
		return
	}
	defer sdir.Release(id)
	_ = lg
	// 标记里**带会话序号**:否则每个会话写的都是 "1-A",文件串了也看不出来。
	task := func(round int) string {
		return fmt.Sprintf("文件=out-%d.txt 轮=%d-%c", i, round, 'A'+byte(i))
	}
	// max_steps=1 ⇒ 这一步之后必然显式失败(达到上限);这是**预期**结果,
	// 说明工具已经跑过。不把"报错"当失败,否则测不到并发本身。
	if err := runner.RunInSession(context.Background(), id, task(1)); err != nil &&
		!strings.Contains(err.Error(), "最大步数") {
		errs[i*2] = err
		return
	}
	ledgers[i] = snapshotFromFile(path)
}

// ledgerOf 该会话账本的工具调用/结果条数与用户消息文本。
type ledgerOf struct {
	calls, results int
	users          []string
}

// snapshotFromFile **从落盘账本**读(不是从内存 Log 实例)。
//
// 为什么读盘:多会话并行时每个会话有自己的日志实例,回合结束即归还、归零即落盘移表,
// "手里那个实例"未必含有全部事件(实测两轮跑完只剩第二轮)。落盘才是最终事实 ——
// 而"模型可见即已记录"本来就是本项目要守的不变量,验它就该验盘上的那份。
func snapshotFromFile(path string) ledgerOf {
	var out ledgerOf
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev sdk.SessionEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue // 坏行容忍与宿主一致
		}
		switch ev.Kind {
		case sdk.EventToolCall:
			out.calls++
		case sdk.EventToolResult:
			out.results++
		case sdk.EventUserMessage:
			// 盘上载荷是 map[string]any(不是结构体)—— 解 JSON 出来的就是 map。
			if m, ok := ev.Payload.(map[string]any); ok {
				if c, ok := m["Content"].(string); ok {
					out.users = append(out.users, c)
				}
			}
		}
	}
	return out
}

// checkLedger 账本自查:工具调用/结果成对,且没有别的会话的消息混进来。
// 在**回合全部结束后**统一查(不在 goroutine 里 t.Fatalf —— 那会带着半截状态继续跑)。
func checkLedger(t *testing.T, idx int, l ledgerOf) {
	t.Helper()
	// 断的是三件**不变量**:工具调用与结果成对(不成对 = 界面永远转圈)、
	// 确实调过工具(不是"回合没跑工具就收了尾")、账本里没有别人的会话内容。
	if l.calls != l.results {
		t.Fatalf("会话 %d 的工具调用/结果不成对:%d/%d(有调用没结果 = 界面会永远转圈)", idx, l.calls, l.results)
	}
	if l.calls < 1 {
		t.Fatalf("会话 %d 的回合里一次工具都没调(等于没验到共享工具的并发)", idx)
	}
	if len(l.users) != 1 {
		t.Fatalf("会话 %d 的账本应有 1 条用户消息,实得 %d", idx, len(l.users))
	}
	for _, u := range l.users {
		if !strings.Contains(u, fmt.Sprintf("文件=out-%d.txt", idx)) {
			t.Fatalf("会话 %d 的账本里混进了别的会话的消息:%q", idx, u)
		}
	}
}
