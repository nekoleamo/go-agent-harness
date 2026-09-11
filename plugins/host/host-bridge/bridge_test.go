// 桥测试:编译外部插件→加载→执行→杀进程验证崩溃隔离(宿主存活、调用转结构化错误)。
package hostbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildExternalPlugin 编译 tool-echo 到 dir。
func buildExternalPlugin(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "tool-echo"), "../../../extplugins/tool-echo")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("编译外部插件失败: %v\n%s", err, out)
	}
}

// buildEnv:host-tools + host-bridge(dir)。
func buildEnv(t *testing.T, dir string) (sdk.Ctx, *plugin.Registry) {
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
		{"host-bridge", func() sdk.Plugin { return &Plugin{} }, &sdk.Manifest{ID: "host-bridge", APIVersion: ">=1.0,<2.0", Requires: []string{"ctx.tools"}}, map[string]any{"dir": dir, "watch": true}},
	}
	for _, d := range defs {
		mm := *d.m
		mm.Data = d.data
		if err := reg.Register(d.f, &mm); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.StartSubset(c, map[string]bool{"host-tools": true, "host-bridge": true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.DisposeAll)
	return c, reg
}

func toolNames(tools sdk.ToolRegistry) []string {
	defs := tools.List()
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}

func TestExternalToolLoadAndExecute(t *testing.T) {
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	c, _ := buildEnv(t, dir)

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	t.Logf("工具定义: %+v", tools.List())
	def, ok := tools.Get("echo")
	if !ok {
		t.Fatal("外部插件工具 echo 应注册")
	}
	if !strings.Contains(def.Description, "外部插件") {
		t.Fatalf("定义应来自外部插件: %+v", def)
	}
	res, err := tools.Execute(context.Background(), "echo", `{"text":"你好"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" || !strings.Contains(res.Content, "你好") {
		t.Fatalf("外部工具应回显: %+v", res)
	}
}

func TestExternalPluginHotReload(t *testing.T) {
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	c, _ := buildEnv(t, dir)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	// 正常调用
	res, err := tools.Execute(context.Background(), "echo", `{"text":"before"}`)
	if err != nil || res.Error != "" {
		t.Fatalf("初始调用失败: %v %+v", err, res)
	}
	// touch 触发 watcher(fsnotify write 事件 → debounce → 重载)
	if err := os.Chtimes(filepath.Join(dir, "tool-echo"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	// 等待重载完成(300ms debounce + 新进程启动),工具应仍可用
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		res, err := tools.Execute(context.Background(), "echo", `{"text":"after"}`)
		if err != nil {
			t.Fatalf("重载后调用不得报错: %v", err)
		}
		if res.Error == "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if res.Error != "" {
		t.Fatalf("热重载后 echo 应可用: %+v", res)
	}
}

func TestExternalPluginCrashIsolation(t *testing.T) {
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	c, reg := buildEnv(t, dir)

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Execute(context.Background(), "echo", `{"text":"ok"}`); err != nil {
		t.Fatal(err)
	}
	t.Logf("kill 前工具定义: %+v", tools.List())
	t.Log("kill 前工具列表: " + fmt.Sprint(toolNames(tools)))

	killed := killPluginProcess("tool-echo")
	if !killed {
		t.Skip("未找到外部插件进程,跳过崩溃隔离验证")
	}

	// 宿主应存活;调用返回结构化错误而非宿主崩溃
	deadline := time.Now().Add(10 * time.Second)
	var blocked string
	for time.Now().Before(deadline) {
		res, err := tools.Execute(context.Background(), "echo", `{"text":"again"}`)
		if err != nil {
			t.Fatalf("宿主侧不应 panic,got err %v", err)
		}
		if res.Error != "" {
			blocked = res.Error
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if blocked == "" {
		t.Fatal("进程被杀后调用应转结构化错误")
	}
	t.Logf("崩溃后调用结果: %s", blocked)
	if len(reg.Snapshot()) != 2 {
		t.Fatalf("宿主插件应保持存活: %v", reg.Snapshot())
	}
}

// killPluginProcess 精确名匹配击杀插件进程(macOS comm 名 = tool-echo)。
func killPluginProcess(name string) bool {
	out, err := exec.Command("pgrep", "-x", name).Output()
	if err != nil {
		return false
	}
	for _, pid := range strings.Fields(string(out)) {
		exec.Command("kill", "-9", pid).Run()
	}
	time.Sleep(300 * time.Millisecond)
	return true
}

// TestWatchMissingDirNoFail watch 开启但目录不存在 → Start 成功(空插件集降级,不监听;
// 防默认开 watch 后空目录/测试临时目录拖垮 boot)。
func TestWatchMissingDirNoFail(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	mm := &sdk.Manifest{ID: "host-tools", APIVersion: ">=1.0,<2.0", Provides: []string{"ctx.tools"}}
	if err := reg.Register(func() sdk.Plugin { return &hosttools.Plugin{} }, mm); err != nil {
		t.Fatal(err)
	}
	bm := &sdk.Manifest{ID: "host-bridge", APIVersion: ">=1.0,<2.0", Requires: []string{"ctx.tools"}}
	bm.Data = map[string]any{"dir": filepath.Join(t.TempDir(), "plugins-no-such"), "watch": true}
	if err := reg.Register(func() sdk.Plugin { return &Plugin{} }, bm); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, map[string]bool{"host-tools": true, "host-bridge": true}); err != nil {
		t.Fatalf("目录不存在 + watch 开不应失败: %v", err)
	}
}

// TestBadPluginDoesNotBreakBoot P3 首启健壮性:目录含无法启动的坏插件(缺配置/
// 崩溃/不可执行)时,boot 不得整体失败——坏插件记 ERROR 跳过,好插件正常加载。
func TestBadPluginDoesNotBreakBoot(t *testing.T) {
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	// 坏插件:shell 脚本立即 exit 1(go-plugin 握手失败→加载错误)
	bad := filepath.Join(dir, "tool-bad")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, _ := buildEnv(t, dir) // Start 不得因坏插件失败

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	// 好插件仍在:echo 已注册且可调用
	def, ok := tools.Get("echo")
	if !ok {
		t.Fatal("坏插件存在时,正常插件 echo 仍应注册(P3 软降级)")
	}
	if !strings.Contains(def.Description, "外部插件") {
		t.Fatalf("定义应来自外部插件: %+v", def)
	}
	res, err := tools.Execute(context.Background(), "echo", `{"text":"ok"}`)
	if err != nil || res.Error != "" {
		t.Fatalf("坏插件存在时正常工具应可用: %v %+v", err, res)
	}
	if _, ok := tools.Get("bad"); ok {
		t.Fatal("坏插件的工具不应注册")
	}
}

// TestToolLevelTimeout 工具级超时(P0-2):定义声明 timeout_ms 覆写全局 3s。
func TestToolLevelTimeout(t *testing.T) {
	if got := rpcTimeoutFor(sdk.ToolDefinition{TimeoutMs: 0}); got != 3*time.Second {
		t.Fatalf("未声明超时应回落全局默认: %v", got)
	}
	if got := rpcTimeoutFor(sdk.ToolDefinition{TimeoutMs: 90_000}); got != 90*time.Second {
		t.Fatalf("应使用工具声明超时: %v", got)
	}
	if got := rpcTimeoutFor(sdk.ToolDefinition{TimeoutMs: 1500}); got != 1500*time.Millisecond {
		t.Fatalf("毫秒换算不符: %v", got)
	}
}

// TestCrashAutoRespawn 进程崩溃自动拉起(P0-3):连接断裂 → 调用转错误 +
// 异步重建新实例(节流内),之后调用恢复正常。
func TestCrashAutoRespawn(t *testing.T) {
	dir := t.TempDir()
	buildExternalPlugin(t, dir)

	// 仅 host-tools(不启动 host-bridge 插件,手工构造 Bridge 便于检查条目)
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	mm := sdk.Manifest{ID: "host-tools", APIVersion: ">=1.0,<2.0", Provides: []string{"ctx.tools"}}
	if err := reg.Register(func() sdk.Plugin { return &hosttools.Plugin{} }, &mm); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, map[string]bool{"host-tools": true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.DisposeAll)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	b := &Bridge{dir: dir, tools: tools, entries: map[string]*extEntry{}}
	if err := b.loadEntries(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.closeAll)
	path := filepath.Join(dir, "tool-echo")

	res, err := tools.Execute(context.Background(), "echo", `{"text":"before"}`)
	if err != nil || res.Error != "" {
		t.Fatalf("基准调用失败: %v %+v", err, res)
	}
	old := b.entries[path]
	if old == nil {
		t.Fatal("条目缺失")
	}
	// 模拟进程死亡:连接断裂
	old.client.Close()

	// 调用应转结构化错误,且触发自动拉起(轮询直到新实例恢复)
	deadline := time.Now().Add(10 * time.Second)
	var recovered bool
	for time.Now().Before(deadline) {
		res, err := tools.Execute(context.Background(), "echo", `{"text":"after"}`)
		if err != nil {
			t.Fatalf("宿主不应 panic: %v", err)
		}
		if res.Error == "" && strings.Contains(res.Content, "after") {
			b.mu.RLock()
			recovered = b.entries[path] != old
			b.mu.RUnlock()
			if recovered {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !recovered {
		t.Fatal("崩溃后应自动拉起并恢复调用")
	}
}

// TestPathParamsCrossBridge 能力声明必须跨桥协议两跳原样传递:
// 外部侧(serve.go td)→ JSON → 宿主侧(bridge.go defDTO)→ 注册表 sdk.ToolDefinition。
// 字段名/标签两端漂移会让声明静默丢失(路径沙箱随之失效),故以真实结构体往返锚定。
func TestPathParamsCrossBridge(t *testing.T) {
	want := []sdk.PathParam{
		{Arg: "target", Access: sdk.PathWrite, Many: true},
		{Arg: "dir", Access: sdk.PathRead, Optional: true},
	}
	raw, err := json.Marshal(defDTO{Name: "save_note", Description: "d", InputSchema: map[string]any{"type": "object"}, TimeoutMs: 5000, PathParams: want})
	if err != nil {
		t.Fatal(err)
	}
	var got defDTO
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.PathParams, want) {
		t.Fatalf("能力声明未跨桥传递: %+v", got.PathParams)
	}
	if got.TimeoutMs != 5000 || got.Name != "save_note" {
		t.Fatalf("既有字段受影响: %+v", got)
	}
	// 旧单工具协议:整份 JSON 反序列化进 sdk.ToolDefinition(声明天然携带)
	var legacy sdk.ToolDefinition
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(legacy.PathParams, want) {
		t.Fatalf("旧协议路径声明丢失: %+v", legacy.PathParams)
	}
}
