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
	"sort"
	"strings"
	"sync"
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
	return capsTestBridgeWith(t, dir, sb, logger, &cbStubTools{})
}

// capsTestBridgeWith 同上,但用调用方给的注册表(分桶用例要数"注册了几次")。
func capsTestBridgeWith(t *testing.T, dir string, sb *stubSandbox, logger *slog.Logger, tools sdk.ToolRegistry) *Bridge {
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
		tools:   tools,
		entries: map[string]*extEntry{},
	}
}

// cntRegistry 计数注册表:分桶**不得**带来重复注册。
// 真实的 ctx.tools 在同名注册上是非静默的(记警告并跳过)⇒ 重复注册的表现是
// "工具莫名少一半",正是最容易在分桶改造里踩到的那类故障。
type cntRegistry struct {
	cbStubTools
	mu    sync.Mutex
	n     int
	names []string
}

func (r *cntRegistry) Register(t sdk.Tool) sdk.Disposer {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	r.names = append(r.names, t.Definition().Name)
	return func() {}
}

func (r *cntRegistry) count() (int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n, append([]string{}, r.names...)
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
	caps, ok, _ := probeCapabilities(bin, "")
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
	if caps, ok, _ := probeCapabilities(bin, ""); !ok || caps.CredentialReadDeny || len(caps.DataWrites) != 0 {
		t.Fatalf("零声明应探测成功且为空: ok=%v caps=%+v", ok, caps)
	}

	// ③ 旧插件(tool-echo 用裸 ServeRPC,不认 --gah-caps):探测失败 = 未声明
	buildExternalPlugin(t, dir)
	echo := filepath.Join(dir, testutil.ExeName("tool-echo"))
	if caps, ok, _ := probeCapabilities(echo, ""); ok {
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
	argv, env, wrapped := b.wrapPluginArgv(bin, "", Capabilities{}, false, nil)
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
	if argv, env, wrapped := b.wrapPluginArgv(bin, "", Capabilities{CredentialReadDeny: true}, true, nil); !wrapped || argv[0] == bin || len(env) != 1 {
		t.Fatalf("自报 shell 语义的插件不得被免除包装: argv=%v env=%v wrapped=%v", argv, env, wrapped)
	}

	// ③ 全权档 → 不包装(协作层也不拦,内核层无需施加)
	sb.mode = sdk.SandboxFullAccess
	if _, _, wrapped := b.wrapPluginArgv(bin, "", Capabilities{}, true, nil); wrapped {
		t.Fatal("全权档不得包装")
	}

	// ④ 显式关闭 → 不包装
	sb.mode = sdk.SandboxWorkspace
	t.Setenv(pluginKernelSandboxEnv, "0")
	if _, _, wrapped := b.wrapPluginArgv(bin, "", Capabilities{}, true, nil); wrapped {
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
		e, lerr := b.loadOne(bin, "")
		if lerr != nil {
			t.Fatalf("加载夹具失败: %v", lerr)
		}
		t.Cleanup(onlyInst(t, e).kill)
		if requireWrapped && !onlyInst(t, e).wrapped {
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

// onlyInst 取条目里**唯一**的实例(刚 loadOne 出来时必然只有一个):分桶后实例挂在
// e.insts 上,断言要落到实例而不是条目。
func onlyInst(t *testing.T, e *extEntry) *pluginInstance {
	t.Helper()
	if len(e.insts) != 1 {
		t.Fatalf("应恰好 1 个实例,得 %d: %v", len(e.insts), e.insts)
	}
	for _, inst := range e.insts {
		return inst
	}
	return nil
}

// instancesOf 取条目当前全部实例(按档位/根字符串排序,便于逐条断言)。
func instancesOf(e *extEntry) []*pluginInstance {
	out := make([]*pluginInstance, 0, len(e.insts))
	for _, inst := range e.insts {
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool { return tierKey(out[i].mode, out[i].root) < tierKey(out[j].mode, out[j].root) })
	return out
}

// TestPluginInstanceBucketedBySandboxContext 进程实例按**沙箱上下文**分桶:
// 同 (档, 根) 复用同一个进程,异档另起一个 —— 这是方案 A 的核心断言。
//
// 为什么必须分桶(2026-10-24):内核 profile 是启动时静态串,而"该按哪一档"由**调用方会话**
// 决定。只有一个实例时,两个页签不同档交替调用只能反复冷启动(方案 B 的代价),并且每次都丢掉
// 插件进程内状态;分桶后同档复用、异档各有一个,进程档永远等于本次调用的期望档。
//
// **反向验证**:把 tierKey 改回恒定 ""(即不分桶)⇒ 本用例红(实例数停在 1)。
func TestPluginInstanceBucketedBySandboxContext(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	root := t.TempDir()
	sb := &stubSandbox{mode: sdk.SandboxWorkspace, root: root} // 全局:workspace-write
	reg := &cntRegistry{}
	b := capsTestBridgeWith(t, root, sb, logger, reg)
	bin := buildCapsFixture(t, root)

	e, err := b.loadOne(bin, "")
	if err != nil {
		t.Fatalf("加载夹具失败: %v", err)
	}
	t.Cleanup(func() {
		for _, inst := range instancesOf(e) {
			inst.kill()
		}
	})
	// 注册与实例分离:注册表里只有一件"与档无关的代理",异档拉起实例**不重注册**。
	unreg := b.registerAll(e)
	t.Cleanup(unreg)
	b.mu.Lock()
	b.entries[bin] = e
	b.mu.Unlock()
	tc := e.tools["caps"]
	if tc == nil {
		t.Fatalf("夹具未暴露 caps 工具: %v", e.tools)
	}

	wt := t.TempDir()
	ctxRO := sdk.WithSandboxHint(context.Background(), sdk.SandboxHint{Mode: sdk.SandboxReadOnly, Root: wt})
	_ = wt
	// ① 只读档:另起一个实例,档/根跟住 hint
	if res, err := tc.Execute(ctxRO, `{"a":1}`); err != nil {
		t.Fatalf("调用失败: %v", err)
	} else if v, ok := res.(string); !ok || !strings.Contains(v, "ok:") {
		t.Fatalf("按新档拉起的实例应正常返回: %#v", res)
	}
	roInst := b.instanceForTest(t, bin, string(sdk.SandboxReadOnly), wt)
	if roInst.mode != string(sdk.SandboxReadOnly) || roInst.root != wt {
		t.Fatalf("只读桶实例的档/根不符: %+v", roInst)
	}

	// ② 同一档再调:复用同一个进程(不新起、不重建)
	if _, err := tc.Execute(ctxRO, `{"a":2}`); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if again := b.instanceForTest(t, bin, string(sdk.SandboxReadOnly), wt); again != roInst {
		t.Fatal("同档调用应复用同一实例,而不是每次重建(方案 B 的代价)")
	}

	// ③ 工作区档 + 隔离根:再起一个;总共两个实例,各自的档/根都对
	ctxWT := sdk.WithSandboxHint(context.Background(), sdk.SandboxHint{Mode: sdk.SandboxWorkspace, Root: root})
	if _, err := tc.Execute(ctxWT, `{"a":3}`); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	b.mu.RLock()
	insts := instancesOf(b.entries[bin])
	b.mu.RUnlock()
	if len(insts) != 2 {
		t.Fatalf("两档应各一个实例,得 %d: %+v", len(insts), insts)
	}
	modes := []string{insts[0].mode + "@" + insts[0].root, insts[1].mode + "@" + insts[1].root}
	sort.Strings(modes)
	want := []string{string(sdk.SandboxReadOnly) + "@" + wt, string(sdk.SandboxWorkspace) + "@" + root}
	sort.Strings(want)
	if strings.Join(modes, "|") != strings.Join(want, "|") {
		t.Fatalf("实例档/根不符:\n got=%v\nwant=%v", modes, want)
	}
	// 异档调用**不得**再注册(注册 = 与档无关的代理,实例只是它背后的进程):
	// 判据是"同一个工具名没被注册两次" —— 真实注册表会跳过同名并记警告(工具少一半)。
	_, names := reg.count()
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Fatalf("分桶带来了重复注册(真实注册表会跳过同名 ⇒ 工具少一半): %v", names)
		}
		seen[n] = true
	}
	if len(seen) != 2 { // 夹具暴露 caps + caps_write
		t.Fatalf("应注册夹具的两个工具,得 %v", names)
	}
}

// instanceForTest 断言某桶实例存在并返回它(测试辅助)。
func (b *Bridge) instanceForTest(t *testing.T, path, mode, root string) *pluginInstance {
	t.Helper()
	b.mu.RLock()
	defer b.mu.RUnlock()
	e := b.entries[path]
	if e == nil {
		t.Fatalf("条目缺失: %s", path)
	}
	inst := e.insts[tierKey(mode, root)]
	if inst == nil {
		t.Fatalf("桶 %q 无实例: %v", tierKey(mode, root), e.insts)
	}
	return inst
}

// TestPluginBucketKernelEnforcesPerSessionTier 分桶的**安全判据**:同一份插件代码、同一个
// 目标文件,在工作区档桶里写得进去,在只读档桶里被内核拒。
//
// 为什么这条比"实例数对了"更重要:实例数只是机制;**真正要证明的是"会话只读"能关上插件
// 进程自身的写** —— 那是协作层看不见的写(插件自己拼路径),也是本层存在的唯一理由。
// 目标文件放在工作区根**内部**:协作层会放行,能拦住它只可能是内核 profile。
//
// 平台无内核沙箱(Windows 等)⇒ Skip 并说明,不假装覆盖。
//
// **反向验证**:分桶退化成单桶(或 Execute 不按 hint 选桶)⇒ 只读档那次会写成功 ⇒ 红。
func TestPluginBucketKernelEnforcesPerSessionTier(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	// 工作区根**不能**取 t.TempDir():系统临时目录(/private/var/folders、/tmp)本来就在
	// 内核白名单里(否则 TMPDIR/GOCACHE 会大面积断裂),放在那里连只读档也写得进去 ——
	// 用例会变成"恒绿但什么也没测"。取 HOME 下的临时目录,才是真正需要按档放行/拒绝的落点。
	home, herr := os.UserHomeDir()
	if herr != nil || home == "" {
		t.Skip("无 HOME:无法选内核白名单外的落点")
	}
	root, terr := os.MkdirTemp(home, ".gah-bucket-probe-")
	if terr != nil {
		t.Fatalf("建工作区根失败: %v", terr)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	sb := &stubSandbox{mode: sdk.SandboxWorkspace, root: root}
	b := capsTestBridge(t, root, sb, logger)
	bin := buildCapsFixture(t, root)
	// 目标文件必须在**拉起进程之前**定下:实例的环境在 startPlugin 那一刻固化。
	target := filepath.Join(root, "inside.txt")
	t.Setenv("FIXTURE_WRITE_PATH", target)

	e, err := b.loadOne(bin, "")
	if err != nil {
		t.Fatalf("加载夹具失败: %v", err)
	}
	t.Cleanup(func() {
		for _, inst := range instancesOf(e) {
			inst.kill()
		}
	})
	if !onlyInst(t, e).wrapped {
		t.Skip("当前平台无法施加内核沙箱(darwin 需 sandbox-exec,linux 需 Landlock ABI≥1):跳过")
	}
	b.mu.Lock()
	b.entries[bin] = e
	b.mu.Unlock()
	tc := e.tools["caps_write"]
	if tc == nil {
		t.Fatalf("夹具未暴露 caps_write: %v", e.tools)
	}

	// ① 工作区档:根内部的写放行(证明这条路径在协作层是"合法"的)
	ctxWS := sdk.WithSandboxHint(context.Background(), sdk.SandboxHint{Mode: sdk.SandboxWorkspace, Root: root})
	res, err := tc.Execute(ctxWS, `{}`)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if v, _ := res.(string); !strings.HasPrefix(v, "wrote:") {
		t.Fatalf("工作区档下根内写应成功: %q", v)
	}
	if err := os.Remove(target); err != nil {
		t.Fatalf("清理探测文件失败: %v", err)
	}

	// ② 只读档:**同一目标、同一份代码**,内核 profile 不同 ⇒ 必须被拒
	ctxRO := sdk.WithSandboxHint(context.Background(), sdk.SandboxHint{Mode: sdk.SandboxReadOnly, Root: root})
	res, err = tc.Execute(ctxRO, `{}`)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if v, _ := res.(string); strings.HasPrefix(v, "wrote:") {
		t.Fatalf("会话只读档下插件进程自身的写未被拦住(分桶没生效?): %q", v)
	}
	if _, serr := os.Stat(target); serr == nil {
		t.Fatalf("只读档下不该留下文件: %s", target)
	}
}

// TestReloadAndUnloadCoverAllBuckets 生命周期必须覆盖**同一件插件的全部桶**:漏一个桶
// 就是"孤儿进程拿着旧档的写权在跑"(用户以为已重载/已卸载,实际没有)。
//
// 两条判据:① 重载 = 旧实例的进程真的没了(对它发 RPC 必失败)+ 只剩一个桶;
// ② 卸载 = 条目没了 + 调用给"重建中"(而不是让调用落进一个已死的 client)。
//
// **反向验证**:重载里只关"当前档那个实例"(分桶前的写法)⇒ 旧实例仍可 RPC ⇒ 红。
func TestReloadAndUnloadCoverAllBuckets(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	root := t.TempDir()
	sb := &stubSandbox{mode: sdk.SandboxWorkspace, root: root}
	b := capsTestBridge(t, root, sb, logger)
	bin := buildCapsFixture(t, root)

	e, err := b.loadOne(bin, "")
	if err != nil {
		t.Fatalf("加载夹具失败: %v", err)
	}
	unreg := b.registerAll(e)
	b.mu.Lock()
	e.unreg = unreg
	b.entries[bin] = e
	b.mu.Unlock()
	t.Cleanup(b.closeAll)

	tc := e.tools["caps"]
	wt := t.TempDir()
	ctxRO := sdk.WithSandboxHint(context.Background(), sdk.SandboxHint{Mode: sdk.SandboxReadOnly, Root: wt})
	if _, err := tc.Execute(ctxRO, `{"a":1}`); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	oldRO := b.instanceForTest(t, bin, string(sdk.SandboxReadOnly), wt)
	if len(instancesOf(e)) != 2 {
		t.Fatalf("应有 2 个桶实例: %v", len(instancesOf(e)))
	}

	// ① 重载:全部桶的进程都要停,新条目只带当前档一个实例
	if err := b.reload(bin, ""); err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	var out string
	if err := rpcCall(oldRO.client, "Plugin.Definitions", struct{}{}, &out, time.Second); err == nil {
		t.Fatal("重载后旧实例的进程应已关闭(只关当前档那个桶会留下孤儿)")
	}
	b.mu.RLock()
	after := b.entries[bin]
	b.mu.RUnlock()
	if after == nil || len(after.insts) != 1 {
		t.Fatalf("重载后应只剩当前档一个实例: %+v", after)
	}

	// ② 卸载:条目摘除 + 调用给结构化错误(不留"半死"client)
	if n := b.unloadPath(bin); n != 1 {
		t.Fatalf("卸载应摘掉 1 个条目,得 %d", n)
	}
	res, rerr := after.tools["caps"].Execute(context.Background(), `{}`)
	if rerr != nil {
		t.Fatalf("宿主不应 panic: %v", rerr)
	}
	if !strings.Contains(errText(res), "重建中") {
		t.Fatalf("卸载后调用应给「重建中」: %#v", res)
	}
}

// TestPluginBucketEvictsIdleInstances 保留数上限:桶键含工作根 ⇒ 不设上限就会随 worktree
// 数量无界增长(长跑时表现为"孤儿进程拿着旧档的写权",用户 `ps` 一脸问号)。
// 规则 = 当前档 + 最近使用的 maxIdleInstances 个,其余回收(关进程 + 摘表)。
func TestPluginBucketEvictsIdleInstances(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	root := t.TempDir()
	sb := &stubSandbox{mode: sdk.SandboxWorkspace, root: root}
	reg := &cntRegistry{}
	b := capsTestBridgeWith(t, root, sb, logger, reg)
	bin := buildCapsFixture(t, root)

	e, err := b.loadOne(bin, "")
	if err != nil {
		t.Fatalf("加载夹具失败: %v", err)
	}
	t.Cleanup(func() {
		for _, inst := range instancesOf(e) {
			inst.kill()
		}
	})
	// 注册与实例分离:注册表里只有一件"与档无关的代理",异档拉起实例**不重注册**。
	unreg := b.registerAll(e)
	t.Cleanup(unreg)
	b.mu.Lock()
	b.entries[bin] = e
	b.mu.Unlock()
	tc := e.tools["caps"]

	// 四个不同的桶(两档 × 两个根),逐个调用
	type bucket struct {
		mode sdk.SandboxMode
		root string
	}
	roots := []string{t.TempDir(), t.TempDir()}
	var order []bucket
	for _, m := range []sdk.SandboxMode{sdk.SandboxReadOnly, sdk.SandboxWorkspace} {
		for _, r := range roots {
			order = append(order, bucket{m, r})
		}
	}
	for _, bk := range order {
		ctx := sdk.WithSandboxHint(context.Background(), sdk.SandboxHint{Mode: bk.mode, Root: bk.root})
		if res, err := tc.Execute(ctx, `{"a":1}`); err != nil || res == nil {
			t.Fatalf("调用失败: %v", err)
		}
	}

	b.mu.RLock()
	insts := instancesOf(b.entries[bin])
	b.mu.RUnlock()
	if len(insts) != maxIdleInstances+1 {
		t.Fatalf("实例数应封顶在 %d(当前档 + %d 个空闲档),得 %d", maxIdleInstances+1, maxIdleInstances, len(insts))
	}
	// 最早用过的那一桶应被回收
	b.mu.RLock()
	_, alive := b.entries[bin].insts[tierKey(string(order[0].mode), order[0].root)]
	b.mu.RUnlock()
	if alive {
		t.Fatalf("最久未用的桶应被回收: %v", tierKey(string(order[0].mode), order[0].root))
	}

	// 有调用在跑的实例**不回收**:长耗时工具跑到一半被"空闲回收"掐掉,用户看到的是
	// "外部插件不可达",而它是被我们自己杀的。宁可暂时超上限。
	b.mu.RLock()
	var busy *pluginInstance
	var busyKey string
	for k, inst := range b.entries[bin].insts {
		if busy == nil || inst.lastUsed.Load() < busy.lastUsed.Load() {
			busy, busyKey = inst, k
		}
	}
	b.mu.RUnlock()
	if busy == nil {
		t.Fatal("夹具状态异常:没有实例")
	}
	busy.inflight.Add(1)
	defer busy.inflight.Add(-1)
	ctxNew := sdk.WithSandboxHint(context.Background(), sdk.SandboxHint{Mode: sdk.SandboxReadOnly, Root: t.TempDir()})
	if _, err := tc.Execute(ctxNew, `{"a":1}`); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	b.mu.RLock()
	_, busyAlive := b.entries[bin].insts[busyKey]
	b.mu.RUnlock()
	if !busyAlive {
		t.Fatal("有调用在跑的实例不得被回收(会表现为「外部插件不可达」)")
	}
}
