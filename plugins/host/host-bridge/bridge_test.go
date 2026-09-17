// 桥测试:编译外部插件→加载→执行→杀进程验证崩溃隔离(宿主存活、调用转结构化错误)。
package hostbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/nekoleamo/go-agent-harness/internal/testutil"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildExternalPlugin 编译 tool-echo 到 dir。
func buildExternalPlugin(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, testutil.ExeName("tool-echo")), "../../../extplugins/tool-echo")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("编译外部插件失败: %v\n%s", err, out)
	}
}

// buildEnv:host-tools + host-bridge(dir)。
func buildEnv(t *testing.T, dir string) (sdk.Ctx, *plugin.Registry) {
	t.Helper()
	return buildEnvWith(t, dir, slog.New(slog.DiscardHandler))
}

// buildEnvWith 同 buildEnv,但可注入日志器(要断言「启动日志里没有 ERROR」时用)。
func buildEnvWith(t *testing.T, dir string, logger *slog.Logger) (sdk.Ctx, *plugin.Registry) {
	t.Helper()
	return buildEnvWatch(t, dir, logger, true)
}

// buildEnvWatch 再开一个旋钮:是否监听插件目录(watch)。
// 关掉 watcher 的场景:测试自己要显式调 Reload —— 否则 300ms 去抖的 watcher 会
// 在 Reload 返回之后紧跟着「撤销并重载」同一个路径,把刚注册的工具短暂撤下来
// (观测到:Reload 返回时工具表还是空的,900ms 后才又有 —— 测试断言全凭时序)。
func buildEnvWatch(t *testing.T, dir string, logger *slog.Logger, watch bool) (sdk.Ctx, *plugin.Registry) {
	t.Helper()
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
		{"host-bridge", func() sdk.Plugin { return &Plugin{} }, &sdk.Manifest{ID: "host-bridge", APIVersion: ">=1.0,<2.0", Requires: []string{"ctx.tools"}}, map[string]any{"dir": dir, "watch": watch}},
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

// waitUnlocked 轮询等到 path 不再被占用(Windows 进程退出后文件锁滞后释放)。
//
// 直接尝试 os.Remove:能删就说明锁已释放(删掉也无妨,t.TempDir() 的 RemoveAll 对
// 不存在的子项不报错)。最多等 30 秒 —— Windows CI 上观测到新建的 exe 会被实时扫描/
// 杀软短暂持有(甚至超过 5 秒),窗口太短就会以「TempDir RemoveAll: Access is denied」
// 这种与断言无关的清理失败结束用例。超时不报错 —— 真正的删除失败仍由 TempDir 的
// cleanup 报出,这里只负责给它创造收敛条件;非 Windows 上通常第一次就成功。
func waitUnlocked(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 150; i++ {
		if err := os.Remove(path); err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
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
	if err := os.Chtimes(filepath.Join(dir, testutil.ExeName("tool-echo")), time.Now(), time.Now()); err != nil {
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

// TestPluginStderrCapture 环形缓冲:只留尾部若干行(含未换行的残留),超长截断。
func TestPluginStderrCapture(t *testing.T) {
	p := &pluginStderr{}
	for i := 1; i <= pluginStderrLines+5; i++ {
		fmt.Fprintf(p, "第 %d 行\n", i)
	}
	tail := p.tail()
	if strings.Contains(tail, "第 1 行") {
		t.Fatalf("应只保留尾部 %d 行: %s", pluginStderrLines, tail)
	}
	if !strings.Contains(tail, fmt.Sprintf("第 %d 行", pluginStderrLines+5)) {
		t.Fatalf("尾部最新行必须在: %s", tail)
	}
	// 未换行的残留也要收(插件可能不换行就退出)
	p2 := &pluginStderr{}
	fmt.Fprint(p2, "没有换行的最后一句")
	if got := p2.tail(); !strings.Contains(got, "没有换行的最后一句") {
		t.Fatalf("未换行残留应收进尾部: %q", got)
	}
	// 只有空白 ⇒ 视为无输出(避免给错误拼上无意义的空白)
	p3 := &pluginStderr{}
	fmt.Fprint(p3, "  \n \r\n")
	if got := p3.tail(); got != "" {
		t.Fatalf("纯空白不应上报: %q", got)
	}
	// 超长不换行的输出不得无限涨
	p4 := &pluginStderr{}
	fmt.Fprint(p4, strings.Repeat("x", pluginStderrBytes*3))
	if got := p4.tail(); len(got) > pluginStderrBytes+4 {
		t.Fatalf("超长输出应截断,现长 %d", len(got))
	}
}

// TestPluginStderrSurfacesInLoadError 真实故障场景(对齐真机 tool-mcp):插件在 go-plugin
// 握手之前就退出时,宿主此前只能报 "Failed to read any lines from plugin's stdout" 这种谜语;
// 现在必须把插件自己写的 stderr 一并报出 —— 桌面版没有终端,这是唯一的现场。
func TestPluginStderrSurfacesInLoadError(t *testing.T) {
	if testutil.IsWindows() {
		t.Skip("用 shell 脚本构造「握手前退出」:Windows 无 sh(捕获逻辑本身与平台无关)")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "tool-exit-before-handshake")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'tool-mcp: 未配置任何 MCP server(设置面板「MCP」分区)' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := startPlugin(bin, "127.0.0.1:1", "tok")
	if err == nil {
		t.Fatal("握手前退出的插件应报错")
	}
	if !strings.Contains(err.Error(), "未配置任何 MCP server") {
		t.Fatalf("错误必须带上插件自身 stderr(否则用户只能看到 go-plugin 谜语): %v", err)
	}
}

// TestPluginIdleMarkerIsNotAnError 插件自述空闲(工具类插件未配置后端)= 「没用到这个功能」,
// 不是故障:宿主必须记 INFO 跳过,启动日志里不能出现 ERROR(真机:每次启都会看到一条
// 「跳过加载失败的外部插件 tool-mcp」,用户合理理解成「装坏了」)。
func TestPluginIdleMarkerIsNotAnError(t *testing.T) {
	if testutil.IsWindows() {
		t.Skip("用 shell 脚本构造「自述空闲后退出」:Windows 无 sh(归类逻辑本身与平台无关)")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "tool-idle")
	script := "#!/bin/sh\necho 'GAH_PLUGIN_IDLE: 未配置任何 MCP server(设置面板「MCP server」段)' >&2\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// 归类:必须能被 errors.Is 认出来,且自述原因保留(供日志给出「去哪配」)
	_, _, _, err := startPlugin(bin, "127.0.0.1:1", "tok")
	if err == nil || !errors.Is(err, errPluginIdle) {
		t.Fatalf("自述空闲应归类为 errPluginIdle,得 %v", err)
	}
	if !strings.Contains(err.Error(), "未配置任何 MCP server") {
		t.Fatalf("应保留插件自述的原因: %v", err)
	}
	// 整条启动路径:同一个目录里放空闲插件 + 正常插件,boot 不失败且日志无 ERROR
	buildExternalPlugin(t, dir)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	c, _ := buildEnvWith(t, dir, logger)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	if _, ok := tools.Get("echo"); !ok {
		t.Fatal("空闲插件存在时,正常插件 echo 仍应注册")
	}
	logged := buf.String()
	if strings.Contains(logged, "level=ERROR") {
		t.Fatalf("自述空闲不得上报 ERROR,日志:\n%s", logged)
	}
	if !strings.Contains(logged, "自述空闲") || !strings.Contains(logged, "未配置任何 MCP server") {
		t.Fatalf("应记 INFO 并带上自述原因,日志:\n%s", logged)
	}
	// 重载路径同理:用户把 MCP 配置清空并点「保存并重载」⇒ tool-mcp 自述空闲。
	// 这不是「重启失败」(旧代码在这里返回错误 + ERROR,面板会报「重载失败」)。
	var extp2 sdk.ExternalPlugins
	c2, _ := buildEnvWatch(t, dir, slog.New(slog.DiscardHandler), false)
	if err := c2.Inject("ctx.extplugins", &extp2); err != nil {
		t.Fatal(err)
	}
	if err := extp2.Reload("tool-idle"); err != nil {
		t.Fatalf("自述空闲的重载不应报错(配置已保存,只是没有要加载的东西): %v", err)
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
	path := filepath.Join(dir, testutil.ExeName("tool-echo"))

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
// 字段名/标签两端漂移会让声明静默丢失(路径沙箱/工具级审批随之失效),故以真实结构体往返锚定。
func TestPathParamsCrossBridge(t *testing.T) {
	want := []sdk.PathParam{
		{Arg: "target", Access: sdk.PathWrite, Many: true},
		{Arg: "dir", Access: sdk.PathRead, Optional: true},
	}
	raw, err := json.Marshal(defDTO{Name: "save_note", Description: "d", InputSchema: map[string]any{"type": "object"}, TimeoutMs: 5000, PathParams: want, ApprovalTargetParam: "name"})
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
	if got.ApprovalTargetParam != "name" {
		t.Fatalf("代理工具目标声明未跨桥传递: %+v", got)
	}
	// 旧单工具协议:整份 JSON 反序列化进 sdk.ToolDefinition(声明天然携带)
	var legacy sdk.ToolDefinition
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(legacy.PathParams, want) {
		t.Fatalf("旧协议路径声明丢失: %+v", legacy.PathParams)
	}
	if legacy.ApprovalTargetParam != "name" {
		t.Fatalf("旧协议代理目标声明丢失: %+v", legacy.ApprovalTargetParam)
	}
}

// TestExternalPluginReloadByName ctx.extplugins.Reload(NOND-M1 第 3 步):
// 按名重启外部插件(配置改完后重读)+ 未加载时的补加载 + 名字不存在显式报错。
func TestExternalPluginReloadByName(t *testing.T) {
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	c, _ := buildEnv(t, dir)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	var extp sdk.ExternalPlugins
	if err := c.Inject("ctx.extplugins", &extp); err != nil {
		t.Fatalf("host-bridge 应 Provide ctx.extplugins: %v", err)
	}
	if extp == nil {
		t.Fatal("ctx.extplugins 不应为 nil")
	}
	// 已加载:重载成功且工具仍可用
	if err := extp.Reload("tool-echo"); err != nil {
		t.Fatalf("重载已加载插件应成功: %v", err)
	}
	res, err := tools.Execute(context.Background(), "echo", `{"text":"after-reload"}`)
	if err != nil || res.Error != "" {
		t.Fatalf("重载后调用应可用: %v %+v", err, res)
	}
	// 名字不存在:显式错误(不静默成功)
	if err := extp.Reload("tool-nope"); err == nil || !strings.Contains(err.Error(), "未安装") {
		t.Fatalf("不存在的插件名应显式报错: %v", err)
	}
	// 空名:显式错误
	if err := extp.Reload("  "); err == nil {
		t.Fatal("空名应报错")
	}
}

// TestExternalPluginReloadLoadsNewBinary 启动时目录不存在(空插件集),
// 之后放入二进制 → Reload 应补加载(用户新装插件/新加 MCP server 无需重启 gah)。
func TestExternalPluginReloadLoadsNewBinary(t *testing.T) {
	dir := t.TempDir()
	// Windows:插件进程退出后 exe 的文件锁由内核异步释放,而 buildExternalPlugin 把
	// tool-echo.exe 编译在 t.TempDir() 里 —— DisposeAll 一返回就 RemoveAll 会撞上还没
	// 释放的锁,报 unlinkat ... Access is denied(go-plugin 日志里的
	// "TerminateProcess: Access is denied." 是它对已经自行退出的进程再补一刀的无害噪声,
	// 不代表进程没死)。本 cleanup 注册在 TempDir 之后 → LIFO 先执行 → 等到 exe 可删。
	t.Cleanup(func() { waitUnlocked(t, filepath.Join(dir, testutil.ExeName("tool-echo"))) })
	c, _ := buildEnvWatch(t, dir, slog.New(slog.DiscardHandler), false) // 目录为空:boot 期零外部插件
	// 不启用 watcher:本用例要验的是「显式 Reload 能补加载」,watcher 同时抢同一个
	// 路径会让断言变成看时序(它会在 Reload 之后又撤销+重载一次)。watch 通路另有用例覆盖。
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	if names := toolNames(tools); len(names) != 0 {
		t.Fatalf("初始应无外部工具: %v", names)
	}
	buildExternalPlugin(t, dir) // 事后放入 tool-echo
	var extp sdk.ExternalPlugins
	if err := c.Inject("ctx.extplugins", &extp); err != nil {
		t.Fatal(err)
	}
	if err := extp.Reload("tool-echo"); err != nil {
		t.Fatalf("补加载应成功: %v", err)
	}
	if _, ok := tools.Get("echo"); !ok {
		t.Fatalf("补加载后工具应可用: %v", toolNames(tools))
	}
}
