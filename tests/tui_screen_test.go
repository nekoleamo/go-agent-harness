// TUI 探针的最小 ANSI 屏幕模型(仅测试用)。
//
// 为什么需要它:tui 是差分重绘,原始 pty 字节流里同一帧的新旧文本交替出现
// (例如 `/approval` 的回显会被读成 `/appro → 无匹配val → 无匹配`),直接
// strings.Contains 判"屏幕上有没有某内容"会既误判又难读。
// 这里把 TUI 实际用到的序列作用到一个虚拟屏上,断言改读屏幕文本。
//
// 刻意只实现 TUI 用得到的子集(光标定位/清行清屏/回车换行制表/退格),未识别序列跳过;
// 宽字符按 rune 计一格(列偏移对"包含某文本"的断言无影响)。
package tests

import (
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

const (
	scrRows = 24 // 与 runTUIViaPty* 的 pty.Winsize 一致
	scrCols = 80
)

type tuiScreen struct {
	mu   sync.Mutex
	buf  [][]rune
	row  int
	col  int
	pend []byte // 未消费完的转义序列(跨 read 边界)
}

// screenTextOf 把一段原始 pty 流灌进屏幕模型,返回当前屏幕文本。
// 供旧探针用:差分重绘会把同一段文本拆成多次输出(逐键重绘),且机器忙时部分帧可能
// 落在采样窗口之外 —— 原始 diff 上做 Contains 会假阴;"当前屏幕"与读到多少帧无关。
func screenTextOf(raw string) string {
	sc := newTuiScreen()
	sc.feed([]byte(raw))
	return sc.text()
}

func newTuiScreen() *tuiScreen {
	s := &tuiScreen{}
	s.buf = make([][]rune, scrRows)
	for i := range s.buf {
		s.buf[i] = make([]rune, scrCols)
		for j := range s.buf[i] {
			s.buf[i][j] = ' '
		}
	}
	return s
}

func (s *tuiScreen) feed(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pend = append(s.pend, b...)
	for len(s.pend) > 0 {
		c := s.pend[0]
		if c != 0x1b {
			// 必须按 UTF-8 解码:中文一个字 3 字节,按字节喂屏幕会得到乱码
			r, size := utf8.DecodeRune(s.pend)
			if r == utf8.RuneError && size <= 1 {
				if len(s.pend) < utf8.UTFMax {
					return // 可能是被 read 边界切断的半个字符,等下一块
				}
				s.pend = s.pend[1:] // 真非法字节:跳过
				continue
			}
			s.put(r)
			s.pend = s.pend[size:]
			continue
		}
		if len(s.pend) < 2 {
			return // 序列还没收完
		}
		switch s.pend[1] {
		case '[': // CSI ... 终结字节 @..~
			end := 2
			for end < len(s.pend) && (s.pend[end] < 0x40 || s.pend[end] > 0x7e) {
				end++
			}
			if end >= len(s.pend) {
				return // 未收完
			}
			s.csi(string(s.pend[2:end]), s.pend[end])
			s.pend = s.pend[end+1:]
		case ']': // OSC ... BEL / ST
			end := 2
			for end < len(s.pend) {
				if s.pend[end] == 0x07 {
					break
				}
				if s.pend[end] == 0x1b && end+1 < len(s.pend) && s.pend[end+1] == '\\' {
					break
				}
				end++
			}
			if end >= len(s.pend) {
				return
			}
			if s.pend[end] == 0x07 {
				s.pend = s.pend[end+1:]
			} else {
				s.pend = s.pend[end+2:]
			}
		default: // 双字节转义(如 ESC c 复位)
			if len(s.pend) < 2 {
				return
			}
			s.pend = s.pend[2:]
		}
	}
}

// csi 处理 TUI 用到的 CSI 子集。
func (s *tuiScreen) csi(params string, final byte) {
	n, ps := parseParams(params)
	switch final {
	case 'H', 'f': // 光标定位(1-based;缺省 = 1)
		r, c := 1, 1
		if len(ps) > 0 && ps[0] > 0 {
			r = ps[0]
		}
		if len(ps) > 1 && ps[1] > 0 {
			c = ps[1]
		}
		s.row, s.col = clamp(r-1, 0, scrRows-1), clamp(c-1, 0, scrCols-1)
	case 'A':
		s.row = clamp(s.row-maxInt(n, 1), 0, scrRows-1)
	case 'B':
		s.row = clamp(s.row+maxInt(n, 1), 0, scrRows-1)
	case 'C':
		s.col = clamp(s.col+maxInt(n, 1), 0, scrCols-1)
	case 'D':
		s.col = clamp(s.col-maxInt(n, 1), 0, scrCols-1)
	case 'G':
		s.col = clamp(maxInt(n, 1)-1, 0, scrCols-1)
	case 'd':
		s.row = clamp(maxInt(n, 1)-1, 0, scrRows-1)
	case 'J': // 清屏:0=光标到末尾 1=开头到光标 2=全屏 3=含回滚
		switch n {
		case 1:
			for j := 0; j < s.col; j++ {
				s.buf[s.row][j] = ' '
			}
			for i := 0; i < s.row; i++ {
				s.clearRow(i)
			}
		case 2, 3:
			for i := range s.buf {
				s.clearRow(i)
			}
		default:
			for j := s.col; j < scrCols; j++ {
				s.buf[s.row][j] = ' '
			}
			for i := s.row + 1; i < scrRows; i++ {
				s.clearRow(i)
			}
		}
	case 'K': // 清行:0=光标到行尾 1=行首到光标 2=整行
		switch n {
		case 1:
			for j := 0; j <= s.col && j < scrCols; j++ {
				s.buf[s.row][j] = ' '
			}
		case 2:
			s.clearRow(s.row)
		default:
			for j := s.col; j < scrCols; j++ {
				s.buf[s.row][j] = ' '
			}
		}
	case 'm': // SGR:颜色/样式,忽略
	default:
		// 其它(含 ?25l 光标显隐、?2026 同步刷新等私有序列)忽略
	}
}

func (s *tuiScreen) clearRow(i int) {
	for j := range s.buf[i] {
		s.buf[i][j] = ' '
	}
}

// put 写入一个 rune(宽字符占两列,这里只占一格 —— 对文本包含断言无影响)。
func (s *tuiScreen) put(r rune) {
	switch r {
	case '\r':
		s.col = 0
		return
	case '\n':
		s.row = clamp(s.row+1, 0, scrRows-1)
		return
	case '\t':
		s.col = clamp((s.col/8+1)*8, 0, scrCols-1)
		return
	case 0x08: // 退格
		s.col = clamp(s.col-1, 0, scrCols-1)
		return
	case 0x07:
		return
	}
	// 宽字符(CJK/全角)占两列 —— 否则后续所有 CSI 行列定位都会整体错位,
	// 屏幕会画成一团(实测:按一格算时 /tree 的输出与帮助面板互相穿插)。
	w := runeWidth(r)
	if s.col >= scrCols {
		s.col = scrCols - 1
	}
	s.buf[s.row][s.col] = r
	if w == 2 && s.col+1 < scrCols {
		s.buf[s.row][s.col+1] = 0 // 0 = 宽字符的延续格(text() 输出时跳过)
	}
	s.col = clamp(s.col+w, 0, scrCols-1)
}

// runeWidth 终端显示宽度:0(组合符)/1(半角)/2(CJK 与全角)。
// 覆盖 TUI 实际会画的范围(界面全中文 + 框线 + ⊕★ 等符号),不做完整 Unicode 表。
func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 0x1100:
		return 1
	case r >= 0x1100 && r <= 0x115F, // 韩文字母
		r >= 0x2E80 && r <= 0x303E,   // CJK 部首/标点
		r >= 0x3041 && r <= 0x33FF,   // 假名/注音/CJK 兼容
		r >= 0x3400 && r <= 0x4DBF,   // CJK 扩展 A
		r >= 0x4E00 && r <= 0x9FFF,   // CJK 统一表意
		r >= 0xA000 && r <= 0xA4CF,   // 彝文
		r >= 0xAC00 && r <= 0xD7A3,   // 韩文音节
		r >= 0xF900 && r <= 0xFAFF,   // CJK 兼容表意
		r >= 0xFE30 && r <= 0xFE6F,   // CJK 兼容形式
		r >= 0xFF00 && r <= 0xFF60,   // 全角形式
		r >= 0xFFE0 && r <= 0xFFE6,   // 全角符号
		r >= 0x1F300 && r <= 0x1F64F, // 表情
		r >= 0x20000 && r <= 0x3FFFD: // CJK 扩展 B+
		return 2
	}
	return 1
}

