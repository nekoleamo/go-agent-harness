// kernel_test.go:内核级沙箱的档位判定表 + 真实拦截验证(darwin 真跑;其余平台跳过)。
//
// 真实拦截是本任务的核心价值所在:协作层(写目标裁决)看不见解释器/构建系统内部的写,
// 只有内核层拦得住 —— 所以这里用真子进程断言「文件确实没被创建」,而不只断言 argv 形态。
package toolshell

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// kernelTestEnv 固定测试环境:jail 开、内核沙箱开(默认值),避开外部环境干扰。
// GAH_HOME 指向临时目录,jail 随之落在临时目录内(便携纪律:一切经 sdk.Home 派生)。
func kernelTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	t.Setenv("GAH_SHELL_JAIL", "1")
	t.Setenv(kernelSandboxEnv, "1")
}

// kernelSupportedHere 本机是否具备内核级能力:直接问 kernelWrap 是否给出包装 ——
// 平台无关(不引用 darwin/linux 专属符号),因此 Linux 上同样能跑到真实拦截验证(Landlock 分支)。
// 调用前必须先 kernelTestEnv(它固定 GAH_SHELL_JAIL/内核开关,否则环境噪声会让探测失真)。
func kernelSupportedHere() bool {
	return len(kernelWrap(sdk.SandboxHint{Mode: sdk.SandboxWorkspace, Root: os.TempDir()}, true)) > 0
}

// runWrapped 以指定档位执行脚本,返回输出与错误(错误非空 = 命令失败)。
func runWrapped(t *testing.T, mode sdk.SandboxMode, root, script string) (string, error) {
	t.Helper()
	env, err := jailEnv(sdk.SanitizedEnv(os.Environ()))
	if err != nil {
		t.Fatalf("jailEnv: %v", err)
	}
	pre := kernelWrap(sdk.SandboxHint{Mode: mode, Root: root}, true)
	argv := prefixedArgv(pre, "sh", "-c", script)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.WaitDelay = 3 * time.Second
	setProcessGroup(cmd)
	defer killProcessGroup(cmd)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ---------- 档位判定表(不依赖平台能力) ----------

func TestKernelWrapDecisionTable(t *testing.T) {
	kernelTestEnv(t)
	ws := sdk.SandboxHint{Mode: sdk.SandboxWorkspace, Root: t.TempDir()}

	if got := kernelWrap(ws, true); runtime.GOOS == "darwin" && len(got) == 0 {
		t.Fatal("workspace 档位在 darwin 应施加包装")
	}

	t.Setenv(kernelSandboxEnv, "0")
	if got := kernelWrap(ws, true); got != nil {
		t.Fatalf("GAH_SHELL_KERNEL_SANDBOX=0 应不施加, got %v", got)
	}
	t.Setenv(kernelSandboxEnv, "1")

	if got := kernelWrap(ws, false); got != nil {
		t.Fatalf("宿主未注入档位应不施加, got %v", got)
	}
	if got := kernelWrap(sdk.SandboxHint{Mode: sdk.SandboxFullAccess, Root: ws.Root}, true); got != nil {
		t.Fatalf("full-access 应不施加, got %v", got)
	}
	if got := kernelWrap(sdk.SandboxHint{Mode: "bogus"}, true); got != nil {
		t.Fatalf("未知档位应不施加(不猜), got %v", got)
	}

	// workspace-write 但根未知 → 按 read-only 处理(判不定的写更危险):包装应与 read-only 一致。
	ro := kernelWrap(sdk.SandboxHint{Mode: sdk.SandboxReadOnly}, true)
	wsNoRoot := kernelWrap(sdk.SandboxHint{Mode: sdk.SandboxWorkspace}, true)
	if fmt.Sprint(ro) != fmt.Sprint(wsNoRoot) {
		t.Fatalf("根未知的 workspace 档应退化为 read-only:\nro=%v\nws=%v", ro, wsNoRoot)
	}

	// 环境 jail 关闭 → 白名单失去锚点,显式跳过(而非静默降级成半边可用)。
	t.Setenv("GAH_SHELL_JAIL", "0")
	if got := kernelWrap(ws, true); got != nil {
		t.Fatalf("jail 关闭时应跳过, got %v", got)
	}
}

// prefixedArgv 必须用新底层数组:否则 append 会就地覆写包装切片(调用方复用时会串味)。
func TestPrefixedArgvNoAliasing(t *testing.T) {
	pre := make([]string, 2, 8) // 预留容量,正是会触发就地 append 的形态
	pre[0], pre[1] = "sandbox-exec", "-p"
	argv := prefixedArgv(pre, "sh", "-c", "true")
	if &argv[0] == &pre[0] {
		t.Fatal("argv 复用了 pre 的底层数组(会覆写包装)")
	}
	if pre[0] != "sandbox-exec" || pre[1] != "-p" {
		t.Fatalf("pre 被改写: %v", pre)
	}
	if got := strings.Join(argv, " "); got != "sandbox-exec -p sh -c true" {
		t.Fatalf("argv 拼接错误: %s", got)
	}
}

// ---------- darwin profile 形态(EvalSymlinks 是实测踩到的坑) ----------

func TestDarwinProfileResolvesSymlinksAndReadOnlyScope(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("仅 darwin 有 seatbelt profile")
	}
	kernelTestEnv(t)
	ws := t.TempDir()
	resolved := resolvePath(ws)

	pre := kernelWrap(sdk.SandboxHint{Mode: sdk.SandboxWorkspace, Root: ws}, true)
	if len(pre) < 3 {
		t.Fatalf("darwin 上应给出 seatbelt 包装, got %v", pre)
	}
	prof := pre[2] // [sandbox-exec, -p, profile]
	if !strings.Contains(prof, `(subpath "`+resolved+`")`) {
		t.Fatalf("workspace 白名单未按解析后路径给出:%s", prof)
	}
	if resolved != ws && strings.Contains(prof, `(subpath "`+ws+`")`) {
		t.Fatalf("profile 含未解析路径(seatbelt 按真实路径匹配,该条形同不设):%s", prof)
	}
	if !strings.Contains(prof, `(subpath "`+resolvePath(jailRoot())+`")`) {
		t.Fatalf("profile 未放行 jail(TMPDIR/GOCACHE 会写不通):%s", prof)
	}
	// deny 写 + 只放行写权利:读与网络不受限(与协作层范围一致)。
	if !strings.Contains(prof, "(deny file-write*)") || !strings.Contains(prof, "(allow file-write*") {
		t.Fatalf("profile 缺少 deny/allow file-write* 结构:%s", prof)
	}
	for _, want := range []string{`(literal "/dev/null")`, `(subpath "/dev/fd")`, `(regex #"^/dev/ttys[0-9]+")`} {
		if !strings.Contains(prof, want) {
			t.Fatalf("profile 缺少 %s(pty/stdio 会失败):%s", want, prof)
		}
	}

	// read-only:不放行 workspace,只放行 jail。
	roPre := kernelWrap(sdk.SandboxHint{Mode: sdk.SandboxReadOnly}, true)
	if len(roPre) < 3 {
		t.Fatalf("darwin 上应给出 seatbelt 包装, got %v", roPre)
	}
	ro := roPre[2]
	if strings.Contains(ro, resolved) {
		t.Fatalf("read-only profile 不应放行 workspace:%s", ro)
	}
	if !strings.Contains(ro, `(subpath "`+resolvePath(jailRoot())+`")`) {
		t.Fatalf("read-only profile 应保留 jail:%s", ro)
	}
}

