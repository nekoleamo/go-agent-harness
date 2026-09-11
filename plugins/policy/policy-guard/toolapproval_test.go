// E-A 工具级审批单测:data.approval_tools 名单 + 三档语义(open/smart/strict)+ 未列出工具不受影响。
//
// 背景:审批链原先只识别 `shell` 的文本危险模式 → 非 shell 但有远程/破坏副作用的工具
// (远程发送类副作用工具)没有统一闸门。本文件锁定补齐后的语义与默认零影响。
package policyguard

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// remoteSendTool 最小远程副作用工具替身(名字刻意不在 shell 危险模式覆盖范围内)。
type remoteSendTool struct{}

func (r *remoteSendTool) Name() string { return "tool-remote-send" }

func (r *remoteSendTool) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	return tools.Register(&remoteSendImpl{}), nil
}

type remoteSendImpl struct{}

func (r *remoteSendImpl) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: "deploy_tool", Description: "远程发送(测试替身)", InputSchema: map[string]any{"type": "object"},
	}
}

func (r *remoteSendImpl) Execute(_ context.Context, args string) (any, error) {
	return map[string]any{"sent": args}, nil
}

// recordingConfirm 记录确认提示文本的确认服务。
type recordingConfirm struct {
	resp    bool
	prompts []string
}

func (r *recordingConfirm) Confirm(_ context.Context, prompt string) (bool, error) {
	r.prompts = append(r.prompts, prompt)
	return r.resp, nil
}

