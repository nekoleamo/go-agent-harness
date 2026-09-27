package hosttools

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
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

// schemaTool 带 inputSchema 的工具替身(注册期推断提示用)。
type schemaTool struct {
	name     string
	schema   map[string]any
	declared bool // PathParamsDeclared:作者显式声明"无路径参数"
}

func (s *schemaTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: s.name, InputSchema: s.schema, PathParamsDeclared: s.declared}
}

func (s *schemaTool) Execute(context.Context, string) (any, error) { return "ok", nil }

// TestRegisterWarnsInferredPathParams 未声明路径参数的工具在注册期点名一次 ——
// 裁决从"放行"变成"推断"是行为变化(2026-09-27 审计 F2),作者该知道;声明即可覆盖。
func TestRegisterWarnsInferredPathParams(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	c := ctx.New(logger, event.New(logger))
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	pathSchema := map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}

	tools.Register(&schemaTool{name: "mcp_srv_write_file", schema: pathSchema})
	if !strings.Contains(buf.String(), "未声明路径参数") || !strings.Contains(buf.String(), "mcp_srv_write_file") {
		t.Fatalf("应提示未声明工具的推断裁决: %s", buf.String())
	}
	// 作者显式声明"无路径参数" → 不唠叨(解除开关必须真的能解除)
	buf.Reset()
	tools.Register(&schemaTool{name: "no_paths_tool", schema: pathSchema, declared: true})
	if strings.Contains(buf.String(), "no_paths_tool") {
		t.Fatalf("PathParamsDeclared 的工具不应提示: %s", buf.String())
	}
	// 无路径面(schema 里没有路径型参数名)→ 不提示
	buf.Reset()
	tools.Register(&schemaTool{name: "mcp_srv_echo", schema: map[string]any{
		"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}})
	if strings.Contains(buf.String(), "mcp_srv_echo") {
		t.Fatalf("无路径面的工具不应提示: %s", buf.String())
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

	// B3(2026-09-27):被忽略者必须**可见** —— 否则“插件启用成功但工具没注册上”只能翻日志。
	rep, ok := tools.(sdk.ToolConflictReporter)
	if !ok {
		t.Fatal("注册表应实现 sdk.ToolConflictReporter")
	}
	cf := rep.ToolConflicts()
	if len(cf) != 1 || cf[0].Name != "same" || !strings.Contains(cf[0].Ignored, "v2") {
		t.Fatalf("应记下被忽略者 v2: %+v", cf)
	}
	// 日志级别为 Error(不是 Warn):这属于“你以为装上了其实没装”
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	c2 := ctx.New(logger, event.New(logger))
	if _, err := (&Plugin{}).Start(c2, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var tools2 sdk.ToolRegistry
	if err := c2.Inject("ctx.tools", &tools2); err != nil {
		t.Fatal(err)
	}
	tools2.Register(&fixedTool{id: "v1"})
	tools2.Register(&fixedTool{id: "v2"})
	log := buf.String()
	if !strings.Contains(log, "level=ERROR") || !strings.Contains(log, "被忽略") || !strings.Contains(log, "实现v2") {
		t.Fatalf("冲突应 ERROR 级点名被忽略者: %s", log)
	}
}
