// P4-6 多行输入单测:行切分/光标换算、Shift+Enter 换行、↑/↓ 行间移动(列意图)、
// 外部编辑器回填、渲染多行输入区、模型层键分派与 submit 多行/空白语义。
package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/internal/testutil"
)

// —— 行工具:segLines / cursorLineCol ——

func TestSegLines(t *testing.T) {
	cases := []struct {
		in   string
		want [][2]int
	}{
		{"", [][2]int{{0, 0}}},
		{"ab", [][2]int{{0, 2}}},
		{"a\nb", [][2]int{{0, 1}, {2, 3}}},
		{"a\n", [][2]int{{0, 1}, {2, 2}}},
		{"\n", [][2]int{{0, 0}, {1, 1}}},
		{"\n\nx", [][2]int{{0, 0}, {1, 1}, {2, 3}}},
	}
	for _, c := range cases {
		segs := segLines([]rune(c.in))
		if len(segs) != len(c.want) {
			t.Fatalf("%q 段数 = %d,期望 %d", c.in, len(segs), len(c.want))
		}
		for i, w := range c.want {
			if segs[i].start != w[0] || segs[i].end != w[1] {
				t.Fatalf("%q 段 %d = [%d,%d),期望 [%d,%d)", c.in, i, segs[i].start, segs[i].end, w[0], w[1])
			}
		}
	}
}

func TestCursorLineCol(t *testing.T) {
	cases := []struct {
		in     string
		cursor int
		line   int
		col    int
	}{
		{"abc", 0, 0, 0},
		{"abc", 1, 0, 1},
		{"abc", 3, 0, 3},
		{"abc", 99, 0, 3}, // 越界钳制
		{"a\nb", 0, 0, 0},
		{"a\nb", 1, 0, 1}, // 换行前:归首行尾
		{"a\nb", 2, 1, 0}, // 第二行首
		{"a\nb", 3, 1, 1},
		{"a\nb\n", 4, 2, 0}, // 尾随换行后的空行
		{"", 0, 0, 0},
		{"\n", 0, 0, 0},
		{"\n", 1, 1, 0},
	}
	for _, c := range cases {
		l, col := cursorLineCol([]rune(c.in), c.cursor)
		if l != c.line || col != c.col {
			t.Fatalf("%q c%d → (行%d,列%d),期望 (行%d,列%d)", c.in, c.cursor, l, col, c.line, c.col)
		}
	}
}

// —— Shift+Enter 换行与 undo ——

func TestInsertNewlineAndUndo(t *testing.T) {
	s := &State{Input: "ab", Cursor: 1}
	s.InsertNewline()
	if s.Input != "a\nb" || s.Cursor != 2 {
		t.Fatalf("插换行后 = %q c%d", s.Input, s.Cursor)
	}
	// 光标在新行首继续键入
	s.InsertRune('x')
	if s.Input != "a\nxb" || s.Cursor != 3 {
		t.Fatalf("换行后键入 = %q c%d", s.Input, s.Cursor)
	}
	// undo:一次回到插入换行前(InsertNewline 与 InsertRune 型不同不合并)
	if !s.Undo() || s.Input != "a\nb" || s.Cursor != 2 {
		t.Fatalf("undo1 应回 a\\nb: %q c%d", s.Input, s.Cursor)
	}
	if !s.Undo() || s.Input != "ab" || s.Cursor != 1 {
		t.Fatalf("undo2 应回 ab: %q c%d", s.Input, s.Cursor)
	}
}

// —— ↑/↓ 行间移动与列意图 ——

