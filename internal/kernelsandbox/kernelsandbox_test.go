// kernelsandbox 单测:判据本身(档位/开关/锚点/标记)与 darwin profile 结构。
// 真正的"内核拦得住"由 tool-shell 的真实拦截用例与 tests/kernel_sandbox_e2e_test.go 覆盖
// (那两条需要本机具备能力,这里跑的是纯函数部分 —— 跨平台都能跑,CI 三个平台都拦得住回归)。
package kernelsandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 注意:Wrap 在"已标记"或"开关关"时返回 nil;所有用例都要显式清掉 MarkerEnv,
// 否则跑在别的沙箱里(CI 容器)会整体退化成 nil 而"测试通过"。
func clearMarker(t *testing.T) {
	t.Helper()
	t.Setenv(MarkerEnv, "")
	t.Setenv("GAH_KERNEL_SANDBOXED", "")
}

func TestWrapDecisionTable(t *testing.T) {
	clearMarker(t)
	jail := t.TempDir()
	root := t.TempDir()
	base := Spec{Jail: jail, Label: "ut"}

	// 档位未知 / 全权档:不施加,不打扰
	if got := Wrap(base); got != nil {
		t.Fatalf("档位为空应不施加, got %v", got)
	}
	if got := Wrap(Spec{Jail: jail, Mode: "full-access", Label: "ut"}); got != nil {
		t.Fatalf("全权档应不施加, got %v", got)
	}
	// 档位非法:不施加(且告警)
	if got := Wrap(Spec{Jail: jail, Mode: "bogus", Label: "ut"}); got != nil {
		t.Fatalf("非法档位应不施加, got %v", got)
	}
	// 显式关闭:不施加
	t.Setenv("UT_SWITCH", "0")
	if got := Wrap(Spec{Jail: jail, Mode: "workspace-write", Switch: "UT_SWITCH", Label: "ut"}); got != nil {
		t.Fatalf("开关=0 应不施加, got %v", got)
	}
	t.Setenv("UT_SWITCH", "1")
	// 缺少 jail 锚点:不施加(告警)—— 没有锚点的 profile 会让构建类命令大面积失败
	if got := Wrap(Spec{Mode: "workspace-write", Label: "ut"}); got != nil {
		t.Fatalf("缺 jail 应不施加, got %v", got)
	}
	// 已在内核沙箱内:不重复施加(不可嵌套)
	t.Setenv(MarkerEnv, "1")
	if got := Wrap(Spec{Jail: jail, Mode: "workspace-write", Label: "ut"}); got != nil {
		t.Fatalf("已标记应不重复施加, got %v", got)
	}
	clearMarker(t)

	// workspace 档缺根 → 降级 read-only(判不定的写更危险):两平台都必须"只放行 jail"
	got := Wrap(Spec{Jail: jail, Mode: "workspace-write", Label: "ut"})
	if len(got) == 0 {
		t.Skipf("本平台无内核级能力(%s):只验到判据层", runtime.GOOS)
	}
	switch runtime.GOOS {
	case "darwin":
		if len(got) != 3 || got[1] != "-p" {
			t.Fatalf("darwin 应为 [sandbox-exec -p profile]:%v", got)
		}
		if strings.Contains(got[2], ResolvePath(root)) {
			t.Fatalf("工作根未知时不得放行任何工作根:%s", got[2])
		}
		if !strings.Contains(got[2], ResolvePath(jail)) {
			t.Fatalf("read-only 降级仍须放行 jail:%s", got[2])
		}
	default:
		// 其余平台(含 linux 的 argv 编码细节,见 linux_test.go):只验到判据层
		t.Skipf("本平台的包装形态由平台专属用例断言(%s)", runtime.GOOS)
	}
}

func TestResolvePathSymlinkedAncestorForMissingPath(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("本平台不支持软链:%v", err)
	}
	// 尚不存在的子路径:必须解析到软链目标之后再拼回剩余部分(否则白名单形同不设)
	want := filepath.Join(ResolvePath(real), "missing", "jail")
	if got := ResolvePath(filepath.Join(link, "missing", "jail")); got != want {
		t.Fatalf("ResolvePath 应解析软链祖先:got %s, want %s", got, want)
	}
	// 未解析的路径按字面比较(不必与软链目标相同)
	if got := ResolvePath(filepath.Join(base, "ghost1", "ghost2")); !strings.HasPrefix(got, ResolvePath(base)) {
		t.Fatalf("整条链都不存在时应保留可用前缀:got %s", got)
	}
}

func TestDefaultRWAndEnvParsing(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp/ut-tmpdir")
	rw := DefaultRWPaths()
	joined := strings.Join(rw, "\n")
	for _, want := range []string{"/tmp/ut-tmpdir", "/tmp"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("默认白名单应含 %s:%v", want, rw)
		}
	}
	// 只收缓存/临时区,不收"应用数据"目录(需要它们的进程由用户显式点名)
	for _, bad := range []string{"Application Support", "/.local/share"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("默认白名单不应含 %s:%v", bad, rw)
		}
	}

	t.Setenv("UT_RW", "/a:: /b ")
	got := RWPathsFromEnv("UT_RW")
	if len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Fatalf("冒号分隔解析(空段/空白忽略)不符:%v", got)
	}
	if RWPathsFromEnv("UT_RW_UNSET") != nil {
		t.Fatal("未设环境变量应返回 nil")
	}
}

func TestPrefixedArgvNoAliasing(t *testing.T) {
	pre := []string{"wrap", "-p", "prof"}
	argv := PrefixedArgv(pre, "sh", "-c", "true")
	if len(pre) != 3 || pre[0] != "wrap" || pre[2] != "prof" {
		t.Fatalf("PrefixedArgv 就地覆写了原切片:%v", pre)
	}
	if len(argv) != 6 || argv[3] != "sh" || argv[5] != "true" {
		t.Fatalf("拼接结果不符:%v", argv)
	}
}

func TestMarkerSemantics(t *testing.T) {
	t.Setenv(MarkerEnv, "1")
	if !Marked() {
		t.Fatal("MarkerEnv=1 应视为已在内核沙箱内")
	}
	t.Setenv(MarkerEnv, "0")
	if Marked() {
		t.Fatal("MarkerEnv=0 不应视为已在内核沙箱内")
	}
}
