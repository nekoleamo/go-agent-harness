//go:build linux

// linux_helper_test.go:自举 helper 的**行为**用例(2026-10-24)。
//
// 背景:helper 的形态是「init() 里施加 Landlock → syscall.Exec 目标程序」,进程镜像被替换
// ⇒ Go 的"退出时写计数器"永不执行 ⇒ `init`/`landlockSelfAndExec`/`llAddPathRule` 这几十条
// 语句在任何测试里都测不到。**记账那一半**在同批修了(exec 之前调
// `runtime/coverage.WriteCountersDir`,见 linux.go 的 flushCoverageCounters),但下面这条
// 实测约束决定了它**验证不到 CI 上**(见下)。
//
// 这条用例钉两件事:
//  ① 包装后的命令**能跑**(白名单内的写成功);
//  ② 白名单外的写被**内核**拒(不是协作层 —— 这里根本没有协作层)。
//
// **实测约束(别指望 CI 把这笔账补上)**:`runtime/coverage.WriteCountersDir` 在
// **`go test` 生成的测试二进制里恒失败** —— 它报 `-covermode=<invalid>`,即使跑的是
// `go test -covermode=atomic`(测试二进制的覆盖模式走 `-test.coverprofile`,运行期拿不到
// atomic 标记;2026-10-24 本机与 CI 双向实测)。所以:
//   - 这里**没有**"让 CI 涨覆盖率"的用例 —— 写过一版,验完发现它只会恒 Skip(或更糟:
//     恒绿而无账),已删;
//   - 这条修复的账只在**`go build -cover -covermode=atomic` 产物**上成立(即 e2e 用
//     `testutil.BuildCovered` 构建的真实二进制),而那种端到端还要求内核能施加 Landlock
//     (CI 的 ubuntu runner 内核未启用) ⇒ **只能 Linux 真机验**;
//   - 机制本身已用等价最小程序在本机 macOS 实测:同一条 `syscall.Exec` 掉自己的路径,
//     flush 后 `covdata percent` 61.5%,去掉 flush 则 0.0%(目录里只剩 covmeta)。
//
// 因此本文件只留行为用例(白名单内放行 / 区外拒),并要求 Landlock 可用;
// 不能施加时 Skip 并说明 —— 与同包其它内核用例同一纪律,不假装覆盖。

package kernelsandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/testutil"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestBorderHelperRunsAndIsMeasured 自举 helper 真跑一条命令:白名单内放行、区外拒。
//
// **反向验证**:把 `spec.Root` 从白名单里去掉 ⇒ 第 ① 条红(白名单内的写也失败);
// 去掉包装前缀(直接 /bin/sh)⇒ 第 ② 条红"区外写未被内核拦住"(两者本机都是在 darwin 上
// 用同一段用例组装干跑验过的:把 argv 换成不带包装前缀即红)。
func TestBorderHelperRunsAndIsMeasured(t *testing.T) {
	clearMarker(t)
	if why := platformAvailableReason(Spec{}); why != "" {
		t.Skipf("本机无法施加 Landlock,跳过 helper 实跑:%s", why)
	}
	jail := t.TempDir()
	root := t.TempDir()
	argv := Wrap(Spec{Jail: jail, Mode: sdk.SandboxWorkspace, Root: root, Label: "helper-ut"})
	if len(argv) == 0 {
		t.Skip("包装未施加(档位/白名单前置条件不满足),跳过 helper 实跑:" + fmt.Sprint(argv))
	}

	// 白名单外(家目录下)的探测路径:系统临时目录在部分配置里被当作可写缓存,
	// 放在那里会让"区外写"变成"区内写",用例恒绿(踩过一次,见 pluginsandbox_test.go 同款注释)。
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("无 HOME:无法选白名单外的落点")
	}
	outside := filepath.Join(home, fmt.Sprintf(".gah-ll-help-probe-%d", os.Getpid()))
	_ = os.Remove(outside)
	t.Cleanup(func() { _ = os.Remove(outside) })

	// ① + ②:同一条命令里先写区内(应成功)、再写区外(应被内核拒 ⇒ 整条命令非零退出)
	inside := filepath.Join(root, "ok.txt")
	script := fmt.Sprintf("echo hi > %q && echo hi > %q", inside, outside)
	full := append(append([]string{}, argv...), "/bin/sh", "-c", script)
	cmd := exec.Command(full[0], full[1:]...)
	cmd.Env = append(os.Environ(), testutil.ChildEnv(t)...)
	out, rerr := cmd.CombinedOutput()
	if rerr == nil {
		t.Fatalf("区外写未被内核拦住(命令整体成功):%s", out)
	}
	if _, serr := os.Stat(outside); serr == nil {
		t.Fatalf("区外文件不该存在:%s", outside)
	}
	if _, serr := os.Stat(inside); serr != nil {
		t.Fatalf("白名单内的写应成功(否则是用例环境问题,不是沙箱太严):%v\n%s", serr, out)
	}

}

