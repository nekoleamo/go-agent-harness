// tool-session-search 工具面单测(S-P2-5):装配注册、入参校验(非法 JSON / 非法 scope)、
// 走真数据根($GAH_HOME/sessions)的端到端调用。
package toolsessionsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubCtx 最小 sdk.Ctx(与 tool-doc 同款:只做服务容器)。
type stubCtx struct{ svc map[string]any }

func (c *stubCtx) Provide(k string, v any) error { c.svc[k] = v; return nil }
func (c *stubCtx) Inject(k string, out any) error {
	v, ok := c.svc[k]
	if !ok {
		return fmt.Errorf("ctx: service %q not provided", k)
	}
	reflect.ValueOf(out).Elem().Set(reflect.ValueOf(v))
	return nil
}
func (c *stubCtx) Subscribe(string, sdk.AnyListener) sdk.Disposer { return func() {} }
func (c *stubCtx) Emit(context.Context, string, any, sdk.DispatchMode) (any, error) {
	return nil, nil
}
func (c *stubCtx) Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// stubTools 记录注册的工具(供插件装配断言)。
type stubTools struct{ reg map[string]sdk.Tool }

func (t *stubTools) Register(tool sdk.Tool) sdk.Disposer {
	t.reg[tool.Definition().Name] = tool
	return func() { delete(t.reg, tool.Definition().Name) }
}
func (t *stubTools) List() []sdk.ToolDefinition {
	out := make([]sdk.ToolDefinition, 0, len(t.reg))
	for _, v := range t.reg {
		out = append(out, v.Definition())
	}
	return out
}
func (t *stubTools) Get(name string) (sdk.ToolDefinition, bool) {
	v, ok := t.reg[name]
	if !ok {
		return sdk.ToolDefinition{}, false
	}
	return v.Definition(), true
}
func (t *stubTools) Unregister(name string) { delete(t.reg, name) }
func (t *stubTools) Execute(context.Context, string, string) (*sdk.ToolResult, error) {
	return nil, fmt.Errorf("stub: Execute 未实现")
}

// TestPluginRegistersToolAndDisposerRevokes:注册即副作用,卸载即撤销。
func TestPluginRegistersToolAndDisposerRevokes(t *testing.T) {
	tools := &stubTools{reg: map[string]sdk.Tool{}}
	var reg sdk.ToolRegistry = tools // 编译期确认桩实现满足契约
	c := &stubCtx{svc: map[string]any{"ctx.tools": reg}}
	dis, err := (&Plugin{}).Start(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tools.Get(ToolName); !ok {
		t.Fatalf("应注册 %s:%v", ToolName, tools.List())
	}
	dis()
	if _, ok := tools.Get(ToolName); ok {
		t.Fatal("Disposer 应撤销注册")
	}
	// 缺 ctx.tools → 显式失败(不静默不起作用)
	if _, err := (&Plugin{}).Start(&stubCtx{svc: map[string]any{}}, nil); err == nil {
		t.Fatal("缺 ctx.tools 应报错")
	}
}

// TestExecuteArgsValidation:坏 JSON = 错误;非法 scope = 结构化错误(不 panic)。
func TestExecuteArgsValidation(t *testing.T) {
	tool := NewTool()
	if _, err := tool.Execute(context.Background(), `{`); err == nil {
		t.Fatal("坏 JSON 应返回 error")
	}
	out, err := tool.Execute(context.Background(), `{"query":"x","scope":"planet"}`)
	if err != nil {
		t.Fatalf("非法 scope 不该是执行错误(交模型自纠): %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok || !strings.Contains(fmt.Sprint(m["error"]), "scope") {
		t.Fatalf("应回结构化错误: %#v", out)
	}
}

// TestExecuteEndToEndWithHome:走真数据根($GAH_HOME/sessions)的端到端调用。
func TestExecuteEndToEndWithHome(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "sessions")
	ws := filepath.Join(home, "work")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAH_HOME", home)
	writeFile(t, filepath.Join(root, "workspaces.json"), fmt.Sprintf(`[{"key":"work","dir":%q,"ts":1}]`, ws))
	writeFile(t, filepath.Join(root, "work-20260101-1.jsonl"),
		ev("user/message", 1, "2026-01-01T10:00:00+08:00", userMsg("排查 goroutine 泄漏的现场"))+"\n")

	// 工具内部按进程 cwd 判定「当前工作区」→ 切到合成工作区
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()

	tool := NewTool()
	out, err := tool.Execute(context.Background(), `{"query":"goroutine 泄漏","limit":3}`)
	if err != nil {
		t.Fatal(err)
	}
	res, ok := out.(Result)
	if !ok {
		t.Fatalf("应回 Result: %T", out)
	}
	if res.Mode != "match" || len(res.Hits) != 1 {
		t.Fatalf("应命中 1 个会话: %+v", res)
	}
	h := res.Hits[0]
	if h.File != "work-20260101-1.jsonl" || h.ID != "20260101-1" || h.Dir != ws {
		t.Fatalf("命中会话信息不对: %+v", h)
	}
	if !strings.Contains(h.Matches[0].Text, "goroutine") {
		t.Fatalf("片段不含命中词: %+v", h.Matches[0])
	}
	// 结果可 JSON 序列化(过桥/进模型上下文的前提)
	if _, err := json.Marshal(res); err != nil {
		t.Fatalf("结果应可序列化: %v", err)
	}
	// 空查询 → 最近会话
	out, err = tool.Execute(context.Background(), `{"query":""}`)
	if err != nil {
		t.Fatal(err)
	}
	if r2 := out.(Result); r2.Mode != "recent" || len(r2.Hits) != 1 {
		t.Fatalf("空查询应为最近会话模式: %+v", r2)
	}
	// 跨工作区:scope=all 也能看到(当前只有本工作区会话 → 仍 1 条)
	out, _ = tool.Execute(context.Background(), `{"query":"goroutine","scope":"all"}`)
	if r3 := out.(Result); len(r3.Hits) != 1 || r3.Scope != "all" {
		t.Fatalf("scope=all 结果不对: %+v", r3)
	}
}
