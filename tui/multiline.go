// P4-6 多行输入(P2b):Shift+Enter 换行、↑/↓ 行间移动(列意图记忆)、
// 外部编辑器回填(ApplyExternal,编辑器命令见 editor.go)。纯逻辑,可脱离终端单测。
package tui

// lineSeg 输入的一行:[start, end) rune 下标(不含行尾 \n;末行到文本尾;空行 start==end)。
type lineSeg struct{ start, end int }

// inputPhys 输入文本折行成物理行并换算光标位置(超长输入滚动窗口用)。
// 折行复用 wrapSegment(双宽字符不跨行拆分,长行不再横向截断);空输入 = 单空物理行。
// 返回 phys(全部物理行)、curPhys(光标所在物理行下标)、curCol(该行内 rune 列)。
// 光标视为字符间位置:行尾(换行前)归当前行末,末行文本尾归末行末。
func inputPhys(text string, cursor, w int) (phys []string, curPhys, curCol int) {
	if w < 1 {
		w = 1
	}
	rs := []rune(text)
	n := len(rs)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > n {
		cursor = n
	}
	start := 0
	for i := 0; i <= n; i++ {
		isNL := i < n && rs[i] == '\n'
		if i < n && !isNL {
			continue
		}
		seg := string(rs[start:i]) // 逻辑行 [start,i)(不含 \n;末尾空行 start==i)
		sub := wrapSegment(seg, w)
		if cursor >= start && cursor <= i { // 光标落本逻辑行(含行尾/末行文本尾)
			pre := wrapSegment(string(rs[start:cursor]), w) // 光标前缀分段=全量折行的前段划分(前缀式贪心)
			curPhys = len(phys) + len(pre) - 1
			curCol = len([]rune(pre[len(pre)-1]))
		}
		phys = append(phys, sub...)
		start = i + 1
	}
	if len(phys) == 0 {
		phys = []string{""}
		curPhys, curCol = 0, 0
	}
	return phys, curPhys, curCol
}

// segLines 把输入 runes 按 \n 切分成行区间(尾随 \n 产生末尾空行段;空串 = 单空行)。
// 例:""→[{0,0}]、"a\nb"→[{0,1},{2,3}]、"a\n"→[{0,1},{2,2}]。
func segLines(rs []rune) []lineSeg {
	segs := make([]lineSeg, 0, 4)
	start := 0
	for i, r := range rs {
		if r == '\n' {
			segs = append(segs, lineSeg{start, i})
			start = i + 1
		}
	}
	segs = append(segs, lineSeg{start, len(rs)})
	return segs
}

// cursorLineCol 输入 runes 中 cursor 所在的 (行号, 行内列)。
// cursor 视为字符间位置:等于某行 start(cursor 落在该行首字符前)归该行;
// 指向 \n 时归上一行尾(光标在换行前,属当前行末)。
func cursorLineCol(rs []rune, cursor int) (line, col int) {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(rs) {
		cursor = len(rs)
	}
	start := 0
	for i := 0; i < cursor; i++ {
		if rs[i] == '\n' {
			line++
			start = i + 1
		}
	}
	return line, cursor - start
}

// InsertNewline Shift+Enter:光标处插入换行(输入进入多行;undo 一步)。
func (s *State) InsertNewline() {
	s.snapshotUndo('n')
	b := []rune(s.Input)
	out := make([]rune, 0, len(b)+1)
	out = append(out, b[:s.Cursor]...)
	out = append(out, '\n')
	out = append(out, b[s.Cursor:]...)
	s.Input = string(out)
	s.Cursor++
	s.vActive = false
}

// LineUp ↑:多行内上移一行(列意图记忆;首行再上 = 段首)。
// 单行输入退化为光标回头(CursorHome)——与 M6.17 "↑/↓ = 输入行首/尾" 一致。
func (s *State) LineUp() { s.lineMove(-1) }

// LineDown ↓:多行内下移一行(列意图记忆;末行再下 = 段尾)。
// 单行输入退化为光标回尾(CursorEnd)。
func (s *State) LineDown() { s.lineMove(1) }

// lineMove 垂直移动实现(dir=-1 上/1 下)。
// 意图列 vCol:首次垂直移动捕捉当前列,之后沿行间移动保持该列(短行钳到行尾),
// 直到线性编辑失效(见 state.go 各编辑函数 s.vActive = false)。
func (s *State) lineMove(dir int) {
	rs := []rune(s.Input)
	n := len(rs)
	if s.Cursor < 0 {
		s.Cursor = 0
	}
	if s.Cursor > n {
		s.Cursor = n
	}
	segs := segLines(rs)
	if len(segs) <= 1 {
		// 单行:退化首/尾(保持既有 ↑=Home/↓=End 语义)
		if dir < 0 {
			s.Cursor = 0
		} else {
			s.Cursor = n
		}
		s.vActive = false
		return
	}
	line, start := 0, 0
	for i := 0; i < s.Cursor; i++ {
		if rs[i] == '\n' {
			line++
			start = i + 1
		}
	}
	col := s.Cursor - start
	desired := col
	if s.vActive {
		desired = s.vCol // 沿意图列继续行间移动
	} else {
		s.vCol = col // 首次垂直移动:捕捉当前列为意图列
		s.vActive = true
	}
	tgt := line + dir
	switch {
	case tgt < 0:
		s.Cursor = 0 // 首行再上:段首
		s.vActive = false
		return
	case tgt >= len(segs):
		s.Cursor = n // 末行再下:段尾
		s.vActive = false
		return
	}
	ts, te := segs[tgt].start, segs[tgt].end
	c := desired
	if c > te-ts {
		c = te - ts // 短行钳到行尾(不越过换行)
	}
	s.Cursor = ts + c
	// 意图列保留(vCol/vActive):同一意图列继续在行间移动
}

// ApplyExternal 外部编辑器整段回填:内容与当前输入相同则不动作;
// 否则以编辑前文本为一步 undo,光标到整段尾,redo 清空(编辑器结果不可重做撤销)。
func (s *State) ApplyExternal(text string) {
	if text == s.Input {
		return
	}
	s.snapshotUndo('e')
	s.Input = text
	s.Cursor = len([]rune(text))
	s.vActive = false
	s.redo = nil
	s.PickDismissed = true
}
