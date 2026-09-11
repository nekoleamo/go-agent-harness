// 工具执行入口不变式(R10 ②,host-bridge 宿主回调侧):外部插件进程的工具调用
// 只能经宿主注入的 ctx.tools(全流水线)执行 —— 参数原样透传、策略 veto 文本逐字回传。
//
// 与 callback_rpc_test.go 的分工:那里覆盖「外部侧代理 ↔ 宿主」的整链对账(含错误分支);
// 这里只钉「宿主回调分发点必须委派给注入 registry,不得有第二执行路径」这一条不变式。
package hostbridge

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// vetoRegistry 记录 (name, args) 的 registry 替身:注入 veto 结果即模拟 policy-guard
// 在 tools/pre-execute 的拦截(host-tools 契约:veto 经 ToolResult.Error 回传,Go error 为 nil)。
type vetoRegistry struct {
	mu    sync.Mutex
	calls [][2]string
	res   sdk.ToolResult
}

func (r *vetoRegistry) Register(sdk.Tool) sdk.Disposer { return func() {} }

func (r *vetoRegistry) List() []sdk.ToolDefinition {
	return []sdk.ToolDefinition{{Name: "shell"}}
}

func (r *vetoRegistry) Get(name string) (sdk.ToolDefinition, bool) {
	if name != "shell" {
		return sdk.ToolDefinition{}, false
	}
	return sdk.ToolDefinition{Name: "shell"}, true
}

func (r *vetoRegistry) Execute(_ context.Context, name, args string) (*sdk.ToolResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, [2]string{name, args})
	res := r.res
	r.mu.Unlock()
	out := res
	return &out, nil
}

var _ sdk.ToolRegistry = (*vetoRegistry)(nil)

// TestCallbackToolExecuteDelegatesToInjectedRegistry 宿主回调 tools.execute 经注入 registry
// 执行:名字与参数原样下发(外部进程不能自选执行路径,也没有绕过 registry 的入口)。
func TestCallbackToolExecuteDelegatesToInjectedRegistry(t *testing.T) {
	reg := &vetoRegistry{res: sdk.ToolResult{Content: `{"output":"ok"}`}}
	cb := NewCallback(reg, nil, nil, "tok")

	args, err := json.Marshal(map[string]string{"Name": "shell", "Args": `{"command":"echo hi"}`})
	if err != nil {
		t.Fatal(err)
	}
	var reply string
	if err := cb.Call(CallArgs{Service: "tools", Method: "execute", Args: string(args), Token: "tok"}, &reply); err != nil {
		t.Fatal(err)
	}
	reg.mu.Lock()
	calls := append([][2]string(nil), reg.calls...)
	reg.mu.Unlock()
	if len(calls) != 1 || calls[0][0] != "shell" || calls[0][1] != `{"command":"echo hi"}` {
		t.Fatalf("宿主回调应经注入 registry 且参数原样: %v", calls)
	}
	var res sdk.ToolResult
	if err := json.Unmarshal([]byte(reply), &res); err != nil || res.Content != `{"output":"ok"}` {
		t.Fatalf("结果应回传 registry 内容: %s(%v)", reply, err)
	}
}

// TestCallbackToolVetoReplyIsVerbatim 策略 veto 文本必须逐字回传外部插件:
// 引擎(如 tool-workflow)据此把「被拦截」告诉模型,自行改写或放弃,而不是当成功继续。
func TestCallbackToolVetoReplyIsVerbatim(t *testing.T) {
	const veto = "blocked: 沙箱拒绝写 workspace 之外的路径"
	reg := &vetoRegistry{res: sdk.ToolResult{Error: veto, Content: "{}"}}
	cb := NewCallback(reg, nil, nil, "")

	args, err := json.Marshal(map[string]string{"Name": "shell", "Args": `{"command":"echo x > /tmp/a"}`})
	if err != nil {
		t.Fatal(err)
	}
	var reply string
	if err := cb.Call(CallArgs{Service: "tools", Method: "execute", Args: string(args)}, &reply); err != nil {
		t.Fatal(err)
	}
	var res sdk.ToolResult
	if err := json.Unmarshal([]byte(reply), &res); err != nil {
		t.Fatalf("回复应可解析: %s(%v)", reply, err)
	}
	if res.Error != veto {
		t.Fatalf("veto 文本应逐字回传: %q", res.Error)
	}
	if calls := len(reg.calls); calls != 1 {
		t.Fatalf("veto 来自 registry 裁决,必须先经 registry: %d", calls)
	}
}

// TestCallbackToolsListDelegatesToInjectedRegistry 工具清单同源(外部插件侧枚举即宿主 registry),
// 未知方法显式报错(不静默成功)。
func TestCallbackToolsListDelegatesToInjectedRegistry(t *testing.T) {
	cb := NewCallback(&vetoRegistry{}, nil, nil, "")
	var reply string
	if err := cb.Call(CallArgs{Service: "tools", Method: "list"}, &reply); err != nil {
		t.Fatal(err)
	}
	var defs []sdk.ToolDefinition
	if err := json.Unmarshal([]byte(reply), &defs); err != nil || len(defs) != 1 || defs[0].Name != "shell" {
		t.Fatalf("tools.list 应取自 registry: %s(%v)", reply, err)
	}
	if err := cb.Call(CallArgs{Service: "tools", Method: "nope"}, &reply); err == nil {
		t.Fatal("未知方法应显式报错")
	}
}
