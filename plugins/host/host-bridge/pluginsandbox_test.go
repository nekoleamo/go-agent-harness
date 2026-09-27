// pluginsandbox_test.go:外部插件进程内核沙箱接线的测试(能力自报 + 包装判定 + fail-closed 重载)。
//
// 纪律:包装判定不 mock kernelsandbox.Wrap —— 断言的是"宿主是否把包装前缀前缀到 argv 上"
// 这一可观察事实(darwin 上是 sandbox-exec,Linus 上是自举 helper);平台不支持时 Skip 并说明,
// 不让测试假装覆盖了没覆盖的分支。
package hostbridge

import (
	"bytes"
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/internal/kernelsandbox"
	"github.com/nekoleamo/go-agent-harness/internal/testutil"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildCapsFixture 编译能力自报夹具(testdata/tool-caps)。
func buildCapsFixture(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, testutil.ExeName("tool-caps"))
	out, err := exec.Command("go", "build", "-o", bin, "./testdata/tool-caps").CombinedOutput()
	if err != nil {
		t.Fatalf("编译能力自报夹具失败: %v\n%s", err, out)
	}
	return bin
}

// capsTestBridge 最小 Bridge(真 ctx.sandbox 桩 + 假工具注册表):只测包装/重载,不起整个插件树。
func capsTestBridge(t *testing.T, dir string, sb *stubSandbox, logger *slog.Logger) *Bridge {
	t.Helper()
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if err := c.Provide("ctx.sandbox", sdk.Sandbox(sb)); err != nil {
		t.Fatal(err)
	}
	return &Bridge{
		dir:     dir,
		hostCtx: c,
		lg:      logger,
		tools:   &cbStubTools{}, // registerAll 要非 nil;这里不关心注册结果
		entries: map[string]*extEntry{},
	}
}

// TestProbeCapabilities 能力自报探测:声明的插件报出声明,不认该参数的插件(旧插件)探测失败。
//
// 这两条决定了宿主对插件的默认处理(**未声明 = 按普通插件包装**),是最不能漂的一环。
func TestProbeCapabilities(t *testing.T) {
	dir := t.TempDir()
	bin := buildCapsFixture(t, dir)

	// ① 自报提供者 + 数据目录
	t.Setenv("FIXTURE_SANDBOX_PROVIDER", "1")
	t.Setenv("FIXTURE_DATA_WRITES", "memory,todos")
	caps, ok := probeCapabilities(bin)
	if !ok {
		t.Fatal("自报能力的插件应探测成功")
	}
	if !caps.SandboxProvider {
		t.Fatal("应读到 SandboxProvider 声明")
	}
	if strings.Join(caps.DataWrites, ",") != "memory,todos" {
		t.Fatalf("应读到数据目录声明: %v", caps.DataWrites)
	}

	// ② 零声明(探测成功但什么都没声明)= 「普通插件」:宿主仍按包装处理
	t.Setenv("FIXTURE_SANDBOX_PROVIDER", "0")
	t.Setenv("FIXTURE_DATA_WRITES", "")
	if caps, ok := probeCapabilities(bin); !ok || caps.SandboxProvider || len(caps.DataWrites) != 0 {
		t.Fatalf("零声明应探测成功且为空: ok=%v caps=%+v", ok, caps)
	}

	// ③ 旧插件(tool-echo 用裸 ServeRPC,不认 --gah-caps):探测失败 = 未声明
	buildExternalPlugin(t, dir)
	echo := filepath.Join(dir, testutil.ExeName("tool-echo"))
	if caps, ok := probeCapabilities(echo); ok {
		t.Fatalf("不认 --gah-caps 的旧插件必须归为未声明: %+v", caps)
	}
}

// TestValidDataWrites 数据根写声明的校验:声明来自**被约束方**,越权项必须被丢且留下 ERROR。
func TestValidDataWrites(t *testing.T) {
	var buf bytes.Buffer
	lg := slog.New(slog.NewTextHandler(&buf, nil))
	b := &Bridge{lg: lg}

	got := b.validDataWrites("/x/tool-y", []string{
		"memory",                // 合法
		" todos ",               // 合法(去空白)
		"",                      // 空项:静默跳过
		"config",                // 保留集:凭据
		"plugins",               // 保留集:插件产物
		"ui-plugins",            // 保留集:前端插件
		"../etc",                // 越界
		"/etc/passwd",           // 绝对路径
		"a/b",                   // 嵌套(只允许直接子目录)
		"..",                    // 上跳
		".",                     // 当前目录
		strings.Repeat("x", 65), // 过长
	})
	want := []string{filepath.Join(sdk.Home(), "memory"), filepath.Join(sdk.Home(), "todos")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("白名单不符:\n got=%v\nwant=%v", got, want)
	}
	logs := buf.String()
	for _, kw := range []string{"保留集", "直接子目录名", "过长"} {
		if !strings.Contains(logs, kw) {
			t.Fatalf("越权声明必须留下可归因的 ERROR(缺 %q):\n%s", kw, logs)
		}
	}
}

