// sandbox_hint_test.go:有效沙箱档位跨进程桥协议的往返契约(host-bridge 两侧)。
//
// 为什么单独锚定:插件进程拿不到 ctx.sandbox 服务,档位只能经协议字段下传 —— 客户端
// 漏填 → 插件退化为协作式控制(安全能力静默失效);服务端漏挂 → 插件读不到档位;
// 字段为空时若「猜」档位 → 旧宿主被误加约束(破坏兼容)。三处都属于跨进程静默漂移,
// 故以真实结构体往返 + 两侧桩把语义钉死。
package hostbridge

import (
	"context"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// hintStubTool 记录执行时从 ctx 读到的沙箱档位(服务端侧替身)。
type hintStubTool struct {
	name  string
	calls int
	hint  sdk.SandboxHint
	ok    bool
}

func (t *hintStubTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: t.name, Description: "记录沙箱档位", InputSchema: map[string]any{"type": "object"}}
}

func (t *hintStubTool) Execute(ctx context.Context, _ string) (any, error) {
	t.calls++
	t.hint, t.ok = sdk.SandboxHintOf(ctx)
	return map[string]any{"hinted": t.ok}, nil
}

// —— 宿主侧(客户端):ctx 上的 hint 必须落到协议字段 ——

func TestBridgeClientForwardsSandboxHintNamed(t *testing.T) {
	p := &fakeMultiPlugin{named: ExecReply{Content: `"ok"`}}
	env := newFakeBridgeEnv(t, serveFake(t, p), "echo", sdk.ToolDefinition{Name: "echo"})
	ctx := sdk.WithSandboxHint(context.Background(), sdk.SandboxHint{Mode: sdk.SandboxWorkspace, Root: "/ws/named"})

	if _, err := env.toolOf(t, "echo").Execute(ctx, `{"a":1}`); err != nil {
		t.Fatal(err)
	}
	if p.lastArgs.SandboxMode != string(sdk.SandboxWorkspace) || p.lastArgs.WorkspaceRoot != "/ws/named" {
		t.Fatalf("有效档应随调用下传,got mode=%q root=%q", p.lastArgs.SandboxMode, p.lastArgs.WorkspaceRoot)
	}
	// 既有契约不回归:参数原样 + CallID 照常携带
	if p.lastArgs.JSONArgs != `{"a":1}` || p.lastArgs.CallID == "" || p.lastArgs.Name != "echo" {
		t.Fatalf("既有协议字段不应受影响: %+v", p.lastArgs)
	}
}

// 旧单工具协议回退路径同样携带档位(否则旧插件形态下安全能力静默缺失)。
func TestBridgeClientForwardsSandboxHintOnLegacyFallback(t *testing.T) {
	p := &fakeLegacyPlugin{
		def:   sdk.ToolDefinition{Name: "legacy"},
		reply: ExecReply{Content: `"ok"`},
	}
	env := newFakeBridgeEnv(t, serveFake(t, p), "legacy", sdk.ToolDefinition{Name: "legacy"})
	ctx := sdk.WithSandboxHint(context.Background(), sdk.SandboxHint{Mode: sdk.SandboxFullAccess, Root: "/ws/legacy"})

	if _, err := env.toolOf(t, "legacy").Execute(ctx, `{}`); err != nil {
		t.Fatal(err)
	}
	if len(p.calls) != 1 || p.calls[0] != "Execute" {
		t.Fatalf("应走旧协议回退,got %v", p.calls)
	}
	if p.lastArg.SandboxMode != string(sdk.SandboxFullAccess) || p.lastArg.WorkspaceRoot != "/ws/legacy" {
		t.Fatalf("回退路径也应携带档位,got mode=%q root=%q", p.lastArg.SandboxMode, p.lastArg.WorkspaceRoot)
	}
}

// 未注入 hint(旧宿主/直连测试):两字段必须留空,不得凭空填档位。
func TestBridgeClientWithoutHintSendsEmptyFields(t *testing.T) {
	p := &fakeMultiPlugin{named: ExecReply{Content: `"ok"`}}
	env := newFakeBridgeEnv(t, serveFake(t, p), "echo", sdk.ToolDefinition{Name: "echo"})

	if _, err := env.exec(t, "echo", `{}`); err != nil {
		t.Fatal(err)
	}
	if p.lastArgs.SandboxMode != "" || p.lastArgs.WorkspaceRoot != "" {
		t.Fatalf("无 hint 时不得填档位,got mode=%q root=%q", p.lastArgs.SandboxMode, p.lastArgs.WorkspaceRoot)
	}
}

