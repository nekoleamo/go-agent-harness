// TUI pty 级探针:真实终端行为回归——首次输入、双击 Ctrl+C 退出、非 TTY 启动、
// 历史会话滚动条+PgUp。探针断言须与现行 TUI 语义同步(M6.15 启动首屏干净不重放历史、
// M6.17 ↑↓ 归输入框光标——历史浏览走 PgUp/PgDn/滚轮/滚动条;选择器命令级为前缀过滤,
// 继续输入超命令名即脱选择态,故交互须分步选中)。2026-09-06 对齐修复后全库 -race 全绿。
package tests

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// runTUIViaPty 在 pty 中启动 gah,返回输出流读取函数与进程句柄。
func runTUIViaPty(t *testing.T, bin string, env []string) (*os.File, *exec.Cmd, chan string) {
	return runTUIViaPtyArgs(t, bin, env)
}

// runTUIViaPtyArgs 带启动参数(如 --profile tui)的 pty 启动。
func runTUIViaPtyArgs(t *testing.T, bin string, env []string, args ...string) (*os.File, *exec.Cmd, chan string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
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

// drainUntil 在窗口内高频轮询输出流,任一 cond 出现即提前返回(命中=true)。
// M16 T1:冷启窗口从固定 10s/5s 收紧到上限 4s/3s,响应快则零等待——不再空等满窗口。
func drainUntil(out chan string, d time.Duration, conds ...string) (string, bool) {
	var sb strings.Builder
	deadline := time.After(d)
	for {
		select {
		case s, ok := <-out:
			if !ok {
				return sb.String(), false
			}
			sb.WriteString(s)
			for _, c := range conds {
				if strings.Contains(sb.String(), c) {
					return sb.String(), true
				}
			}
			// 窗口内高频轮询(读不到新数据也须提前退出判断;channel 无数据时经 ticker 回落)
		case <-deadline:
			return sb.String(), false
		}
	}
}

// TestTUIProbe 首次输入 + 退出链 + 输出检查。
func TestTUIProbe(t *testing.T) {
	bin := buildGahCurrent(t) // 当前源码构建(2026-09-08:统一探针二进制防仓库根旧二进制漂移)
	// R8(2026-09-16):数据根唯一 = 二进制同级 gah-data(GAH_HOME env 被忽略;空则首启自动新建)
	env := probeEnv()

	ptmx, cmd, out := runTUIViaPty(t, bin, env)
	defer ptmx.Close()

	// 1) 启动:应渲染出状态栏(M16 T1:窗口 10s→4s,条件命中即提前退出)
	// 负载容错:`go test ./... -race` 并行跑 60+ 包时,4s 窗口偶发不够;判据区分
	// "有输出但慢"(延长一窗)与"零输出"(死机,立即失败)。
	boot, ok := drainUntil(out, 4*time.Second, "工作区: ")
	if !ok && len(boot) > 0 {
		more, ok2 := drainUntil(out, 4*time.Second, "工作区: ")
		boot += more
		ok = ok2
	}
	t.Logf("boot 输出 %d 字节(命中=%v)", len(boot), ok)
	if !ok {
		// 尾部 400 字节(日志被 firstN 截断会误导,看尾段)
		t.Fatalf("4s 未见 TUI 首帧状态栏;尾段: %q", firstN(tailS(boot, 600), 600))
	}

	// 2) 首次输入:发送文本 + 回车 → 应有“思考中”或流式(窗口 5s→3s,命中即提前)
	io.WriteString(ptmx, "你好\r")
	firstInput, ok := drainUntil(out, 3*time.Second, "思考中", "assistant", "执行工具")
	t.Logf("首次输入后输出 %d 字节(命中=%v)", len(firstInput), ok)
	if !ok {
		// 慢机补 2s 观察;再无声则死机
		firstInput += drain(out, 2*time.Second)
		if !strings.Contains(firstInput, "思考中") && !strings.Contains(firstInput, "assistant") && !strings.Contains(firstInput, "执行工具") {
			t.Fatalf("首次输入疑似死机,输出尾段: %q", firstN(tailS(firstInput, 600), 600))
		}
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
	bin := buildGahCurrent(t) // 当前源码构建(2026-09-08:统一探针二进制防漂移)
	cmd := exec.Command(bin)
	cmd.Env = probeEnv() // R8:数据根 = bin 同级 gah-data(不再传 GAH_HOME env)
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

// TestTUIProbeRealHome 用仓库便携根 gah-data 数据(config+sessions,拷贝到临时 home 不污染)
// 复现“首次输入死机/退出只能 kill”。死机时抓 panic 特征(stderr 混入 pty 流)。
// 2026-09-08:数据源 ~/.gah → ../gah-data(便携根;~/.gah 已迁移删除,全部数据只走 gah-data)。
func TestTUIProbeRealHome(t *testing.T) {
	realHome := filepath.Join("..", "gah-data") // 测试 cwd = tests/,相对仓库便携根
	if _, err := os.Stat(realHome); err != nil {
		t.Skip("仓库 gah-data 不可用: " + err.Error())
	}
	bin := buildGahCurrent(t) // 当前源码构建(2026-09-08:统一探针二进制防漂移)
	// R8:数据根 = bin 同级 gah-data(拷贝仓库便携根内容做预置数据;不传 GAH_HOME env)
	dataRoot := probeDataDir(t, bin)
	for _, sub := range []string{"config", "sessions"} {
		if err := exec.Command("cp", "-R", filepath.Join(realHome, sub), filepath.Join(dataRoot, sub)).Run(); err != nil {
			t.Skipf("拷贝 gah-data %s 失败: %v", sub, err)
		}
	}
	// cwd 需要匹配用户原项目(会话按 cwd 派生 key;拷贝含全量 sessions,任意 cwd 可恢复其会话)
	env := probeEnv()

	ptmx, cmd, out := runTUIViaPty(t, bin, env)
	defer ptmx.Close()

	boot, _ := drainUntil(out, 4*time.Second, "工作区: ", "panic:")
	if !strings.Contains(boot, "工作区: ") && len(boot) > 0 && !strings.Contains(boot, "panic:") {
		more, _ := drainUntil(out, 4*time.Second, "工作区: ", "panic:") // 负载容错:同上
		boot += more
	}
	t.Logf("boot %d 字节", len(boot))
	if strings.Contains(boot, "panic:") {
		t.Fatalf("启动即 panic: %q", tailS(boot, 800))
	}

	// 首次输入:"你好\r" → 观察(窗口 10s→4s×2 轮):应有“思考中”或流式;全静止 = 死机
	io.WriteString(ptmx, "你好\r")
	var got string
	for i := 0; i < 2; i++ {
		got += drain(out, 2*time.Second)
		if strings.Contains(got, "panic:") {
			t.Fatalf("首输入后 panic: %q", tailS(got, 800))
		}
		if strings.Contains(got, "思考中") || strings.Contains(got, "执行工具") {
			t.Logf("首输入响应出现(第 %d 轮)", i+1)
			break
		}
	}
	if !strings.Contains(got, "思考中") && !strings.Contains(got, "执行工具") {
		extra := drain(out, 2*time.Second)
		full := boot + got + extra
		if strings.Contains(full, "panic:") {
			t.Fatalf("首输入死机伴随 panic: %q", tailS(full, 1000))
		}
		t.Fatalf("首输入 10s 无响应(死机);尾段: %q", firstN(tailS(got, 600), 600))
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

// TestTUIProbeScrollbarAndArrow 滚动条 + 滚动键真实终端链路(对齐现行语义):
// 启动首屏为新会话空历史、干净(不自动重放,不强制滚动条——M6.15 语义),
// 经 /session switch 进入历史会话(超窗)→ 滚动条轨道 ░(滑块 █ 与输入光标块同字,
// 避开假阳性,仅查轨道);滚动键用 PgUp(\x1b[5~,M6.17 ↑/↓ 已归还输入框光标,
// 历史浏览走 PgUp/PgDn/滚轮/滚动条)。
// 2026-09-08 修复(自包含化):此前依赖真实 ~/.gah 拷贝 + 仓库根 gah 旧二进制 + "列表
// 第一项=超窗会话"脆弱假设(便携迁移后主会话/数据不可控致 ░ 断言不稳定)。现改为
// 测试自构造超窗会话(400 行事件,key 由 cwd 经 sdk.ProjectKey 推导)+ 当前源码构建二进制。
func TestTUIProbeScrollbarAndArrow(t *testing.T) {
	bin := buildGahCurrent(t) // 当前源码构建(消除仓库根旧二进制漂移)
	cwd := repoRoot(t)
	dataRoot := probeDataDir(t, bin) // R8:数据根 = bin 同级 gah-data(GAH_HOME env 被忽略)
	sessionsDir := filepath.Join(dataRoot, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 自构造超窗会话:400 行 user 事件(jsonl;key = cwd 派生,与运行时一致)
	key := sdk.ProjectKey(cwd)
	bigPath := filepath.Join(sessionsDir, key+"-probebig.jsonl")
	writeProbeSession(bigPath, 400)

	env := probeEnv()
	ptmx, cmd, out := runTUIViaPtyWorkingDir(t, bin, env, cwd)
	defer ptmx.Close()

	boot := drain(out, 10*time.Second)
	if strings.Contains(boot, "panic:") {
		t.Fatalf("启动 panic: %q", tailS(boot, 800))
	}
	// 断言 1:启动首屏干净(空历史新会话,滚动条轨道 ░ 属可选——不强制)
	t.Logf("启动首屏滚动条轨道 ░: %v(空历史首屏干净,不强制)", strings.Contains(boot, "░"))

	// 断言 2:直接命令切换进入超窗会话(/session switch <id>;避免选择器项序假设)
	io.WriteString(ptmx, "/session switch probebig\r")
	replay, ok := drainUntil(out, 6*time.Second, "░", "panic:")
	hasBar := strings.Contains(replay, "░")
	t.Logf("进入历史会话后滚动条轨道 ░: %v(命中 %v;replay %d 字节)", hasBar, ok, len(replay))
	if strings.Contains(replay, "panic:") {
		t.Fatalf("切换会话 panic: %q", tailS(boot+replay, 800))
	}
	if !hasBar {
		t.Fatalf("超窗会话首屏无滚动条轨道 ░;输入段: %q", firstN(tailS(boot+replay, 800), 800))
	}

	// 断言 3:PgUp(\x1b[5~)滚动历史 → 每次应产生新渲染 diff(输出增加)
	io.WriteString(ptmx, "\x1b[5~")
	diff := drain(out, 3*time.Second)
	t.Logf("PgUp 后 diff %d 字节", len(diff))
	if len(diff) == 0 {
		t.Fatalf("PgUp 无渲染响应(死键)")
	}
	io.WriteString(ptmx, "\x1b[5~")
	diff2 := drain(out, 3*time.Second)
	if len(diff2) == 0 {
		t.Fatalf("第二次 PgUp 无渲染响应")
	}

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

// probeEnv R8(2026-09-16)收紧后:gah 数据根唯一 = 二进制同级 gah-data(GAH_HOME env
// 被忽略并告警),探针启动不再传 GAH_HOME;数据隔离由 buildGahCurrent 每测试独立
// TempDir 保证(bin 同级 gah-data 首启自动新建)。
func probeEnv() []string {
	return append(os.Environ(), "TERM=xterm-256color", "LANG=zh_CN.UTF-8")
}

// probeDataDir 返回 bin 同级 gah-data 数据根并确保存在(探针预置数据用)。
func probeDataDir(t *testing.T, bin string) string {
	t.Helper()
	d := filepath.Join(filepath.Dir(bin), "gah-data")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

// buildGahCurrent 用当前源码构建 gah 到临时目录(探针与源码一致,防仓库根旧二进制漂移)。
// 构建缓存命中后重复调用近乎零成本。
func buildGahCurrent(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "gah")
	cmd := exec.Command("go", "build", "-o", out, "../cmd/gah")
	cmd.Dir = "."
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("构建当前 gah 失败: %v\n%s", err, tailS(string(b), 800))
	}
	return out
}

// repoRoot 仓库根绝对路径(探针进程以它为 cwd,用于验证「会话按项目 cwd 隔离」)。
// 不硬编码开发者机器路径:换机/容器里 cmd.Dir 指向不存在目录会得到
// 「fork/exec …/gah: no such file or directory」(实测踩到,真因是 cwd 不存在而非二进制缺失)。
func repoRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("解析仓库根失败: %v", err)
	}
	return abs
}

// writeProbeSession 写一个超窗测试会话:path 下追加 n 条 user/message 事件(jsonl)。
func writeProbeSession(path string, n int) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	for i := 1; i <= n; i++ {
		b, _ := json.Marshal(sdk.SessionEvent{
			Seq:     uint64(i),
			Kind:    sdk.EventUserMessage,
			Payload: sdk.UserMessage{Content: fmt.Sprintf("探针消息第 %d 条 用于超窗滚动验证", i)},
		})
		f.Write(append(b, '\n'))
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
	// 数据源 = 仓库便携根 gah-data(2026-09-08:~/.gah 已迁移删除,全部数据只走 gah-data)
	realHome := filepath.Join("..", "gah-data") // 测试 cwd = tests/,相对仓库便携根
	if _, err := os.Stat(realHome); err != nil {
		t.Skip("仓库 gah-data 不可用: " + err.Error())
	}
	bin := buildGahCurrent(t)        // 当前源码构建(2026-09-08:统一探针二进制防漂移)
	dataRoot := probeDataDir(t, bin) // R8:数据根 = bin 同级 gah-data,预置仓库便携根内容
	for _, sub := range []string{"config", "sessions"} {
		if err := exec.Command("cp", "-R", filepath.Join(realHome, sub), filepath.Join(dataRoot, sub)).Run(); err != nil {
			t.Skipf("拷贝 gah-data %s 失败: %v", sub, err)
		}
	}
	env := probeEnv()
	ptmx, cmd, out := runTUIViaPtyWorkingDir(t, bin, env, repoRoot(t))
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
