// M14 外部命令桥测试:外部插件声明命令 → host-bridge 转注册 ctx.commands →
// 斜杠命令执行经 RPC 转发外部进程;未装配 ctx.commands 时命令跳过(工具不受影响)、
// 同名冲突拒绝跳过(记警告)、卸载随 Disposer 撤销。加载链复用 buildExternalPlugin
// (tool-echo 已带 /echo 命令示例)。
package hostbridge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/internal/testutil"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-commands"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildEnvWithCmds 装配 host-tools + host-commands + host-bridge(dir)。
func buildEnvWithCmds(t *testing.T, dir string) (sdk.Ctx, *plugin.Registry) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	defs := []struct {
		id   string
		f    sdk.Factory
		m    *sdk.Manifest
		data map[string]any
	}{
		{"host-tools", func() sdk.Plugin { return &hosttools.Plugin{} }, &sdk.Manifest{ID: "host-tools", APIVersion: ">=1.0,<2.0", Provides: []string{"ctx.tools"}}, nil},
		{"host-commands", func() sdk.Plugin { return &hostcommands.Plugin{} }, &sdk.Manifest{ID: "host-commands", APIVersion: ">=1.0,<2.0", Provides: []string{"ctx.commands"}}, nil},
		{"host-bridge", func() sdk.Plugin { return &Plugin{} }, &sdk.Manifest{ID: "host-bridge", APIVersion: ">=1.0,<2.0", Requires: []string{"ctx.tools"}}, map[string]any{"dir": dir, "watch": true}},
	}
	for _, d := range defs {
		mm := *d.m
		mm.Data = d.data
		if err := reg.Register(d.f, &mm); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.StartSubset(c, map[string]bool{"host-tools": true, "host-commands": true, "host-bridge": true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.DisposeAll)
	return c, reg
}

// TestExternalCommandLoadAndRun 命令注册 + 自由级声明 + 经 RPC 执行。
func TestExternalCommandLoadAndRun(t *testing.T) {
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	c, _ := buildEnvWithCmds(t, dir)

	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	spec, ok := cmds.Get("echo")
	if !ok {
		t.Fatalf("外部命令 /echo 应注册(已注册: %v)", commandNames(cmds))
	}
	if !strings.Contains(spec.Desc, "外部插件") {
		t.Errorf("Desc 应来自外部插件: %q", spec.Desc)
	}
	if spec.Usage != "/echo <文本>" {
		t.Errorf("Usage 应来自外部插件声明: %q", spec.Usage)
	}
	// 自由级声明(枚举选项求值经 RPC)
	if len(spec.Args) != 1 || spec.Args[0].FreeArgs == nil {
		t.Fatalf("/echo 应声明一级自由参数 Args[0].FreeArgs: %+v", spec.Args)
	}
	if got := spec.Args[0].FreeArgs(nil); len(got) != 1 || got[0] != "文本" {
		t.Errorf("自由参数名应来自 DTO: %v", got)
	}
	// 执行:输出文本(meta 行),无错误
	out, err := spec.Run([]string{"你好"})
	if err != nil {
		t.Fatalf("外部命令执行失败: %v", err)
	}
	if !strings.Contains(out, "外部命令 echo: 你好") {
		t.Errorf("输出应来自外部进程: %q", out)
	}
	// 工具仍可用(命令与工具共存)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "echo", `{"text":"ok"}`)
	if err != nil || res.Error != "" {
		t.Fatalf("工具执行应不受命令影响: %v %+v", err, res)
	}
}

// TestExternalCommandSkippedNoRegistry 未装配 ctx.commands:命令不注册,工具仍加载。
func TestExternalCommandSkippedNoRegistry(t *testing.T) {
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	c, _ := buildEnv(t, dir) // 仅 host-tools + host-bridge

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	if _, ok := tools.Get("echo"); !ok {
		t.Fatal("工具应不受 ctx.commands 缺失影响")
	}
	var cr sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cr); err == nil {
		// ctx.commands 未提供:注入应失败(断言环境正确)
		t.Fatal("测试环境应无 ctx.commands")
	}
}

