package policyapproval

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

// build 装配 host-tools + policy-approval(可选确认服务)。
func build(t *testing.T, confirm sdk.ConfirmService) sdk.Ctx {
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
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&echoTool{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMatchDangerous(t *testing.T) {
	cases := map[string]bool{
		`{"command":"ls -la"}`:           false,
		`{"command":"rm -rf /tmp/x"}`:    true,
		`{"command":"git push -f"}`:      true,
		`{"command":"sudo apt install"}`: true,
		`{"command":"chmod 777 f"}`:      true,
		`{"command":"echo hi"}`:          false,
	}
	for args, want := range cases {
		_, hit := matchDangerous(args)
		if hit != want {
			t.Errorf("args=%s 期望危险=%v,got %v", args, want, hit)
		}
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
	c := build(t, &fakeConfirm{resp: false})
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
	c2 := build(t, &fakeConfirm{resp: true})
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
