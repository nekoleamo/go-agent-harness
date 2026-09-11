// 工具执行入口不变式(R10 ②):ctx.tools 是唯一执行入口,且每次执行都必过
// tools/pre-execute —— 策略插件(policy-guard)只订阅该事件,故"veto 能挡住执行"
// 是安全不变式:一旦某处绕过事件直接调工具实现,沙箱/审批就静默失效。
package hosttools

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// countedTool 记录执行次数的桩工具(veto 生效时次数必须为 0)。
type countedTool struct {
	calls  atomic.Int32
	lastAt atomic.Value // string:收到的参数
}

func (c *countedTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "counted", InputSchema: map[string]any{"type": "object"}}
}
func (c *countedTool) Execute(_ context.Context, args string) (any, error) {
	c.calls.Add(1)
	c.lastAt.Store(args)
	return map[string]any{"ok": true}, nil
}

// TestExecuteVetoBlocksBeforeToolRun waterfall veto 必须发生在工具执行之前:
// 订阅者返回错误 → 结果 Error 带 blocked 前缀、工具零执行;
// 撤销订阅后同一调用恢复执行(注册即副作用、卸载即撤销)。
func TestExecuteVetoBlocksBeforeToolRun(t *testing.T) {
	c := newCtx(t)
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	tool := &countedTool{}
	tools.Register(tool)

	if res, err := tools.Execute(context.Background(), "counted", `{"a":1}`); err != nil || res.Error != "" {
		t.Fatalf("无策略订阅时应正常执行: %+v err=%v", res, err)
	}
	if tool.calls.Load() != 1 || tool.lastAt.Load() != `{"a":1}` {
		t.Fatalf("工具应收到原始参数: calls=%d args=%v", tool.calls.Load(), tool.lastAt.Load())
	}

	d := c.Subscribe("tools/pre-execute", func(_ context.Context, ev *sdk.Event) error {
		call, ok := ev.Payload.(*sdk.ToolCallEvent)
		if !ok {
			return nil
		}
		if call.Name == "counted" {
			return errors.New("沙箱拒绝:写路径在 workspace 之外")
		}
		return nil
	})
	res, err := tools.Execute(context.Background(), "counted", `{"a":2}`)
	if err != nil {
		t.Fatalf("veto 应经结果回传而非 Go error: %v", err)
	}
	if !strings.HasPrefix(res.Error, "blocked: ") || !strings.Contains(res.Error, "沙箱拒绝") {
		t.Fatalf("veto 结果应带 blocked 前缀与原因: %+v", res)
	}
	if tool.calls.Load() != 1 {
		t.Fatalf("veto 命中时工具不得执行: calls=%d", tool.calls.Load())
	}
	if tool.lastAt.Load() != `{"a":1}` {
		t.Fatalf("veto 命中时参数不得下发: %v", tool.lastAt.Load())
	}

	d() // 撤销订阅:同一调用恢复
	res, err = tools.Execute(context.Background(), "counted", `{"a":3}`)
	if err != nil || res.Error != "" {
		t.Fatalf("撤销 veto 后应恢复执行: %+v err=%v", res, err)
	}
	if tool.calls.Load() != 2 {
		t.Fatalf("撤销后应执行一次: calls=%d", tool.calls.Load())
	}
}

// TestToolResultBroadcastCarriesVeto broadcast(tool/result)必须带上 veto 文本:
// UI/日志凭它显示"被政策拦截"而非静默无输出。
func TestToolResultBroadcastCarriesVeto(t *testing.T) {
	c := newCtx(t)
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	tools.Register(&countedTool{})

	var got []string
	c.Subscribe("tool/result", func(_ context.Context, ev *sdk.Event) error {
		if re, ok := ev.Payload.(*sdk.ToolResultEvent); ok {
			got = append(got, re.Name+"|"+re.Error)
		}
		return nil
	})
	c.Subscribe("tools/pre-execute", func(context.Context, *sdk.Event) error {
		return errors.New("政策拦截")
	})
	if _, err := tools.Execute(context.Background(), "counted", `{}`); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "counted|blocked: ") || !strings.Contains(got[0], "政策拦截") {
		t.Fatalf("tool/result 应广播 veto 文本: %v", got)
	}
}

// TestExecuteUnknownToolIsExplicit 入口仍须对未知工具显式回执(不静默成功),
// 这条同时证明 Execute 是唯一入口:未注册的实现拿不到调用。
func TestExecuteUnknownToolIsExplicit(t *testing.T) {
	c := newCtx(t)
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "no_such_tool", `{}`)
	if err != nil || !strings.Contains(res.Error, "no_such_tool") {
		t.Fatalf("未知工具应显式回执: %+v err=%v", res, err)
	}
}
