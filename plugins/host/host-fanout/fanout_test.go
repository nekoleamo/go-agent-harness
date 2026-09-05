// host-fanout 测试:装配 llm/mock 后直接调用 ctx.fanout 服务(独立上下文 ReAct)。
package hostfanout

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-llm"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/plugins/adapter/llm-mock"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-shell"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildEnv 装配 ctx.tools/ctx.llm/ctx.systemPrompt + 本插件,返回 ctx.fanout。
// llm-mock 脚本:请求1 调 shell,请求2 文本收尾(与 workflow 测试同构)。
func buildEnv(t *testing.T) sdk.FanoutService {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	for _, pl := range []sdk.Plugin{
		&hosttools.Plugin{},
		&toolshell.Plugin{},
	} {
		if _, err := pl.Start(c, &sdk.Manifest{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&hostllm.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&llmmock.Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{
		"script": `[{"tool":{"name":"shell","args":"{\"command\":\"echo sub-ok\"}"}},{"text":"子代理完成","finish":"stop"}]`,
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
	return svc
}

// TestAgent 单子代理:独立回合并返回最终文本。
func TestAgent(t *testing.T) {
	svc := buildEnv(t)
	res, err := svc.Agent(context.Background(), "任务A")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "子代理完成") {
		t.Fatalf("应返回子代理最终文本: %q", res)
	}
}

// TestParallel 并发扇出:多子代理聚合,顺序与输入对应。
func TestParallel(t *testing.T) {
	svc := buildEnv(t)
	results := svc.Parallel(context.Background(), []string{"甲", "乙"})
	if len(results) != 2 {
		t.Fatalf("应 2 个结果: %d", len(results))
	}
	for i, r := range results {
		if r.Input != []string{"甲", "乙"}[i] {
			t.Fatalf("顺序应对应输入: %+v", results)
		}
		if r.Error != "" || !strings.Contains(r.Result, "子代理完成") {
			t.Fatalf("每项应含结果: %+v", r)
		}
	}
}

// TestPipeline 串行链:上一步输出作为下一步输入。
func TestPipeline(t *testing.T) {
	svc := buildEnv(t)
	steps, final, err := svc.Pipeline(context.Background(), []string{"第一步", "第二步"})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 {
		t.Fatalf("应 2 步: %d", len(steps))
	}
	if !strings.Contains(final, "子代理完成") {
		t.Fatalf("最终应含子代理结果: %q", final)
	}
	for _, s := range steps {
		if s.Error != "" || !strings.Contains(s.Result, "子代理完成") {
			t.Fatalf("每步应含结果: %+v", steps)
		}
	}
	// 链式:第 2 步输入应为第 1 步输出
	if steps[1].Input != steps[0].Result {
		t.Fatalf("第 2 步输入应等于第 1 步输出: %q vs %q", steps[1].Input, steps[0].Result)
	}
}

// TestContextCancel 取消传播:ctx 取消后子代理退出。
func TestContextCancel(t *testing.T) {
	svc := buildEnv(t)
	ctx2, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.Agent(ctx2, "任务")
	if err == nil {
		t.Fatal("取消的 ctx 应报错")
	}
}

// TestSpawnAgentLifecycle M9.2:后台 spawn → 完成(done 含结果)→ 状态可查;再 kill 报错。
func TestSpawnAgentLifecycle(t *testing.T) {
	svc := buildEnv(t)
	id, err := svc.SpawnAgent(context.Background(), "后台任务")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("spawn 应返回句柄 id")
	}
	// 轮询完成(mock 两步 LLM,很快)
	var h sdk.AgentHandle
	deadline := time.Now().Add(5 * time.Second)
	for {
		var ok bool
		h, ok = svc.AgentStatus(id)
		if !ok {
			t.Fatalf("会话 %s 应存在", id)
		}
		if h.State == sdk.AgentDone || h.State == sdk.AgentFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待超时: %+v", h)
		}
		time.Sleep(30 * time.Millisecond)
	}
	if h.State != sdk.AgentDone || !strings.Contains(h.Result, "子代理完成") {
		t.Fatalf("后台子代理应 done 且含结果: %+v", h)
	}
	// ListAgents 应含该会话
	found := false
	for _, x := range svc.ListAgents() {
		if x.ID == id && x.State == sdk.AgentDone {
			found = true
		}
	}
	if !found {
		t.Fatalf("list 应含已完成会话 %s", id)
	}
	// 已完成 kill → 错误
	if err := svc.KillAgent(id); err == nil {
		t.Fatal("已完成会话 kill 应报错")
	}
}

// TestSpawnAgentKill M9.2:运行中 kill → killed 状态(不再产出结果)。
func TestSpawnAgentKill(t *testing.T) {
	svc := buildEnv(t)
	id, err := svc.SpawnAgent(context.Background(), "可终止任务")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.KillAgent(id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		h, ok := svc.AgentStatus(id)
		if !ok {
			t.Fatal("会话应存在")
		}
		if h.State == sdk.AgentKilled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("kill 后应转 killed: %+v", h)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 二次 kill → 错误
	if err := svc.KillAgent(id); err == nil {
		t.Fatal("二次 kill 应报错")
	}
}