// 空档位 = 未知:即使 hint 存在也不下传(否则插件会按空档位误判)。
func TestSandboxHintFieldsEmptyMode(t *testing.T) {
	cases := []struct {
		name     string
		ctx      context.Context
		wantMode string
		wantRoot string
	}{
		{"无 hint", context.Background(), "", ""},
		{"空档位", sdk.WithSandboxHint(context.Background(), sdk.SandboxHint{Root: "/ws"}), "", ""},
		{"只读档", sdk.WithSandboxHint(context.Background(), sdk.SandboxHint{Mode: sdk.SandboxReadOnly}), "read-only", ""},
		{"workspace+root", sdk.WithSandboxHint(context.Background(), sdk.SandboxHint{Mode: sdk.SandboxWorkspace, Root: "/ws"}), "workspace-write", "/ws"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, root := sandboxHintFields(tc.ctx)
			if mode != tc.wantMode || root != tc.wantRoot {
				t.Fatalf("got mode=%q root=%q,want mode=%q root=%q", mode, root, tc.wantMode, tc.wantRoot)
			}
		})
	}
}

// —— 插件侧(服务端):协议字段必须挂回 ctx ——

func TestToolServerExecuteNamedPassesSandboxHint(t *testing.T) {
	tool := &hintStubTool{name: "x"}
	srv := newToolServer(map[string]sdk.Tool{"x": tool}, nil)
	var reply ExecReply

	err := srv.ExecuteNamed(&ExecNamedArgs{
		Name: "x", JSONArgs: `{}`,
		SandboxMode: string(sdk.SandboxReadOnly), WorkspaceRoot: "/ws/server",
	}, &reply)
	if err != nil || reply.Error != "" {
		t.Fatalf("执行失败: err=%v reply=%+v", err, reply)
	}
	if !tool.ok || tool.hint.Mode != sdk.SandboxReadOnly || tool.hint.Root != "/ws/server" {
		t.Fatalf("插件侧应读到下传档位,got ok=%v %+v", tool.ok, tool.hint)
	}
}

// 旧协议(单工具)路径同样挂档位。
func TestToolServerExecuteLegacyPassesSandboxHint(t *testing.T) {
	tool := &hintStubTool{name: "only"}
	srv := newToolServer(map[string]sdk.Tool{"only": tool}, nil)
	var reply ExecReply

	err := srv.Execute(&ExecArgs{
		JSONArgs:    `{}`,
		SandboxMode: string(sdk.SandboxFullAccess), WorkspaceRoot: "/ws/legacy",
	}, &reply)
	if err != nil || reply.Error != "" {
		t.Fatalf("执行失败: err=%v reply=%+v", err, reply)
	}
	if !tool.ok || tool.hint.Mode != sdk.SandboxFullAccess || tool.hint.Root != "/ws/legacy" {
		t.Fatalf("旧协议路径应读到档位,got ok=%v %+v", tool.ok, tool.hint)
	}
}

// 字段为空 = 旧宿主:不挂 hint(不猜档位),且执行照常。
func TestToolServerEmptySandboxModeNoHint(t *testing.T) {
	tool := &hintStubTool{name: "x"}
	srv := newToolServer(map[string]sdk.Tool{"x": tool}, nil)
	var reply ExecReply

	if err := srv.ExecuteNamed(&ExecNamedArgs{Name: "x", JSONArgs: `{}`}, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != "" || tool.calls != 1 {
		t.Fatalf("空档位不应影响执行: reply=%+v calls=%d", reply, tool.calls)
	}
	if tool.ok {
		t.Fatalf("空档位不得挂 hint,got %+v", tool.hint)
	}
}

// withSandboxHint 单元契约:空档位不改 ctx;非空档位置入可读回的值。
func TestWithSandboxHintHelper(t *testing.T) {
	base := context.Background()
	if got := withSandboxHint(base, callMeta{}); sdkHintOK(got) {
		t.Fatal("空档位不应挂 hint")
	}

	got := withSandboxHint(base, callMeta{sandboxMode: string(sdk.SandboxWorkspace), workspaceRoot: "/ws/h"})
	h, ok := sdk.SandboxHintOf(got)
	if !ok || h.Mode != sdk.SandboxWorkspace || h.Root != "/ws/h" {
		t.Fatalf("应挂入下传档位,got ok=%v %+v", ok, h)
	}
	if sdkHintOK(base) {
		t.Fatal("不得污染入参 ctx")
	}
}

// sdkHintOK 判定 ctx 上是否存在沙箱档位。
func sdkHintOK(ctx context.Context) bool {
	_, ok := sdk.SandboxHintOf(ctx)
	return ok
}
