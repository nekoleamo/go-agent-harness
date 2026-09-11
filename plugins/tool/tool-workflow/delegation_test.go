// 工具执行入口不变式(R10 ②,tool-workflow 侧):脚本内的工具函数必须经注入的
// ctx.tools(全流水线)执行 —— 绝不直接调用工具实现,否则沙箱/审批静默失效。
package toolworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// recordingTools 记录调用的 registry 替身(可注入 veto 结果/错误)。
type recordingTools struct {
	mu    sync.Mutex
	calls []string // "name|args"
	defs  []sdk.ToolDefinition
	res   sdk.ToolResult
	err   error
}

func (r *recordingTools) Register(sdk.Tool) sdk.Disposer { return func() {} }

func (r *recordingTools) List() []sdk.ToolDefinition {
	if r.defs != nil {
		return r.defs
	}
	return []sdk.ToolDefinition{{Name: "shell"}, {Name: "read_file"}}
}

func (r *recordingTools) Get(name string) (sdk.ToolDefinition, bool) {
	for _, d := range r.List() {
		if d.Name == name {
			return d, true
		}
	}
	return sdk.ToolDefinition{}, false
}

func (r *recordingTools) Execute(_ context.Context, name, args string) (*sdk.ToolResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, name+"|"+args)
	res, err := r.res, r.err
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := res
	return &out, nil
}

func (r *recordingTools) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

var _ sdk.ToolRegistry = (*recordingTools)(nil)

// TestWorkflowToolCallsGoThroughInjectedRegistry 脚本内工具调用经注入 registry,
// 参数按 JSON 原样下发(kwargs 与单位置 command 两种写法)。
func TestWorkflowToolCallsGoThroughInjectedRegistry(t *testing.T) {
	reg := &recordingTools{res: sdk.ToolResult{Content: `{"output":"ok"}`}}
	w := NewExecutor(reg, nil, nil, slog.New(slog.DiscardHandler), nil)

	out, err := w.Run(context.Background(), `result = shell({"command": "echo hi"})`)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := out.(map[string]any); !ok || m["output"] != "ok" {
		t.Fatalf("脚本应取回 registry 结果: %#v", out)
	}
	calls := reg.snapshot()
	if len(calls) != 1 || calls[0] != `shell|{"command":"echo hi"}` {
		t.Fatalf("应经注入 registry 执行且参数原样: %v", calls)
	}
	// 单位置字符串简写 → 归一为 {"command": ...}
	if _, err := w.Run(context.Background(), `result = read_file("a.txt")`); err != nil {
		t.Fatal(err)
	}
	calls = reg.snapshot()
	if len(calls) != 2 || calls[1] != `read_file|{"command":"a.txt"}` {
		t.Fatalf("单位置参数应归一为 command 并原样下发: %v", calls)
	}
}

// TestWorkflowSurfacesPolicyVeto 策略 veto(结果 Error = "blocked: ...")必须以错误文本
// 回给脚本,不得被吞成成功。
func TestWorkflowSurfacesPolicyVeto(t *testing.T) {
	reg := &recordingTools{res: sdk.ToolResult{Error: "blocked: 沙箱拒绝写 workspace 外路径", Content: "{}"}}
	w := NewExecutor(reg, nil, nil, slog.New(slog.DiscardHandler), nil)

	out, err := w.Run(context.Background(), `result = shell({"command": "echo x > /tmp/a"})`)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("veto 应回错误对象: %#v", out)
	}
	msg, _ := m["error"].(string)
	if !strings.Contains(msg, "blocked: 沙箱拒绝") {
		t.Fatalf("veto 文本应透传脚本: %#v", out)
	}
	// 证据链:registry 确实被调用过(veto 是 registry 的裁决,不是 workflow 自己拦的)
	if calls := reg.snapshot(); len(calls) != 1 {
		t.Fatalf("应经 registry 执行一次: %v", calls)
	}
	// 经 Execute 走完整入口(JSON 序列化路径)同样保留 veto 文本
	raw, err := w.Execute(context.Background(), `{"script":"result = shell({\"command\":\"x\"})"}`)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "blocked: 沙箱拒绝") {
		t.Fatalf("Execute 入口应保留 veto 文本: %s", b)
	}
}

// TestWorkflowSurfacesRegistryError registry 返回 Go error 时同样回给脚本(不静默)。
func TestWorkflowSurfacesRegistryError(t *testing.T) {
	reg := &recordingTools{err: errors.New("流水线故障")}
	w := NewExecutor(reg, nil, nil, slog.New(slog.DiscardHandler), nil)
	out, err := w.Run(context.Background(), `result = shell({"command": "x"})`)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := out.(map[string]any)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "流水线故障") {
		t.Fatalf("registry 错误应透传: %#v", out)
	}
}

// TestWorkflowNestedWorkflowBlocked 嵌套 workflow 不可达:引擎不把 workflow/workflow_collect
// 暴露为脚本函数(先于调用的强制),故不存在绕过外层入口再起一层的路径;registry 零调用。
func TestWorkflowNestedWorkflowBlocked(t *testing.T) {
	reg := &recordingTools{defs: []sdk.ToolDefinition{{Name: "workflow"}, {Name: "shell"}}}
	w := NewExecutor(reg, nil, nil, slog.New(slog.DiscardHandler), nil)
	_, err := w.Run(context.Background(), `result = workflow({"script": "result = 1"})`)
	if err == nil || !strings.Contains(err.Error(), "undefined: workflow") {
		t.Fatalf("嵌套应被禁用(未暴露为函数): %v", err)
	}
	if calls := reg.snapshot(); len(calls) != 0 {
		t.Fatalf("禁用判定应发生在 registry 调用之前: %v", calls)
	}
}
