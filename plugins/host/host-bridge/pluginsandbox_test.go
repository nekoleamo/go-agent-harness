// pluginsandbox_test.go:外部插件进程内核沙箱接线的测试(能力自报 + 包装判定 + fail-closed 重载)。
//
// 纪律:包装判定不 mock kernelsandbox.Wrap —— 断言的是"宿主是否把包装前缀前缀到 argv 上"
// 这一可观察事实(darwin 上是 sandbox-exec,Linus 上是自举 helper);平台不支持时 Skip 并说明,
// 不让测试假装覆盖了没覆盖的分支。
package hostbridge

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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

	// ① 自报 shell 语义 + 数据目录
	t.Setenv("FIXTURE_CRED_READ_DENY", "1")
	t.Setenv("FIXTURE_DATA_WRITES", "memory,todos")
	caps, ok := probeCapabilities(bin)
	if !ok {
		t.Fatal("自报能力的插件应探测成功")
	}
	if !caps.CredentialReadDeny {
		t.Fatal("应读到 CredentialReadDeny 声明")
	}
	if strings.Join(caps.DataWrites, ",") != "memory,todos" {
		t.Fatalf("应读到数据目录声明: %v", caps.DataWrites)
	}

	// ② 零声明(探测成功但什么都没声明)= 「普通插件」:宿主仍按包装处理
	t.Setenv("FIXTURE_CRED_READ_DENY", "0")
	t.Setenv("FIXTURE_DATA_WRITES", "")
	if caps, ok := probeCapabilities(bin); !ok || caps.CredentialReadDeny || len(caps.DataWrites) != 0 {
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
		"Config",                // 保留集:大小写折叠(macOS/Windows 默认卷上就是同一个目录)
		"PLUGINS",               // 保留集:同上
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

	// ② 自报「进程内会跑 shell 命令」→ **仍包装**(嵌套由 MarkerEnv 承担,2026-09-27 审计 A6),
	//    只是多带凭据读拒绝(读拒绝本身在 TestPluginSandboxSpecReadDeny 单独钉)。
	if argv, env, wrapped := b.wrapPluginArgv(bin, Capabilities{CredentialReadDeny: true}, true, nil); !wrapped || argv[0] == bin || len(env) != 1 {
		t.Fatalf("自报 shell 语义的插件不得被免除包装: argv=%v env=%v wrapped=%v", argv, env, wrapped)
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

	spec := b.pluginSandboxSpec(b.validDataWrites("x", []string{"memory", "config"}), false)
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

// TestPluginSandboxSpecReadDeny 读拒绝的两个来源:插件自报(默认开,A6 开关语义决策)
// 或用户显式开关。两者都不在 ⇒ 不带(读凭据是插件正当职责)。
func TestPluginSandboxSpecReadDeny(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	root := t.TempDir()
	sb := &stubSandbox{mode: sdk.SandboxWorkspace, root: root}
	b := capsTestBridge(t, root, sb, logger)

	t.Setenv(pluginCredReadDenyEnv, "")
	if got := b.pluginSandboxSpec(nil, false).ReadDeny; len(got) != 0 {
		t.Fatalf("未声明且开关关闭时不应带读拒绝: %v", got)
	}
	if got := b.pluginSandboxSpec(nil, true).ReadDeny; len(got) == 0 {
		t.Fatal("插件自报 shell 语义时应默认带凭据读拒绝(否则 F1 保护静默失效)")
	}
	t.Setenv(pluginCredReadDenyEnv, "1")
	if got := b.pluginSandboxSpec(nil, false).ReadDeny; len(got) == 0 {
		t.Fatal("GAH_EXT_PLUGIN_CRED_READ_DENY=1 时应带凭据读拒绝")
	}
}

// TestSandboxStaleRebuildsBeforeCall 档位变更 → 本次调用**同步重建**到新档位再执行(不拿旧 profile 跑)。
//
// 这是阻断 ②(profile 是启动时静态串)的验收。语义修正见 pluginsandbox.go 的 sandboxStale
// 注释:A6 把「一律拒一次 + 异步重载」改成「先重建再执行」—— 执行的始终是新 profile,
// 同时不会让 `/ws` 切根后的第一次 shell/file 调用必失败(那个回归由
// `tests/TestExternalFileChangeLandsInLedger` 实测报出)。
func TestSandboxStaleRebuildsBeforeCall(t *testing.T) {
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

	// ② 档位切严:本次必须先重建到新档位再执行 —— 调用正常返回,且条目快照已跟到新档位
	sb.mode = sdk.SandboxReadOnly
	res, err = tc.Execute(context.Background(), `{"a":2}`)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if s, ok := res.(string); !ok || !strings.Contains(s, "ok:") {
		t.Fatalf("档位切换后的调用应在新 profile 下执行成功(而非被拒一次): %#v", res)
	}
	b.mu.RLock()
	cur := b.entries[bin]
	b.mu.RUnlock()
	if cur == nil || !cur.wrapped || cur.wrapMode != string(sdk.SandboxReadOnly) {
		t.Fatalf("调用前应以新档位重建: %+v", cur)
	}

	// ③ 新档位下继续可调用(不永久拒绝)
	res, err = cur.tools["caps"].Execute(context.Background(), `{"a":3}`)
	if err != nil {
		t.Fatalf("新档位下调用失败: %v", err)
	}
	if s, ok := res.(string); !ok || !strings.Contains(s, "ok:") {
		t.Fatalf("新档位下应正常返回: %#v", res)
	}
}

// TestWrappedPluginProcessIsKernelConstrained 被包装的插件**进程自身**在内核沙箱内(A6 的核心断言)。
//
// 为何需要这条:tool-basic 自身就是 shell 提供者,它过去被宿主豁免包装 ⇒ 进程内直写
// (memory/todos 数据根、file 工具)只有协作层裁决。撒掉豁免后需要一条**只可能由外层
// profile 生效**的证据:夹具的 in-process `os.WriteFile` 走不到任何协作层,所以
//
//	① 开关开(默认):写工作区外、且不在白名单里的路径 → 失败,且文件不存在;
//	② 开关关:同一路径写成功(灵敏度反证 —— 证明拦住它的确实是外层包装)。
//
// 目标路径取 $HOME 下的探测名(不能用系统临时目录:它在白名单里,会掩盖整件事)。
func TestWrappedPluginProcessIsKernelConstrained(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	root := t.TempDir()
	sb := &stubSandbox{mode: sdk.SandboxWorkspace, root: root}
	b := capsTestBridge(t, root, sb, logger)
	bin := buildCapsFixture(t, root)

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("无 HOME:无法选白名单外路径")
	}
	target := filepath.Join(home, fmt.Sprintf(".gah-a6-probe-%d", os.Getpid()))
	t.Cleanup(func() { _ = os.Remove(target) })
	t.Setenv("FIXTURE_WRITE_PATH", target)

	call := func(requireWrapped bool) string {
		e, lerr := b.loadOne(bin)
		if lerr != nil {
			t.Fatalf("加载夹具失败: %v", lerr)
		}
		t.Cleanup(e.kill)
		if requireWrapped && !e.wrapped {
			t.Skip("当前平台无法施加内核沙箱(zarwin 需 sandbox-exec,linux 需 Landlock ABI≥1):跳过")
		}
		b.mu.Lock()
		b.entries[bin] = e
		b.mu.Unlock()
		res, rerr := e.tools["caps_write"].Execute(context.Background(), `{}`)
		if rerr != nil {
			t.Fatalf("调用失败: %v", rerr)
		}
		s, _ := res.(string)
		return s
	}

	// ① 默认(被包装):插件进程自身的区外写必须被内核拒
	if s := call(true); strings.HasPrefix(s, "wrote:") {
		t.Fatalf("插件进程自身的区外写未被内核拦住: %q", s)
	}
	if _, serr := os.Stat(target); serr == nil {
		t.Fatalf("区外文件不该存在: %s", target)
	}

	// ② 反证:关掉包装 → 同一路径写成功(证明 ① 的失败来自外层 profile)
	t.Setenv(pluginKernelSandboxEnv, "0")
	if s := call(false); !strings.HasPrefix(s, "wrote:") {
		t.Fatalf("关掉包装后同一写应成功: %q", s)
	}
}
