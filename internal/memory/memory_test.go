package memory

// 记忆内核的行为契约(第一百零九批)。
//
// 这里钉的每一条都是"记忆错了会长期害人"的那个方向:
// 解析宽松(人写的文件不该因格式小问题整条丢)、去重(别把同一句记两遍)、
// 来源可追溯(治理的前提)、注入按预算**新的优先**、以及注入块**自带边界声明**。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func p(t *testing.T) string { return filepath.Join(t.TempDir(), "m.md") }

func TestAppendReadRoundTrip(t *testing.T) {
	path := p(t)
	if err := Append(path, "报告里的图表用蓝灰配色,不要渐变", ""); err != nil {
		t.Fatal(err)
	}
	es, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 1 || es[0].Content != "报告里的图表用蓝灰配色,不要渐变" {
		t.Fatalf("往返不对: %+v", es)
	}
	if es[0].Source != "" {
		t.Fatalf("手工写的应无来源,got %q", es[0].Source)
	}
}

func TestAppendRejectsEmptyAndDuplicate(t *testing.T) {
	path := p(t)
	if err := Append(path, "   ", ""); err == nil {
		t.Fatal("空内容应拒")
	}
	if err := Append(path, "记住这条", ""); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "记住这条", ""); err == nil {
		t.Fatal("同一天同一句不应重复写(用户很容易打两遍)")
	}
	if es, _ := Read(path); len(es) != 1 {
		t.Fatalf("应只有 1 条,got %d", len(es))
	}
}

func TestReadSkipsNoiseButKeepsEntries(t *testing.T) {
	path := p(t)
	// 人手写的文件:有注释、有空行、有一行普通文字
	blob := "# 我的记忆\n\n这是一行普通说明,不是记忆\n- [2026-10-01] 真正的记忆(来源: 会话 abc)\n\n"
	if err := os.WriteFile(path, []byte(blob), 0o644); err != nil {
		t.Fatal(err)
	}
	es, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 1 {
		t.Fatalf("只应解析出 1 条,got %d: %+v", len(es), es)
	}
	if es[0].Content != "真正的记忆" || es[0].Source != "abc" {
		t.Fatalf("解析不对: %+v", es[0])
	}
}

func TestRemoveAndRemoveBySource(t *testing.T) {
	path := p(t)
	_ = Append(path, "A(来源: 会话 s1)", "s1")
	_ = Append(path, "B(来源: 会话 s2)", "s2")
	_ = Append(path, "C", "")
	// 按来源整段删:只影响该来源
	n, err := RemoveBySource(path, "s1")
	if err != nil || n != 1 {
		t.Fatalf("按来源删应只删 1 条: n=%d err=%v", n, err)
	}
	es, _ := Read(path)
	if len(es) != 2 {
		t.Fatalf("应剩 2 条,got %d", len(es))
	}
	for _, e := range es {
		if e.Source == "s1" {
			t.Fatal("s1 的记忆没删掉")
		}
	}
	// 按展示序号删。三条同日 ⇒ 展示序 = 写入序倒置 [C, B, A],删 1 = 删 C。
	disp := SortedForDisplay(es)
	if disp[0].Content != "C" {
		t.Fatalf("同日三条的展示序应是新的在前(C 最后写),got %q 在前", disp[0].Content)
	}
	if _, err := Remove(path, 1); err != nil {
		t.Fatal(err)
	}
	es2, _ := Read(path)
	if len(es2) != 1 {
		t.Fatalf("删展示序第 1 条(C)后应剩 1 条(B),got %d", len(es2))
	}
	if !strings.HasPrefix(es2[0].Content, "B") {
		t.Fatalf("删展示序第 1 条(C)后剩的应是 B,got %q", es2[0].Content)
	}
	// 越界
	if _, err := Remove(path, 99); err == nil {
		t.Fatal("越界应报错")
	}
}

func TestRemoveBySourceUnknownIsNoop(t *testing.T) {
	path := p(t)
	_ = Append(path, "A", "")
	n, err := RemoveBySource(path, "不存在的会话")
	if err != nil || n != 0 {
		t.Fatalf("没有来自该来源的记忆应是 no-op: n=%d err=%v", n, err)
	}
}

