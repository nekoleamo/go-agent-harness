// NOND-M1-3b 单测:代理工具(声明 sdk.ToolDefinition.ApprovalTargetParam)的逐工具审批。
//
// 背景:MCP search 模式下工具不再进注册表,模型只能经 mcp_call{name:…} 间接调用。
// 若审批只按被调工具名匹配,`data.approval_tools: [mcp_srv_read]` 这类逐工具规则会被
// 一个间接名整体绕过。修法 = 工具自述「真实目标参数名」,宿主审批据此匹配。
package policyguard

import (
	"context"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// proxyPlugin 注册一个代理工具替身:proxy_call{name:…} 代表对另一个工具的调用。
type proxyPlugin struct{}

func (p *proxyPlugin) Name() string { return "tool-proxy-test" }

func (p *proxyPlugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	return tools.Register(&proxyImpl{}), nil
}

type proxyImpl struct{}

func (p *proxyImpl) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:                "proxy_call",
		Description:         "代理调用替身(测试)",
		InputSchema:         map[string]any{"type": "object"},
		ApprovalTargetParam: "name",
	}
}

func (p *proxyImpl) Execute(_ context.Context, args string) (any, error) {
	return map[string]any{"proxied": args}, nil
}

// plainTool 未声明代理参数的工具(对照组:声明缺失时行为必须与改动前一致)。
type plainTool struct{}

func (p *plainTool) Name() string { return "tool-plain-test" }

func (p *plainTool) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	return tools.Register(&plainImpl{}), nil
}

type plainImpl struct{}

func (p *plainImpl) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: "plain_tool", Description: "普通工具替身(测试)",
		InputSchema: map[string]any{"type": "object"},
	}
}

func (p *plainImpl) Execute(_ context.Context, args string) (any, error) {
	return map[string]any{"plain": args}, nil
}