// TestWrapPluginArgvDecision 包装判定表:自报提供者/全权档/显式关闭 → 不包装;其余 → 包装。
func TestWrapPluginArgvDecision(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	root := t.TempDir()
	sb := &stubSandbox{mode: sdk.SandboxWorkspace, root: root}
	b := capsTestBridge(t, root, sb, logger)
	bin := "/tmp/tool-x"

	// ① 未声明(旧插件/普通插件)→ 包装(安全侧默认)。平台不支持时 Skip 并说清。
	argv, env, wrapped := b.wrapPluginArgv(bin, Capabilities{}, false, nil)
	if !wrapped {
		if argv[0] != bin || len(env) != 0 {
			t.Fatalf("未包装时 argv/env 不应被改动: %v %v", argv, env)
		}
		t.Skip("当前平台无法施加内核沙箱(darwin 需 sandbox-exec,linux 需 Landlock ABI≥1):跳过包装断言")
	}
	if argv[0] == bin || len(argv) < 2 || argv[len(argv)-1] != bin {
		t.Fatalf("包装前缀应在插件本体之前: %v", argv)
	}
	if len(env) != 1 || env[0] != kernelsandbox.MarkerEnv+"=1" {
		t.Fatalf("被包装的插件进程必须打上已沙箱标记(否则它内部再施加会嵌套失败): %v", env)
	}

	// ② 自报内核沙箱提供者 → 不包装(嵌套必失败)
	if argv, env, wrapped := b.wrapPluginArgv(bin, Capabilities{SandboxProvider: true}, true, nil); wrapped || argv[0] != bin || len(env) != 0 {
		t.Fatalf("自报提供者不得被包装: argv=%v env=%v wrapped=%v", argv, env, wrapped)
	}

	// ③ 全权档 → 不包装(协作层也不拦,内核层无需施加)
	sb.mode = sdk.SandboxFullAccess
	if _, _, wrapped := b.wrapPluginArgv(bin, Capabilities{}, true, nil); wrapped {
		t.Fatal("全权档不得包装")
	}

	// ④ 显式关闭 → 不包装
	sb.mode = sdk.SandboxWorkspace
	t.Setenv(pluginKernelSandboxEnv, "0")
	if _, _, wrapped := b.wrapPluginArgv(bin, Capabilities{}, true, nil); wrapped {
		t.Fatal("GAH_EXT_PLUGIN_SANDBOX=0 时不得包装")
	}
}

// TestPluginSandboxSpecRWPaths 自报数据目录与用户点名路径进白名单(档位/根取自 ctx.sandbox)。
func TestPluginSandboxSpecRWPaths(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	root := t.TempDir()
	sb := &stubSandbox{mode: sdk.SandboxWorkspace, root: root}
	b := capsTestBridge(t, root, sb, logger)
	extra := t.TempDir()
	t.Setenv(pluginRWPathsEnv, extra)
	t.Setenv(pluginCredReadDenyEnv, "1")

	spec := b.pluginSandboxSpec(b.validDataWrites("x", []string{"memory", "config"}))
	if spec.Mode != sdk.SandboxWorkspace || spec.Root != root || spec.Jail != sdk.JailDir() {
		t.Fatalf("档位/根/临时区不符: %+v", spec)
	}
	joined := strings.Join(spec.RW, "|")
	for _, want := range []string{filepath.Join(sdk.Home(), "memory"), extra} {
		if !strings.Contains(joined, want) {
			t.Fatalf("白名单缺 %s: %v", want, spec.RW)
		}
	}
	if strings.Contains(joined, filepath.Join(sdk.Home(), "config")) {
		t.Fatalf("保留集不得进白名单: %v", spec.RW)
	}
	if len(spec.ReadDeny) == 0 {
		t.Fatal("凭据读拒绝开关=1 时应带上拒绝目录")
	}
}

// TestSandboxStaleFailClosed 档位变更 → 本次调用 fail-closed 拒绝 + 异步重载到新档位。
//
// 这是阻断 ②(profile 是启动时静态串)的验收:不检查就继续跑 = 档位切严后仍按旧档写。
func TestSandboxStaleFailClosed(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	root := t.TempDir()
	sb := &stubSandbox{mode: sdk.SandboxWorkspace, root: root}
	b := capsTestBridge(t, root, sb, logger)
	bin := buildCapsFixture(t, root)

	e, err := b.loadOne(bin)
	if err != nil {
		t.Fatalf("加载夹具失败: %v", err)
	}
	t.Cleanup(e.kill)
	tc := e.tools["caps"]
	if tc == nil {
		t.Fatalf("夹具未暴露 caps 工具: %v", e.tools)
	}
	if !e.wrapped {
		t.Skip("当前平台无法施加内核沙箱:跳过陈旧性断言")
	}
	b.entries[bin] = e

	// ① 档位未变:正常调用(顺带证明被包装的插件确实能跑起来)
	res, err := tc.Execute(context.Background(), `{"a":1}`)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if s, ok := res.(string); !ok || !strings.Contains(s, "ok:") {
		t.Fatalf("被包装插件的调用应正常返回: %#v", res)
	}

	// ② 档位切严:本次必须拒绝(不拿旧 profile 跑),并触发重载
	sb.mode = sdk.SandboxReadOnly
	res, err = tc.Execute(context.Background(), `{"a":2}`)
	if err != nil {
		t.Fatalf("陈旧调用应回结构化错误而非传输错误: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok || !strings.Contains(m["error"].(string), "沙箱上下文已变更") {
		t.Fatalf("档位变更必须 fail-closed: %#v", res)
	}

	// ③ 重载完成后条目的快照跟上新档位,调用恢复(不永久拒绝)
	deadline := time.Now().Add(10 * time.Second)
	for {
		b.mu.RLock()
		cur := b.entries[bin]
		done := cur != nil && cur.wrapMode == string(sdk.SandboxReadOnly) && cur.wrapped
		var trc *toolRPCClient
		if cur != nil {
			trc = cur.tools["caps"]
		}
		b.mu.RUnlock()
		if done && trc != nil {
			if res, err := trc.Execute(context.Background(), `{"a":3}`); err == nil {
				if s, ok := res.(string); ok && strings.Contains(s, "ok:") {
					return // 新档位下正常
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("重载未在预期内完成(entries=%v)", b.entries)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