func TestLineMoveMulti(t *testing.T) {
	// "aa\nbbbb\ncc" 三行(下标 0,2,5,7… 见注释:aa[0-2) bb[3-7) cc[8-10))
	s := &State{Input: "aa\nbbbb\ncc", Cursor: 10} // 末行尾
	s.LineUp()                                     // → bb 行 min(2,4)=2 → 下标 3+2=5
	if s.Input != "aa\nbbbb\ncc" || s.Cursor != 5 {
		t.Fatalf("上移一次: c=%d 期望 5", s.Cursor)
	}
	s.LineUp() // → aa 行 min(2,2)=2 → 下标 0+2=2
	if s.Cursor != 2 {
		t.Fatalf("再上移: c=%d 期望 2", s.Cursor)
	}
	s.LineUp() // 首行再上 = 段首
	if s.Cursor != 0 {
		t.Fatalf("首行再上应段首: c=%d", s.Cursor)
	}
	s.LineDown() // → bb 行首(意图列 0)
	if s.Cursor != 3 {
		t.Fatalf("首行下移应 bb 行首: c=%d", s.Cursor)
	}
	s.LineDown() // → cc 行首
	if s.Cursor != 8 {
		t.Fatalf("再下移应 cc 行首: c=%d", s.Cursor)
	}
	s.LineDown() // 末行再下 = 段尾
	if s.Cursor != 10 {
		t.Fatalf("末行再下应段尾: c=%d", s.Cursor)
	}
}

func TestLineMoveColumnMemory(t *testing.T) {
	// "aaaa\nbbbbbb\ncc":行0 长4、行1 长6、行2 长2。
	s := &State{Input: "aaaa\nbbbbbb\ncc", Cursor: 0}
	s.Cursor = 0 // 行0
	s.CursorRight()
	s.CursorRight() // 行0 列2(不触发 vCol 记忆?向右已失效)
	// 直接连续下移两次:第一次捕捉列 2,第二行沿用列 2
	s.LineDown() // 捕捉 col2 → bb 行 下标 5+2=7
	if s.Cursor != 7 {
		t.Fatalf("下行列意图: c=%d 期望 7", s.Cursor)
	}
	s.LineDown() // 末行 cc 长2 → min(2,2)=2 → 下标 12+2=14(行尾)
	if s.Cursor != 14 {
		t.Fatalf("再下行: c=%d 期望 14", s.Cursor)
	}
	// 长行(行1)列 2 记忆:上移回行1 用列 2(从末行)
	s.LineUp() // 意图列 2 → bb 行 7
	if s.Cursor != 7 {
		t.Fatalf("上行保持列意图: c=%d 期望 7", s.Cursor)
	}
}

func TestLineMoveEmptyLine(t *testing.T) {
	// "a\n\nb":中间空行;从 b 行上移应落在空行首(空行 start=2)。
	s := &State{Input: "a\n\nb", Cursor: 4} // 行2 "b" 尾(下标 0a 1\n 2空 3\n 4b)
	s.LineUp()
	if s.Cursor != 2 {
		t.Fatalf("上移到空行首: c=%d 期望 2", s.Cursor)
	}
	s.LineUp() // 空行再上 = a 行
	if s.Cursor != 1 {
		t.Fatalf("空行上移: c=%d 期望 1(a 行尾)", s.Cursor)
	}
}

func TestLineMoveSingleLineDegrade(t *testing.T) {
	s := &State{Input: "hello", Cursor: 5}
	s.LineUp()
	if s.Cursor != 0 {
		t.Fatalf("单行 ↑ 应回头: c=%d", s.Cursor)
	}
	s.LineDown()
	if s.Cursor != 5 {
		t.Fatalf("单行 ↓ 应回尾: c=%d", s.Cursor)
	}
}

// —— 外部编辑器回填 ——

func TestApplyExternalAndUndo(t *testing.T) {
	s := &State{Input: "旧文本", Cursor: 3}
	s.ApplyExternal("新内容\n第二行")
	if s.Input != "新内容\n第二行" || s.Cursor != 7 {
		t.Fatalf("回填 = %q c%d", s.Input, s.Cursor)
	}
	if !s.PickDismissed {
		t.Fatal("回填后应置 PickDismissed(不激活选择器)")
	}
	if !s.Undo() || s.Input != "旧文本" {
		t.Fatalf("undo 应回编辑前: %q", s.Input)
	}
	// 内容相同:不动作(undo 栈不新增)
	n := len(s.undo)
	s.Input = "abc"
	s.ApplyExternal("abc")
	if len(s.undo) != n {
		t.Fatal("无变化不应入 undo 栈")
	}
}

// —— 渲染:多行输入区 ——