// sandbox-exec 缺失时的降级:不施加、不崩溃,并说明原因(不静默降级)。
func TestDarwinMissingSandboxExecFallsBack(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("仅 darwin 有 seatbelt 前端")
	}
	kernelTestEnv(t)
	orig := sandboxExec
	sandboxExec = filepath.Join(t.TempDir(), "no-such-sandbox-exec")
	t.Cleanup(func() { sandboxExec = orig })
	if got := platformWrap(sdk.SandboxWorkspace, t.TempDir()); got != nil {
		t.Fatalf("sandbox-exec 缺失时应不施加, got %v", got)
	}
}

// ---------- 真实拦截(核心价值) ----------

func TestKernelSandboxBlocksIndirectWrites(t *testing.T) {
	kernelTestEnv(t)
	if !kernelSupportedHere() {
		t.Skip("本机无内核级沙箱能力,跳过真实拦截验证")
	}
	ws := t.TempDir()
	outside := t.TempDir()

	// ① 工作区内写:放行(内核沙箱不能把正常开发流也拦掉)
	if out, err := runWrapped(t, sdk.SandboxWorkspace, ws, "echo x > "+ws+"/in.txt"); err != nil {
		t.Fatalf("工作区内写应放行,却失败: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(ws, "in.txt")); err != nil {
		t.Fatalf("工作区内文件应已创建: %v", err)
	}

	// ② 工作区外写:拒绝且文件不存在
	target := filepath.Join(outside, "out.txt")
	if out, err := runWrapped(t, sdk.SandboxWorkspace, ws, "echo x > "+target); err == nil {
		t.Fatalf("工作区外写应被拒,却成功:%s", out)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("工作区外文件竟被创建")
	}

	// ③ 解释器内部写(协作层完全看不见 —— 本任务的核心价值)
	if _, err := exec.LookPath("python3"); err == nil {
		py := filepath.Join(outside, "py.txt")
		script := `python3 -c "open('` + py + `','w').write('1')"`
		if out, err := runWrapped(t, sdk.SandboxWorkspace, ws, script); err == nil {
			t.Fatalf("解释器内部写应被拒,却成功:%s", out)
		}
		if _, err := os.Stat(py); err == nil {
			t.Fatal("解释器内部写的文件竟被创建")
		}
	}

	// ④ 家目录写:拒绝且不留痕($HOME 由凭据/配置读需要,刻意不放行写)
	home, herr := os.UserHomeDir()
	if herr == nil && home != "" {
		probe := filepath.Join(home, ".gah_kernel_probe_"+strconv.Itoa(os.Getpid()))
		t.Cleanup(func() { _ = os.Remove(probe) })
		if out, err := runWrapped(t, sdk.SandboxWorkspace, ws, "echo x > "+probe); err == nil {
			t.Fatalf("家目录写应被拒,却成功:%s", out)
		}
		if _, err := os.Stat(probe); err == nil {
			t.Fatal("家目录探针文件竟被创建")
		}
	}

	// ⑤ 普通命令:正常执行
	if out, err := runWrapped(t, sdk.SandboxWorkspace, ws, "echo hi"); err != nil || !strings.Contains(out, "hi") {
		t.Fatalf("普通命令应正常执行:err=%v out=%s", err, out)
	}

	// ⑥ jail(TMPDIR)可写:否则 go build / npm 之类会大面积失败(jail 白名单存在的意义)
	if out, err := runWrapped(t, sdk.SandboxWorkspace, ws, `echo t > "$TMPDIR/probe.txt" && cat "$TMPDIR/probe.txt"`); err != nil {
		t.Fatalf("jail(TMPDIR)写应放行,却失败: %v\n%s", err, out)
	}
}

func TestKernelSandboxReadOnlyMode(t *testing.T) {
	kernelTestEnv(t)
	if !kernelSupportedHere() {
		t.Skip("本机无内核级沙箱能力,跳过真实拦截验证")
	}
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "read.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatalf("准备读文件失败: %v", err)
	}

	// ① 工作区内写:read-only 档一律拒
	if out, err := runWrapped(t, sdk.SandboxReadOnly, ws, "echo x > "+ws+"/ro.txt"); err == nil {
		t.Fatalf("read-only 档工作区内写应被拒,却成功:%s", out)
	}
	if _, err := os.Stat(filepath.Join(ws, "ro.txt")); err == nil {
		t.Fatal("read-only 档竟写成功")
	}

	// ② jail 内写:放行(gah 自己的临时/缓存区)
	if out, err := runWrapped(t, sdk.SandboxReadOnly, ws, `echo j > "$TMPDIR/ro.txt"`); err != nil {
		t.Fatalf("read-only 档 jail 内写应放行,却失败: %v\n%s", err, out)
	}

	// ③ 读:放行
	if out, err := runWrapped(t, sdk.SandboxReadOnly, ws, "cat "+ws+"/read.txt"); err != nil || !strings.Contains(out, "ok") {
		t.Fatalf("read-only 档读应放行:err=%v out=%s", err, out)
	}
}

