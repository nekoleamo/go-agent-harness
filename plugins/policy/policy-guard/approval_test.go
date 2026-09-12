package policyguard

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
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
		`{"command":"rmdir /tmp/d"}`:                 true, // 真机反馈:rmdir 此前漏网
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
		`{"command":"rm -v /tmp/f"}`:                 true, // 真机反馈:rm -v 此前漏网
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

	// 用户允许(沙箱取 full-access 以隔离本用例的两层:审批层放行 ≠ 放开沙箱档位,
	// 默认 workspace-write 下 /tmp 写目标会另被 shell 路径裁决拒绝,见 shellguard_test.go)
	c2 := build(t, &fakeConfirm{resp: true}, map[string]any{"approval": "smart", "sandbox": "full-access"})
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

// countingConfirm 记录确认调用次数:无人值守时**必须一次都不问**(没人在场,
// 问了也只是把拒绝拖到超时,还会在 UI 上弹一个没人看的窗)。
type countingConfirm struct {
	mu    sync.Mutex
	calls int
	resp  bool
}

func (c *countingConfirm) Confirm(context.Context, string) (bool, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.resp, nil
}

func (c *countingConfirm) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// TestUnattendedDeniesApprovalInAllModes NOND-W4:定时任务(无人值守)触发时,
// 需审批的动作在三档下一律拒绝,且不弹确认;对照组证明「拒绝来自无人值守标记」。
func TestUnattendedDeniesApprovalInAllModes(t *testing.T) {
	const danger = `{"command":"rm -rf /tmp/x"}` // host-tools 前置到 echo 工具,不真执行
	for _, mode := range []string{"open", "smart", "strict"} {
		// 用 full-access 隔离沙箱层(默认 workspace-write 下 /tmp 写目标另会被路径裁决拒绝)
		cf := &countingConfirm{resp: true}
		c := build(t, cf, map[string]any{"approval": mode, "sandbox": "full-access"})
		var tools sdk.ToolRegistry
		if err := c.Inject("ctx.tools", &tools); err != nil {
			t.Fatal(err)
		}
		res, err := tools.Execute(sdk.WithUnattended(context.Background()), "shell", danger)
		if err != nil {
			t.Fatalf("%s:流水线应吞 veto 为结构化结果,got %v", mode, err)
		}
		if res.Error == "" {
			t.Fatalf("%s 档下无人值守的危险动作必须被拒", mode)
		}
		if !strings.Contains(res.Error, "无人值守") {
			t.Fatalf("%s:错误应说明是无人值守导致,got %q", mode, res.Error)
		}
		if cf.count() != 0 {
			t.Fatalf("%s 档下不得弹确认(无人值守没有应答者),调用了 %d 次", mode, cf.count())
		}

		// 对照:同一命令在有人值守时按档位既有语义(open/smart 放行、strict 拒绝)
		cf2 := &countingConfirm{resp: true}
		c2 := build(t, cf2, map[string]any{"approval": mode, "sandbox": "full-access"})
		var tools2 sdk.ToolRegistry
		if err := c2.Inject("ctx.tools", &tools2); err != nil {
			t.Fatal(err)
		}
		res2, err := tools2.Execute(context.Background(), "shell", danger)
		if err != nil {
			t.Fatal(err)
		}
		switch mode {
		case "open":
			if res2.Error != "" {
				t.Fatalf("有人值守 open 档应放行,got %q", res2.Error)
			}
			if cf2.count() != 0 {
				t.Fatal("open 档不应弹确认")
			}
		case "smart":
			if res2.Error != "" {
				t.Fatalf("有人值守 smart 档用户同意后应放行,got %q", res2.Error)
			}
			if cf2.count() != 1 {
				t.Fatalf("smart 档应询问一次,got %d", cf2.count())
			}
		case "strict":
			if res2.Error == "" || strings.Contains(res2.Error, "无人值守") {
				t.Fatalf("有人值守 strict 档应按严格档拒绝,got %q", res2.Error)
			}
		}
	}
}

// TestUnattendedAppliesToToolApproval 工具级审批(E-A)同样受无人值守约束。
func TestUnattendedAppliesToToolApproval(t *testing.T) {
	cf := &countingConfirm{resp: true}
	c := build(t, cf, map[string]any{"approval": "open", "approval_tools": []any{"shell"}})
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	// 有人值守 + open:工具在审批名单内,open 档直接放行
	res, err := tools.Execute(context.Background(), "shell", `{"command":"echo hi"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("open 档下名单内工具应放行,got %q", res.Error)
	}
	// 无人值守:名单内工具即使 open 档也拒绝
	res2, err := tools.Execute(sdk.WithUnattended(context.Background()), "shell", `{"command":"echo hi"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Error == "" || !strings.Contains(res2.Error, "无人值守") {
		t.Fatalf("无人值守下名单内工具应被拒并说明原因,got %q", res2.Error)
	}
	if cf.count() != 0 {
		t.Fatalf("无人值守不得弹确认,got %d", cf.count())
	}
}
