// lifecycle_test.go:批二 —— 启用/停用/卸载的**行为**钉住。
//
// 钉的是那些「看着对但实际会漏」的不变量:
//   - 停用 = 进程没了 + 工具从注册表消失,但文件与白名单条目**留着**;
//   - 停用状态在 **boot 时**就要生效(止损手段在真实事故里的用法是"重启回来它还在吗");
//   - 重新启用**重验漂移**:tag 被 force-push 过 ⇒ 拒绝,并给可执行出路;
//   - 删掉二进制 ⇒ 进程真的消失(不是「重启后才发现」);
//   - 回合执行中停用 ⇒ 正在跑的工具调用给**可读错误**,宿主不崩。
package hostbridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/install"
	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/internal/testutil"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// homeFor 把 GAH_HOME 指到临时目录(偏好与来源账都落在它下面)。
func homeFor(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	return home
}

// callErr 调一次外部插件的工具,返回**任何**失败(Execute 的 err 或结果里的 Error)。
//
// 为什么两个都要看:工具不存在/调用失败时 host-tools 返回的是 (result, nil) + result.Error
// —— 只看 err 会把「工具没了」误读成「调用成功」。
func callErr(t *testing.T, tools sdk.ToolRegistry, args string) string {
	t.Helper()
	res, err := tools.Execute(context.Background(), "echo", args)
	if err != nil {
		return err.Error()
	}
	return res.Error
}

// bridgeEnv 起一套 host-tools + host-bridge,返回控制面与工具注册表。
func bridgeEnv(t *testing.T, dir string) (sdk.ExternalPlugins, sdk.ToolRegistry) {
	t.Helper()
	c, _ := buildEnv(t, dir)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	var extp sdk.ExternalPlugins
	if err := c.Inject("ctx.extplugins", &extp); err != nil {
		t.Fatal(err)
	}
	if extp == nil {
		t.Fatal("ctx.extplugins 不应为 nil")
	}
	return extp, tools
}

// TestDisableRemovesProcessAndTools 停用 ⇒ 进程停掉 + 工具从注册表消失 + 文件留着。
//
// 「文件留着」这一条是「停用 ≠ 卸载」的全部意义:留着文件与白名单条目,
// 重新启用时**不需要重新 trust**(哈希仍匹配)。
func TestDisableRemovesProcessAndTools(t *testing.T) {
	homeFor(t)
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	extp, tools := bridgeEnv(t, dir)

	// 前置:工具确实在
	if e := callErr(t, tools, `{"text":"x"}`); e != "" {
		t.Fatalf("前置:工具应可用: %s", e)
	}

	if err := extp.Disable("tool-echo"); err != nil {
		t.Fatalf("停用应成功: %v", err)
	}
	// ① 工具消失
	if e := callErr(t, tools, `{"text":"x"}`); e == "" {
		t.Error("停用后工具仍可调用 —— 停用没生效")
	}
	// ② 文件还在
	if _, err := os.Stat(filepath.Join(dir, testutil.ExeName("tool-echo"))); err != nil {
		t.Errorf("停用不该删文件: %v", err)
	}
	// ③ 偏好里记下了(跨重启的止损手段)
	if !prefs.IsExternalDisabled("tool-echo") {
		t.Error("停用状态应落盘(否则重启后它又自己回来了)")
	}
	// ④ 停用是幂等的(点两次不是错误)
	if err := extp.Disable("tool-echo"); err != nil {
		t.Errorf("重复停用应幂等: %v", err)
	}
	// ⑤ 清单里能看到它,且带 disabled 标记(否则用户以为自己停错了)
	list := extp.List()
	if len(list) != 1 || !list[0].Disabled || list[0].Loaded {
		t.Errorf("清单应列出停用态: %+v", list)
	}
}

// TestDisabledAtBootNotStarted 停用状态**在启动时**就要生效(boot 时不加载)。
//
// 为什么单独测:这是止损手段在**真实事故**里的用法 —— 用户关掉 gah、停用、
// 再开回来。如果只在运行中生效,重启一次它就回来了,整条止损路径是假的。
func TestDisabledAtBootNotStarted(t *testing.T) {
	homeFor(t)
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	prefs.SetExternalDisabled("tool-echo", true)

	extp, tools := bridgeEnv(t, dir)
	if e := callErr(t, tools, `{"text":"x"}`); e == "" {
		t.Error("停用过的插件在 boot 时不该被加载")
	}
	if list := extp.List(); len(list) != 1 || list[0].Loaded {
		t.Errorf("清单应为「未加载」: %+v", list)
	}
}