// text 屏幕文本(每行去尾部空白;首尾空行也去掉,便于 contains 判断)。
func (s *tuiScreen) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, scrRows)
	for i := range s.buf {
		line := make([]rune, 0, scrCols)
		for _, r := range s.buf[i] {
			if r == 0 {
				continue // 宽字符延续格
			}
			line = append(line, r)
		}
		out = append(out, strings.TrimRight(string(line), " "))
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}

// contains 屏幕上是否含某子串(按"整屏文本包含"判定)。
func (s *tuiScreen) contains(sub string) bool {
	return strings.Contains(s.text(), sub)
}

func parseParams(params string) (int, []int) {
	if params == "" {
		return 0, nil
	}
	// 去掉私有前缀(如 "?25")与中间字节
	params = strings.TrimLeft(params, "?>=!")
	parts := strings.Split(params, ";")
	ps := make([]int, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			ps = append(ps, 0)
			continue
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			v = 0
		}
		ps = append(ps, v)
	}
	if len(ps) > 0 {
		return ps[0], ps
	}
	return 0, ps
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// tuiSess 一次 TUI 会话:pty + 屏幕模型 + 原始流(存在性兜底断言用)。
type tuiSess struct {
	t     *testing.T
	ptmx  *os.File
	cmd   *exec.Cmd
	out   chan string
	scr   *tuiScreen
	rawMu sync.Mutex
	raw   strings.Builder
}

