// A-1 条目 71:TUI `/preview` pager **全键位**的 pty 真机验收(pager 全屏模态 + 真文件 + 真按键)。
//
// 口径(不假勾):
//   - 键位行为(**本文件覆盖**):↑/↓ 单行、PgUp/PgDn 整页、Home/End、g/G 首尾、←/→ 横移、
//     滚轮、`/` 搜索 + Enter 应用 + n/N 跳转、q/Esc 关闭,以及关闭后的输入框焦点回归;
//   - 宽表对齐(**单元层覆盖**):`tui/docview_test.go` 的 `TestAsciiTableCJKAndBudget`
//     (列宽预算/CJK 宽度/超宽截断);**观感**(配色、列间距)仍属人眼 → 归 A-1a;
//   - 大小写键位(**单元层 + 本文件双覆盖**):`TestDocPagerCaseKeysRealShape` + 本文件的
//     `G`/`g`/`n`/`N` 真按键(第 41 批实测过 Code 恒小写导致的 G→g 错判);
//   - 断言读**屏幕模型**(TUI 差分重绘:raw 的"最后一次出现"可能是上一帧,实测 PgDn 后
//     raw 只到 21/52 而屏幕已是 41/52);偏移量读状态行 `x/y 行`,取最后一个(屏幕底部)。
package tests

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const previewTitle = "acc-prevew.md"

// pagerStatRE 取状态行 `x/y 行`(Render 里 `%d/%d 行 · q/Esc 关闭 …`)。
// 容错 `1//52`:屏幕模型对"只重写变化单元格"的差分帧偶尔会多画一个 `/`(真实终端不会)。
var pagerStatRE = regexp.MustCompile(`(\d+)/+(\d+) 行`)

// pagerStat 从**屏幕**读 pager 的 `x/y 行`(最后一个 = 底部状态行 = 当前态)。
func pagerStat(t *testing.T, s *tuiSess) (off, total int) {
	t.Helper()
	m := pagerStatRE.FindAllStringSubmatch(stripANSI(s.screen()), -1)
	if len(m) == 0 {
		t.Fatalf("条目 71:pager 状态行未出现 `x/y 行`;屏尾 %q", tailS(stripANSI(s.screen()), 200))
	}
	last := m[len(m)-1]
	off, _ = strconv.Atoi(last[1])
	total, _ = strconv.Atoi(last[2])
	return off, total
}

// waitPagerOff 等状态行偏移到期望值(按键 → 重绘有延迟,不能立即断言)。
func waitPagerOff(t *testing.T, s *tuiSess, want int, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if off, _ := pagerStat(t, s); off == want {
			return
		}
		time.Sleep(80 * time.Millisecond)
	}
	off, total := pagerStat(t, s)
	t.Errorf("条目 71:%s:偏移应为 %d,实际 %d/%d", what, want, off, total)
}

// previewCorpus 造语料:宽表(3 列含 CJK)+ 足量长文(可滚)+ 两处搜索词 + 一行超宽(横移用)。
func previewCorpus() string {
	var b strings.Builder
	b.WriteString("# 预览条目 71\n\n")
	b.WriteString("| 序号 | 名称 | 说明文字 |\n")
	b.WriteString("| --- | --- | --- |\n")
	for i := 1; i <= 4; i++ {
		b.WriteString("| " + strconv.Itoa(i) + " | 苹果" + strconv.Itoa(i) + " | 这是一段用于验证列宽与对齐的中文说明 |\n")
	}
	b.WriteString("\n")
	// 超宽行:标记落在第 88 列(窗口 80 列,须横移 ≥ 20 列才可见)
	b.WriteString(strings.Repeat("x", 88) + "HSCROLLEND\n\n")
	for i := 1; i <= 30; i++ {
		b.WriteString("第 " + strconv.Itoa(i) + " 行正文,用于制造可滚动的长文档。\n")
	}
	b.WriteString("\n搜索命中甲\n")
	for i := 1; i <= 12; i++ {
		b.WriteString("填充 " + strconv.Itoa(i) + "\n")
	}
	b.WriteString("\n搜索命中乙\n")
	return b.String()
}