// TestEnableAfterDisable 停用 → 启用 ⇒ 工具回来,且**不需要重新 trust**。
func TestEnableAfterDisable(t *testing.T) {
	homeFor(t)
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	extp, tools := bridgeEnv(t, dir)
	if err := extp.Disable("tool-echo"); err != nil {
		t.Fatal(err)
	}
	if err := extp.Enable("tool-echo"); err != nil {
		t.Fatalf("启用应成功: %v", err)
	}
	if e := callErr(t, tools, `{"text":"back"}`); e != "" {
		t.Errorf("启用后工具应回来: %s", e)
	}
	if prefs.IsExternalDisabled("tool-echo") {
		t.Error("启用后不应还留在停用名单里")
	}
}

// TestDisableDuringToolCall 回合执行中停用 ⇒ 正在跑的工具调用给**可读错误**,宿主不崩。
//
// 这是「随时支持启停」的真正代价与承诺(批二 §2.8):调用会因 RPC 断连失败,
// 用户可重试;但宿主绝不能 panic —— 一个行为不良的插件不该能把整个会话带崩。
func TestDisableDuringToolCall(t *testing.T) {
	homeFor(t)
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	extp, tools := bridgeEnv(t, dir)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			// 有错误是预期内的(调用会因断连失败),但必须是普通 error 而不是崩溃。
			if e := callErr(t, tools, `{"text":"race"}`); strings.Contains(e, "panic") {
				t.Errorf("不应出现 panic 文案: %s", e)
				return
			}
		}
	}()
	time.Sleep(20 * time.Millisecond)
	if err := extp.Disable("tool-echo"); err != nil {
		t.Fatalf("停用应成功: %v", err)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("停用期间的工具调用卡死了 —— 那意味着 RPC 断连没有软降级")
	}
}

// TestRemovedFileUnloadsRunningPlugin 删掉二进制 ⇒ **进程真的消失**(不是重启后才发现)。
//
// 这是批二要修的核心洞:watcher 此前只报 Write|Create|Rename,Remove 被显式排除,
// 而外部插件这条路没有 plugin-manager 介入 ⇒ 用户 rm 掉文件,进程继续跑到 gah 重启。
// 「我删了它」给的是虚假的安全感。
func TestRemovedFileUnloadsRunningPlugin(t *testing.T) {
	homeFor(t)
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	// watch 开着(这正是缺口所在的那条路)
	extp, tools := bridgeEnv(t, dir)
	if err := os.Remove(filepath.Join(dir, testutil.ExeName("tool-echo"))); err != nil {
		t.Fatal(err)
	}
	// watcher 去抖 300ms,给足余量(Windows 上进程退出与文件释放更慢)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if callErr(t, tools, `{"text":"x"}`) != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if e := callErr(t, tools, `{"text":"x"}`); e == "" {
		t.Errorf("删掉二进制后工具仍在 —— 进程没被停(虚假的安全感)")
	}
	for _, info := range extp.List() {
		if info.Name == "tool-echo" && info.Loaded {
			t.Errorf("删掉二进制后仍报已加载: %+v", info)
		}
	}
}