// TestExternalCommandConflictSkipped 宿主先注册同名 /echo → bridge 装配时外部命令被跳过
// (显式记警告,插件继续加载;先到先得不覆盖)。StartOne 单独启动 bridge 模拟时序。
func TestExternalCommandConflictSkipped(t *testing.T) {
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	defs := []struct {
		id   string
		f    sdk.Factory
		m    *sdk.Manifest
		data map[string]any
	}{
		{"host-tools", func() sdk.Plugin { return &hosttools.Plugin{} }, &sdk.Manifest{ID: "host-tools", APIVersion: ">=1.0,<2.0", Provides: []string{"ctx.tools"}}, nil},
		{"host-commands", func() sdk.Plugin { return &hostcommands.Plugin{} }, &sdk.Manifest{ID: "host-commands", APIVersion: ">=1.0,<2.0", Provides: []string{"ctx.commands"}}, nil},
		{"host-bridge", func() sdk.Plugin { return &Plugin{} }, &sdk.Manifest{ID: "host-bridge", APIVersion: ">=1.0,<2.0", Requires: []string{"ctx.tools"}}, map[string]any{"dir": dir}},
	}
	for _, d := range defs {
		mm := *d.m
		mm.Data = d.data
		if err := reg.Register(d.f, &mm); err != nil {
			t.Fatal(err)
		}
	}
	// 第一阶段:先启动 host-tools + host-commands(bridge 暂不启动)
	if err := reg.StartSubset(c, map[string]bool{"host-tools": true, "host-commands": true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.DisposeAll)

	// 宿主先注册同名 /echo
	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	hostCmd, err := cmds.Register(sdk.CommandSpec{Name: "echo", Usage: "/echo <宿主>", Desc: "宿主命令先到", Run: func([]string) (string, error) { return "host", nil }})
	if err != nil {
		t.Fatalf("宿主预注册 /echo 应成功: %v", err)
	}
	defer hostCmd()

	// 第二阶段:启动 bridge(外部 /echo 同名 → 跳过,记警告,不覆盖)
	if err := reg.StartOne(c, "host-bridge"); err != nil {
		t.Fatalf("bridge 启动应继续(冲突仅跳过命令): %v", err)
	}
	spec, ok := cmds.Get("echo")
	if !ok || spec.Desc != "宿主命令先到" {
		t.Errorf("/echo 应为宿主命令(先到先得,外部命令跳过): %+v", spec)
	}
}

// TestExternalCommandRegistryRejectsDup 注册表本身拒绝同名(不静默)。
func TestExternalCommandRegistryRejectsDup(t *testing.T) {
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	c, _ := buildEnvWithCmds(t, dir)
	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	if _, err := cmds.Register(sdk.CommandSpec{Name: "echo", Desc: "再注册", Run: func([]string) (string, error) { return "", nil }}); err == nil {
		t.Fatal("同名命令注册应被拒绝(先到先得)\n")
	}
}

// TestExternalCommandUnregisteredOnDispose 卸载后命令随 Disposer 撤销。
func TestExternalCommandUnregisteredOnDispose(t *testing.T) {
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	c, reg := buildEnvWithCmds(t, dir)

	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	if _, ok := cmds.Get("echo"); !ok {
		t.Fatalf("装配后 /echo 应在注册表")
	}
	reg.DisposeAll()
	if _, ok := cmds.Get("echo"); ok {
		t.Error("卸载后 /echo 应撤销")
	}
}

func commandNames(cmds sdk.CommandRegistry) []string {
	var out []string
	for _, s := range cmds.List() {
		out = append(out, s.Name)
	}
	return out
}

// TestServeCommandsDTOAndEnum 进程内验证协议核心:toolServer 的 Commands DTO 往返
// (Enum 级标记/FreeArgs 静态序列)+ CommandOptions 枚举求值 + RunCommand 成功/错误语义。
func TestServeCommandsDTOAndEnum(t *testing.T) {
	spec := sdk.CommandSpec{
		Name: "demo", Usage: "/demo list|run <参数>", Desc: "演示命令",
		Args: []sdk.ArgLevel{
			{Options: func([]string) []sdk.Option {
				return []sdk.Option{{Value: "list", Desc: "列表"}, {Value: "run", Desc: "运行"}}
			}},
			{FreeArgs: func([]string) []string { return []string{"参数"} }},
		},
		Run: func(args []string) (string, error) {
			if len(args) < 2 {
				return "部分输出", errors.New("缺参数")
			}
			return "out:" + strings.Join(args, ","), nil
		},
	}
	s := &toolServer{commands: map[string]sdk.CommandSpec{"demo": spec}}

	// Commands:DTO 往返(排序/Args 声明)
	var raw string
	if err := s.Commands(struct{}{}, &raw); err != nil {
		t.Fatalf("Commands: %v", err)
	}
	var dtos []CommandDTO
	if err := json.Unmarshal([]byte(raw), &dtos); err != nil {
		t.Fatalf("DTO 解析: %v", err)
	}
	if len(dtos) != 1 || dtos[0].Name != "demo" || dtos[0].Usage != "/demo list|run <参数>" {
		t.Fatalf("DTO 内容错误: %+v", dtos)
	}
	if len(dtos[0].Args) != 2 || !dtos[0].Args[0].Enum || len(dtos[0].Args[1].FreeArgs) != 1 || dtos[0].Args[1].FreeArgs[0] != "参数" {
		t.Errorf("Args 声明错误(0=Enum,1=FreeArgs): %+v", dtos[0].Args)
	}

	// CommandOptions:枚举级求值 / 非枚举级与越界 = 空
	var optsRaw string
	if err := s.CommandOptions(&CmdOptionsArgs{Name: "demo", Level: 0}, &optsRaw); err != nil {
		t.Fatalf("CommandOptions: %v", err)
	}
	var opts []sdk.Option
	if err := json.Unmarshal([]byte(optsRaw), &opts); err != nil || len(opts) != 2 || opts[0].Value != "list" {
		t.Errorf("枚举选项应含 list/run: %v (%v)", opts, err)
	}
	var empty string
	if err := s.CommandOptions(&CmdOptionsArgs{Name: "demo", Level: 1}, &empty); err != nil || empty != "" {
		t.Errorf("非枚举级应返回空: %q err=%v", empty, err)
	}
	if err := s.CommandOptions(&CmdOptionsArgs{Name: "demo", Level: 9}, &empty); err != nil || empty != "" {
		t.Errorf("越界级应返回空: %q err=%v", empty, err)
	}
	if err := s.CommandOptions(&CmdOptionsArgs{Name: "nope", Level: 0}, &empty); err != nil || empty != "" {
		t.Errorf("未知命令应返回空: %q err=%v", empty, err)
	}

	// RunCommand:成功 / 业务错误(输出+错误并存)/ 未知命令
	var reply ExecReply
	if err := s.RunCommand(&RunCommandArgs{Name: "demo", Args: []string{"list", "x"}}, &reply); err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if reply.Error != "" || reply.Content != "out:list,x" {
		t.Errorf("成功执行: content=%q err=%q", reply.Content, reply.Error)
	}
	if err := s.RunCommand(&RunCommandArgs{Name: "demo", Args: []string{"list"}}, &reply); err != nil {
		t.Fatalf("RunCommand(错误): %v", err)
	}
	if reply.Error == "" || reply.Content != "部分输出" {
		t.Errorf("业务错误应回传并保留输出: content=%q err=%q", reply.Content, reply.Error)
	}
	if err := s.RunCommand(&RunCommandArgs{Name: "nope", Args: nil}, &reply); err != nil {
		t.Fatalf("RunCommand(未知): %v", err)
	}
	if !strings.Contains(reply.Error, "无此命令") {
		t.Errorf("未知命令应显式报错: %q", reply.Error)
	}

	// DTO → 宿主 ArgLevel 构造(Enum 级→Options 经 RPC;FreeArgs 级→静态;皆空→直接执行)。
	// br 用空 Bridge(entries nil;clientFor 读 nil map 安全返回 nil → 枚举选项空)。
	var rpcOpts []sdk.Option
	client := &commandRPCClient{br: &Bridge{}, def: CommandDTO{Args: []CommandArgDTO{{Enum: true}, {FreeArgs: []string{"参数"}}, {}}}}
	levels := client.args()
	if len(levels) != 3 || levels[0].Options == nil || levels[1].FreeArgs == nil || levels[2].Options != nil {
		t.Fatalf("args() 级联构造错误: %+v", levels)
	}
	if len(levels[0].Options(nil)) != 0 { // 无 RPC client 时枚举返回 nil→空
		t.Error("无 client 时枚举选项应为空")
	}
	if got := levels[1].FreeArgs(nil); len(got) != 1 || got[0] != "参数" {
		t.Errorf("自由级参数名应来自 DTO: %v", got)
	}
	_ = rpcOpts
}

// buildTempCmdPlugin 临时构建一个纯命令插件(无工具)到 dir/<bin>。
// body 覆盖 main 包源码;构建走仓库 go.work(与 buildExternalPlugin 同法)。
func buildTempCmdPlugin(t *testing.T, dir, bin, body string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, testutil.ExeName(bin)), src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("编译临时命令插件失败: %v\n%s", err, out)
	}
}