func TestRenderInputLineMultiline(t *testing.T) {
	s := &State{Input: "ab\ncd\nef", Cursor: 6} // 光标 = 行2 首(cd 行尾换行后)
	out := stripColor(renderInputLine(s, 80))
	// F15.1:指标信息在独立行,输入行不含(多行输入逐字等价);提示符与续行缩进统一 2 列
	if !strings.Contains(out, "❯ ab\n  cd\n  █ef") {
		t.Fatalf("多行输入渲染: %q", out)
	}
	// 光标在行0 内部
	s2 := &State{Input: "ab\ncd", Cursor: 1}
	if out := stripColor(renderInputLine(s2, 80)); !strings.Contains(out, "❯ a█b\n  cd") {
		t.Fatalf("光标行0: %q", out)
	}
	// 单行逐字等价(历史行为不变;指标行独立;思考等级语义移至框边框色)
	s3 := &State{Input: "hello", Cursor: 2}
	if out := stripColor(renderInputLine(s3, 80)); out != "❯ he█llo" {
		t.Fatalf("单行渲染: %q", out)
	}
}

// TestInputEdgeThinkingColor P5:框边框色随思考等级(off 灰/low 蓝灰/med 绿/high 紫)。
func TestInputEdgeThinkingColor(t *testing.T) {
	cases := []struct {
		level string
		want  string // 24-bit RGB 分量(lipgloss 输出格式 38;2;r;g;b)
	}{
		{"", "38;2;102;92;84"}, {"off", "38;2;102;92;84"}, {"low", "38;2;131;165;152"},
		{"medium", "38;2;142;192;124"}, {"high", "38;2;211;134;155"},
	}
	for _, c := range cases {
		out := renderInputFrame("x", 80, c.level)
		if !strings.Contains(out, c.want) {
			t.Fatalf("思考 %q 框边框应含色 %s: %q", c.level, c.want, out)
		}
	}
}

func TestRenderMultiLineInputLayout(t *testing.T) {
	// 输入两行:主区高度自动扣 1(总输出行数恒定 = height-1,与单行输入一致)。
	s := &State{Input: "a\nb", Cursor: 1}
	for i := 0; i < 18; i++ { // 填满主区(两行输入 mainH=11)验证恒定公式
		s.Lines = append(s.Lines, Line{Kind: "user", Text: "行" + string(rune('0'+i))})
	}
	out := Render(s, 80, 20)
	lines := strings.Split(out, "\n")
	if len(lines) != 19 { // 恒定 height-1(输入区含圆角框:内容 2 + 顶/底边框 2)
		t.Fatalf("输出应 19 行,实际 %d", len(lines))
	}
	// F15.2:末行为指标行(模型/上下文在最后一行下面),其上是状态栏
	if !strings.Contains(stripColor(lines[len(lines)-1]), "上下文 -") {
		t.Fatalf("末行应为指标行: %q", lines[len(lines)-1])
	}
	if !strings.Contains(stripColor(lines[len(lines)-2]), "工作区:") {
		t.Fatalf("状态栏应在指标行之上: %q", lines[len(lines)-2])
	}
	// 圆角框:分隔线 → 顶边框 → 内容首行(光标块) → 内容续行 → 底边框
	if !strings.Contains(stripColor(lines[12]), "╭") || !strings.Contains(stripColor(lines[15]), "╰") {
		t.Fatalf("输入区顶/底圆角边框应渲染: %q / %q", lines[12], lines[15])
	}
	if !strings.Contains(stripColor(lines[13]), "❯ a█") {
		t.Fatalf("输入内容首行应含光标块: %q", lines[13])
	}
	if got := strings.TrimRight(stripColor(lines[14]), " "); !strings.Contains(got, "b") || !strings.HasPrefix(got, "│") {
		t.Fatalf("续行应于框内缩进对齐: %q", got)
	}
}

// —— 编辑器命令解析 ——

