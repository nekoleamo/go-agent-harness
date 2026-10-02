// 「回合中止」语义的测试:用户按停止不该被当成策略拒绝(2026-10-03 实机反馈)。
//
// 对外可见的事实只有一个 —— 工具结果的 Error 文本。blocked 前缀意味着「换个写法
// 重试」,中止意味着「这轮结束了」;两者混用会让每按一次停止都跳一条红色错误。
package hosttools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestVetoAbortedHasNoBlockedPrefix 中止(veto 错误链上带 sdk.ErrAborted)不带 blocked 前缀。
func TestVetoAbortedHasNoBlockedPrefix(t *testing.T) {
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

	d := c.Subscribe("tools/pre-execute", func(_ context.Context, ev *sdk.Event) error {
		call, ok := ev.Payload.(*sdk.ToolCallEvent)
		if ok && call.Name == "counted" {
			return sdk.AbortedError("策略 guard: web_fetch 未执行(等待域名确认时被停止)")
		}
		return nil
	})
	defer d()

	res, err := tools.Execute(context.Background(), "counted", `{"a":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(res.Error, "blocked: ") {
		t.Fatalf("中止不应带 blocked 前缀: %q", res.Error)
	}
	if !strings.Contains(res.Error, "被停止") {
		t.Fatalf("中止文案应说明是被停止: %q", res.Error)
	}
	if tool.calls.Load() != 0 {
		t.Fatalf("veto 命中时工具不得执行: calls=%d", tool.calls.Load())
	}
}

// TestVetoDeniedKeepsBlockedPrefix 真正的策略拒绝仍然带 blocked(不能被一起改掉)。
func TestVetoDeniedKeepsBlockedPrefix(t *testing.T) {
	c := newCtx(t)
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	tools.Register(&countedTool{})

	d := c.Subscribe("tools/pre-execute", func(_ context.Context, ev *sdk.Event) error {
		call, ok := ev.Payload.(*sdk.ToolCallEvent)
		if ok && call.Name == "counted" {
			return errors.New("沙箱拒绝:写路径在 workspace 之外")
		}
		return nil
	})
	defer d()

	res, _ := tools.Execute(context.Background(), "counted", `{"a":1}`)
	if !strings.HasPrefix(res.Error, "blocked: ") {
		t.Fatalf("策略拒绝必须保留 blocked 前缀: %q", res.Error)
	}
}
