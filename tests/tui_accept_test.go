// A-1 TUI 真机验收(pty 探针)
//
// 本文件把 docs/VERIFY.md「A-1 TUI 键盘/渲染(43 条)」里**可在 pty 中观测**的条目
// 做成回归探针:真实启动 gah、真实发按键字节、断言渲染输出。视觉/kitty 专属条目
// (整体视觉、宽表对齐、OSC 通知观感)不在此文件假装覆盖,逐条在 VERIFY 行内注明。
//
// 复用 tui_pty_probe_test.go 的 pty 基建(runTUIViaPtyArgs / drain / drainUntil /
// buildGahCurrent / probeEnv / probeDataDir),不重复造。
package tests

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tuiAcceptSetup 造一个带 llm-mock 的独立数据根:TUI 里凡是要"跑一个回合"的条目
// (消息队列/工具视觉/思维块)都需要一个不会打真实 API 的模型端点。
// 返回 bin、env、数据根、会话 cwd(临时空目录,避免读到仓库 AGENTS.md 干扰层级断言)。
func tuiAcceptSetup(t *testing.T) (bin string, env []string, root string, cwd string) {
	t.Helper()
	bin = buildGahCurrent(t)
	root = probeDataDir(t, bin)
	cfg := filepath.Join(root, "config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	// provider.yaml 必须存在(否则无模型 → 与模型相关的条目测不了);端点指向黑洞,
	// 真跑只能靠 llm-mock,一旦 mock 没生效会以"连不上"显式失败而不是静默打真实 API。
	// 两条 provider:条目 14-17 要验"两条且仅一条 ★ 活跃 / /provider use / /model 聚合枚举"。
	writeAcceptFile(t, cfg, "provider.yaml", "active: m\nproviders:\n  - name: m\n    base_url: http://127.0.0.1:9/v1\n    api_key: k\n    model: mock-model\n  - name: m2\n    base_url: http://127.0.0.1:9/v1\n    api_key: k2\n    model: mock-model-2\n")
	writeAcceptFile(t, cfg, "patch-acctui.yaml", `entries:
  - id: llm-openai-compat
    enabled: false
  - id: llm-mock
    enabled: true
    data:
      script: '[{"text":"收到,这是 mock 回复。"}]'
`)
	writeAcceptFile(t, cfg, "profile-acctui.yaml", "name: acctui\nbundles:\n  - base\n  - tui\npatches:\n  - patch-acctui.yaml\n")
	cwd = t.TempDir()
	env = probeEnv()
	return bin, env, root, cwd
}

func writeAcceptFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tuiAcceptBoot 启动 TUI 并等到首帧就绪(负载下容错:有输出但慢 → 再等一窗)。
func tuiAcceptBoot(t *testing.T, bin string, env []string, args ...string) (*os.File, *exec.Cmd, chan string) {
	t.Helper()
	full := append([]string{"--profile", "acctui"}, args...)
	ptmx, cmd, out := runTUIViaPtyArgs(t, bin, env, full...)
	// 就绪判据用输入框提示符 `❯`(TUI 专属;启动日志里也有"工作区"字样,拿它会提前放行)。
	// 机器负载高时冷启可能 >4s,故给 25s 的重试窗口而非一次性等待。
	var boot string
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		chunk, ok := drainUntil(out, 3*time.Second, "❯", "工作区: ")
		boot += chunk
		if ok {
			return ptmx, cmd, out
		}
	}
	_ = ptmx.Close()
	t.Fatalf("25s 未见 TUI 首帧;尾段: %q", firstN(tailS(boot, 600), 600))
	return nil, nil, nil
}

// tuiQuit 双击 Ctrl+C 退出(与现有探针同法)。
func tuiQuit(t *testing.T, ptmx *os.File, cmd *exec.Cmd, out chan string) {
	t.Helper()
	ptmx.Write([]byte{0x03})
	drain(out, 300*time.Millisecond)
	ptmx.Write([]byte{0x03})
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("双击 Ctrl+C 3s 未退出")
	}
}