// TestEnableRechecksTagDrift 重新启用**重验漂移**(批二 §2.5)。
//
// 不重验的话「停用」就是绕过漂移守卫的后门:用户停用半年后回来,作者可能已经把 tag 改了,
// 而我们会把一份几个月前的二进制当成"当初那一版"放回去。
//
// 用一个本地裸仓库把 tag 强推一遍(不需要网络)。
func TestEnableRechecksTagDrift(t *testing.T) {
	home := homeFor(t)
	remote, work := makeTaggedRemote(t, "v1.0.0")
	var ledger install.SourceLedger
	ledger.Record(install.SourceEntry{
		Repo: remote, Ref: "v1.0.0", Kind: install.KindTag,
		Commit:   gitOut(t, work, "rev-parse", "HEAD"),
		PluginID: "demo", Origin: install.OriginUser, InstalledAt: "2026-10-03T00:00:00Z",
	})
	if err := install.WriteSources(home, &ledger); err != nil {
		t.Fatal(err)
	}
	// 二进制落在 <home>/plugins/demo/ 下(安装器布局:父目录名 = 插件 id = 账的键)
	plugDir := filepath.Join(home, "plugins", "demo")
	if err := os.MkdirAll(plugDir, 0o755); err != nil {
		t.Fatal(err)
	}
	buildExternalPlugin(t, plugDir)
	renameBin(t, filepath.Join(plugDir, testutil.ExeName("tool-echo")), filepath.Join(plugDir, testutil.ExeName("tool-demo")))

	extp, _ := bridgeEnv(t, plugDir)
	if err := extp.Disable("tool-demo"); err != nil {
		t.Fatal(err)
	}
	// ① tag 没动 ⇒ 启用成功
	if err := extp.Enable("tool-demo"); err != nil {
		t.Fatalf("tag 未漂移时应能启用: %v", err)
	}
	if err := extp.Disable("tool-demo"); err != nil {
		t.Fatal(err)
	}
	// ② 强推同一个 tag ⇒ 拒绝,且文案给两条出路
	forcePushTag(t, work, remote, "v1.0.0")
	err := extp.Enable("tool-demo")
	if err == nil {
		t.Fatal("同名 tag 被 force-push 后必须拒绝启用")
	}
	for _, want := range []string{"--accept-drift", "commit sha"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("拒绝文案应含 %q: %v", want, err)
		}
	}
	// ③ 拒绝后仍是停用态(不能出现"既没启用也没停用"的中间态)
	if !prefs.IsExternalDisabled("tool-demo") {
		t.Error("拒绝启用后应仍处于停用态")
	}
}

// TestEnableAllowsWhenRemoteUnreachable 问不到远端 ⇒ **放行**(判不出漂移 ≠ 有漂移)。
//
// 反过来会让离线用户永远开不了插件 —— 那是拿一个真问题换一个假问题。
func TestEnableAllowsWhenRemoteUnreachable(t *testing.T) {
	home := homeFor(t)
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	var ledger install.SourceLedger
	ledger.Record(install.SourceEntry{
		// 指向一个不存在的本地路径 = 问不到远端
		Repo: filepath.Join(t.TempDir(), "gone.git"), Ref: "v1.0.0", Kind: install.KindTag,
		Commit: strings.Repeat("a", 40), PluginID: "tool-echo",
	})
	if err := install.WriteSources(home, &ledger); err != nil {
		t.Fatal(err)
	}
	extp, _ := bridgeEnv(t, dir)
	if err := extp.Disable("tool-echo"); err != nil {
		t.Fatal(err)
	}
	if err := extp.Enable("tool-echo"); err != nil {
		t.Errorf("问不到远端时应放行(离线不该开不了插件): %v", err)
	}
}

// —— 小工具 ——

// makeTaggedRemote 造一个裸 remote + 一个工作仓库,并打上 tag。返回(remote 路径, 工作区)。
func makeTaggedRemote(t *testing.T, tag string) (string, string) {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitCmd(t, t.TempDir(), "init", "-q", "--bare", remote)
	work := t.TempDir()
	gitCmd(t, work, "init", "-q")
	if err := os.WriteFile(filepath.Join(work, "x"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "config", "user.email", "t@t")
	gitCmd(t, work, "config", "user.name", "t")
	gitCmd(t, work, "add", ".")
	gitCmd(t, work, "commit", "-qm", "init")
	gitCmd(t, work, "tag", tag)
	gitCmd(t, work, "push", "-q", remote, "HEAD:refs/heads/main")
	gitCmd(t, work, "push", "-q", remote, "refs/tags/"+tag+":refs/tags/"+tag)
	return remote, work
}

// forcePushTag 强推同名 tag(模拟作者重推 / 账号易手后的新人)。
func forcePushTag(t *testing.T, work, remote, tag string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(work, "x"), []byte("hijacked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "add", ".")
	gitCmd(t, work, "commit", "-qm", "hijack")
	gitCmd(t, work, "tag", "-f", tag)
	gitCmd(t, work, "push", "-qf", remote, "refs/tags/"+tag+":refs/tags/"+tag)
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v(%s)", args, err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v(%s)", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// renameBin 重命名二进制(让文件名与账里的插件 id 对上)。
func renameBin(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}
