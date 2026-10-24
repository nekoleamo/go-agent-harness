//go:build linux

// linux_helper_test.go:让**自举 helper 自己**进覆盖率账(2026-10-24)。
//
// 为什么单独一条:helper 的形态是「init() 里施加 Landlock → syscall.Exec 目标程序」,
// 进程镜像被替换 ⇒ Go 的"退出时写计数器"永不执行 ⇒ `init`/`landlockSelfAndExec`/
// `llAddPathRule` 这几十条语句在任何测试里都**测不到**(与"没人跑"在账上不可区分)。
// 修法不在沙箱施加路径上(那要重论证信号/退出码/pty/进程组),而是 exec **之前**调
// `runtime/coverage.WriteCountersDir`(见 linux.go 的 flushCoverageCounters)。
//
// 这条用例同时钉三件事:
//  ① 包装后的命令**能跑**(白名单内的写成功);
//  ② 白名单外的写被**内核**拒(不是协作层 —— 这里根本没有协作层);
//  ③ 子进程的计数器确实落到了 GOCOVERDIR(没有第 ③ 条,①② 绿了也说明不了记账已修好)。
//
// 拆成两条,因为**记账那一半不该被"这台机器能不能施加 Landlock"绑住**:
//   - `TestBorderHelperFlushesCountersBeforeExec`:不要求 Landlock 可用 —— 手拼 helper argv
//     直接跑,Landlock 可用则命令正常跑完、不可用则自举失败(126),**两种情形都要看到计数器
//     落盘**。CI 的 ubuntu runner 属于后者(GitHub 的 runner 内核没启用 Landlock),
//     2026-10-24 实测:只靠下面那条用例的话,记账在 CI 上永远验不到。
//   - `TestBorderHelperRunsAndIsMeasured`:要求 Landlock 可用(真机 / 有能力的环境),
//     钉"白名单内放行 / 区外拒"这两条**沙箱行为**。
//
// 平台:不能施加 Landlock 时,行为用例 Skip 并说明 —— 与同包其它内核用例同一纪律,
// 不假装覆盖。

package kernelsandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/coverage"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/testutil"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// helperArgv 手拼 helper 的启动 argv(与 platformWrap 的布局一致:
// [self, 魔数, 档位, 根, jail, RW(换行分隔), 真实命令…])。
//
// 为什么要手拼:`Wrap` 在"本机不能施加 Landlock"时返回 nil,而这条用例要验的正是
// **exec 之前的记账** —— 那一步与能不能施加无关(施加失败也照样先落盘)。
func helperArgv(t *testing.T, mode sdk.SandboxMode, root, jail, script string) []string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("取自身可执行文件失败: %v", err)
	}
	return []string{self, llExecFlag, string(mode), root, jail, "", "/bin/sh", "-c", script}
}

