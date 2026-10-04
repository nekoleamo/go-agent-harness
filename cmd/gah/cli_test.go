// main() 的进程级用例:这些分支只在真正的二进制里走得到(main 直接 os.Exit / 读 os.Args),
// 进程内单测碰不到 —— 而它们恰恰是用户最先撞上的那一层:版本号、非交互护栏、`doc` 子命令路由、
// 插件装卸/列举、配置导出。
//
// 为什么不测 -install/-install-ui:它们真的会去 `git clone`(外部网络),不许进单测。
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/testutil"
)

var (
	cliBin string // 构建一次,整包共享(TestMain 清理)
	cliDir string
)

func TestMain(m *testing.M) {
	code := m.Run()
	// 子进程覆盖数据交给覆盖率门(必须在 os.Exit 之前 flush:covdata 目录里的数据
	// 由子进程退出时写入,转换要在用例跑完之后做)。
	if testutil.RunDir(nil) != "" {
		testutil.Flush(nil, "github.com/nekoleamo/go-agent-harness/cmd/gah")
	}
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
	// **带覆盖插桩**构建:不带 -cover 的产物根本不写 GOCOVERDIR(main() 的那些分支
	// 永远进不了覆盖率账 —— 这就是这个包长期停在 56% 的成因,见
	// scripts/coverage-check.sh 头注「子进程盲区」与 internal/testutil/coverchild.go)。
	// 未设合并目录时 BuildCovered 照常工作,只是没人来合并那份数据(门会把子进程那份
	// 算作「无数据」而不是「0 覆盖」)。
	testutil.BuildCovered(t, cliBin, ".")
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
	// 子进程覆盖:设了合并目录才有这一步(没设时 envFromChild 返回 nil)。
	if env := testutil.ChildEnv(t); env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
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

// TestCLISourceLedgerFlags 来源账相关的两个 CLI 出口(批一)。
//
// 两者都**不发网络请求**、不进 git:只读 $GAH_HOME/plugins 下的盘上事实。
// 它们的存在本身值得钉住 —— 「检查更新」是一个很容易被后来人改成「顺便就装了」的开关,
// 而那正是本批定下「不做自动更新」的地方。
func TestCLISourceLedgerFlags(t *testing.T) {
	res := runCLI(t, "null", "-ephemeral", "-list-plugins")
	if res.code != 0 {
		t.Fatalf("-list-plugins = %d %q", res.code, res.stderr)
	}
	// 空数据根时无兼容性问题可报 ⇒ 不打那条提示(否则每次都得先判空串)。
	if strings.Contains(res.stdout, "声明的协议版本") {
		t.Errorf("空数据根不该报兼容性问题: %q", res.stdout)
	}
	res = runCLI(t, "null", "-ephemeral", "-check-plugin-updates")
	if res.code != 0 {
		t.Fatalf("-check-plugin-updates = %d %q", res.code, res.stderr)
	}
	if !strings.Contains(res.stdout, "没有可检查的来源") {
		t.Errorf("无来源时应说清为什么没得查: %q", res.stdout)
	}
}

// TestCLIListPluginsShowsSource 清单里要看得见「这台机器上的这个插件当初是从哪一份代码装的」。
//
// 不用 -ephemeral(它每次开一个全新的临时 home,没法预置来源账),改为在被测二进制旁边摆一个
// gah-data/ —— 数据根就是二进制同级目录,这也是便携形态本身。
func TestCLIListPluginsShowsSource(t *testing.T) {
	bin := cliBinary(t)
	home := filepath.Join(filepath.Dir(bin), "gah-data")
	dir := filepath.Join(home, "plugins", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool-demo"), []byte("#!/bin/sh\nv1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := "# gah 插件来源账(测试)\nsources:\n  - repo: github.com/a/b\n    ref: v1.2.0\n    kind: tag\n" +
		"    commit: 9f2c1ab3e5f7aa11bb22cc33dd44ee55ff6607\n    plugin_id: demo\n" +
		"    api_version: v0.9\n    origin: user\n    installed_at: 2026-10-03T21:14:07Z\n"
	if err := os.WriteFile(filepath.Join(home, "plugins", "sources.yaml"), []byte(ledger), 0o600); err != nil {
		t.Fatal(err)
	}
	res := runCLI(t, "null", "-list-plugins")
	if res.code != 0 {
		t.Fatalf("-list-plugins = %d %q", res.code, res.stderr)
	}
	for _, want := range []string{"github.com/a/b", "@v1.2.0", "9f2c1ab3e5f7"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("清单缺 %q:\n%s", want, res.stdout)
		}
	}
	// 协议版本不在本版 gah 支持范围内 ⇒ 必须有汇总提示(而不是静默)。
	if !strings.Contains(res.stdout, "声明的协议版本") {
		t.Errorf("不兼容插件应被提示:\n%s", res.stdout)
	}
	// 按 commit 固定的来源没有「更新」这回事。这里只断言**行被打印出来**:
	// 状态本身(no_update / unreachable)取决于这台机器能不能出网,断言它就是写一条网络测试。
	res = runCLI(t, "null", "-check-plugin-updates")
	if res.code != 0 || !strings.Contains(res.stdout, "demo\t") {
		t.Errorf("-check-plugin-updates = %d %q", res.code, res.stdout)
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

// TestCLITrustUIPlugin `gah -trust-ui-plugin <id>` / `-untrust-ui-plugin`:UI 侧放行口。
//
// 这条命令存在的原因:批四给 UI 侧建了**默认强制**的完整性闸,于是「手工拷一个 UI 插件
// 目录进 ui-plugins/ 就能用」那条老路被断掉了 —— 必须给一条**明确的出路**,否则用户
// 只看到「我放的插件不见了」,以为产品坏了。
func TestCLITrustUIPlugin(t *testing.T) {
	bin := cliBinary(t)
	home := filepath.Join(filepath.Dir(bin), "gah-data")
	uiRoot := filepath.Join(home, "ui-plugins")
	dir := filepath.Join(uiRoot, "demo")
	if err := os.MkdirAll(filepath.Join(dir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"),
		[]byte(`{"id":"demo","version":"1","slots":[{"name":"v1:extra-panel","priority":1,"module":"./dist/p.js"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dist", "p.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 建闸(等价于 ui-web-app 在 Start 时做的事)
	if err := os.MkdirAll(uiRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uiRoot, "SHA256SUMS"),
		[]byte("# gah 外部插件白名单\n# 空清单即强制\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runCLI(t, "null", "-trust-ui-plugin", "demo")
	if res.code != 0 {
		t.Fatalf("trust-ui-plugin 应成功 = %d %q(stderr %q)", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "demo") {
		t.Errorf("回执应带上 id: %q", res.stdout)
	}
	// 清单里应真有一条(不是假装成功)
	raw, err := os.ReadFile(filepath.Join(uiRoot, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "demo") {
		t.Errorf("登记应落到 SHA256SUMS:\n%s", raw)
	}
	// 撤销
	res = runCLI(t, "null", "-untrust-ui-plugin", "demo")
	if res.code != 0 {
		t.Fatalf("untrust-ui-plugin 应成功 = %d %q(stderr %q)", res.code, res.stdout, res.stderr)
	}
	raw, _ = os.ReadFile(filepath.Join(uiRoot, "SHA256SUMS"))
	if strings.Contains(string(raw), "  demo") {
		t.Errorf("撤销后不应还有该条目:\n%s", raw)
	}
	// 目录不存在 ⇒ 显式报错(不假装成功)
	res = runCLI(t, "null", "-trust-ui-plugin", "ghost")
	if res.code == 0 {
		t.Errorf("不存在的 id 应报错,得到 %q", res.stdout)
	}
}

// fakeArchBin 造一个**只有头部**的产物,魔数指向当前平台(与 internal/install 的 archOf 同源)。
//
// 不造真能跑的二进制:它保证实现哪天不小心去执行它时测试会失败 —— 而这条路径本就不该执行。
func fakeArchBin(goos, goarch string) []byte {
	buf := make([]byte, 0x200)
	switch goos {
	case "linux":
		copy(buf, []byte{0x7f, 'E', 'L', 'F'})
		m := uint16(0x3e)
		if goarch == "arm64" {
			m = 0xb7
		}
		binary.LittleEndian.PutUint16(buf[18:20], m)
	case "darwin":
		buf[0], buf[1], buf[2], buf[3] = 0xcf, 0xfa, 0xed, 0xfe
		c := uint32(0x01000007)
		if goarch == "arm64" {
			c = 0x0100000c
		}
		binary.LittleEndian.PutUint32(buf[4:8], c)
	default: // windows
		buf[0], buf[1] = 'M', 'Z'
		off := uint32(0x40)
		binary.LittleEndian.PutUint32(buf[0x3c:0x40], off)
		buf[off], buf[off+1], buf[off+2], buf[off+3] = 'P', 'E', 0, 0
		m := uint16(0x8664)
		if goarch == "arm64" {
			m = 0xaa64
		}
		binary.LittleEndian.PutUint16(buf[off+4:off+6], m)
	}
	return buf
}

// TestCLIInstallArtifact `gah -install-artifact <url> -id <id> -name <name>`:
// 装一个别人已构建好的产物,**本机不需要 Go**。
func TestCLIInstallArtifact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fakeArchBin(runtime.GOOS, runtime.GOARCH))
	}))
	defer srv.Close()

	bin := cliBinary(t)
	home := filepath.Join(filepath.Dir(bin), "gah-data")
	// ① 路径注入必须被拒(id / name 会直接拼进落位路径)
	for _, c := range []struct{ id, name string }{
		{"../evil", "tool-demo"}, {"demo", "../../evil"}, {"demo", "echo"},
	} {
		res := runCLI(t, "null", "-install-artifact", srv.URL+"/tool-demo", "-id", c.id, "-name", c.name)
		if res.code == 0 {
			t.Errorf("id=%q name=%q 应被拒,得到 %q", c.id, c.name, res.stdout)
		}
	}
	// ② 正常安装
	res := runCLI(t, "null", "-install-artifact", srv.URL+"/tool-demo", "-id", "demo", "-name", "tool-demo")
	if res.code != 0 {
		t.Fatalf("产物安装应成功 = %d %q(stderr %q)", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "未执行任何构建命令") {
		t.Errorf("回执要明说没跑构建: %q", res.stdout)
	}
	p := filepath.Join(home, "plugins", "demo", "tool-demo")
	fi, err := os.Stat(p)
	if err != nil || fi.Size() == 0 {
		t.Fatalf("产物未落位: %v", err)
	}
	// 执行位只在 POSIX 形态上有意义:Windows 的权限位由 ACL 管,Go 的 os.Chmod(0o755)
	// 在那里**不产生** POSIX 执行位(AGENTS.md 跨平台坑 ③)。写死这条断言就是让 test-windows
	// 稳定红 —— v0.5.0 的 CI 红里就有这一条(2026-10-04)。
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("落位产物应可执行: %v", fi.Mode().Perm())
	}
	// ③ 来源账里是 artifact,且不进更新检查
	raw, err := os.ReadFile(filepath.Join(home, "plugins", "sources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "kind: artifact") {
		t.Errorf("来源账应记 kind=artifact:\n%s", raw)
	}
	res = runCLI(t, "null", "-check-plugin-updates")
	if strings.Contains(res.stdout, "demo\t") {
		t.Errorf("产物来源不该进更新检查: %q", res.stdout)
	}
}
