// TUI pty 级复现探针(临时):真实终端行为验证——首次输入、双击 Ctrl+C 退出、非 TTY 启动。
// 目标:对“首次输入死机 / 退出卡死 / 非 TTY 死机”取证,输出每步事件序列。
package tests

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// runTUIViaPty 在 pty 中启动 gah,返回输出流读取函数与进程句柄。
func runTUIViaPty(t *testing.T, bin string, env []string) (*os.File, *exec.Cmd, chan string) {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = env
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan string, 512)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				out <- string(buf[:n])
			}
			if err != nil {
				close(out)
				return
			}
		}
	}()
	return ptmx, cmd, out
}

// drain 在 timeout 内累计读取输出(channel 关闭停止)。
func drain(out chan string, d time.Duration) string {
	var sb strings.Builder
	deadline := time.After(d)
	for {
		select {
		case s, ok := <-out:
			if !ok {
				return sb.String()
			}
			sb.WriteString(s)
		case <-deadline:
			return sb.String()
		}
	}
}

// TestTUIProbe 首次输入 + 退出链 + 输出检查。
func TestTUIProbe(t *testing.T) {
	bin := filepath.Join("..", "gah")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("仓库根无 gah 二进制")
	}
	home := t.TempDir()
	env := append(os.Environ(),
		"GAH_HOME="+home,
		"TERM=xterm-256color",
		"LANG=zh_CN.UTF-8",
	)

	ptmx, cmd, out := runTUIViaPty(t, bin, env)
	defer ptmx.Close()

	// 1) 启动:应渲染出状态栏(冷启释放外部插件较慢,窗口 10s;日志走 stderr 混流)
	boot := drain(out, 10*time.Second)
	t.Logf("boot 输出 %d 字节", len(boot))
	if !strings.Contains(boot, "gah |") {
		// 尾部 400 字节(日志被 firstN 截断会误导,看尾段)
		t.Fatalf("10s 未见 TUI 首帧状态栏;尾段: %q", firstN(tailS(boot, 600), 600))
	}

	// 2) 首次输入:发送文本 + 回车 → 应有“思考中”或流式(5s 窗口)
	io.WriteString(ptmx, "你好\r")
	firstInput := drain(out, 5*time.Second)
	t.Logf("首次输入后输出 %d 字节", len(firstInput))
	if !strings.Contains(firstInput, "思考中") && !strings.Contains(firstInput, "assistant") {
		t.Fatalf("首次输入疑似死机,输出尾段: %q", firstN(firstInput+drain(out, 5*time.Second), 600))
	}

	// 3) 双击 Ctrl+C:两次间隔 300ms → 应触发 quit(进程退出 ≤3s)
	ptmx.Write([]byte{0x03})
	drain(out, 300*time.Millisecond)
	ptmx.Write([]byte{0x03})
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		t.Log("双击 Ctrl+C 正常退出 ✓")
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("双击 Ctrl+C 3s 未退出:退出卡死复现")
	}
}

// TestTUIProbeNonTTY 非 TTY 启动:stdin/stdout 均为 pipe → 期望 gah 立即降级退出,
// 而非死机;3s 内进程应退出或给出错误。
func TestTUIProbeNonTTY(t *testing.T) {
	bin := filepath.Join("..", "gah")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("仓库根无 gah 二进制")
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "GAH_HOME="+t.TempDir())
	stdin, _ := cmd.StdinPipe()
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { stdin.Close() }()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		t.Log("非 TTY 启动:直接退出(降级/报错)✓")
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("非 TTY 启动 3s 未退出:死机复现")
	}
}