// buildTools 装配 host-tools + policy-guard(data)+ 远程工具替身。
func buildTools(t *testing.T, confirm sdk.ConfirmService, data map[string]any) sdk.Ctx {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	if confirm != nil {
		if err := c.Provide("ctx.confirm", confirm); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	md := &sdk.Manifest{}
	if data != nil {
		md.Data = data
	}
	if _, err := (&Plugin{}).Start(c, md); err != nil {
		t.Fatal(err)
	}
	if _, err := (&remoteSendTool{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	return c
}

// execTool 执行工具并返回结构化结果。
func execTool(t *testing.T, c sdk.Ctx, name, args string) *sdk.ToolResult {
	t.Helper()
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), name, args)
	if err != nil {
		t.Fatalf("流水线应吞 veto 为结构化结果,got err %v", err)
	}
	return res
}

func TestToolApprovalGateAllModes(t *testing.T) {
	const args = `{"to":"alice","text":"部署完成"}`
	listed := map[string]any{"approval_tools": []any{"deploy_tool"}}

	// smart + 用户拒绝 → veto
	rec := &recordingConfirm{resp: false}
	c := buildTools(t, rec, map[string]any{"approval": "smart", "approval_tools": []any{"deploy_tool"}})
	if res := execTool(t, c, "deploy_tool", args); res.Error == "" {
		t.Fatal("smart 档用户拒绝后应拦截工具调用")
	}
	// 确认提示应含工具名与参数摘要(用户据此判断要做什么)
	if len(rec.prompts) != 1 || !strings.Contains(rec.prompts[0], "工具调用 [deploy_tool") ||
		!strings.Contains(rec.prompts[0], "部署完成") {
		t.Fatalf("确认提示应含工具名与参数摘要: %q", rec.prompts)
	}

	// smart + 用户批准 → 放行
	c2 := buildTools(t, &recordingConfirm{resp: true}, map[string]any{"approval": "smart", "approval_tools": []any{"deploy_tool"}})
	if res := execTool(t, c2, "deploy_tool", args); res.Error != "" {
		t.Fatalf("smart 档批准后应放行,got %+v", res)
	}

	// strict + 有确认通道也直接拒绝
	c3 := buildTools(t, &recordingConfirm{resp: true}, map[string]any{"approval": "strict", "approval_tools": []any{"deploy_tool"}})
	if res := execTool(t, c3, "deploy_tool", args); res.Error == "" || !strings.Contains(res.Error, "严格档") {
		t.Fatalf("strict 档应直接拒绝: %+v", res)
	}

	// open + 用户拒绝也放行(开放档语义)
	c4 := buildTools(t, &recordingConfirm{resp: false}, map[string]any{"approval": "open", "approval_tools": []any{"deploy_tool"}})
	if res := execTool(t, c4, "deploy_tool", args); res.Error != "" {
		t.Fatalf("open 档应放行,got %+v", res)
	}

	// 运行期切档对工具级审批同样生效
	var ap sdk.ApprovalService
	if err := c3.Inject("ctx.approval", &ap); err != nil {
		t.Fatal(err)
	}
	ap.SetMode(sdk.ApprovalOpen)
	if res := execTool(t, c3, "deploy_tool", args); res.Error != "" {
		t.Fatalf("切 open 后应放行,got %+v", res)
	}
	_ = listed
}

func TestToolApprovalDefaultOffAndUnlisted(t *testing.T) {
	// 默认(无 data / 空名单):任意工具不受工具级审批影响
	c := buildTools(t, nil, nil)
	if res := execTool(t, c, "deploy_tool", `{}`); res.Error != "" {
		t.Fatalf("默认名单为空时不应拦截: %+v", res)
	}
	var ap sdk.ApprovalService
	if err := c.Inject("ctx.approval", &ap); err != nil {
		t.Fatal(err)
	}
	// 名单只对列出的工具生效
	c2 := buildTools(t, nil, map[string]any{"approval_tools": []any{"other_tool"}})
	if res := execTool(t, c2, "deploy_tool", `{}`); res.Error != "" {
		t.Fatalf("未列出工具不应拦截: %+v", res)
	}
	// shell 危险模式与工具名单相互独立:名单含 shell 时普通命令也要审批
	c3 := buildTools(t, &recordingConfirm{resp: false}, map[string]any{"approval_tools": []any{"shell"}})
	if res := execTool(t, c3, "shell", `{"command":"ls -la"}`); res.Error == "" {
		t.Fatal("名单含 shell 时普通命令也应拦截")
	}
}

func TestToolApprovalNoConfirmChannelDenies(t *testing.T) {
	c := buildTools(t, nil, map[string]any{"approval_tools": []any{"deploy_tool"}})
	res := execTool(t, c, "deploy_tool", `{"to":"a"}`)
	if res.Error == "" || !strings.Contains(res.Error, "无确认通道") {
		t.Fatalf("无确认通道应安全拒绝: %+v", res)
	}
	var ap sdk.ApprovalService
	if err := c.Inject("ctx.approval", &ap); err != nil {
		t.Fatal(err)
	}
	if got := ap.(*ApprovalPolicy).ApprovalTools(); len(got) != 1 || got[0] != "deploy_tool" {
		t.Fatalf("名单快照异常: %+v", got)
	}
	if ap.(*ApprovalPolicy).RequiresToolApproval("") {
		t.Fatal("空工具名不应命中")
	}
}

func TestParseApprovalTools(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"list-any", []any{"a", "b"}, []string{"a", "b"}},
		{"list-string", []string{"a", "b"}, []string{"a", "b"}},
		{"csv", "a,b", []string{"a", "b"}},
		{"whitespace", "a b\tc\nd", []string{"a", "b", "c", "d"}},
		{"mixed-any", []any{"a", 42, "b"}, []string{"a", "b"}},
		{"nil", nil, nil},
		{"empty", []any{}, nil},
		{"blank-items", []any{"", " ", "a"}, []string{"a"}},
	}
	for _, c := range cases {
		got := parseApprovalTools(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("%s: %+v want %+v", c.name, got, c.want)
		}
		for _, w := range c.want {
			if !got[w] {
				t.Fatalf("%s: 缺 %q(得 %+v)", c.name, w, got)
			}
		}
	}
}

func TestArgPreview(t *testing.T) {
	if got := argPreview("  a \n b\tc  ", 120); got != "a b c" {
		t.Fatalf("应压空白: %q", got)
	}
	if got := argPreview("   ", 120); got != "" {
		t.Fatalf("空白应归零: %q", got)
	}
	long := strings.Repeat("字", toolArgPreviewRunes+20)
	got := []rune(argPreview(long, toolArgPreviewRunes))
	if len(got) != toolArgPreviewRunes+1 || got[len(got)-1] != '…' {
		t.Fatalf("应按上限截断: %d", len(got))
	}
	// 上限 0 = 不截断
	if got := argPreview(long, 0); len([]rune(got)) != len([]rune(long)) {
		t.Fatalf("max=0 不应截断: %d", len([]rune(got)))
	}
}