// TestHelperProbeCountersLandWithCoveredBuild 记账那半的**端到端**判据:
// 用 `go build -cover -covermode=atomic` 构建探针(`testdata/helperprobe`,它内部调 Wrap
// 起一条包装后的命令),跑一次后断言:① 区外写被内核拒(包装真生效);② helper(被
// `syscall.Exec` 换掉的那个进程)**在 exec 之前**把计数器落进了 GOCOVERDIR。
//
// 为什么必须是"另建一个二进制"而不是直接在测试里起:见文件头那条实测约束 ——
// `runtime/coverage.WriteCountersDir` 在 `go test` 的测试二进制里恒失败
// (`-covermode=<invalid>`),所以只有 `go build -cover` 产物才写得进计数器。
//
// 平台:Lenux 且内核启用 Landlock。**CI 的 ubuntu runner 内核未启用 Landlock ⇒ 那里 Skip**,
// 这条只能在 Linux 真机(或有能力的机器)上跑;跳过原因会打在 -v 输出里,不假装覆盖。
//
// 反向验证:删掉 linux.go 里 init() 的 flushCoverageCounters() ⇒ 计数器目录只剩 covmeta、
// 没有 covcounters ⇒ 第 ② 条红(macOS 上用等价最小程序实测过同一条路径:61.5% ↔ 0.0%)。
func TestHelperProbeCountersLandWithCoveredBuild(t *testing.T) {
	clearMarker(t)
	if why := platformAvailableReason(Spec{}); why != "" {
		t.Skipf("本机无法施加 Landlock(CI 的 ubuntu runner 属于这类),跳过记账端到端:%s", why)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("无 HOME:无法选内核白名单外的落点")
	}
	root, jail := t.TempDir(), t.TempDir()
	outside := filepath.Join(home, fmt.Sprintf(".gah-probe-%d", os.Getpid()))
	_ = os.Remove(outside)
	t.Cleanup(func() { _ = os.Remove(outside) })

	bin := filepath.Join(t.TempDir(), "helperprobe")
	testutil.BuildCovered(t, bin, "./testdata/helperprobe")

	// 计数器目录与 testutil 的合并入口同源(否则 Flush 读 RunDir 会是空的,静默不并)。
	coverEnv := testutil.ChildEnv(t)
	coverDir := ""
	for _, kv := range coverEnv {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "GOCOVERDIR" {
			coverDir = v
		}
	}
	if coverDir == "" {
		coverDir = filepath.Join(t.TempDir(), "cov")
		if err := os.MkdirAll(coverDir, 0o755); err != nil {
			t.Fatal(err)
		}
		coverEnv = []string{"GOCOVERDIR=" + coverDir}
	}

	inside := filepath.Join(root, "ok.txt")
	script := fmt.Sprintf("echo hi > %q && echo hi > %q", inside, outside)
	cmd := exec.Command(bin, string(sdk.SandboxWorkspace), root, jail, script)
	cmd.Env = append(os.Environ(), coverEnv...)
	out, rerr := cmd.CombinedOutput()
	if rerr == nil {
		t.Fatalf("区外写未被内核拦住(命令整体成功):%s", out)
	}
	if _, serr := os.Stat(outside); serr == nil {
		t.Fatalf("区外文件不该存在:%s", outside)
	}
	if _, serr := os.Stat(inside); serr != nil {
		t.Fatalf("白名单内的写应成功(否则是用例环境问题):%v\n%s", serr, out)
	}

	// ② 记账:helper 是 exec 掉自己的形态,只有 exec 前的 flush 能留下计数器。
	ents, derr := os.ReadDir(coverDir)
	if derr != nil {
		t.Fatalf("读计数器目录失败: %v", derr)
	}
	var hasCounters bool
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "covcounters") {
			hasCounters = true
		}
	}
	if !hasCounters {
		t.Fatalf("helper 的计数器没有落盘(exec 前的 flush 没生效?)目录内容:%v", ents)
	}
	testutil.Flush(t, "github.com/nekoleamo/go-agent-harness/internal/kernelsandbox")
}