// submitCmd 提交一条命令:命令参数带补全候选时,第一次回车只"应用候选",故补一次回车
// (第二次落在已打开 pager 上时 Enter 无副作用:HandleKey 对 Enter 仅在搜索输入态有意义)。
func submitCmd(s *tuiSess, text string) {
	s.send(text)
	time.Sleep(400 * time.Millisecond)
	s.send("\r")
	time.Sleep(400 * time.Millisecond)
	s.send("\r")
}

// TestTUIAcceptPreviewPager 条目 71:全键位 + 搜索 + 横移 + 关闭后焦点回归。
func TestTUIAcceptPreviewPager(t *testing.T) {
	bin, env, _, cwd := tuiAcceptSetup(t)
	writeAcceptFile(t, cwd, previewTitle, previewCorpus())
	s := newTuiSessIn(t, bin, env, cwd, "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}

	// 打开(第 41 批真缺陷 #18 回归点:此前 `/preview <文件>` 会让 TUI 假死 ——
	// 命令 Run 在 UI 循环内 Emit(doc/open) → 订阅回调 program.Send 自阻塞)
	submitCmd(s, "/preview "+previewTitle)
	if !s.waitScreen("q/Esc 关闭", 10*time.Second) {
		t.Fatalf("条目 71:`/preview %s` 未打开 pager;屏=%q", previewTitle, stripANSI(s.screen()))
	}
	// 键位说明书齐(全键位的"文档面")
	plain := stripANSI(s.screen())
	for _, sub := range []string{"q/Esc 关闭", "↑↓/PgUp/PgDn 滚动", "←→ 横移", "/ 搜索"} {
		if !strings.Contains(plain, sub) {
			t.Errorf("条目 71:状态行缺键位提示 %q", sub)
		}
	}
	// 宽表内容进了 pager(对齐与截断的量化断言在 tui 单元层)
	if !s.waitScreen("苹果", 5*time.Second) {
		t.Errorf("条目 71:宽表内容未出现在 pager 屏幕")
	}
	if !strings.Contains(stripANSI(s.screen()), previewTitle) {
		t.Errorf("条目 71:标题行未显示文件名 %q", previewTitle)
	}

	off0, total := pagerStat(t, s)
	if off0 != 1 {
		t.Errorf("条目 71:打开时偏移应为 1,实际 %d", off0)
	}
	if total < 30 {
		t.Fatalf("条目 71:语料仅 %d 行,滚动类断言无意义", total)
	}

	// —— 单行 ↑/↓ ——
	s.send("\x1b[B")
	waitPagerOff(t, s, 2, "↓ 单行下滚")
	s.send("\x1b[A")
	waitPagerOff(t, s, 1, "↑ 单行上滚")

	// —— 整页 PgDn/PgUp:窗口 80x24 → page = h-4 = 20(确定性)——
	s.send("\x1b[6~")
	waitPagerOff(t, s, 21, "PgDn 整页")
	s.send("\x1b[6~")
	waitPagerOff(t, s, 41, "PgDn 第二页")
	s.send("\x1b[5~")
	waitPagerOff(t, s, 21, "PgUp 回一页")

	// —— 首尾:End/G → 末行;Home/g → 首行(真缺陷 #19:G 曾被当 g)——
	s.send("\x1b[F")
	waitPagerOff(t, s, total, "End 到末行")
	s.send("\x1b[H")
	waitPagerOff(t, s, 1, "Home 回首行")
	s.send("G")
	waitPagerOff(t, s, total, "G 到末行")
	s.send("g")
	waitPagerOff(t, s, 1, "g 回首行")

	// —— 滚轮:SGR 65=下滚(off+3)、64=上滚(off-3)——
	s.send("\x1b[<65;10;10M")
	waitPagerOff(t, s, 4, "滚轮下 +3")
	s.send("\x1b[<64;10;10M")
	waitPagerOff(t, s, 1, "滚轮上 -3")

	// —— 搜索:/ 输入 → Enter 应用 → 跳首个命中;n/N 跳转 ——
	s.send("/命中")
	if !s.waitScreen("搜索: 命中", 5*time.Second) {
		t.Errorf("条目 71:搜索输入态未显示 `搜索: <词>`;屏尾 %q", tailS(stripANSI(s.screen()), 200))
	}
	s.send("\r")
	if !s.waitScreen("2 命中", 5*time.Second) {
		t.Fatalf("条目 71:搜索未报命中数(`2 命中`);屏尾 %q", tailS(stripANSI(s.screen()), 200))
	}
	hit1, _ := pagerStat(t, s)
	if hit1 <= 1 {
		t.Errorf("条目 71:搜索应跳到首个命中行(>1),实际 %d", hit1)
	}
	s.send("n")
	deadline := time.Now().Add(5 * time.Second)
	var hit2 int
	for time.Now().Before(deadline) {
		if hit2, _ = pagerStat(t, s); hit2 != hit1 {
			break
		}
		time.Sleep(80 * time.Millisecond)
	}
	if hit2 <= hit1 {
		t.Errorf("条目 71:`n` 未跳到下一个命中(甲=%d,乙=%d)", hit1, hit2)
	}
	s.send("N")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if off, _ := pagerStat(t, s); off == hit1 {
			break
		}
		time.Sleep(80 * time.Millisecond)
	}
	if off, _ := pagerStat(t, s); off != hit1 {
		t.Errorf("条目 71:`N` 未回到上一个命中(应 %d,实际 %d)", hit1, off)
	}

	// —— 横移:先回顶(超宽行在文首,搜索后窗口已滚到文档中部)——
	s.send("g")
	waitPagerOff(t, s, 1, "横移前回顶")
	if !s.waitScreen("苹果", 5*time.Second) {
		t.Fatalf("条目 71:回顶后宽表未回到窗口,横移断言无法进行")
	}
	// 超宽行末尾标记在第 88 列,窗口 80 列 → 初始不可见,→ 累积 20 列后可见
	if strings.Contains(s.screen(), "HSCROLLEND") {
		t.Errorf("条目 71:超宽行标记初始就可见(横移断言失效,请调语料列宽)")
	}
	for i := 0; i < 5; i++ {
		s.send("\x1b[C")
		time.Sleep(150 * time.Millisecond)
	}
	// "渲染出来过"用 raw(累积流);差分帧下屏幕模型对局部重绘不可靠
	if !s.waitRaw("HSCROLLEND", 5*time.Second) {
		t.Errorf("条目 71:→ 横移 20 列后仍未渲染出行尾标记(横移未生效?)")
	}
	for i := 0; i < 5; i++ {
		s.send("\x1b[D")
		time.Sleep(150 * time.Millisecond)
	}
	// 强制一次整帧重绘(↑↓ 让窗口内容变化)后再看屏幕:标记应已回到窗口外
	s.send("\x1b[B")
	time.Sleep(200 * time.Millisecond)
	s.send("\x1b[A")
	if !s.waitScreenGone("HSCROLLEND", 5*time.Second) {
		t.Errorf("条目 71:← 横移回 0 列后标记仍可见")
	}

	// —— 关闭:Esc 关掉(焦点回输入框,键入进输入区)——
	s.send("\x1b")
	time.Sleep(600 * time.Millisecond)
	s.send("zzz")
	if !s.waitScreen("zzz", 3*time.Second) {
		t.Errorf("条目 71:Esc 关闭后输入框未拿到焦点(input=%q)", s.inputLine())
	}
	s.send("\x15") // Ctrl+U 清掉输入
	time.Sleep(300 * time.Millisecond)

	// —— 再开一次用 q 关闭(两条关闭键都要可用)——
	submitCmd(s, "/preview "+previewTitle)
	if !s.waitScreen("q/Esc 关闭", 10*time.Second) {
		t.Fatalf("条目 71:第二次打开失败")
	}
	s.send("q")
	if !s.waitScreenGone("q/Esc 关闭", 5*time.Second) {
		t.Errorf("条目 71:q 关闭后状态行仍在屏幕上(未退出 pager)")
	}
	s.send("yyy")
	if !s.waitScreen("yyy", 3*time.Second) {
		t.Errorf("条目 71:q 关闭后输入框未拿到焦点(input=%q)", s.inputLine())
	}
	_ = os.WriteFile("/tmp/preview-accept-raw.txt", []byte(s.rawText()), 0o644)
}