// withProxy 在 buildTools 基础上再挂代理工具替身。
func withProxy(t *testing.T, confirm sdk.ConfirmService, data map[string]any) sdk.Ctx {
	t.Helper()
	c := buildTools(t, confirm, data)
	if _, err := (&proxyPlugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&plainTool{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	return c
}

// 核心回归:按**真实工具名**写的规则在代理路径上同样生效,且确认文案显示代理关系。
func TestProxyToolApprovalMatchesTargetName(t *testing.T) {
	rec := &recordingConfirm{resp: false}
	c := withProxy(t, rec, map[string]any{"approval": "smart", "approval_tools": []any{"inner_tool"}})

	res := execTool(t, c, "proxy_call", `{"name":"inner_tool","arguments":{"p":1}}`)
	if res.Error == "" {
		t.Fatal("真实目标在名单内,代理调用应被拦截")
	}
	if len(rec.prompts) != 1 || !strings.Contains(rec.prompts[0], "proxy_call → inner_tool") {
		t.Fatalf("确认提示应显示代理关系: %q", rec.prompts)
	}

	// 未列出的目标 → 不拦截(名单只对列出的生效)
	rec2 := &recordingConfirm{resp: false}
	c2 := withProxy(t, rec2, map[string]any{"approval": "smart", "approval_tools": []any{"inner_tool"}})
	if res := execTool(t, c2, "proxy_call", `{"name":"other_tool"}`); res.Error != "" {
		t.Fatalf("未列出的目标不应拦截: %+v", res)
	}
	if len(rec2.prompts) != 0 {
		t.Fatalf("未列出的目标不应弹确认: %q", rec2.prompts)
	}
}

// 批准则放行(strict 档的语义与直接调用一致)。
func TestProxyToolApprovalApproveAndStrict(t *testing.T) {
	c := withProxy(t, &recordingConfirm{resp: true}, map[string]any{"approval": "smart", "approval_tools": []any{"inner_tool"}})
	if res := execTool(t, c, "proxy_call", `{"name":"inner_tool"}`); res.Error != "" {
		t.Fatalf("smart 档批准后应放行: %+v", res)
	}

	c2 := withProxy(t, &recordingConfirm{resp: true}, map[string]any{"approval": "strict", "approval_tools": []any{"inner_tool"}})
	res := execTool(t, c2, "proxy_call", `{"name":"inner_tool"}`)
	if res.Error == "" || !strings.Contains(res.Error, "严格档") {
		t.Fatalf("strict 档应直接拒绝: %+v", res)
	}
	if !strings.Contains(res.Error, "inner_tool") {
		t.Fatalf("拒绝文案应指明真实目标: %+v", res)
	}
}

// 代理工具自身也能被名单覆盖(两层规则相互独立)。
func TestProxyToolItselfListable(t *testing.T) {
	rec := &recordingConfirm{resp: false}
	c := withProxy(t, rec, map[string]any{"approval": "smart", "approval_tools": []any{"proxy_call"}})
	if res := execTool(t, c, "proxy_call", `{"name":"anything"}`); res.Error == "" {
		t.Fatal("名单含代理工具本身时应拦截所有代理调用")
	}
}

// 无人值守(定时任务)一律拒:代理路径不得成为绕过口。
func TestProxyToolUnattendedDenies(t *testing.T) {
	c := withProxy(t, &recordingConfirm{resp: true}, map[string]any{"approval": "open", "approval_tools": []any{"inner_tool"}})
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	un := sdk.WithUnattended(context.Background())
	res, err := tools.Execute(un, "proxy_call", `{"name":"inner_tool"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "无人值守") {
		t.Fatalf("无人值守下连 open 档也应拒: %+v", res)
	}
}

// 参数异常/缺 name → 没有真实目标:按代理工具自身名匹配(不误伤,也不静默放行名单内工具)。
func TestProxyToolMalformedArgsFallBackToProxyName(t *testing.T) {
	// 名单只有 inner_tool,参数里给不出目标 → 不拦截
	c := withProxy(t, &recordingConfirm{resp: false}, map[string]any{"approval_tools": []any{"inner_tool"}})
	for _, args := range []string{`{}`, `{"name":""}`, `{"name":"  "}`, `not json`, `{"name":42}`} {
		if res := execTool(t, c, "proxy_call", args); res.Error != "" {
			t.Fatalf("参数 %q 无真实目标时不应拦截: %+v", args, res)
		}
	}
	// 名单含代理工具自身 → 仍拦截(兜底语义)
	c2 := withProxy(t, &recordingConfirm{resp: false}, map[string]any{"approval_tools": []any{"proxy_call"}})
	if res := execTool(t, c2, "proxy_call", `not json`); res.Error == "" {
		t.Fatal("名单含代理工具自身时,参数不可解析也应拦截")
	}
}

// 未声明 ApprovalTargetParam 的工具行为不变(零回归)。
func TestPlainToolUnaffected(t *testing.T) {
	rec := &recordingConfirm{resp: false}
	c := withProxy(t, rec, map[string]any{"approval": "smart", "approval_tools": []any{"inner_tool"}})
	if res := execTool(t, c, "plain_tool", `{"name":"inner_tool"}`); res.Error != "" {
		t.Fatalf("未声明的工具不应被目标名匹配影响: %+v", res)
	}
	if len(rec.prompts) != 0 {
		t.Fatalf("不应弹确认: %q", rec.prompts)
	}

	// 直接单元测试:approvalTarget 只认声明
	c3 := withProxy(t, nil, nil)
	if got := approvalTarget(c3, "proxy_call", `{"name":"inner_tool"}`); got != "inner_tool" {
		t.Fatalf("声明工具应解析出目标,got %q", got)
	}
	if got := approvalTarget(c3, "plain_tool", `{"name":"inner_tool"}`); got != "" {
		t.Fatalf("未声明工具不应解析目标,got %q", got)
	}
	if got := approvalTarget(c3, "not_registered", `{"name":"x"}`); got != "" {
		t.Fatalf("未注册工具不应解析目标,got %q", got)
	}
	if got := approvalTarget(c3, "proxy_call", ``); got != "" {
		t.Fatalf("空参数不应解析目标,got %q", got)
	}
}