func firstN(s string, n int) string {
	s = strings.ReplaceAll(s, "\x1b", "<ESC>")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// TestTUIProbeRealHome 用用户真实 ~/.gah 数据(config+sessions,拷贝到临时 home 不污染)
// 复现“首次输入死机/退出只能 kill”。死机时抓 panic 特征(stderr 混入 pty 流)。
func TestTUIProbeRealHome(t *testing.T) {
	realHome := os.Getenv("HOME") + "/.gah"
	if _, err := os.Stat(realHome + "/sessions"); err != nil {
		t.Skip("无真实 ~/.gah 数据")
	}
	tmp := t.TempDir()
	for _, sub := range []string{"config", "sessions"} {
		if err := exec.Command("cp", "-R", realHome+"/"+sub, tmp+"/"+sub).Run(); err != nil {
			t.Fatalf("拷贝 %s 失败: %v", sub, err)
		}
	}
	bin := filepath.Join("..", "gah")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("仓库根无 gah 二进制")
	}
	// cwd 需要匹配用户原项目(会话按 cwd 派生 key;拷贝含全量 sessions,任意 cwd 可恢复其会话)
	env := append(os.Environ(), "GAH_HOME="+tmp, "TERM=xterm-256color", "LANG=zh_CN.UTF-8")

	ptmx, cmd, out := runTUIViaPty(t, bin, env)
	defer ptmx.Close()

	boot := drain(out, 10*time.Second)
	t.Logf("boot %d 字节", len(boot))
	if strings.Contains(boot, "panic:") {
		t.Fatalf("启动即 panic: %q", tailS(boot, 800))
	}

	// 首次输入:"你好\r" → 观察 10s:应有“思考中”或流式或错误;全静止 = 死机
	io.WriteString(ptmx, "你好\r")
	var got string
	for i := 0; i < 10; i++ {
		got += drain(out, 1*time.Second)
		if strings.Contains(got, "panic:") {
			t.Fatalf("首输入后 panic: %q", tailS(got, 800))
		}
		if strings.Contains(got, "思考中") || strings.Contains(got, "执行工具") {
			t.Logf("首输入响应出现(第 %d 秒)", i+1)
			break
		}
	}
	if !strings.Contains(got, "思考中") && !strings.Contains(got, "执行工具") {
		extra := drain(out, 5*time.Second)
		full := boot + got + extra
		if strings.Contains(full, "panic:") {
			t.Fatalf("首输入死机伴随 panic: %q", tailS(full, 1000))
		}
		t.Fatalf("首输入 15s 无响应(死机);尾段: %q", firstN(tailS(got, 600), 600))
	}

	// 退出链:双击 Ctrl+C
	ptmx.Write([]byte{0x03})
	drain(out, 300*time.Millisecond)
	ptmx.Write([]byte{0x03})
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		t.Log("真实数据下退出正常 ✓")
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("真实数据下退出卡死复现")
	}
}

// tailS 取字符串尾部 n 个 rune。
func tailS(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return "…" + string(r[len(r)-n:])
	}
	return s
}

// TestTUIProbeScrollbarAndArrow 滚动条 + ↑ 键真实终端链路:历史会话超窗口时
// 首屏应渲染滚动条(轨道 ░/滑块 █);发送 ↑(xterm 序列 \x1b[A,即 kitty 默认编码)
// 应产生滚动 diff(diff 无变化 = 未响应)。真实 home 数据拷贝(项目会话 6k+ 行事件)。
func TestTUIProbeScrollbarAndArrow(t *testing.T) {
	realHome := os.Getenv("HOME") + "/.gah"
	if _, err := os.Stat(realHome + "/sessions"); err != nil {
		t.Skip("无真实 ~/.gah 数据")
	}
	tmp := t.TempDir()
	for _, sub := range []string{"config", "sessions"} {
		if err := exec.Command("cp", "-R", realHome+"/"+sub, tmp+"/"+sub).Run(); err != nil {
			t.Fatalf("拷贝 %s 失败: %v", sub, err)
		}
	}
	abs, aerr := filepath.Abs(filepath.Join("..", "gah"))
	if aerr != nil {
		t.Fatal(aerr)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Skip("仓库根无 gah 二进制")
	}
	// cwd 用原项目目录(会话 key 按路径派生,恢复 6k+ 行事件的会话)
	env := append(os.Environ(), "GAH_HOME="+tmp, "TERM=xterm-256color", "LANG=zh_CN.UTF-8")
	ptmx, cmd, out := runTUIViaPtyWorkingDir(t, abs, env, "/Users/nekoleamo/Documents/Working/go-agent-harness")
	defer ptmx.Close()

	boot := drain(out, 10*time.Second)
	if strings.Contains(boot, "panic:") {
		t.Fatalf("启动 panic: %q", tailS(boot, 800))
	}
	// 断言 1:首屏含滚动条——轨道 ░(滑块 █ 与输入光标块同字,避开假阳性,仅查轨道)
	hasBar := strings.Contains(boot, "░")
	t.Logf("首屏滚动条轨道 ░: %v(boot %d 字节)", hasBar, len(boot))
	if !hasBar {
		t.Fatalf("首屏无滚动条轨道 ░;尾段: %q", firstN(tailS(boot, 500), 500))
	}

	// 断言 2:发送 ↑(\x1b[A)→ 期望产生滚动渲染 diff(输出增加且含屏内容)
	before := len(boot)
	io.WriteString(ptmx, "\x1b[A")
	diff := drain(out, 3*time.Second)
	t.Logf("↑ 后 diff %d 字节", len(diff))
	if len(diff) == 0 {
		t.Fatalf("↑ 键无渲染响应(死键)")
	}
	// 断言 3:连续再按 ↑(offset 继续增)应有新 diff;↓ 回到与此前同屏 → diff 空属正常
	io.WriteString(ptmx, "\x1b[A")
	diff2 := drain(out, 3*time.Second)
	if len(diff2) == 0 {
		t.Fatalf("第二次 ↑ 无渲染响应")
	}
	io.WriteString(ptmx, "\x1b[B")
	drain(out, 1*time.Second) // 回底:屏恢复原样,diff 可为空(正常)
	drain(out, 2*time.Second)
	_ = before

	// 退出
	ptmx.Write([]byte{0x03})
	drain(out, 300*time.Millisecond)
	ptmx.Write([]byte{0x03})
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		t.Log("探针退出正常 ✓")
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("退出卡死")
	}
}