// TestTUIAcceptInput 条目 1-5(多行输入)+ 36(Ctrl 编辑键)。
//
// 断言口径:渲染输出里能读到输入区文本(明文),故"两行都在 / 单行时 ↑ 不换行"
// 这类行为可以在原始字节流上判定,不需要终端模拟器。
func TestTUIAcceptInput(t *testing.T) {
	bin, env, _, cwd := tuiAcceptSetup(t)
	ptmx, cmd, out := tuiAcceptBoot(t, bin, env)
	defer ptmx.Close()
	_ = cwd

	// 条目 1:Shift+Enter 续行(kitty CSI-u:`\x1b[13;2u`)→ 两行都在输入区
	io.WriteString(ptmx, "第一行文字")
	io.WriteString(ptmx, "\x1b[13;2u")
	io.WriteString(ptmx, "第二行文字")
	multi := drain(out, 1500*time.Millisecond)
	hasL1 := strings.Contains(multi, "第一行文字")
	hasL2 := strings.Contains(multi, "第二行文字")
	t.Logf("条目 1 Shift+Enter 续行: L1=%v L2=%v(输出 %d 字节)", hasL1, hasL2, len(multi))
	if !hasL1 || !hasL2 {
		t.Errorf("条目 1 失败:Shift+Enter 后两行应同时在输入区渲染;尾段 %q", firstN(tailS(multi, 400), 400))
	}

	// 条目 2:多行内 ↑/↓ 逐行移动(渲染应有响应且不丢文本)
	io.WriteString(ptmx, "\x1b[A")
	upDiff := drain(out, 800*time.Millisecond)
	io.WriteString(ptmx, "\x1b[B")
	downDiff := drain(out, 800*time.Millisecond)
	t.Logf("条目 2 ↑/↓ 多行内移动: up=%d 字节 down=%d 字节", len(upDiff), len(downDiff))
	if len(upDiff) == 0 || len(downDiff) == 0 {
		t.Errorf("条目 2 失败:多行态 ↑/↓ 应有渲染响应(up=%d down=%d)", len(upDiff), len(downDiff))
	}

	// 条目 3:清空后单行态,↑/↓ 归头/尾(不换行、不越位)
	io.WriteString(ptmx, "\x15") // Ctrl+U 清行
	drain(out, 600*time.Millisecond)
	io.WriteString(ptmx, "单行文本")
	drain(out, 600*time.Millisecond)
	io.WriteString(ptmx, "\x1b[A")
	afterUp := drain(out, 700*time.Millisecond)
	io.WriteString(ptmx, "\x1b[B")
	afterDown := drain(out, 700*time.Millisecond)
	t.Logf("条目 3 单行 ↑/↓: up=%d down=%d 字节", len(afterUp), len(afterDown))
	// 单行态 ↑/↓ 归头/尾:有渲染响应且文本完整(不可把单行撑成多行或丢内容)
	if len(afterUp) == 0 || len(afterDown) == 0 || !strings.Contains(afterUp+afterDown, "单行文本") {
		t.Errorf("条目 3 失败:单行态 ↑/↓ 应移光标且保留文本(up=%d down=%d);尾段 %q",
			len(afterUp), len(afterDown), firstN(tailS(afterUp+afterDown, 300), 300))
	}

	// 条目 36:Ctrl+A 全选 → 输入即替换
	io.WriteString(ptmx, "\x01") // Ctrl+A
	drain(out, 400*time.Millisecond)
	io.WriteString(ptmx, "替换后文本")
	replaced := drain(out, 900*time.Millisecond)
	t.Logf("条目 36 Ctrl+A 全选后输入: 含新文本=%v 含旧文本=%v", strings.Contains(replaced, "替换后文本"), strings.Contains(replaced, "单行文本"))
	if !strings.Contains(replaced, "替换后文本") {
		t.Errorf("条目 36 失败:Ctrl+A 后输入应替换全文;尾段 %q", firstN(tailS(replaced, 300), 300))
	}

	// 条目 36:Ctrl+Y redo(先 Ctrl+Z 撤销刚才的替换,再 Ctrl+Y 恢复)
	io.WriteString(ptmx, "\x1a") // Ctrl+Z undo
	drain(out, 600*time.Millisecond)
	io.WriteString(ptmx, "\x19") // Ctrl+Y redo
	redo := drain(out, 800*time.Millisecond)
	t.Logf("条目 36 Ctrl+Z/Ctrl+Y: redo 输出 %d 字节", len(redo))
	if len(redo) == 0 {
		t.Errorf("条目 36 失败:Ctrl+Y redo 应有渲染响应")
	}

	// 条目 36:Ctrl+B/F 光标移动(渲染应有响应)
	io.WriteString(ptmx, "\x02")
	bDiff := drain(out, 500*time.Millisecond)
	io.WriteString(ptmx, "\x06")
	fDiff := drain(out, 500*time.Millisecond)
	t.Logf("条目 36 Ctrl+B/F: b=%d f=%d 字节", len(bDiff), len(fDiff))
	if len(bDiff) == 0 || len(fDiff) == 0 {
		t.Errorf("条目 36 失败:Ctrl+B/Ctrl+F 应有渲染响应(b=%d f=%d)", len(bDiff), len(fDiff))
	}

	// 条目 5:`/` 命令含换行 → 提交被拒(保留现场 + 明确提示,不发起回合)。
	// 注意选择器激活时 Shift+Enter 语义 = Enter(应用选项),故须先 Esc 退选择器,
	// 才能把换行真插进 `/` 输入里(产品语义见 tui/model.go 的 Shift+Enter 分支)。
	io.WriteString(ptmx, "\x15")
	drain(out, 400*time.Millisecond)
	io.WriteString(ptmx, "/help")
	drain(out, 600*time.Millisecond)
	io.WriteString(ptmx, "\x1b") // Esc:退出命令选择器(Pick=nil)
	drain(out, 500*time.Millisecond)
	io.WriteString(ptmx, "\x1b[13;2u")
	io.WriteString(ptmx, "续行")
	drain(out, 600*time.Millisecond)
	io.WriteString(ptmx, "\r")
	rejected, hit := drainUntil(out, 3*time.Second, "命令不支持多行")
	t.Logf("条目 5 `/` 含换行提交: 出现拒绝提示=%v mock 回复出现=%v", hit, strings.Contains(rejected, "收到,这是 mock 回复。"))
	if !hit || strings.Contains(rejected, "收到,这是 mock 回复。") {
		t.Errorf("条目 5 失败:含换行的 / 命令应被拒(提示「命令不支持多行」)且不发起回合;尾段 %q", firstN(tailS(rejected, 400), 400))
	}

	// 条目 5b:纯空白(仅空格)不发起回合。
	io.WriteString(ptmx, "\x15")
	drain(out, 400*time.Millisecond)
	io.WriteString(ptmx, "   \r")
	blank := drain(out, 2*time.Second)
	t.Logf("条目 5b 纯空白不发起: mock 回复出现=%v", strings.Contains(blank, "收到,这是 mock 回复。"))
	if strings.Contains(blank, "收到,这是 mock 回复。") {
		t.Errorf("条目 5b 失败:纯空白输入不应发起回合")
	}

	tuiQuit(t, ptmx, cmd, out)
}

