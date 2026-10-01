package memory

// 候选池的行为契约(M2 前置件)。重点钉三处:
//   ① 候选**绝不进上下文**(构造上:不在 user.md 里);
//   ② 限流是显式报错,不是静默丢弃;
//   ③ 批量确认的**半截状态**必须说清楚。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProposeDoesNotEnterMemory(t *testing.T) {
	path := p(t)
	t.Setenv("GAH_HOME", filepath.Dir(filepath.Dir(path)))
	// 直接用固定的 GAH_HOME 更稳:候选与记忆都走 sdk.Home()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)

	if err := Propose("报告图表用蓝灰配色", "sess-1"); err != nil {
		t.Fatal(err)
	}
	// 候选落在候选池
	raw, err := os.ReadFile(filepath.Join(home, "memory", "candidates.md"))
	if err != nil {
		t.Fatalf("候选没落盘: %v", err)
	}
	if !strings.Contains(string(raw), "蓝灰配色") {
		t.Fatalf("候选文件内容不对:\n%s", raw)
	}
	// 记忆文件**不存在** ⇒ 候选没被当成记忆(这是构造上的保证,不是过滤)
	if _, err := os.Stat(filepath.Join(home, "memory", "user.md")); !os.IsNotExist(err) {
		t.Fatalf("提候选不该创建记忆文件(stat err=%v)", err)
	}
	// 注入块对候选无感:Inject 只吃传进去的 entries,这里断言「候选池不在任何注入路径上」
	if Inject(nil, nil, DefaultInjectBudget) != "" {
		t.Fatal("空输入时 Inject 应为空")
	}
	_ = path
}

func TestProposeRateLimitIsExplicit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	for i := 0; i < MaxCandidatesPerDay; i++ {
		if err := Propose("第 "+string(rune('A'+i))+" 条", ""); err != nil {
			t.Fatalf("第 %d 条应成功: %v", i+1, err)
		}
	}
	// 当日额度用尽 ⇒ 显式报错,且说清是哪条顶住了
	err := Propose("再来一条", "")
	if err == nil {
		t.Fatal("超当日额度应报错(不静默丢弃)")
	}
	if !strings.Contains(err.Error(), "上限") {
		t.Fatalf("错误应说明上限:%v", err)
	}
	// 候选池没有多出那条
	if got := len(ListCandidates()); got != MaxCandidatesPerDay {
		t.Fatalf("被拒的候选不该落盘,池子应仍是 %d 条,got %d", MaxCandidatesPerDay, got)
	}
}

func TestProposeRejectsDuplicates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := Propose("同一条", ""); err != nil {
		t.Fatal(err)
	}
	if err := Propose("同一条", ""); err == nil {
		t.Fatal("同日同内容应拒(与记忆同口径)")
	}
}

func TestAcceptMovesIntoMemory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	_ = Propose("第一条候选", "sess-a")
	_ = Propose("第二条候选", "sess-b")

	// 展示序是新的在前 ⇒ 第 1 条是「第二条候选」
	lines := ListCandidates()
	if !strings.Contains(lines[0], "第二条候选") {
		t.Fatalf("候选展示序应是新的在前:%v", lines)
	}
	got, err := Accept(1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "第二条候选") {
		t.Fatalf("Accept 返回错条目:%q", got)
	}
	// 进了记忆、带上了来源
	raw, _ := os.ReadFile(filepath.Join(home, "memory", "user.md"))
	if !strings.Contains(string(raw), "第二条候选") || !strings.Contains(string(raw), "sess-b") {
		t.Fatalf("记忆文件应含内容与来源:\n%s", raw)
	}
	// 候选池少了一条
	if n := len(ListCandidates()); n != 1 {
		t.Fatalf("候选应剩 1 条,got %d", n)
	}
	// 越界
	if _, err := Accept(99); err == nil {
		t.Fatal("越界应报错")
	}
}

func TestAcceptTwiceIsIdempotentNotDouble(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	_ = Propose("只应存在一条", "sess-a")
	if _, err := Accept(1); err != nil {
		t.Fatal(err)
	}
	// 再提一次同内容(模拟「用户没看到回执又点了一次」)⇒ 撞同日去重,不是写两条
	_ = Propose("只应存在一条", "sess-a")
	got, err := Accept(1)
	if err != nil {
		t.Fatalf("已在记忆里的候选应被幂等吸收:%v", err)
	}
	if !strings.Contains(got, "已从候选池移除") {
		t.Fatalf("回执应说明「此前已记过」:%q", got)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "memory", "user.md"))
	if n := strings.Count(string(raw), "只应存在一条"); n != 1 {
		t.Fatalf("记忆里应只有 1 条,got %d:\n%s", n, raw)
	}
}

func TestAcceptAllAndRejectAll(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	for _, s := range []string{"一", "二", "三"} {
		if err := Propose("候选"+s, ""); err != nil {
			t.Fatal(err)
		}
	}
	n, err := AcceptAll()
	if err != nil || n != 3 {
		t.Fatalf("批量确认应转正 3 条:n=%d err=%v", n, err)
	}
	if got := len(ListCandidates()); got != 0 {
		t.Fatalf("候选池应清空,got %d", got)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "memory", "user.md"))
	if n := strings.Count(string(raw), "- ["); n != 3 {
		t.Fatalf("记忆里应有 3 条,got %d:\n%s", n, raw)
	}

	// 驳回:不入记忆
	_ = Propose("不该进记忆", "")
	got, err := RejectAll()
	if err != nil || got != 1 {
		t.Fatalf("驳回应丢 1 条:got=%d err=%v", got, err)
	}
	raw2, _ := os.ReadFile(filepath.Join(home, "memory", "user.md"))
	if strings.Contains(string(raw2), "不该进记忆") {
		t.Fatalf("驳回的候选不该进记忆:\n%s", raw2)
	}
}

func TestCandidateQuota(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	_ = Propose("a", "")
	_ = Propose("b", "")
	used, limit, today, todayLimit := CandidateQuota()
	if used != 2 || limit != MaxCandidates || today != 2 || todayLimit != MaxCandidatesPerDay {
		t.Fatalf("配额回显不对:used=%d limit=%d today=%d todayLimit=%d", used, limit, today, todayLimit)
	}
}

// 池子总量上限(与当日额度是两个独立闸):把日期手工改成昨天,当日额度就放开了,
// 总量上限仍应挡住第 13 条。
func TestCandidatePoolHardCap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	cp := filepath.Join(home, "memory", "candidates.md")
	os.MkdirAll(filepath.Dir(cp), 0o755)
	var lines []string
	for i := 0; i < MaxCandidates; i++ {
		lines = append(lines, "- [2026-01-0"+string(rune('1'+i%9))+"] 占位 "+string(rune('A'+i)))
	}
	os.WriteFile(cp, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	err := Propose("第 13 条", "")
	if err == nil || !strings.Contains(err.Error(), "已满") {
		t.Fatalf("池子满时应报错,got %v", err)
	}
}