// 全权档:不加包装,命令按原样执行(不引入额外进程层)。
func TestKernelSandboxFullAccessUnwrapped(t *testing.T) {
	kernelTestEnv(t)
	outside := t.TempDir()
	target := filepath.Join(outside, "full.txt")
	if out, err := runWrapped(t, sdk.SandboxFullAccess, t.TempDir(), "echo x > "+target); err != nil {
		t.Fatalf("full-access 档应放行任意写: %v\n%s", err, out)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("full-access 档文件应已创建: %v", err)
	}
}

// TestKernelSandboxWithoutGahHome(Linux 回归,CI 实证):GAH_HOME 为空(直连/嵌入形态)时,
// jailEnv 会把子进程 TMPDIR 改到 <jail>/tmp,而 helper 是**重新 exec 的同一二进制** ——
// 若它用 sdk.Home() 反推 jail,GAH_HOME 空时 Home() 回落到 TMPDIR,得到 <jail>/tmp/jail(自指、不存在)
// → landlock_add_rule ENOENT → 自举失败 → **所有命令 exit 126**(CI 上 tests 包 e2e 就这样红的)。
// 修法:jail 根由父进程经 argv 传给 helper。本用例按该形态真跑一条区内写,必须成功且内核层仍生效。
func TestKernelSandboxWithoutGahHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("仅 Linux 有 Landlock 自举 helper(其它平台不重新 exec)")
	}
	base := t.TempDir()
	t.Setenv("GAH_HOME", "") // 空 = sdk.Home() 回落 os.TempDir()(模拟嵌入/直连)
	t.Setenv("TMPDIR", base) // 固定 TMPDIR,避免污染真实 /tmp/jail
	t.Setenv("GAH_SHELL_JAIL", "1")
	t.Setenv(kernelSandboxEnv, "1")
	if !kernelSupportedHere() {
		t.Skip("本机无内核级沙箱能力,跳过自举回归")
	}
	ws := t.TempDir()
	in := filepath.Join(ws, "inside.txt")
	if out, err := runWrapped(t, sdk.SandboxWorkspace, ws, "echo ok > "+in); err != nil {
		t.Fatalf("GAH_HOME 为空时区内写应成功(自举 jail 根是否自指?): %v\n%s", err, out)
	}
	if _, err := os.Stat(in); err != nil {
		t.Fatalf("区内文件应已创建: %v", err)
	}
	// 内核层仍必须生效(不能为了修自举而放行越界写)
	outside := filepath.Join(t.TempDir(), "escaped.txt")
	if out, err := runWrapped(t, sdk.SandboxWorkspace, ws, "echo x > "+outside); err == nil {
		t.Fatalf("越界写竟成功(内核层未生效): %s", out)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("越界文件竟被创建")
	}
}