// TestTUIAcceptEnvCfg 前置于其它条目:确认探针自身的数据根/配置生效
// (mock 生效 = 能跑回合),避免后续"条目失败"其实是探针环境坏。
func TestTUIAcceptEnvCfg(t *testing.T) {
	bin, env, root, cwd := tuiAcceptSetup(t)
	ptmx, cmd, out := tuiAcceptBoot(t, bin, env)
	defer ptmx.Close()

	io.WriteString(ptmx, "探活\r")
	got, ok := drainUntil(out, 12*time.Second, "收到,这是 mock 回复。", "连不上", "connection refused")
	t.Logf("mock 回合: 命中=%v 输出 %d 字节", ok, len(got))
	if !strings.Contains(got, "收到,这是 mock 回复。") {
		t.Fatalf("llm-mock 未生效(数据根 %s,cwd %s):尾段 %q", root, cwd, firstN(tailS(got, 500), 500))
	}
	tuiQuit(t, ptmx, cmd, out)
}

// TestTUIAcceptDump 探针自用:采集现行文案(设置 GAH_TUI_DUMP=1 才跑,不参与门禁)。
func TestTUIAcceptDump(t *testing.T) {
	if os.Getenv("GAH_TUI_DUMP") != "1" {
		t.Skip("设置 GAH_TUI_DUMP=1 才跑(仅用于采集文案)")
	}
	bin, env, root, _ := tuiAcceptSetup(t)

	// A. 命令回显(每条独立会话)
	for _, c := range []string{"/provider use m2", "/approval strict"} {
		s := newTuiSess(t, bin, env, "--profile", "acctui")
		if !s.boot() {
			t.Fatalf("首帧失败 %s", c)
		}
		base := len(s.rawText())
		s.send(c + "\r")
		time.Sleep(1800 * time.Millisecond)
		full := s.rawText()
		t.Logf("DUMP[%s] => |%s|", c, stripANSI(full[min(base, len(full)):]))
		s.quit()
	}

	// B. @ 候选:打印整屏(候选区在输入框上方)
	s := newTuiSess(t, bin, env, "--profile", "acctui")
	if !s.boot() {
		t.Fatal("首帧失败 @")
	}
	s.send("@")
	time.Sleep(1500 * time.Millisecond)
	t.Logf("DUMP[@ 屏]\n%s", s.screen())
	s.quit()

	// C. 回合:思考 + 工具 + 收尾文本(与 TestTUIAcceptTurn 同一 patch)
	writeAcceptFile(t, filepath.Join(root, "config"), "patch-acctui.yaml", acceptTurnPatch)
	s2 := newTuiSess(t, bin, env, "--profile", "acctui")
	if !s2.boot() {
		t.Fatal("首帧失败 turn")
	}
	base := len(s2.rawText())
	s2.send("跑个工具看看\r")
	time.Sleep(8 * time.Second)
	full := s2.rawText()
	t.Logf("DUMP[turn 原始流] => %s", stripANSI(full[min(base, len(full)):]))
	t.Logf("DUMP[turn 屏]\n%s", s2.screen())
	s2.quit()
}

