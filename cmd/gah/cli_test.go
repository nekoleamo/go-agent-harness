// main() 的进程级用例:这些分支只在真正的二进制里走得到(main 直接 os.Exit / 读 os.Args),
// 进程内单测碰不到 —— 而它们恰恰是用户最先撞上的那一层:版本号、非交互护栏、`doc` 子命令路由、
// 插件装卸/列举、配置导出。
//
// 为什么不测 -install/-install-ui:它们真的会去 `git clone`(外部网络),不许进单测。
package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var (
	cliBin string // 构建一次,整包共享(TestMain 清理)
	cliDir string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if cliDir != "" {
		_ = os.RemoveAll(cliDir)
	}
	os.Exit(code)
}

// cliBinary 构建被测 CLI(整包只建一次)。
func cliBinary(t *testing.T) string {
	t.Helper()
	if cliBin != "" {
		return cliBin
	}
	dir, err := os.MkdirTemp("", "gah-cli-")
	if err != nil {
		t.Fatal(err)
	}
	cliDir = dir
	name := "gah"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	cliBin = filepath.Join(dir, name)
	out, err := exec.Command("go", "build", "-o", cliBin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build gah: %v\n%s", err, out)
	}
	return cliBin
}

type cliResult struct {
	code           int
	stdout, stderr string
}

// runCLI 跑一次 CLI。stdinMode:"pipe" = 管道(非字符设备,模拟 CI/后台)、
// "null" = 空设备(字符设备,模拟交互终端)。
func runCLI(t *testing.T, stdinMode string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(cliBinary(t), args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	switch stdinMode {
	case "pipe":
		cmd.Stdin = bytes.NewReader(nil)
	case "null":
		f, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		cmd.Stdin = f
	default:
		t.Fatalf("未知 stdinMode %q", stdinMode)
	}
	err := cmd.Run()
	res := cliResult{stdout: out.String(), stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.code = ee.ExitCode()
	default:
		t.Fatalf("运行 %v 失败: %v", args, err)
	}
	return res
}

// TestCLIVersionAndTTYGuard 版本号与"非交互终端"护栏:
// 管道/后台启动 TUI 必须**明确拒绝**并指出替代用法(而不是挂死或输出乱码)。
func TestCLIVersionAndTTYGuard(t *testing.T) {
	// 管道(非 TTY)里也要能打版本:CI/脚本常用,不许被 TUI 护栏拦下
	res := runCLI(t, "pipe", "-version")
	if res.code != 0 || !strings.Contains(res.stdout, "github.com/nekoleamo/go-agent-harness") {
		t.Fatalf("-version(管道) = %d %q(stderr %q)", res.code, res.stdout, res.stderr)
	}

	res = runCLI(t, "pipe")
	if res.code != 1 {
		t.Fatalf("非 TTY 启动应退出码 1,得 %d", res.code)
	}
	if !strings.Contains(res.stderr, "TUI 需要交互式终端") || !strings.Contains(res.stderr, "-input") {
		t.Fatalf("护栏提示应指出替代用法:%q", res.stderr)
	}
	// 护栏在**装配之前**:不该顺带创建数据根(否则后台误启动会污染二进制目录)
	home := filepath.Join(filepath.Dir(cliBinary(t)), "gah-data")
	if _, err := os.Stat(home); err == nil {
		t.Fatalf("护栏生效时不该创建 %s", home)
	}
}

// TestCLIDocSubcommandRouting `gah doc …` 的路由发生在标志解析与 TTY 护栏之前:
// 管道里也能用(这是脚本场景的主力用法)。
func TestCLIDocSubcommandRouting(t *testing.T) {
	res := runCLI(t, "pipe", "doc")
	if res.code != 2 || !strings.Contains(res.stderr, "用法: gah doc") {
		t.Fatalf("gah doc 无参应退出码 2 并给用法:%d %q", res.code, res.stderr)
	}
	md := filepath.Join(t.TempDir(), "a.md")
	if err := os.WriteFile(md, []byte("第一行\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = runCLI(t, "pipe", "doc", md)
	if res.code != 0 || !strings.Contains(res.stdout, "第一行") {
		t.Fatalf("gah doc <文件> = %d %q", res.code, res.stdout)
	}
}

// TestCLIPluginAndConfigFlags 本地可完成的标志动作:列举/导出配置/卸载不存在的插件。
// 全部走 -ephemeral(临时数据根),不碰二进制旁的 gah-data。
func TestCLIPluginAndConfigFlags(t *testing.T) {
	res := runCLI(t, "null", "-ephemeral", "-dump-config")
	if res.code != 0 || !strings.Contains(res.stdout, "entries:") {
		t.Fatalf("-dump-config = %d %q(stderr %q)", res.code, res.stdout, res.stderr)
	}
	res = runCLI(t, "null", "-ephemeral", "-list-plugins")
	if res.code != 0 {
		t.Fatalf("-list-plugins = %d %q", res.code, res.stderr)
	}
	res = runCLI(t, "null", "-ephemeral", "-list-ui-plugins")
	if res.code != 0 {
		t.Fatalf("-list-ui-plugins = %d %q", res.code, res.stderr)
	}
	// 卸载不存在的插件:必须显式失败(不许假装成功)
	res = runCLI(t, "null", "-ephemeral", "-uninstall", "ghost")
	if res.code != 1 || !strings.Contains(res.stderr, "uninstall: 失败") {
		t.Fatalf("-uninstall ghost = %d %q", res.code, res.stderr)
	}
	res = runCLI(t, "null", "-ephemeral", "-uninstall-ui", "ghost")
	if res.code != 1 || !strings.Contains(res.stderr, "uninstall-ui: 失败") {
		t.Fatalf("-uninstall-ui ghost = %d %q", res.code, res.stderr)
	}
}

// TestExecRealPathMissingFallsBack 路径不存在时 EvalSymlinks 失败:
// 只能原样返回(调用方据此报"不可便携"),不能返回空串让数据根落到别处。
func TestExecRealPathMissingFallsBack(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope", "gah")
	if got := execRealPath(p); got != p {
		t.Fatalf("execRealPath 失败时应原样返回:%q", got)
	}
}