func TestEditorCommandResolution(t *testing.T) {
	// VISUAL 优先;支持参数;不可执行跳过;回退 nano/全无 nil。
	// "存在的可执行文件"用测试进程自身的副本(t.TempDir() 不含空格):POSIX 的
	// /bin/cat、/bin/echo 在 Windows 上不存在,会让该平台假失败。
	src, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), testutil.ExeName("ed"))
	if err := os.WriteFile(exe, body, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", exe+" -x")
	if ed := editorCommand(); len(ed) != 2 || ed[0] != exe || ed[1] != "-x" {
		t.Fatalf("EDITOR 带参应解析: %v", ed)
	}
	t.Setenv("VISUAL", exe)
	t.Setenv("EDITOR", "definitely-not-a-real-editor-xyz")
	if ed := editorCommand(); len(ed) != 1 || ed[0] != exe {
		t.Fatalf("VISUAL 应优先: %v", ed)
	}
	t.Setenv("VISUAL", "definitely-not-a-real-editor-xyz")
	if ed := editorCommand(); len(ed) == 0 || ed[0] == "definitely-not-a-real-editor-xyz" {
		t.Fatalf("不可执行应跳过: %v", ed)
	}
}

// —— finishExternal 读回与临时文件清理 ——

func TestFinishExternalReadsBack(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(p, []byte("外部内容\n二行"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &Model{state: &State{Input: "原始", Cursor: 2}}
	m.finishExternal(editorDoneMsg{path: p})
	if m.state.Input != "外部内容\n二行" || m.state.Cursor != 7 {
		t.Fatalf("读回 = %q c%d", m.state.Input, m.state.Cursor)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("临时文件应清理")
	}
	if !m.state.Undo() || m.state.Input != "原始" {
		t.Fatalf("undo 应回编辑前: %q", m.state.Input)
	}
	// 编辑器运行失败:保留原输入并提示
	p2 := filepath.Join(dir, "out2.txt")
	_ = os.WriteFile(p2, []byte("x"), 0o600)
	m2 := &Model{state: &State{Input: "原样"}}
	m2.finishExternal(editorDoneMsg{path: p2, err: os.ErrClosed})
	if m2.state.Input != "原样" {
		t.Fatal("失败应保留原输入")
	}
	if m2.state.Error == "" {
		t.Fatal("失败应提示错误")
	}
}

// —— 模型层键分派:Shift+Enter / Enter 提交 / 多行命令与空白拦截 ——

func TestShiftEnterAndSubmit(t *testing.T) {
	var sent []string
	m := &Model{state: &State{}}
	m.onSubmit = func(s string) { sent = append(sent, s) }
	m.onCommand = func(s string) error { sent = append(sent, "CMD:"+s); return nil }

	// 输入 hello 后 Shift+Enter:换行不提交
	for _, r := range "hello" {
		m.state.InsertRune(r)
	}
	if _, _ = m.Update(keyPress(tea.KeyEnter, tea.ModShift)); m.state.Input != "hello\n" {
		t.Fatalf("Shift+Enter 应插入换行: %q", m.state.Input)
	}
	if len(sent) != 0 {
		t.Fatal("Shift+Enter 不应提交")
	}
	// Shift+Enter 再补第二行,普通 Enter 提交整段
	for _, r := range "world" {
		m.state.InsertRune(r)
	}
	m.state.Cursor = len([]rune(m.state.Input))
	if _, _ = m.Update(keyPress(tea.KeyEnter, 0)); len(sent) != 1 || sent[0] != "hello\nworld" {
		t.Fatalf("Enter 应提交多行: %v", sent)
	}
	// 多行 / 命令拒绝(保留现场)
	m.state.Input = "/model\nx"
	m.state.Cursor = 7
	if _, _ = m.Update(keyPress(tea.KeyEnter, 0)); len(sent) != 1 {
		t.Fatal("多行命令不应执行")
	}
	if m.state.Input != "/model\nx" {
		t.Fatalf("多行命令应保留现场: %q", m.state.Input)
	}
	last := m.state.Lines[len(m.state.Lines)-1]
	if last.Kind != "meta" || !strings.Contains(last.Text, "不支持多行") {
		t.Fatalf("应提示命令不支持多行: %+v", last)
	}
	// 单行命令正常分发
	m.state.Input = "/help"
	if _, _ = m.Update(keyPress(tea.KeyEnter, 0)); len(sent) != 2 || sent[1] != "CMD:/help" {
		t.Fatalf("单行命令应分发: %v", sent)
	}
	// 纯空白(仅换行)不发起回合
	m.state.Input = "\n\n"
	if _, _ = m.Update(keyPress(tea.KeyEnter, 0)); len(sent) != 2 {
		t.Fatal("纯空白不应提交")
	}
}
