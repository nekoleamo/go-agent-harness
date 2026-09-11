// sandbox_hint_test.go:有效沙箱档位下传到工具 ctx 的契约(host-tools 侧)。
//
// 为什么单独锚定:host-tools 是**唯一执行入口**(tools/pre-execute → 执行),外部进程
// 插件里的工具拿不到 ctx.sandbox 服务 —— 由本入口把「审批联动后的有效档位」写进 ctx,
// 工具侧(如 tool-shell 的内核级沙箱)才能按档位施加约束。此处断言的是:
// 有效档优先于声明档、无沙箱/取不到时不挂 hint(不猜档位)、失败不影响执行。
package hosttools

import (
	"context"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubSandbox 只读档位替身(不实现 sdk.EffectiveSandbox → 声明档即有效档)。
type stubSandbox struct {
	mode sdk.SandboxMode
	root string
}

func (s *stubSandbox) Mode() sdk.SandboxMode     { return s.mode }
func (s *stubSandbox) SetMode(m sdk.SandboxMode) { s.mode = m }
func (s *stubSandbox) Root() string              { return s.root }
func (s *stubSandbox) ValidatePath(string) error { return nil }

// linkedSandbox 声明档 ≠ 有效档(审批 open/strict 覆盖沙箱档时的真实形态)。
type linkedSandbox struct {
	stubSandbox
	effective sdk.SandboxMode
}

func (s *linkedSandbox) EffectiveMode() sdk.SandboxMode { return s.effective }

// hintTool 记录执行时从 ctx 读到的沙箱档位。
type hintTool struct {
	calls int
	hint  sdk.SandboxHint
	ok    bool
}

func (t *hintTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "probe", Description: "记录沙箱档位", InputSchema: map[string]any{"type": "object"}}
}

func (t *hintTool) Execute(ctx context.Context, _ string) (any, error) {
	t.calls++
	t.hint, t.ok = sdk.SandboxHintOf(ctx)
	return map[string]any{"hinted": t.ok, "mode": string(t.hint.Mode)}, nil
}

func newToolsCtx(t *testing.T) (sdk.Ctx, sdk.ToolRegistry) {
	t.Helper()
	c := newCtx(t)
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	return c, tools
}

// 有效档 ≠ 声明档时必须下发**有效**档(只读 Mode() 会与实际拦截行为不一致)。
func TestExecutePassesEffectiveSandboxHint(t *testing.T) {
	c, tools := newToolsCtx(t)
	sb := &linkedSandbox{stubSandbox{mode: sdk.SandboxWorkspace, root: "/ws/linked"}, sdk.SandboxFullAccess}
	if err := c.Provide("ctx.sandbox", sb); err != nil {
		t.Fatal(err)
	}
	tool := &hintTool{}
	tools.Register(tool)

	res, err := tools.Execute(context.Background(), "probe", "{}")
	if err != nil || res.Error != "" {
		t.Fatalf("执行失败: err=%v res=%+v", err, res)
	}
	if !tool.ok {
		t.Fatal("工具应读到 sandbox hint")
	}
	if tool.hint.Mode != sdk.SandboxFullAccess || tool.hint.Root != "/ws/linked" {
		t.Fatalf("应下发有效档 + workspace 根,got %+v", tool.hint)
	}
}

// 桩/旧实现不实现 EffectiveSandbox 时,声明档即有效档(不得凭空推断联动)。
func TestExecutePassesDeclaredModeWithoutEffectiveSandbox(t *testing.T) {
	c, tools := newToolsCtx(t)
	if err := c.Provide("ctx.sandbox", &stubSandbox{mode: sdk.SandboxReadOnly, root: "/ws/decl"}); err != nil {
		t.Fatal(err)
	}
	tool := &hintTool{}
	tools.Register(tool)

	if _, err := tools.Execute(context.Background(), "probe", "{}"); err != nil {
		t.Fatal(err)
	}
	if !tool.ok || tool.hint.Mode != sdk.SandboxReadOnly || tool.hint.Root != "/ws/decl" {
		t.Fatalf("无 EffectiveSandbox 时应下发声明档,got ok=%v %+v", tool.ok, tool.hint)
	}
}

// 未装配沙箱:不挂 hint(工具不得假定档位),且执行本身不受影响。
func TestExecuteWithoutSandboxServiceNoHint(t *testing.T) {
	_, tools := newToolsCtx(t)
	tool := &hintTool{}
	tools.Register(tool)

	res, err := tools.Execute(context.Background(), "probe", "{}")
	if err != nil || res.Error != "" {
		t.Fatalf("未装配沙箱不应影响执行: err=%v res=%+v", err, res)
	}
	if tool.calls != 1 || tool.ok {
		t.Fatalf("应执行一次且无 hint,got calls=%d ok=%v", tool.calls, tool.ok)
	}
}

// 沙箱服务类型不符(Inject 失败):同样不挂 hint,不阻塞执行。
func TestExecuteSandboxInjectErrorNoHint(t *testing.T) {
	c, tools := newToolsCtx(t)
	if err := c.Provide("ctx.sandbox", 42); err != nil { // 非 sdk.Sandbox → Inject 类型不符
		t.Fatal(err)
	}
	tool := &hintTool{}
	tools.Register(tool)

	res, err := tools.Execute(context.Background(), "probe", "{}")
	if err != nil || res.Error != "" {
		t.Fatalf("解析失败不应影响执行: err=%v res=%+v", err, res)
	}
	if tool.ok {
		t.Fatal("解析失败时不得下发 hint")
	}
}

// 档位为空(实现异常):不挂 hint —— 空档位等于「未知」,不能放行给工具猜。
func TestExecuteEmptyModeNoHint(t *testing.T) {
	c, tools := newToolsCtx(t)
	if err := c.Provide("ctx.sandbox", &stubSandbox{root: "/ws/empty"}); err != nil {
		t.Fatal(err)
	}
	tool := &hintTool{}
	tools.Register(tool)

	if _, err := tools.Execute(context.Background(), "probe", "{}"); err != nil {
		t.Fatal(err)
	}
	if tool.ok {
		t.Fatalf("空档位不得下发 hint,got %+v", tool.hint)
	}
}

// 档位运行期可变:同一注册表连续两次执行必须读到**当次**档位(不得缓存首值)。
func TestExecuteReadsSandboxModePerCall(t *testing.T) {
	c, tools := newToolsCtx(t)
	sb := &stubSandbox{mode: sdk.SandboxWorkspace, root: "/ws/live"}
	if err := c.Provide("ctx.sandbox", sb); err != nil {
		t.Fatal(err)
	}
	tool := &hintTool{}
	tools.Register(tool)

	if _, err := tools.Execute(context.Background(), "probe", "{}"); err != nil {
		t.Fatal(err)
	}
	if tool.hint.Mode != sdk.SandboxWorkspace {
		t.Fatalf("首次应为 workspace-write,got %+v", tool.hint)
	}
	sb.SetMode(sdk.SandboxReadOnly) // 运行期切档(/sandbox 或审批联动)
	if _, err := tools.Execute(context.Background(), "probe", "{}"); err != nil {
		t.Fatal(err)
	}
	if tool.hint.Mode != sdk.SandboxReadOnly {
		t.Fatalf("切档后应读到新档位(不得缓存),got %+v", tool.hint)
	}
}