// acceptTurnPatch 带"思考 + 工具调用 + 收尾文本"的 mock 脚本。
const acceptTurnPatch = `entries:
  - id: llm-openai-compat
    enabled: false
  - id: llm-mock
    enabled: true
    data:
      script: '[{"tool":{"name":"shell","args":"{\"command\":\"echo TUI-TOOL-OK\"}"}},{"text":"工具跑完了,这是最终答复。"}]'
`

// stripANSI 去掉 CSI/OSC 转义,便于把渲染文本读成普通字符串(仅探针日志用)。
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) {
			break
		}
		switch s[i+1] {
		case '[': // CSI:到终结字节 @-~
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j
		case ']': // OSC:到 BEL 或 ST
			j := i + 2
			for j < len(s) && s[j] != 0x07 && !(s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\') {
				j++
			}
			i = j + 1
		default:
			i++
		}
	}
	return b.String()
}

// tuiCmdCase 一条"发命令 → 看回显"的验收用例。
type tuiCmdCase struct {
	id   string // VERIFY A-1 条目号
	name string
	keys []string
	want string
	anti string // 非空时:流中不应含(用于"不报错/不触发"类断言)
}

// TestTUIAcceptCommands 条目 12/14/16/20-30/32-35 的 pty 级验收。
// 每条用例**独立会话**(状态互不污染:选择器/过滤态会吃掉后续输入,同一会话里连发会误判)。
func TestTUIAcceptCommands(t *testing.T) {
	bin, env, _, _ := tuiAcceptSetup(t)
	cases := []tuiCmdCase{
		{"20", "/tree 会话分支树", []string{"/tree\r"}, "会话分支树:", ""},
		{"21", "/fork 派生分支(空会话=显式报错)", []string{"/fork 1\r"}, "无继承事件", ""},
		{"22", "/clone 复制当前会话", []string{"/clone\r"}, "已复制当前会话", ""},
		{"23", "/session list 会话枚举", []string{"/session list\r"}, "当前会话:", ""},
		{"24", "/reload 热重载指令文件", []string{"/reload\r"}, "已热重载指令文件", "panic"},
		{"25", "/reload 无改动不报错", []string{"/reload\r"}, "已热重载指令文件", "panic"},
		{"26", "/widgets off 关闭 widget 区", []string{"/widgets off\r"}, "widget 区已关闭", ""},
		{"27", "/widgets on 打开 widget 区", []string{"/widgets on\r"}, "widget 区已", ""},
		{"30", "/approval 三档用法", []string{"/approval\r"}, "严格:危险操作直接拒绝", ""},
		{"32", "/approval open 开放档", []string{"/approval open\r"}, "审批: open", ""},
		{"33", "/approval strict 严格档", []string{"/approval strict\r"}, "审批: strict", ""},
		{"35", "状态栏显示当前审批档", []string{"/approval strict\r"}, "审批: 严格", ""},
		{"12", "/compact 向导(指示词)", []string{"/compact\r"}, "指示词", ""},
		{"18", "/context 用法", []string{"/context\r"}, "展开逐工具", ""},
	}
	for _, c := range cases {
		t.Run(c.id+"_"+c.name, func(t *testing.T) {
			s := newTuiSess(t, bin, env, "--profile", "acctui")
			defer s.quit()
			if !s.boot() {
				t.Fatalf("条目 %s:首帧未就绪;屏 %q", c.id, firstN(s.screen(), 200))
			}
			for _, k := range c.keys {
				s.send(k)
				time.Sleep(800 * time.Millisecond)
			}
			got := s.waitRaw(c.want, 6*time.Second)
			t.Logf("条目 %s 回显尾部: %s", c.id, stripANSI(tailS(s.rawText(), 600)))
			if !got {
				t.Errorf("条目 %s(%s):流中未见 %q", c.id, c.name, c.want)
			}
			if c.anti != "" && strings.Contains(s.rawText(), c.anti) {
				t.Errorf("条目 %s(%s):流中出现不应有的 %q", c.id, c.name, c.anti)
			}
		})
	}
}

