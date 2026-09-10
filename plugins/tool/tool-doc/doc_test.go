// tool-doc 单测(D5):三工具注册 / read_document 行号化与预算分页 /
// doc_open 发 doc/open 且失败意图不广播 / doc_list 目录树。
package tooldoc

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

	"github.com/nekoleamo/go-agent-harness/plugins/host/host-docview"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubCtx 最小 sdk.Ctx(服务容器 + 事件记录)。
type stubCtx struct {
	svc    map[string]any
	events []string
}

func (c *stubCtx) Provide(k string, v any) error { c.svc[k] = v; return nil }
func (c *stubCtx) Inject(k string, out any) error {
	v, ok := c.svc[k]
	if !ok {
		return fmt.Errorf("ctx: service %q not provided", k)
	}
	rv := reflect.ValueOf(out).Elem()
	rv.Set(reflect.ValueOf(v))
	return nil
}
func (c *stubCtx) Subscribe(string, sdk.AnyListener) sdk.Disposer { return func() {} }
func (c *stubCtx) Emit(_ context.Context, name string, _ any, _ sdk.DispatchMode) (any, error) {
	c.events = append(c.events, name)
	return nil, nil
}
func (c *stubCtx) Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// stubTools 收集注册的工具。
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
func (t *stubTools) Execute(ctx context.Context, name, args string) (*sdk.ToolResult, error) {
	v, ok := t.reg[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool %s", name)
	}
	res, err := v.Execute(ctx, args)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(res)
	return &sdk.ToolResult{Content: string(b)}, nil
}

func newEnv(t *testing.T, data map[string]any) (*stubCtx, *stubTools) {
	t.Helper()
	ctx := &stubCtx{svc: map[string]any{}}
	tools := &stubTools{reg: map[string]sdk.Tool{}}
	doc := hostdocview.New(hostdocview.Options{})
	ctx.svc["ctx.doc"] = doc
	ctx.svc["ctx.tools"] = tools
	p := &Plugin{}
	if _, err := p.Start(ctx, &sdk.Manifest{ID: "tool-doc", Data: data}); err != nil {
		t.Fatal(err)
	}
	return ctx, tools
}

func runTool(t *testing.T, tools *stubTools, name, args string) map[string]any {
	t.Helper()
	res, err := tools.Execute(context.Background(), name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("%s 返回非 JSON: %v (%s)", name, err, res.Content)
	}
	return out
}

func TestToolDocRegistration(t *testing.T) {
	_, tools := newEnv(t, nil)
	for _, name := range []string{"read_document", "doc_open", "doc_list"} {
		if _, ok := tools.Get(name); !ok {
			t.Fatalf("缺少工具 %s", name)
		}
	}
}

func TestReadDocument(t *testing.T) {
	_, tools := newEnv(t, nil)
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	if err := os.WriteFile(p, []byte("# 标题\n\n段落一\n\n段落二\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runTool(t, tools, "read_document", fmt.Sprintf(`{"file_path":%q}`, p))
	if out["format"] != "markdown" {
		t.Fatalf("格式异常: %v", out["format"])
	}
	lines, _ := out["lines"].([]any)
	if len(lines) == 0 {
		t.Fatalf("应返回行: %+v", out)
	}
	first, _ := lines[0].(map[string]any)
	if first["number"].(float64) != 1 {
		t.Fatalf("行号应从 1 开始: %+v", first)
	}
	// 分页:offset/limit
	out2 := runTool(t, tools, "read_document", fmt.Sprintf(`{"file_path":%q,"offset":1,"limit":1}`, p))
	lines2, _ := out2["lines"].([]any)
	if len(lines2) != 1 || out2["offset"].(float64) != 1 {
		t.Fatalf("分页未生效: %+v", out2)
	}
	// 缺参数
	if _, err := tools.Execute(context.Background(), "read_document", `{}`); err == nil {
		t.Fatal("缺 file_path 应报错")
	}
	if _, err := tools.Execute(context.Background(), "read_document", `{bad json`); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
	if _, err := tools.Execute(context.Background(), "read_document", fmt.Sprintf(`{"file_path":%q}`, filepath.Join(dir, "nope.md"))); err == nil {
		t.Fatal("不存在文件应报错(结构化)")
	}
}

// 输出字节预算:超过 max_output_kb 时截断并提示。
func TestReadDocumentOutputBudget(t *testing.T) {
	_, tools := newEnv(t, map[string]any{"max_output_kb": 1})
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		sb.WriteString(strings.Repeat("x", 100))
		sb.WriteString("\n")
	}
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runTool(t, tools, "read_document", fmt.Sprintf(`{"file_path":%q,"limit":500}`, p))
	if out["truncatedByBytes"] != true {
		t.Fatalf("应标记截断: %+v", out)
	}
	if out["note"] == nil {
		t.Fatalf("应给出续读提示: %+v", out)
	}
}

func TestDocOpenEmitsEvent(t *testing.T) {
	ctx, tools := newEnv(t, nil)
	dir := t.TempDir()
	p := filepath.Join(dir, "b.md")
	if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runTool(t, tools, "doc_open", fmt.Sprintf(`{"file_path":%q,"page":0}`, p))
	if out["ok"] != true {
		t.Fatalf("应成功: %+v", out)
	}
	if len(ctx.events) != 1 || ctx.events[0] != sdk.EventDocOpen {
		t.Fatalf("应发出 doc/open: %v", ctx.events)
	}
	// 失败意图不广播:不存在 / 不支持格式 / 缺参数
	legacy := filepath.Join(dir, "old.doc")
	if err := os.WriteFile(legacy, []byte("D0CF11E0"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{
		fmt.Sprintf(`{"file_path":%q}`, filepath.Join(dir, "nope.md")),
		fmt.Sprintf(`{"file_path":%q}`, legacy),
		`{}`,
	} {
		if _, err := tools.Execute(context.Background(), "doc_open", args); err == nil {
			t.Fatalf("应失败: %s", args)
		}
	}
	if len(ctx.events) != 1 {
		t.Fatalf("失败意图不应广播: %v", ctx.events)
	}
}

func TestDocList(t *testing.T) {
	_, tools := newEnv(t, nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.csv"), []byte("a,b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runTool(t, tools, "doc_list", fmt.Sprintf(`{"path":%q}`, dir))
	entries, _ := out["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("条目数异常: %+v", entries)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		m := e.(map[string]any)
		seen[m["path"].(string)] = true
		if m["previewable"] != true {
			t.Fatalf("可预览标记缺失: %+v", m)
		}
	}
	// 默认 path(空)→ 当前工作区根可列举(不报错)
	if _, err := tools.Execute(context.Background(), "doc_list", `{}`); err != nil {
		t.Fatalf("空参数 doc_list 应可用: %v", err)
	}
}
