package policyguard

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeConfirm 可编程确认实现。
type fakeConfirm struct {
	resp bool
	err  error
}

func (f *fakeConfirm) Confirm(ctx context.Context, prompt string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.resp, nil
}

// build 装配 host-tools + policy-guard(可选确认服务;审批/沙箱档位由 manifest data 指定)。
func build(t *testing.T, confirm sdk.ConfirmService, data ...map[string]any) sdk.Ctx {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if confirm != nil {
		if err := c.Provide("ctx.confirm", confirm); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	md := &sdk.Manifest{}
	if len(data) > 0 {
		md.Data = data[0]
	}
	if _, err := (&Plugin{}).Start(c, md); err != nil {
		t.Fatal(err)
	}
	if _, err := (&echoTool{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMatchDangerous(t *testing.T) {
	cases := map[string]bool{
		`{"command":"ls -la"}`:                       false,
		`{"command":"rm -rf /tmp/x"}`:                true,
		`{"command":"rmdir /tmp/d"}`:                 true, // IM 真机反馈:rmdir 此前漏网
		`{"command":"rmdir -p a/b"}`:                 true,
		`{"command":"rm -f /tmp/x"}`:                 true,
		`{"command":"unlink /tmp/f"}`:                true,
		`{"command":"shred -u /tmp/x"}`:              true,
		`{"command":"truncate -s 0 f"}`:              true,
		`{"command":"find / -name '*.log' -delete"}`: true,
		`{"command":"git push -f"}`:                  true,
		`{"command":"git push --force-with-lease"}`:  true,
		`{"command":"git reset --hard HEAD~1"}`:      true,
		`{"command":"git clean -fd"}`:                true,
		`{"command":"git push origin main"}`:         false,
		`{"command":"git reset --soft HEAD~1"}`:      false,
		`{"command":"sudo apt install"}`:             true,
		`{"command":"pkexec ls"}`:                    true,
		`{"command":"chmod 777 f"}`:                  true,
		`{"command":"chmod -R 777 dir"}`:             true,
		`{"command":"chmod 644 f"}`:                  false,
		`{"command":"chmod +x f"}`:                   false,
		`{"command":"echo hi"}`:                      false,
		`{"command":"rm -v /tmp/f"}`:                 true, // IM 反馈:rm -v 此前漏网
		`{"command":"rm data.txt"}`:                  true, // 裸 rm(任意形态删除均确认)
	}
	for args, want := range cases {
		_, hit := matchDangerous(args)
		if hit != want {
			t.Errorf("args=%s 期望危险=%v,got %v", args, want, hit)
		}
	}
}

func TestApprovalModes(t *testing.T) {
	exec := func(c sdk.Ctx) sdk.ToolResult {
		t.Helper()
		var tools sdk.ToolRegistry
		if err := c.Inject("ctx.tools", &tools); err != nil {
			t.Fatal(err)
		}
		res, err := tools.Execute(context.Background(), "shell", `{"command":"rm -rf /tmp/x"}`)
		if err != nil {
			t.Fatal(err)
		}
		return *res
	}

	// strict:直接拒绝(确认服务在场也拒绝,allowlist 不生效语义=每次拦截)
	c := build(t, &fakeConfirm{resp: true}, map[string]any{"approval": "strict"})
	var ap sdk.ApprovalService
	if err := c.Inject("ctx.approval", &ap); err != nil {
		t.Fatal("ctx.approval 未提供")
	}
	if res := exec(c); res.Error == "" {
		t.Fatal("strict 档下危险操作应被直接拒绝")
	}

	// open:直接放行
	c2 := build(t, &fakeConfirm{resp: true}, map[string]any{"approval": "open"})
	var ap2 sdk.ApprovalService
	if err := c2.Inject("ctx.approval", &ap2); err != nil {
		t.Fatal(err)
	}
	if res := exec(c2); res.Error != "" {
		t.Fatalf("open 档下危险操作应放行,got %+v", res)
	}

	// smart:命中弹确认,用户拒绝则拦截
	c3 := build(t, &fakeConfirm{resp: false}, map[string]any{"approval": "smart"})
	var ap3 sdk.ApprovalService
	if err := c3.Inject("ctx.approval", &ap3); err != nil {
		t.Fatal(err)
	}
	if res := exec(c3); res.Error == "" {
		t.Fatal("smart 档用户拒绝后应拦截")
	}

	// 运行期切档生效:strict → open 切换后放行
	ap3.SetMode(sdk.ApprovalOpen)
	if res := exec(c3); res.Error != "" {
		t.Fatalf("切 open 档后应放行,got %+v", res)
	}
}

func TestDefaultModeSmart(t *testing.T) {
	// 无 manifest data 时默认 smart:非危险命令放行,危险命令无确认通道拒绝
	c := build(t, nil)
	var ap sdk.ApprovalService
	if err := c.Inject("ctx.approval", &ap); err != nil {
		t.Fatal(err)
	}
	if ap.Mode() != sdk.ApprovalSmart {
		t.Fatalf("默认档应为 smart,got %s", ap.Mode())
	}
}

func TestNoConfirmServiceDenies(t *testing.T) {
	c := build(t, nil)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "shell", `{"command":"rm -rf /tmp/x"}`)
	if err != nil {
		t.Fatalf("流水线应吞 veto 为结构化结果,got err %v", err)
	}
	if res.Error == "" {
		t.Fatal("无确认通道时危险操作应被拒绝")
	}
}

func TestConfirmDenyAndAllow(t *testing.T) {
	// 用户拒绝
	c := build(t, &fakeConfirm{resp: false}, map[string]any{"approval": "smart"})
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "shell", `{"command":"rm -rf /tmp/x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == "" {
		t.Fatal("用户拒绝后应拦截")
	}

	// 用户允许
	c2 := build(t, &fakeConfirm{resp: true}, map[string]any{"approval": "smart"})
	var tools2 sdk.ToolRegistry
	if err := c2.Inject("ctx.tools", &tools2); err != nil {
		t.Fatal(err)
	}
	res2, err := tools2.Execute(context.Background(), "shell", `{"command":"rm -rf /tmp/x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Error != "" {
		t.Fatalf("用户允许后应执行,got %+v", res2)
	}
}

// echoTool 最小工具插件:注册 echo 型 shell 工具(执行直接返回)。
type echoTool struct{}

func (e *echoTool) Name() string { return "tool-echo" }
func (e *echoTool) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	d := tools.Register(&echoToolImpl{})
	return d, nil
}

func (e *echoTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "shell", Description: "echo", InputSchema: map[string]any{"type": "object"}}
}

type echoToolImpl struct{}

func (e *echoToolImpl) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "shell", Description: "echo", InputSchema: map[string]any{"type": "object"}}
}

func (e *echoToolImpl) Execute(ctx context.Context, args string) (any, error) {
	return map[string]any{"echo": args}, nil
}

var _ = errors.Is // 保留引用
