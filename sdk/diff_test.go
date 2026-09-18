// sdk/diff.go 单测(S-P1-1):unified diff 的边界、行号、上下文合并、预算与二进制判定。
// 纯函数断言,用例覆盖工具捕获与命令呈现两条真实调用路径关心的性质。
package sdk

import (
	"fmt"
	"strings"
	"testing"
)

// hunks 取出 hunk 头行(便于断言行号/行数)。
func hunks(diff string) []string {
	var out []string
	for _, l := range strings.Split(diff, "\n") {
		if strings.HasPrefix(l, "@@") {
			out = append(out, l)
		}
	}
	return out
}

// body 取出 hunk 正文行(去掉头行)。
func bodyLines(diff string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSuffix(diff, "\n"), "\n") {
		if l == "" || strings.HasPrefix(l, "@@") {
			continue
		}
		out = append(out, l)
	}
	return out
}

func TestUnifiedDiffIdentical(t *testing.T) {
	r := UnifiedDiff("a\nb\n", "a\nb\n", -1)
	if r.Diff != "" || r.Added != 0 || r.Removed != 0 {
		t.Fatalf("相同内容应为空 diff: %+v", r)
	}
}

func TestUnifiedDiffSingleLineChange(t *testing.T) {
	old := "l1\nl2\nl3\nl4\nl5\nl6\nl7\n"
	nw := "l1\nl2\nl3\nCHANGED\nl5\nl6\nl7\n"
	r := UnifiedDiff(old, nw, -1)
	if r.Added != 1 || r.Removed != 1 {
		t.Fatalf("+/− 计数: %+v", r)
	}
	h := hunks(r.Diff)
	if len(h) != 1 || h[0] != "@@ -1,7 +1,7 @@" {
		t.Fatalf("单 hunk 头: %v\n%q", h, r.Diff)
	}
	lines := bodyLines(r.Diff)
	if lines[3] != "-l4" || lines[4] != "+CHANGED" {
		t.Fatalf("变更行应相邻且带 +/-: %v", lines)
	}
	// 上下文:共 7 行(3 上 + 变更 + 3 下)
	if len(lines) != 8 {
		t.Fatalf("上下文行数应为 6+2: %v", lines)
	}
}

func TestUnifiedDiffLineNumbersWithPrefixAndSuffix(t *testing.T) {
	old := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\n"
	nw := "a\nb\nc\nd\ne\nf\nX\nh\ni\nj\nk\n"
	r := UnifiedDiff(old, nw, 1)
	if r.Added != 1 || r.Removed != 1 {
		t.Fatalf("+/−: %+v", r)
	}
	h := hunks(r.Diff)
	if len(h) != 1 || h[0] != "@@ -6,3 +6,3 @@" {
		t.Fatalf("行号应含剥离前缀的偏移: %v\n%q", h, r.Diff)
	}
}

func TestUnifiedDiffTwoHunksWhenFarApart(t *testing.T) {
	var a, b []string
	for i := 0; i < 40; i++ {
		a = append(a, "line"+string(rune('a'+i%26))+itoa(i))
	}
	b = append(b, a...)
	old := strings.Join(a, "\n") + "\n"
	b[2] = "TOP"
	b[35] = "BOTTOM"
	nw := strings.Join(b, "\n") + "\n"
	r := UnifiedDiff(old, nw, 3)
	if r.Added != 2 || r.Removed != 2 {
		t.Fatalf("两处改动: %+v", r)
	}
	if h := hunks(r.Diff); len(h) != 2 {
		t.Fatalf("相距远的改动应分成 2 个 hunk: %v\n%s", h, r.Diff)
	}
}

func TestUnifiedDiffNearbyChangesMergeIntoOneHunk(t *testing.T) {
	var a []string
	for i := 0; i < 20; i++ {
		a = append(a, "l"+itoa(i))
	}
	b := append([]string{}, a...)
	b[8] = "X"
	b[11] = "Y" // 间隔 3 行(<= 2*ctx)→ 合并
	old := strings.Join(a, "\n") + "\n"
	nw := strings.Join(b, "\n") + "\n"
	r := UnifiedDiff(old, nw, 3)
	if h := hunks(r.Diff); len(h) != 1 {
		t.Fatalf("邻近改动应合并为 1 个 hunk: %v", h)
	}
}

func TestUnifiedDiffPureInsertAndDelete(t *testing.T) {
	ins := UnifiedDiff("a\nb\n", "a\nnew\nb\n", -1)
	if ins.Added != 1 || ins.Removed != 0 {
		t.Fatalf("纯插入: %+v", ins)
	}
	if ls := bodyLines(ins.Diff); ls[1] != "+new" {
		t.Fatalf("纯插入正文: %v", ls)
	}
	del := UnifiedDiff("a\nold\nb\n", "a\nb\n", -1)
	if del.Added != 0 || del.Removed != 1 {
		t.Fatalf("纯删除: %+v", del)
	}
}

func TestUnifiedDiffNewFile(t *testing.T) {
	r := UnifiedDiff("", "x\ny\n", -1)
	if r.Added != 2 || r.Removed != 0 {
		t.Fatalf("新建文件全为新增: %+v", r)
	}
	if h := hunks(r.Diff); len(h) != 1 || h[0] != "@@ -1,0 +1,2 @@" {
		t.Fatalf("空文件起始行号: %v\n%q", h, r.Diff)
	}
}

func TestUnifiedDiffEmptyResultForEmptyDelete(t *testing.T) {
	if r := UnifiedDiff("x\n", "", -1); r.Added != 0 || r.Removed != 1 {
		t.Fatalf("清空文件: %+v", r)
	}
	if r := UnifiedDiff("", "", -1); r.Diff != "" {
		t.Fatalf("空→空应无 diff: %+v", r)
	}
}