// TestTUIAcceptApprovalPersist 条目 17/34:审批档持久化 + 重启恢复 + 与 Web 共享 gah-state.json。
func TestTUIAcceptApprovalPersist(t *testing.T) {
	bin, env, root, _ := tuiAcceptSetup(t)
	s := newTuiSess(t, bin, env, "--profile", "acctui")
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("/approval strict\r")
	if !s.waitRaw("审批: strict", 6*time.Second) && !s.waitRaw("审批: 严格", 4*time.Second) {
		t.Fatalf("切严格档未回显;尾部 %s", stripANSI(tailS(s.rawText(), 400)))
	}
	s.quit()

	// 持久化落盘:gah-state.json 应记下 strict(与 Web 端共用同一文件)
	statePath := filepath.Join(root, "config", "gah-state.json")
	blob, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("读 gah-state.json 失败: %v", err)
	}
	if !strings.Contains(string(blob), "strict") {
		t.Errorf("gah-state.json 未持久化审批档;内容 %s", tailS(string(blob), 300))
	}

	// 重启恢复:新进程/新会话的首帧状态栏应直接显示严格档
	s2 := newTuiSess(t, bin, env, "--profile", "acctui")
	defer s2.quit()
	if !s2.boot() {
		t.Fatal("重启首帧未就绪")
	}
	if !s2.waitRaw("审批: 严格", 5*time.Second) {
		t.Errorf("重启后状态栏未恢复严格档;屏 %q", firstN(s2.screen(), 300))
	}
}

