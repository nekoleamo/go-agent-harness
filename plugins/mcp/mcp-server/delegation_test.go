// 工具执行入口不变式(R10 ②,mcp-server 侧):外部 MCP 客户端的 tools/call 必须经
// 注入的 ctx.tools(全流水线)执行;策略 veto 以 isError 回传,不得当成功返回。
package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubRegistry 记录调用的 registry 替身(可注入 veto 结果)。
type stubRegistry struct {
	mu    sync.Mutex
	calls []string
	res   sdk.ToolResult
}

func (r *stubRegistry) Register(sdk.Tool) sdk.Disposer { return func() {} }

func (r *stubRegistry) List() []sdk.ToolDefinition {
	return []sdk.ToolDefinition{{Name: "shell", Description: "执行 shell", InputSchema: map[string]any{"type": "object"}}}
}

func (r *stubRegistry) Get(name string) (sdk.ToolDefinition, bool) {
	for _, d := range r.List() {
		if d.Name == name {
			return d, true
		}
	}
	return sdk.ToolDefinition{}, false
}

func (r *stubRegistry) Execute(_ context.Context, name, args string) (*sdk.ToolResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, name+"|"+args)
	res := r.res
	r.mu.Unlock()
	return &res, nil
}

func (r *stubRegistry) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

var _ sdk.ToolRegistry = (*stubRegistry)(nil)

// serveCounted 以给定 registry 注入协议流并返回响应行(复用 mcp_test.go 的缓冲/解析器)。
func serveCounted(t *testing.T, reg sdk.ToolRegistry, stream string, want int) map[string]resp {
	t.Helper()
	var buf lockedBuf
	out = &buf
	in = strings.NewReader(stream)
	done := make(chan struct{})
	go serve(reg, in, &buf, done)
	lines := waitN(t, &buf, want)
	close(done)
	return parseResp(t, lines)
}

// TestToolsCallGoesThroughInjectedRegistry tools/call 经注入 registry 执行,rpcErr 与
// 业务结果语义不变(不做私有执行路径)。
func TestToolsCallGoesThroughInjectedRegistry(t *testing.T) {
	reg := &stubRegistry{res: sdk.ToolResult{Content: `{"output":"ok"}`}}
	m := serveCounted(t, reg, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"shell","arguments":{"command":"echo hi"}}}`+"\n", 1)
	if len(m["1"].Result) == 0 {
		t.Fatalf("应有结果响应: %+v", m)
	}
	calls := reg.snapshot()
	if len(calls) != 1 || calls[0] != `shell|{"command":"echo hi"}` {
		t.Fatalf("应经注入 registry 执行且参数原样: %v", calls)
	}
	var cl struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(m["1"].Result, &cl); err != nil || len(cl.Content) != 1 || !strings.Contains(cl.Content[0].Text, "ok") {
		t.Fatalf("结果应回传 registry 内容: %s", m["1"].Result)
	}
}

// TestToolsCallVetoIsError 策略 veto(结果 Error = "blocked: ...")必须以 isError=true
// 回传外部客户端:否则 MCP 客户端会当成功继续。
func TestToolsCallVetoIsError(t *testing.T) {
	reg := &stubRegistry{res: sdk.ToolResult{Error: "blocked: 沙箱拒绝写 workspace 外路径", Content: "{}"}}
	m := serveCounted(t, reg, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"shell","arguments":{"command":"echo x > /tmp/a"}}}`+"\n", 1)
	if len(reg.snapshot()) != 1 {
		t.Fatalf("veto 是 registry 的裁决,必须先经 registry: %v", reg.snapshot())
	}
	var te struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(m["7"].Result, &te); err != nil {
		t.Fatalf("结果不可解析: %s(%v)", m["7"].Result, err)
	}
	if !te.IsError {
		t.Fatalf("veto 应标 isError: %s", m["7"].Result)
	}
	if len(te.Content) != 1 || !strings.Contains(te.Content[0].Text, "blocked: 沙箱拒绝") {
		t.Fatalf("veto 文本应逐字回传: %s", m["7"].Result)
	}
}

// TestToolsListComesFromInjectedRegistry 工具清单同源(模型可见面 = registry.List),
// 插拔后清单立即反映(不缓存副本)。
func TestToolsListComesFromInjectedRegistry(t *testing.T) {
	reg := &stubRegistry{}
	m := serveCounted(t, reg, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`+"\n", 1)
	var lst struct {
		Tools []toolDef `json:"tools"`
	}
	if err := json.Unmarshal(m["2"].Result, &lst); err != nil || len(lst.Tools) != 1 || lst.Tools[0].Name != "shell" {
		t.Fatalf("tools/list 应取自 registry: %s", m["2"].Result)
	}
}
