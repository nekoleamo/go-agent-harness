// 超长输入窗口单测(对齐 web 超长输入修复):inputPhys 折行/光标换算 +
// renderInputLine 窗口封顶滚动(长行折行不再横向截断、多行窗口跟随光标、省略指示)。
package tui

import (
	"fmt"
	"strings"
	"testing"
)

// —— inputPhys:折行与光标物理行/列换算 ——

func TestInputPhys(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		cursor int
		w      int
		phys   []string
		curP   int
		curC   int
	}{
		{"空输入", "", 0, 10, []string{""}, 0, 0},
		{"单行行尾", "hello", 5, 80, []string{"hello"}, 0, 5},
		{"两行光标首行", "ab\ncd", 2, 80, []string{"ab", "cd"}, 0, 2},
		{"两行光标第二行行首", "ab\ncd", 3, 80, []string{"ab", "cd"}, 1, 0},
		{"两行光标第二行行内", "ab\ncd", 4, 80, []string{"ab", "cd"}, 1, 1},
		{"尾随换行行尾", "a\n", 1, 10, []string{"a", ""}, 0, 1},
		{"尾随换行末尾空行", "a\n", 2, 10, []string{"a", ""}, 1, 0},
		{"长行折行光标末", "0123456789", 10, 4, []string{"0123", "4567", "89"}, 2, 2},
		{"长行折行段尾", "0123456789", 4, 4, []string{"0123", "4567", "89"}, 0, 4},
		{"长行折行段内", "0123456789", 6, 4, []string{"0123", "4567", "89"}, 1, 2},
		{"双宽不跨行", "中文中文", 4, 6, []string{"中文中", "文"}, 1, 1},
		{"双宽光标段内", "中文中文", 2, 6, []string{"中文中", "文"}, 0, 2},
		{"长行后多行混合", "abcd\nefghij", 7, 4, []string{"abcd", "efgh", "ij"}, 1, 2},
	}
	for _, c := range cases {
		phys, curP, curC := inputPhys(c.text, c.cursor, c.w)
		if len(phys) != len(c.phys) {
			t.Fatalf("%s: 物理行数 = %d,期望 %d(%v)", c.name, len(phys), len(c.phys), c.phys)
		}
		for i := range c.phys {
			if phys[i] != c.phys[i] {
				t.Fatalf("%s: 物理行 %d = %q,期望 %q", c.name, i, phys[i], c.phys[i])
			}
		}
		if curP != c.curP || curC != c.curC {
			t.Fatalf("%s: 光标物理行/列 = (%d,%d),期望 (%d,%d)", c.name, curP, curC, c.curP, c.curC)
		}
	}
}

// —— inputMaxRows:窗口上限随终端高度 ——

func TestInputMaxRows(t *testing.T) {
	if r := inputMaxRows(24); r != 5 {
		t.Fatalf("height 24 → %d,期望 5", r)
	}
	if r := inputMaxRows(40); r != 10 {
		t.Fatalf("height 40 → %d,期望 10", r)
	}
	if r := inputMaxRows(14); r != 3 {
		t.Fatalf("矮终端 height 14 → %d,期望最少 3", r)
	}
}

// —— renderInputLine 窗口化:封顶滚动 + 光标跟随 + 省略指示 ——

func TestRenderInputWindow(t *testing.T) {
	// 20 行长输入,光标在第 10 行(line9)行尾 → 窗口 3 行(line7-9)+ 1 指示行
	lines := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	in := strings.Join(lines, "\n")
	cursor := 0
	for i := 0; i < 10; i++ {
		cursor += len(lines[i]) + 1
	}
	cursor-- // 减回换行符前 = line9 行尾(窗口包含光标行 line9 而非下一行)
	s := &State{Input: in, Cursor: cursor}
	out := stripColor(renderInputLine(s, 80, 3))
	if nl := strings.Count(out, "\n") + 1; nl != 4 {
		t.Fatalf("窗口渲染应 4 行(3 窗口 + 1 指示),实际 %d:\n%s", nl, out)
	}
	if !strings.Contains(out, "…↑") {
		t.Fatalf("应含省略指示: %q", out)
	}
	if !strings.Contains(out, "█") {
		t.Fatalf("光标块应可见: %q", out)
	}
	if !strings.Contains(out, "line9") {
		t.Fatalf("光标所在行应可见: %q", out)
	}
	if strings.Contains(out, "line0") {
		t.Fatalf("窗口不应含最顶行(已滚动出): %q", out)
	}
	// 无 maxRows(旧签名兼容):全量渲染,无指示
	s2 := &State{Input: in, Cursor: cursor}
	if out2 := stripColor(renderInputLine(s2, 80)); strings.Contains(out2, "…↑") || strings.Count(out2, "\n")+1 != 20 {
		t.Fatalf("全量渲染应 20 行无指示: %q", out2)
	}
	// 光标触底(末行):窗口滚到底,末行可见
	s3 := &State{Input: in, Cursor: len([]rune(in))}
	out3 := stripColor(renderInputLine(s3, 80, 3))
	if !strings.Contains(out3, "line19") || !strings.Contains(out3, "…↑") {
		t.Fatalf("触底应含末行与指示: %q", out3)
	}
	if !strings.Contains(out3, "line17") {
		t.Fatalf("触底窗口(17-19)应含 line17: %q", out3)
	}
	// 短输入(≤窗口上限):全量,无指示,续行缩进不变
	s4 := &State{Input: "a\nb", Cursor: 1}
	if out4 := stripColor(renderInputLine(s4, 80, 3)); strings.Contains(out4, "…↑") || !strings.Contains(out4, "❯ a█\n  b") {
		t.Fatalf("短输入全量渲染: %q", out4)
	}
	// 超宽长单行折行:文本宽 74,80 字符一行折为 2 物理行(不再横向截断),光标可见
	long := strings.Repeat("x", 80)
	s5 := &State{Input: long, Cursor: 60}
	out5 := stripColor(renderInputLine(s5, 80))
	if strings.Count(out5, "\n")+1 != 2 || !strings.Contains(out5, "█") || strings.Contains(out5, "…↑") {
		t.Fatalf("80 字符长行应折为 2 物理行,光标可见且无指示: %q", out5)
	}
}

// TestInputFrameDemo 圆角输入框效果示例(go test -v 展示字符画,顺带断言框符号存在)。
func TestInputFrameDemo(t *testing.T) {
	s := &State{Input: "输入一些文字,演示圆角输入框效果", Cursor: 5, Thinking: "medium"}
	box := renderInputFrame(renderInputLine(s, 80), 80, s.Thinking)
	clean := stripColor(box)
	for _, tok := range []string{"╭", "╮", "╰", "╯", "│", "❯"} {
		if !strings.Contains(clean, tok) {
			t.Fatalf("圆角框应含 %q: %q", tok, clean)
		}
	}
	t.Logf("输入框圆角矩形效果(medium 边框):\n%s", clean)
	// 整屏效果(分隔线/输入框/状态栏/指标行),-v 展示
	st := &State{Input: "演示输入内容", Cursor: 7, Thinking: "high"}
	st.Lines = []Line{{Kind: "user", Text: "你好,这是会话内容"}}
	t.Logf("整屏效果(high 边框):\n%s", stripColor(Render(st, 80, 24)))
}