// newTuiSess 启动一次 TUI 会话并把输出同时喂给屏幕模型与原始流。
func newTuiSess(t *testing.T, bin string, env []string, args ...string) *tuiSess {
	t.Helper()
	ptmx, cmd, out := runTUIViaPtyArgs(t, bin, env, args...)
	s := &tuiSess{t: t, ptmx: ptmx, cmd: cmd, out: out, scr: newTuiScreen()}
	go func() {
		for chunk := range out {
			s.rawMu.Lock()
			s.raw.WriteString(chunk)
			s.rawMu.Unlock()
			s.scr.feed([]byte(chunk))
		}
	}()
	return s
}

// newTuiSessIn 同 newTuiSess,但指定工作目录(= 该会话的工作区,决定沙箱可写范围)。
func newTuiSessIn(t *testing.T, bin string, env []string, dir string, args ...string) *tuiSess {
	t.Helper()
	ptmx, cmd, out := runTUIViaPtyDirArgs(t, bin, env, dir, args...)
	s := &tuiSess{t: t, ptmx: ptmx, cmd: cmd, out: out, scr: newTuiScreen()}
	go func() {
		for chunk := range out {
			s.rawMu.Lock()
			s.raw.WriteString(chunk)
			s.rawMu.Unlock()
			s.scr.feed([]byte(chunk))
		}
	}()
	return s
}

// send 发送按键(原始字节)。
func (s *tuiSess) send(keys string) { _, _ = io.WriteString(s.ptmx, keys) }

// boot 等 TUI 真正可交互:判据用输入框提示符 `❯` + 状态栏"空闲"。
// 不用 "工作区: " —— 启动日志里也会带 workspace/工作区字样,拿它当就绪信号会
// 提前放行(命令在界面就绪前发出被丢掉,实测表现为"所有命令都无回显")。
func (s *tuiSess) boot(args ...string) bool {
	ready := func() bool { return s.waitRaw("❯", 5*time.Second) || s.waitRaw("工作区: ", 5*time.Second) }
	if !ready() {
		return false
	}
	time.Sleep(600 * time.Millisecond) // 等首帧整屏渲染落定
	return true
}

// quit 退出会话:先 Esc 收掉可能打开的选择器/向导(选择器态下 Ctrl+C 语义不同,
// 实测会让退出链变慢甚至卡住),再双击 Ctrl+C;超时强杀(只记日志,不算断言失败 ——
// 退出慢属于探针噪声,不该把条目判成 FAIL)。
func (s *tuiSess) quit() {
	s.send("\x1b")
	time.Sleep(200 * time.Millisecond)
	s.send("\x03")
	time.Sleep(300 * time.Millisecond)
	s.send("\x03")
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		s.t.Logf("退出超时,已强杀(探针噪声,不影响断言)")
	}
	_ = s.ptmx.Close()
}

// waitScreen 轮询直到屏幕上出现 sub(或超时)。
func (s *tuiSess) waitScreen(sub string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if s.scr.contains(sub) {
			return true
		}
		time.Sleep(80 * time.Millisecond)
	}
	return s.scr.contains(sub)
}

// waitRaw 轮询直到**原始字节流**里出现 sub(或超时)。
// 用原始流而非屏幕模型:很多断言只关心"这条命令的回显出现过",屏幕模型的行列
// 语义(滚动/重绘)在长输出下无需参与判定。
func (s *tuiSess) waitRaw(sub string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(s.rawText(), sub) {
			return true
		}
		time.Sleep(80 * time.Millisecond)
	}
	return strings.Contains(s.rawText(), sub)
}

// waitScreenGone 轮询直到屏幕上**不再**出现 sub(用于"队列已清空/提示已消失"类断言;
// 原始流是累计缓冲,出现过就永远 Contains,不能用来判消失)。
func (s *tuiSess) waitScreenGone(sub string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !s.scr.contains(sub) {
			return true
		}
		time.Sleep(80 * time.Millisecond)
	}
	return !s.scr.contains(sub)
}

// inputLine 输入框那行(`│ ❯ … │`);取最后一行匹配(就地重绘会留旧行)。
func (s *tuiSess) inputLine() string {
	lines := strings.Split(s.screen(), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "❯") && strings.Contains(lines[i], "│") {
			return lines[i]
		}
	}
	return ""
}

// statusLineWith 屏幕上包含 sub 的那一行(取最后一行:状态栏是就地重绘,旧行可能仍留在
// 屏模型里 —— 判"实时状态"要看最新那行行内内容,而不是整屏 Contains)。
func (s *tuiSess) statusLineWith(sub string) string {
	lines := strings.Split(s.screen(), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], sub) {
			return lines[i]
		}
	}
	return ""
}

// screen 当前屏幕文本。
func (s *tuiSess) screen() string { return s.scr.text() }

// rawText 累计原始字节流(转义已替换为可读占位,便于日志)。
func (s *tuiSess) rawText() string {
	s.rawMu.Lock()
	defer s.rawMu.Unlock()
	return s.raw.String()
}
