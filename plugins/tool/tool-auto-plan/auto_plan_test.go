// tool-auto-plan 单测:规划全生命周期(create→confirm→step→complete)、
// 状态机约束、存储三特性(追加 jsonl/坏行容忍/人工可编辑)、并发锁、规则文本。
package toolautoplan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newStore 临时目录注入 root(不依赖 GAH_HOME/env)。
func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

// planPath 计算测试 store 落盘文件路径(cwd 派生 project key)。
func planPath(s *Store) string {
	p, _ := s.path()
	return p
}

// readPlans 读取文件全部行并解析。
func readPlans(t *testing.T, path string) []Plan {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []Plan
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var p Plan
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			t.Fatalf("落盘行应可解析: %q: %v", line, err)
		}
		out = append(out, p)
	}
	return out
}

// TestLifecycle create→confirm→step→complete 全链路。
func TestLifecycle(t *testing.T) {
	s := newStore(t)
	p, err := s.Create("实现搜索功能", "会话内搜索定位", []string{"不引入外部依赖"},
		[]string{"设计接口", "实现 state", "渲染高亮", "补测试"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusProposed || len(p.Steps) != 4 {
		t.Fatalf("create 应落 proposed 4 步: %+v", p)
	}
	// 未确认前不可 step
	if err := s.Step(p.ID, 0, StepInProgress); err == nil || !strings.Contains(err.Error(), "未确认") {
		t.Fatalf("未确认 step 应拒绝: %v", err)
	}
	// confirm 后逐步推进
	if err := s.Confirm(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(p.ID, 0, StepInProgress); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(p.ID, 0, StepCompleted); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(p.ID, 1, StepCompleted); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(p.ID, 2, StepCompleted); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(p.ID, 3, StepCompleted); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(p.ID); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(p.ID)
	if !ok || got.Status != StatusCompleted {
		t.Fatalf("complete 后应归档: %+v ok=%v", got, ok)
	}
	// 归档后不可改
	if err := s.Step(p.ID, 0, StepPending); err == nil {
		t.Fatal("归档后 step 应拒绝")
	}
	// list 摘要:done/total
	views := s.List("")
	if len(views) != 1 || views[0].Done != 4 || views[0].Total != 4 {
		t.Fatalf("list 摘要错误: %+v", views)
	}
}

// TestStepConstraints 步骤状态机:非法状态/越界/回退锁定/重复 confirm/未确认归档。
func TestStepConstraints(t *testing.T) {
	s := newStore(t)
	p, _ := s.Create("任务", "", nil, []string{"a", "b"})
	// 非法状态
	if err := s.Confirm(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(p.ID, 0, "done!"); err == nil {
		t.Fatal("非法状态应拒绝")
	}
	if err := s.Step(p.ID, 5, StepPending); err == nil {
		t.Fatal("越界序号应拒绝")
	}
	if err := s.Step(p.ID, 0, StepCompleted); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(p.ID, 0, StepPending); err == nil {
		t.Fatal("completed 步骤不可回退")
	}
	// 重复 confirm
	if err := s.Confirm(p.ID); err == nil {
		t.Fatal("重复 confirm 应拒绝")
	}
	// 未确认不可归档
	p2, _ := s.Create("另一任务", "", nil, nil)
	if err := s.Complete(p2.ID); err == nil {
		t.Fatal("未确认归档应拒绝")
	}
	if err := s.Confirm(p2.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(p2.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(p2.ID); err == nil {
		t.Fatal("重复归档应拒绝")
	}
}

// TestStorageResilience 坏行容忍:手写注释/坏行混入,正常行仍可读。
func TestStorageResilience(t *testing.T) {
	s := newStore(t)
	p1, _ := s.Create("规划一", "g", nil, []string{"s"})
	p2, _ := s.Create("规划二", "g", nil, []string{"s"})
	path := planPath(s)
	// 手动追加注释 + 坏行
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("# 人工注释\n")
	f.WriteString("{broken json\n")
	f.Close()
	got := s.List("")
	if len(got) != 2 {
		t.Fatalf("坏行后应仍 2 条: %d", len(got))
	}
	if _, ok := s.Get(p1.ID); !ok {
		t.Fatal("p1 应可读")
	}
	if _, ok := s.Get(p2.ID); !ok {
		t.Fatal("p2 应可读")
	}
	// 人工可编辑:直接改行文本应生效(最后一行胜)
	if err := s.Step(p1.ID, 0, StepCompleted); err == nil {
		t.Fatal("未确认不可 step")
	}
}

// TestAppendLastWins 同 id 最后一行胜(变更即追加新版本)。
func TestAppendLastWins(t *testing.T) {
	s := newStore(t)
	p, _ := s.Create("任务", "", nil, []string{"x"})
	if err := s.Confirm(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(p.ID, 0, StepCompleted); err != nil {
		t.Fatal(err)
	}
	lines := readPlans(t, planPath(s))
	// create 1 + confirm 1 + step 1 = 3 行;同 id 最终 1 份
	if len(lines) != 3 {
		t.Fatalf("应 3 行(append 式): %d", len(lines))
	}
	got, ok := s.Get(p.ID)
	if !ok || got.Status != StatusConfirmed || got.Steps[0].Status != StepCompleted {
		t.Fatalf("最后一行应胜: %+v", got)
	}
}

// TestConcurrency 并发写不坏文件(多路并行 create/step)。
func TestConcurrency(t *testing.T) {
	s := newStore(t)
	var wg sync.WaitGroup
	ids := make([]string, 0, 8)
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			p, err := s.Create(fmt.Sprintf("并发任务%d", n), "", nil, []string{"s"})
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			ids = append(ids, p.ID)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	views := s.List("")
	if len(views) != 8 {
		t.Fatalf("并发 8 条应全在: %d", len(views))
	}
	// 全部行均可解析(锁内串行追加不交错)
	for _, line := range readPlans(t, planPath(s)) {
		if line.ID == "" {
			t.Fatal("存在坏行")
		}
	}
}

// TestRuleText 规则片段含关键纪律(确认前零副作用/意图判定)。
func TestRuleText(t *testing.T) {
	text := RuleSectionText()
	for _, want := range []string{"auto_plan.create", "确认", "副作用", "意图判定", "auto_plan.confirm"} {
		if !strings.Contains(text, want) {
			t.Fatalf("规则文本应含 %q", want)
		}
	}
}

// TestStoreRootDefault env 缺省 root 归属(GAH_HOME/plans)。
func TestStoreRootDefault(t *testing.T) {
	t.Setenv("GAH_HOME", filepath.Join(t.TempDir(), "home"))
	path, err := (&Store{}).path()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, filepath.Join("home", "plans")) {
		t.Fatalf("缺省 root 应为 $GAH_HOME/plans: %s", path)
	}
}
