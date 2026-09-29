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

// namedTool 具名工具替身(可见性过滤用例要多个不同名字)。
type namedTool struct{ name string }

func (n *namedTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: n.name, Description: "工具 " + n.name, InputSchema: map[string]any{"type": "object"}}
}
func (n *namedTool) Execute(context.Context, string) (any, error) {
	return map[string]any{"ran": n.name}, nil
}

// TestToolCatalogueFilter 角色工具集过滤:List 过滤 / ListAll 全量 / Get 豁免 /
// Execute 显式拒绝(与"不存在"区分)/ Disposer 幂等撤销。
func TestToolCatalogueFilter(t *testing.T) {
	c := newCtx(t)
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var reg sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &reg); err != nil {
		t.Fatal(err)
	}
	reg.Register(&namedTool{name: "shell"})
	reg.Register(&namedTool{name: "file_read"})
	tc, ok := reg.(sdk.ToolCatalogue)
	if !ok {
		t.Fatal("host-tools 应实现 sdk.ToolCatalogue")
	}

	// 未装 filter:全量可见,ListAll 同
	if len(reg.List()) != 2 || len(tc.ListAll()) != 2 {
		t.Fatalf("未过滤应为 2 个,got List=%d ListAll=%d", len(reg.List()), len(tc.ListAll()))
	}

	disp := tc.SetFilter(func(d sdk.ToolDefinition) bool { return d.Name != "shell" })
	if got := reg.List(); len(got) != 1 || got[0].Name != "file_read" {
		t.Fatalf("过滤后 List = %+v,期望只剩 file_read", got)
	}
	if n := len(tc.ListAll()); n != 2 {
		t.Fatalf("ListAll 不该被过滤,got %d", n)
	}
	// Get 是裁决面/自查面:**刻意不应用 filter**(policy-guard 要拿真实目标定义做路径裁决)
	if _, ok := reg.Get("shell"); !ok {
		t.Error("Get 不应受可见性过滤影响")
	}
	// Execute 拒绝:文案必须与"不存在"区分开(否则用户会去查插件安装)
	res, err := reg.Execute(context.Background(), "shell", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Error, "排除") || strings.Contains(res.Error, "不存在") {
		t.Errorf("被排除的工具应给「已排除」文案,got %q", res.Error)
	}
	if res, _ := reg.Execute(context.Background(), "file_read", "{}"); res.Error != "" {
		t.Errorf("可见工具应可执行,got error %q", res.Error)
	}
	// 不存在的工具仍走"不存在"文案(两条路径不能混)
	if res, _ := reg.Execute(context.Background(), "nope", "{}"); !strings.Contains(res.Error, "不存在") {
		t.Errorf("未知工具应报「不存在」,got %q", res.Error)
	}

	disp()
	if n := len(reg.List()); n != 2 {
		t.Fatalf("撤销 filter 后应恢复全量,got %d", n)
	}
	disp() // 幂等:重复撤销不炸
	if res, _ := reg.Execute(context.Background(), "shell", "{}"); res.Error != "" {
		t.Fatalf("撤销后 shell 应可执行,got %q", res.Error)
	}

	// nil = 恢复全量(契约的一部分)
	d2 := tc.SetFilter(func(sdk.ToolDefinition) bool { return false })
	if len(reg.List()) != 0 {
		t.Fatal("拒绝所有工具的 filter 应让 List 为空")
	}
	tc.SetFilter(nil)
	if len(reg.List()) != 2 {
		t.Fatal("SetFilter(nil) 应恢复全量")
	}
	d2() // 后装的已被 nil 覆盖:旧 disposer 不该把 nil 抹成"某一个 filter"
	if len(reg.List()) != 2 {
		t.Fatal("已被覆盖的旧 disposer 不得改变现状")
	}
}

// TestExcludedToolSkipsPreExecute 被排除的工具**不进审批/沙箱裁决**:
// 否则会弹一次毫无意义的确认框(用户点了"允许"也执行不了)。
func TestExcludedToolSkipsPreExecute(t *testing.T) {
	c := newCtx(t)
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var reg sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &reg); err != nil {
		t.Fatal(err)
	}
	var seen int
	c.Subscribe("tools/pre-execute", func(context.Context, *sdk.Event) error {
		seen++
		return nil
	})
	reg.Register(&namedTool{name: "shell"})
	reg.(sdk.ToolCatalogue).SetFilter(func(d sdk.ToolDefinition) bool { return d.Name != "shell" })
	if _, err := reg.Execute(context.Background(), "shell", "{}"); err != nil {
		t.Fatal(err)
	}
	if seen != 0 {
		t.Fatalf("被排除的工具不应进入 pre-execute 裁决,got %d 次", seen)
	}
	if _, err := reg.Execute(context.Background(), "missing", "{}"); err != nil {
		t.Fatal(err)
	}
	if seen != 0 {
		t.Fatalf("未知工具同样不该进裁决,got %d 次", seen)
	}
	reg.Register(&namedTool{name: "file_read"})
	if _, err := reg.Execute(context.Background(), "file_read", "{}"); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("可见工具应进裁决一次,got %d", seen)
	}
}