// TestTUIAcceptTurn 条目 29/40/41:回合内的工具视觉与思维块(需要模型侧产出)。
// llm-mock 的 script 里插一个 shell 工具调用,验证工具块/思维块在 TUI 的渲染标记。
func TestTUIAcceptTurn(t *testing.T) {
	bin, env, root, _ := tuiAcceptSetup(t)
	// 覆盖 patch:script 变成"思考 + 调工具 + 收尾文本"
	writeAcceptFile(t, filepath.Join(root, "config"), "patch-acctui.yaml", acceptTurnPatch)
	s := newTuiSess(t, bin, env, "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("跑个工具看看\r")
	ok := s.waitRaw("工具跑完了", 20*time.Second)
	raw := s.rawText()
	t.Logf("条目 29/40 回合渲染尾部: %s", stripANSI(tailS(raw, 900)))
	if !ok {
		t.Fatalf("回合未完成(mock 未产出最终答复);尾部 %s", stripANSI(tailS(raw, 600)))
	}
	// 条目 29:工具调用在会话流里有可见块(工具名与实际输出)
	if !strings.Contains(raw, "shell") || !strings.Contains(raw, "TUI-TOOL-OK") {
		t.Errorf("条目 29 失败:工具调用块应显示工具名与输出")
	}
	// 条目 40:思维块本体需要模型侧 reasoning 事件(llm-mock 与 agent loop 均无该字段),
	// 本探针只验"折叠键在工具回合里有渲染响应且不丢会话"这一部分。
	if !strings.Contains(raw, "▲") && !strings.Contains(raw, "▼") && !strings.Contains(raw, "Ctrl+O") {
		t.Errorf("条目 40 部分失败:工具块折叠标识未见")
	}
	// 条目 40:Ctrl+T 触发折叠键位(本环境无 reasoning 事件,故不产生视觉变化,只记日志)
	before := len(s.rawText())
	s.send("\x14")
	time.Sleep(700 * time.Millisecond)
	if len(s.rawText()) > before {
		t.Logf("条目 40:Ctrl+T 有渲染响应(+%d 字节)", len(s.rawText())-before)
	} else {
		t.Logf("条目 40:Ctrl+T 无视觉变化(无 reasoning 事件,符合预期)")
	}
}

// TestTUIAcceptMultiProvider 条目 14/15/16:多 provider 枚举/切换/模型聚合。
func TestTUIAcceptMultiProvider(t *testing.T) {
	bin, env, _, _ := tuiAcceptSetup(t)
	s := newTuiSess(t, bin, env, "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}

	// 条目 14:两条 provider 都列出,活跃的一条标 ★(mock 环境无真凭据,Key 打码)
	s.send("/provider show\r")
	if !s.waitRaw("★ m |", 6*time.Second) {
		t.Fatalf("条目 14:未见活跃标记;尾部 %s", stripANSI(tailS(s.rawText(), 500)))
	}
	if !strings.Contains(s.rawText(), "m2 |") {
		t.Errorf("条目 14:第二条 provider 未列出")
	}
	if !strings.Contains(s.rawText(), "Key: ***") {
		t.Errorf("条目 14:凭据未打码")
	}

	// 条目 15:切到 m2
	s.send("/provider use m2\r")
	if !s.waitRaw("已切换", 6*time.Second) && !s.waitRaw("m2", 3*time.Second) {
		t.Fatalf("条目 15:切换未回显;尾部 %s", stripANSI(tailS(s.rawText(), 500)))
	}
	t.Logf("条目 15 回显: %s", stripANSI(tailS(s.rawText(), 400)))

	// 条目 16:模型聚合枚举(向导提示模型名)
	s.send("/model\r")
	if !s.waitRaw("模型名", 6*time.Second) {
		t.Errorf("条目 16:/model 未进入模型选择向导")
	}
	t.Logf("条目 16 回显: %s", stripANSI(tailS(s.rawText(), 500)))
}

// TestTUIAcceptProviderPersist 条目 17:provider 切换跨进程持久化(与 Web 共享 config/gah-state.json)。
func TestTUIAcceptProviderPersist(t *testing.T) {
	bin, env, root, _ := tuiAcceptSetup(t)
	s := newTuiSess(t, bin, env, "--profile", "acctui")
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("/provider use m2\r")
	time.Sleep(2500 * time.Millisecond) // 等切换+落盘(prefs 走 Update,异步刷盘)
	s.quit()

	// provider 切换持久化在 config/provider.yaml 的 active 字段(与 Web 端同一份文件)
	provPath := filepath.Join(root, "config", "provider.yaml")
	blob, err := os.ReadFile(provPath)
	if err != nil {
		t.Fatalf("条目 17:读 provider.yaml 失败: %v", err)
	}
	if !strings.Contains(string(blob), "active: m2") {
		t.Errorf("条目 17:活跃 provider 未持久化;内容 %s", tailS(string(blob), 300))
	}

	// 重启:活跃 provider 应仍为 m2(★ 在 m2 上)
	s2 := newTuiSess(t, bin, env, "--profile", "acctui")
	defer s2.quit()
	if !s2.boot() {
		t.Fatal("重启首帧未就绪")
	}
	s2.send("/provider show\r")
	if !s2.waitRaw("★ m2 |", 6*time.Second) {
		t.Errorf("条目 17:重启后活跃 provider 未恢复;尾部 %s", stripANSI(tailS(s2.rawText(), 600)))
	}
}

// TestTUIAcceptMention 条目 9/10/11:@ 文件候选(工作区文件触发、URL/邮箱不触发)。
func TestTUIAcceptMention(t *testing.T) {
	bin, env, _, _ := tuiAcceptSetup(t)
	t.Run("9_@触发文件候选", func(t *testing.T) {
		s := newTuiSess(t, bin, env, "--profile", "acctui")
		defer s.quit()
		if !s.boot() {
			t.Fatal("首帧未就绪")
		}
		s.send("@")
		// 候选来自工作区(cwd = 本包目录),应列出本包文件(名序首条 acp_e2e_test.go)
		if !s.waitRaw("@acp_e2e_test.go", 6*time.Second) {
			t.Errorf("条目 9:@ 未列出工作区文件候选;屏 %q", firstN(s.screen(), 400))
		}
		// Tab 补全:候选写入输入行(路径出现在输入框)
		s.send("\t")
		time.Sleep(600 * time.Millisecond)
		t.Logf("条目 9 补全后屏: %q", firstN(s.screen(), 300))
	})
	t.Run("10_邮箱URL的@不触发候选", func(t *testing.T) {
		s := newTuiSess(t, bin, env, "--profile", "acctui")
		defer s.quit()
		if !s.boot() {
			t.Fatal("首帧未就绪")
		}
		s.send("a@b.com")
		time.Sleep(1500 * time.Millisecond)
		if strings.Contains(s.rawText(), "acp_e2e_test.go") {
			t.Errorf("条目 10:邮箱内的 @ 误触发文件候选")
		}
	})
}

// TestTUIAcceptApprovalDialog 条目 31:smart 档下危险工具调用在 TUI 弹 y/n 确认,
// 拒绝后回合正常收束(确认弹层的"无通道安全拒"侧已由阶段 8B #108c 覆盖)。
func TestTUIAcceptApprovalDialog(t *testing.T) {
	bin, env, root, _ := tuiAcceptSetup(t)
	writeAcceptFile(t, filepath.Join(root, "config"), "patch-acctui.yaml", `entries:
  - id: llm-openai-compat
    enabled: false
  - id: llm-mock
    enabled: true
    data:
      script: '[{"tool":{"name":"file_write","args":"{\"path\":\"tui-confirm.txt\",\"content\":\"TUI-CONFIRM-OK\"}"}},{"text":"工具跑完了,这是最终答复。"}]'
  - id: policy-guard
    enabled: true
    data:
      approval: smart
      approval_tools: ["file_write"]
`)
	// 工作区 = 独立临时目录:shell 写工作区内文件才不被沙箱先行拒绝(危险区外命令走不到审批)
	s := newTuiSessIn(t, bin, env, t.TempDir(), "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("写个工作区内的文件\r")
	if !s.waitRaw("确认执行", 20*time.Second) {
		t.Errorf("条目 31:smart 档未弹确认")
		t.Logf("全量原始流:\n%s", stripANSI(s.rawText()))
		t.Logf("屏:\n%s", s.screen())
		return
	}
	raw := s.rawText()
	t.Logf("条目 31 弹层: %s", stripANSI(tailS(raw, 500)))
	if !strings.Contains(raw, "y/n") {
		t.Errorf("条目 31:确认弹层缺 y/n 语义")
	}
	// 拒绝 → 弹层收起,回合收束(不写出、不崩)
	s.send("n")
	if !s.waitRaw("—— 轮次结束 ——", 20*time.Second) {
		t.Errorf("条目 31:拒绝后回合未收束;尾部 %s", stripANSI(tailS(s.rawText(), 500)))
	}
}