// runTUIViaPtyWorkingDir 指定工作目录启动 pty。
func runTUIViaPtyWorkingDir(t *testing.T, bin string, env []string, dir string) (*os.File, *exec.Cmd, chan string) {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = env
	cmd.Dir = dir
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan string, 512)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				out <- string(buf[:n])
			}
			if err != nil {
				close(out)
				return
			}
		}
	}()
	return ptmx, cmd, out
}

// TestTUIProbeScrollSettle 滚动后是否会自滚:启动后静置取基线(无输入时输出应停止),
// 发一次 ↑,观察 4s 输出是否持续增长(真循环=每 500ms 都新增;正常=1-2 段后静止)。
func TestTUIProbeScrollSettle(t *testing.T) {
	realHome := os.Getenv("HOME") + "/.gah"
	if _, err := os.Stat(realHome + "/sessions"); err != nil {
		t.Skip("无真实 ~/.gah 数据")
	}
	tmp := t.TempDir()
	for _, sub := range []string{"config", "sessions"} {
		if err := exec.Command("cp", "-R", realHome+"/"+sub, tmp+"/"+sub).Run(); err != nil {
			t.Fatal(err)
		}
	}
	abs, _ := filepath.Abs(filepath.Join("..", "gah"))
	env := append(os.Environ(), "GAH_HOME="+tmp, "TERM=xterm-256color", "LANG=zh_CN.UTF-8")
	ptmx, cmd, out := runTUIViaPtyWorkingDir(t, abs, env, "/Users/nekoleamo/Documents/Working/go-agent-harness")
	defer ptmx.Close()

	drain(out, 10*time.Second) // boot + 首帧

	// 基线:无输入 2s,累计不应 > 2KB(只有空渲染/光标 blink 等少量刷新)
	base := drain(out, 2*time.Second)
	t.Logf("静置基线 2s: %d 字节", len(base))

	// 一次 ↑
	io.WriteString(ptmx, "\x1b[A")
	samples := []int{}
	for i := 0; i < 8; i++ {
		time.Sleep(500 * time.Millisecond)
		samples = append(samples, len(drain(out, 0))) // 0 超时:立即取当前缓冲
		// drain 返回后缓冲已空;用非阻塞 drain 需改法——直接再 drain 200ms
	}
	// 重新按固定窗采样:每次 drain 200ms
	var grows []int
	for i := 0; i < 8; i++ {
		n := len(drain(out, 200*time.Millisecond))
		grows = append(grows, n)
		t.Logf("滚动后 %d.%ds 增量: %d 字节", i, 2, n)
	}
	_ = samples
	// 若后 4 段(1.6s-3.2s)仍持续有输出 → 疑似自滚
	tail := 0
	for _, g := range grows[4:] {
		tail += g
	}
	if tail > 300 {
		t.Fatalf("滚动后持续有输出(疑似自滚): 后4段合计 %d 字节", tail)
	}
	t.Logf("滚动后稳定: 后4段 %d 字节(无自滚)", tail)

	ptmx.Write([]byte{0x03})
	drain(out, 300*time.Millisecond)
	ptmx.Write([]byte{0x03})
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("退出卡死")
	}
}