// ---------- 接线验证:包装真的接在 shell.go / pty.go 上(而不只是 kernelWrap 正确) ----------

func TestShellToolAppliesKernelSandbox(t *testing.T) {
	kernelTestEnv(t)
	if !kernelSupportedHere() {
		t.Skip("本机无内核级沙箱能力,跳过接线验证")
	}
	ws := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "wired.txt")

	tool := &ShellTool{timeout: 10 * time.Second}
	ctx := sdk.WithSandboxHint(context.Background(), sdk.SandboxHint{Mode: sdk.SandboxWorkspace, Root: ws})
	res, err := tool.Execute(ctx, `{"command":"echo x > `+target+`"}`)
	if err != nil {
		t.Fatalf("Execute 不应返回 go error(结构化回传): %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("结果形态异常: %#v", res)
	}
	if _, has := m["exit_error"]; !has {
		t.Fatalf("工作区外写应失败(exit_error),却成功: %#v", m)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("shell 工具接线后工作区外文件竟被创建")
	}

	// 工作区内写仍可用(接线没有把正常用法拦掉)
	if _, err := tool.Execute(ctx, `{"command":"echo x > `+ws+`/ok.txt"}`); err != nil {
		t.Fatalf("工作区内写失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "ok.txt")); err != nil {
		t.Fatalf("工作区内文件应已创建: %v", err)
	}
}

func TestExecPtyUnderKernelSandbox(t *testing.T) {
	kernelTestEnv(t)
	if !kernelSupportedHere() {
		t.Skip("本机无内核级沙箱能力,跳过 pty 验证")
	}
	ws := t.TempDir()
	ctx := sdk.WithSandboxHint(context.Background(), sdk.SandboxHint{Mode: sdk.SandboxWorkspace, Root: ws})

	// pty 下输出可采集:验证 /dev/ttys[N] 白名单正确(缺了它 pty 会静默无输出)
	out, timedOut, err := execPty(ctx, "echo pty-kernel-ok", "")
	if err != nil {
		t.Fatalf("pty 启动失败: %v", err)
	}
	if timedOut {
		t.Fatal("pty 命令不应超时")
	}
	if !strings.Contains(out, "pty-kernel-ok") {
		t.Fatalf("pty 输出缺失(dev/ttys 白名单可能没生效): %q", out)
	}

	// pty 下工作区外写同样被拒
	outside := t.TempDir()
	target := filepath.Join(outside, "pty-wired.txt")
	_, _, perr := execPty(ctx, "echo x > "+target, "")
	if perr != nil {
		t.Fatalf("pty 执行返回错误(应为命令自身失败): %v", perr)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("pty 下工作区外文件竟被创建")
	}
}
