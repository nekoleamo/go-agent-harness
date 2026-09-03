package hosttools

import (
	"context"
	"log/slog"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func newCtx(t *testing.T) sdk.Ctx {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	return ctx.New(logger, bus)
}

type fixedTool struct{ id string }

func (f *fixedTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "same", Description: "实现" + f.id, InputSchema: map[string]any{"type": "object"}}
}
func (f *fixedTool) Execute(ctx context.Context, args string) (any, error) {
	return map[string]any{"impl": f.id}, nil
}

// 工具注册/列举/注销往返
func TestRegisterListDispose(t *testing.T) {
	c := newCtx(t)
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	d := tools.Register(&fixedTool{id: "v1"})
	if n := len(tools.List()); n != 1 {
		t.Fatalf("注册后应有 1 个工具,got %d", n)
	}
	if _, ok := tools.Get("same"); !ok {
		t.Fatal("工具应可取回")
	}
	d()
	if _, ok := tools.Get("same"); ok {
		t.Fatal("注销后工具应消失")
	}
}

// 同名注册:忽略第二个并保留首个实现(替换同名工具需先关闭提供者)
func TestDuplicateNameIgnored(t *testing.T) {
	c := newCtx(t)
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	tools.Register(&fixedTool{id: "v1"})
	tools.Register(&fixedTool{id: "v2"}) // 被忽略
	if n := len(tools.List()); n != 1 {
		t.Fatalf("同名工具应只保留首个,got %d", n)
	}
	res, err := tools.Execute(context.Background(), "same", "{}")
	if err != nil || res.Error != "" {
		t.Fatalf("执行失败: %v %+v", err, res)
	}
	if res.Content != `{"impl":"v1"}` {
		t.Fatalf("应执行首个实现 v1,got %s", res.Content)
	}
}