// TestExternalCmdOnlyPluginWithTimeout 纯命令插件(cmd-*,无工具)加载 + RPC 超时保护:
// 命令声明 TimeoutMs=200,执行/枚举选项被慢进程拖住 → 宿主侧按声明超时快速返回,
// 不阻塞调用方;命令注册、工具面为空(纯命令插件语义)。
func TestExternalCmdOnlyPluginWithTimeout(t *testing.T) {
	dir := t.TempDir()
	body := `package main

import (
	"time"

	"github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func main() {
	hostbridge.ServeTools(nil, map[string]sdk.CommandSpec{
		"slow": {
			Name: "slow", Usage: "/slow", Desc: "慢命令(超时演示,纯命令插件)",
			TimeoutMs: 200,
			Args: []sdk.ArgLevel{{Options: func(picked []string) []sdk.Option {
				time.Sleep(2 * time.Second) // 枚举求值也慢:超时返回 nil
				return []sdk.Option{{Value: "x", Desc: "x"}}
			}}},
			Run: func(args []string) (string, error) {
				time.Sleep(2 * time.Second)
				return "slow done", nil
			},
		},
	})
}`
	buildTempCmdPlugin(t, dir, "cmd-slow", body)
	c, _ := buildEnvWithCmds(t, dir)

	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	spec, ok := cmds.Get("slow")
	if !ok {
		t.Fatalf("纯命令插件 /slow 应注册(已注册: %v)", commandNames(cmds))
	}
	// 纯命令插件:工具面为空
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	if _, ok := tools.Get("slow"); ok {
		t.Error("纯命令插件不应注册工具")
	}
	// 命令执行超时(声明 200ms;真实返回应为快速错误而非等待 2s)
	start := time.Now()
	out, err := spec.Run(nil)
	if err == nil {
		t.Fatalf("慢命令应超时报错: out=%q", out)
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Errorf("超时错误应明确: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 1*time.Second {
		t.Errorf("超时应快速返回(声明 200ms),实际 %v", elapsed)
	}
	// 枚举选项求值超时 → nil(选择器直接执行,不阻塞)
	if op := spec.Args[0].Options(nil); op != nil {
		t.Errorf("枚举选项慢求值应超时返回 nil: %v", op)
	}
}