func TestUnifiedDiffCRLFAndMissingTrailingNewline(t *testing.T) {
	r := UnifiedDiff("a\r\nb\r\n", "a\nb\nc", -1)
	if r.Added != 1 || r.Removed != 0 {
		t.Fatalf("CRLF 归一 + 无尾换行: %+v", r)
	}
	if strings.Contains(r.Diff, "\r") {
		t.Fatalf("diff 不应含 CR: %q", r.Diff)
	}
}

func TestUnifiedDiffCoarseOnHugeMiddle(t *testing.T) {
	// 构造超过 diffMaxCells 的中间段:两侧各 3000 行的全新内容
	var oldL, newL []string
	for i := 0; i < 3000; i++ {
		oldL = append(oldL, "old"+itoa(i))
		newL = append(newL, "new"+itoa(i))
	}
	r := UnifiedDiff(strings.Join(oldL, "\n"), strings.Join(newL, "\n"), 0)
	if !r.Coarse {
		t.Fatalf("超规模应标记 Coarse: %+v", r)
	}
	if r.Added != 3000 || r.Removed != 3000 {
		t.Fatalf("粗粒度仍必须给出真实增删计数: +%d -%d", r.Added, r.Removed)
	}
}

func TestUnifiedDiffNoContext(t *testing.T) {
	r := UnifiedDiff("a\nb\nc\nd\n", "a\nb\nX\nd\n", 0)
	if ls := bodyLines(r.Diff); len(ls) != 2 {
		t.Fatalf("ctx=0 只输出变更行: %v", ls)
	}
}

func TestTruncateDiff(t *testing.T) {
	long := strings.Repeat("+line\n", 100)
	out, cut := TruncateDiff(long, 40)
	if !cut {
		t.Fatal("应报告截断")
	}
	if len(out) > 40+len("\n……(diff 超预算已截断)\n") {
		t.Fatalf("截断后超预算: %d", len(out))
	}
	if !strings.Contains(out, "已截断") {
		t.Fatalf("截断必须显式提示: %q", out)
	}
	if s, cut := TruncateDiff("short", 100); cut || s != "short" {
		t.Fatalf("未超预算不应改动: %q %v", s, cut)
	}
	if _, cut := TruncateDiff(long, 0); cut {
		t.Fatal("预算 <= 0 视为不限制")
	}
}

func TestIsBinaryText(t *testing.T) {
	if IsBinaryText("hello\nworld\n") {
		t.Fatal("纯文本不应判为二进制")
	}
	if !IsBinaryText("PNG\x00\x01\x02") {
		t.Fatal("含 NUL 应判为二进制")
	}
	// 头部窗口(8000 字节)之外的 NUL 不检测:为此读全文件不值当(工具侧另有大小上限)
	if IsBinaryText(strings.Repeat("a", 9000) + "\x00") {
		t.Fatal("只扫头部窗口:NUL 远在窗口外应视为文本")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestBuildFileChange 事件构造口径(内嵌与外部进程两条记录路径共用本函数)。
func TestBuildFileChange(t *testing.T) {
	// 新建文本文件:行计数 + 可读 patch + created
	ev := BuildFileChange("/ws/a.txt", "a.txt", "write", "file_write", true, "", "one\ntwo\n")
	if ev.Path != "/ws/a.txt" || ev.Rel != "a.txt" || ev.Op != "write" || ev.Tool != "file_write" {
		t.Fatalf("基本字段不符: %+v", ev)
	}
	if !ev.Created || ev.Added != 2 || ev.Removed != 0 || ev.Bytes != len("one\ntwo\n") {
		t.Fatalf("新建统计不符: %+v", ev)
	}
	if !strings.Contains(ev.Diff, "+one") || ev.Binary || ev.Truncated {
		t.Fatalf("patch 不符: %+v", ev)
	}
	// 编辑:增删行都记
	ev = BuildFileChange("/ws/a.txt", "a.txt", "edit", "file_edit", false, "one\ntwo\n", "one\nTWO\n")
	if ev.Created || ev.Added != 1 || ev.Removed != 1 {
		t.Fatalf("编辑统计不符: %+v", ev)
	}
	// 二进制:不做行计数,不给 patch(避免拿乱码行数当信号)
	ev = BuildFileChange("/ws/b.bin", "b.bin", "write", "file_write", true, "", "a\x00b")
	if !ev.Binary || ev.Added != 0 || ev.Removed != 0 || ev.Diff != "" {
		t.Fatalf("二进制口径不符: %+v", ev)
	}
	// 内容相同:无增删、无 patch(调用方据此跳过记账)
	ev = BuildFileChange("/ws/a.txt", "a.txt", "write", "file_write", false, "same\n", "same\n")
	if ev.Added != 0 || ev.Removed != 0 || ev.Diff != "" {
		t.Fatalf("内容相同不应有差异: %+v", ev)
	}
	// 超大 diff:按预算截断并显式标记(逐行都不同 → 细粒度 diff 超 32 KiB)
	var b1, b2 strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b1, "old-%03d-%s\n", i, strings.Repeat("x", 40))
		fmt.Fprintf(&b2, "new-%03d-%s\n", i, strings.Repeat("y", 40))
	}
	big1, big2 := b1.String(), b2.String()
	ev = BuildFileChange("/ws/big.txt", "big.txt", "write", "file_write", false, big1, big2)
	if !ev.Truncated || len(ev.Diff) > FileChangeMaxDiffBytes {
		t.Fatalf("超预算应截断并标记: truncated=%v len=%d", ev.Truncated, len(ev.Diff))
	}
	if ev.Added == 0 || ev.Removed == 0 {
		t.Fatalf("截断不影响真实计数: %+v", ev)
	}
}