// TestSortedForDisplaySameDayNewestFirst 同一天内的多条:新的必须在前(日期只到天,
// 只按日期排会让当天保持写入顺序 = 最旧在前;连带后果是预算裁剪先丢刚记的那条)。
func TestSortedForDisplaySameDayNewestFirst(t *testing.T) {
	es := []Entry{
		{Date: "2026-10-02", Content: "最先写的", Raw: "1"},
		{Date: "2026-10-02", Content: "后写的", Raw: "2"},
		{Date: "2026-10-01", Content: "昨天的", Raw: "3"},
	}
	got := SortedForDisplay(es)
	want := []string{"后写的", "最先写的", "昨天的"}
	for i, w := range want {
		if got[i].Content != w {
			t.Fatalf("展示序第 %d 条应是 %q,got %q(完整:%v)", i+1, w, got[i].Content, got)
		}
	}
}
func TestInjectBudgetKeepsNewestFirst(t *testing.T) {
	old := Entry{Date: "2026-01-01", Content: "很旧的口径"}
	recent := Entry{Date: "2026-10-01", Content: "最新口径"}
	text := Inject([]Entry{old, recent}, nil, 1024)
	if !strings.Contains(text, "最新口径") {
		t.Fatalf("应含最新口径:\n%s", text)
	}
	if !strings.Contains(text, "很旧的口径") {
		t.Fatalf("预算够时应两条都在:\n%s", text)
	}
	// 极小预算:只放新的(预算 = 壳开销 + 一条 ⇒ 放得下一条,放不下两条)
	one := len("  · ") + len(recent.Content) + 1
	small := Inject([]Entry{old, recent}, nil, ShellOverhead+one)
	if strings.Contains(small, "很旧的口径") {
		t.Fatalf("预算不足时应丢掉旧的:\n%s", small)
	}
	if !strings.Contains(small, "最新口径") {
		t.Fatalf("预算不足时也应保留新的:\n%s", small)
	}
	// 契约本身:输出总长不得超过预算(含抬头与围栏)
	for _, b := range []int{ShellOverhead + one, 512, 2048} {
		got := Inject([]Entry{old, recent}, []Entry{{Date: "2026-09-01", Content: "项目口径"}}, b)
		if len(got) > b {
			t.Fatalf("输出 %d 字节超过预算 %d", len(got), b)
		}
	}
	// 预算连壳都放不下:宁可不注入
	if got := Inject([]Entry{old}, nil, ShellOverhead); got != "" {
		t.Fatalf("预算不足一整块时不该注入半块: %q", got)
	}
}

// TestInjectDeclaresDataNotInstruction 注入块必须自带边界声明(记忆是数据不是指令,
// 且位次低于指令层)—— 这是防提示注入的关键一行。
func TestInjectDeclaresDataNotInstruction(t *testing.T) {
	text := Inject([]Entry{{Date: "2026-10-01", Content: "x"}}, nil, 1024)
	for _, must := range []string{"以指令为准", "<memory>", "</memory>", "可能已过时"} {
		if !strings.Contains(text, must) {
			t.Fatalf("注入块应含 %q:\n%s", must, text)
		}
	}
}

func TestInjectEmptyIsEmpty(t *testing.T) {
	if Inject(nil, nil, 1024) != "" {
		t.Fatal("没有记忆时注入块应为空(不占提示词)")
	}
}

func TestFormatAndParseLine(t *testing.T) {
	line := Format("2026-10-01", "口径:按季度汇总", "abc-123")
	if !strings.HasPrefix(line, "- [2026-10-01]") || !strings.Contains(line, "来源: 会话 abc-123") {
		t.Fatalf("渲染不对: %q", line)
	}
	e, ok := ParseLine(line)
	if !ok || e.Date != "2026-10-01" || e.Source != "abc-123" || e.Content != "口径:按季度汇总" {
		t.Fatalf("解析不对: %+v ok=%v", e, ok)
	}
	// 非记忆行
	for _, bad := range []string{"", "# 注释", "普通文字", "- [2026/10/01] 日期格式不对"} {
		if _, ok := ParseLine(bad); ok {
			t.Fatalf("不该被解析成记忆: %q", bad)
		}
	}
}
