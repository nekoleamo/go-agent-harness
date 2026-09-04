// host-fanout 测试:装配 llm/mock 后直接调用 ctx.fanout 服务(独立上下文 ReAct)。
package hostfanout

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host-llm"
	"github.com/nekoleamo/go-agent-harness/plugins/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/plugins/host-tools"
	"github.com/nekoleamo/go-agent-harness/plugins/llm-mock"
	"github.com/nekoleamo/go-agent-harness/plugins/tool-shell"
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