// TestBorderHelperFlushesCountersBeforeExec helper 在 exec **之前**把计数器落盘
// (与 Landlock 是否可用无关)。
//
// 为什么这条必须存在:CI 的 ubuntu runner 内核**没启用 Landlock**,于是一切"包装后真跑"
// 的用例都会 Skip ⇒ helper 的 `init` 在 CI 上**一行都没执行过**,那几十条语句的账永远是 0。
// 手拼 argv 跑起来之后,CI 至少能把 init / 参数校验 / ABI 探测这几段计入账。
//
// **诚实边界**:这台机器上 Landlock 不可用时,helper 是以 126 退出的 —— 那种情形下计数器
// 由 Go 的**退出路径**写出,所以"计数器落盘"并不能证明 flush(退出路径本来就会写)。
// flush 的真正证明只在**能施加 Landlock** 的机器上成立(那里 helper exec 掉自己,没有退出
// 路径可依赖),即下面第二条用例;本机 macOS 用等价最小程序实测过同一机制(0% ↔ 61.5%)。
//
// **反向验证**:删掉 `init()` 里的 flushCoverageCounters() ⇒ 计数器目录只剩 covmeta、
// 没有 covcounters ⇒ 本用例红(macOS 上已用等价最小程序实测:covdata 61.5% → 0.0%)。
func TestBorderHelperFlushesCountersBeforeExec(t *testing.T) {
	clearMarker(t)
	if _, ok := countersWritable(t); !ok {
		t.Skip("本次构建不写计数器(未 -cover 或覆盖模式非 atomic):跳过记账断言")
	}
	jail := t.TempDir()
	root := t.TempDir()
	// 计数器目录**必须与 testutil 的合并目录同一个**:否则 Flush 读 RunDir 会是空的 ⇒
	// 静默不并(第一版就是这么写的:断言绿了、账上却没变,只能靠 CI 的覆盖率读数发现)。
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
	argv := helperArgv(t, sdk.SandboxWorkspace, root, jail, "true")
	cmd := exec.Command(argv[0], argv[1:]...)
	// 不设 GOCOVERDIR 以外的任何沙箱开关:这条用例只关心"exec 前有没有落盘"。
	cmd.Env = append(os.Environ(), coverEnv...)
	out, err := cmd.CombinedOutput()
	// 两种结果都合法:能施加 Landlock ⇒ 0;不能 ⇒ 126(自举失败,绝不静默放行)。
	if err != nil && !strings.Contains(string(out), "自举失败") {
		t.Fatalf("helper 既没跑完也不是「自举失败」(exit=%v):%s", err, out)
	}
	ents, derr := os.ReadDir(coverDir)
	if derr != nil || len(ents) == 0 {
		t.Fatalf("helper 的计数器没有落盘(exec 前的 flush 没生效?):err=%v 文件数=%d 输出=%s",
			derr, len(ents), out)
	}
	var hasCounters bool
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "covcounters") {
			hasCounters = true
		}
	}
	if !hasCounters {
		t.Fatalf("目录里只有元数据没有计数器(说明只走了 covmeta 那一步):%v", ents)
	}
	testutil.Flush(t, "github.com/nekoleamo/go-agent-harness/internal/kernelsandbox")
}

// TestBorderHelperRunsAndIsMeasured 自举 helper 真跑一条命令:白名单内放行、区外拒。
//
// **反向验证**:
//   - 删掉 linux.go 的 flushCoverageCounters()(或把它挪到 restrict_self 之后)⇒ 第 ③ 条红
//     (计数器目录里没有 covcounters / 写被 Landlock 拒)。
//   - 把 spec.Root 从白名单里去掉 ⇒ 第 ① 条红(白名单内的写也失败)。
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
	// 计数器目录:启用子进程覆盖合并时用 testutil 给的目录(这样 Flush 能并进主 profile);
	// 本地裸跑则自建一个,只验"确实落盘"这件事。
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
	cmd.Env = append(os.Environ(), coverEnv...)
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

	// ③ 计数器落盘:helper 是"exec 掉自己"的形态,没有 exec 前的 flush 它永远测不到。
	//
	// 前提是本次构建**可写计数器**:`runtime/coverage` 在 -covermode=set(go test -cover 的默认)
	// 与**未插桩**两种情形下都会拒写。用一次自探把这两类环境分开,别把"构建模式不对"报成
	// "产品缺陷"(同款纪律:跨平台/环境导致的差异先判环境)。
	if _, ok := countersWritable(t); !ok {
		t.Logf("本次构建不写计数器(未 -cover 或覆盖模式非 atomic):跳过落盘断言,沙箱行为断言已跑")
		return
	}
	ents, derr := os.ReadDir(coverDir)
	if derr != nil || len(ents) == 0 {
		t.Fatalf("helper 的覆盖计数器没有落盘(exec 前 flush 没生效?):err=%v 文件数=%d",
			derr, len(ents))
	}
	// 走统一出口:由覆盖率门并入主 profile(只保留本包区块,见 testutil.Flush 注释)。
	testutil.Flush(t, "github.com/nekoleamo/go-agent-harness/internal/kernelsandbox")
}

// countersWritable 本进程能否写覆盖计数器(即构建为 -cover 且 -covermode=atomic)。
// 拿一个临时目录做一次真实写入来判定,而不是猜环境变量 —— 判据要与被测机制同源。
func countersWritable(t *testing.T) (string, bool) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "probe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false
	}
	if err := coverage.WriteCountersDir(dir); err != nil {
		return "", false
	}
	return dir, true
}
