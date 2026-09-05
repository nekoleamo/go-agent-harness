// tool-todo 单测:状态机全生命周期、单 in_progress、blockedBy(悬空/环/start 前置/
// delete 被依赖拒)、墓碑与坏行容忍、工具面分发、并发写。
package tooltodo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func newStore(t *testing.T) *Store { return NewStore(t.TempDir()) }

func mustCreate(t *testing.T, s *Store, subject string, blockedBy ...string) string {
	t.Helper()
	tk, err := s.Create(subject, "", "", "", nil, blockedBy)
	if err != nil {
		t.Fatalf("create %q: %v", subject, err)
	}
	if tk.Status != StatusPending {
		t.Fatalf("新建应 pending: %s", tk.Status)
	}
	return tk.ID
}

// TestLifecycle create → start → pend → start → complete;重复/非法转换拒绝。
func TestLifecycle(t *testing.T) {
	s := newStore(t)
	id := mustCreate(t, s, "实现 tool-todo 协议")
	// start
	if _, err := s.Transit("start", id, "编写单测"); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Get(id); g.Status != StatusInProgress || g.ActiveForm != "编写单测" {
		t.Fatalf("start 后应 in_progress: %+v", g)
	}
	// 重复 start 拒绝
	if _, err := s.Transit("start", id, ""); err == nil {
		t.Fatal("重复 start 应拒绝")
	}
	// pend 回退
	if _, err := s.Transit("pend", id, ""); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Get(id); g.Status != StatusPending {
		t.Fatalf("pend 后应 pending: %s", g.Status)
	}
	// 非 in_progress 再 pend 拒绝
	if _, err := s.Transit("pend", id, ""); err == nil {
		t.Fatal("pending 状态再 pend 应拒绝")
	}
	// start → complete
	if _, err := s.Transit("start", id, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transit("complete", id, ""); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Get(id); g.Status != StatusCompleted {
		t.Fatalf("complete 后应 completed: %s", g.Status)
	}
	// completed 不能 start
	if _, err := s.Transit("start", id, ""); err == nil {
		t.Fatal("completed 再 start 应拒绝")
	}
	// 不存在的任务
	if _, err := s.Transit("start", "nope", ""); err == nil {
		t.Fatal("不存在任务 start 应报错")
	}
}

// TestSingleInProgress 同一时刻仅一个 in_progress:第二项 start 被拒,提示先处理前项。
func TestSingleInProgress(t *testing.T) {
	s := newStore(t)
	a := mustCreate(t, s, "任务 A")
	b := mustCreate(t, s, "任务 B")
	if _, err := s.Transit("start", a, ""); err != nil {
		t.Fatal(err)
	}
	_, err := s.Transit("start", b, "")
	if err == nil || !strings.Contains(err.Error(), a) {
		t.Fatalf("第二项 start 应拒绝并提示当前项 %s: %v", a, err)
	}
	// pend A 后可 start B
	if _, err := s.Transit("pend", a, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transit("start", b, ""); err != nil {
		t.Fatalf("A 暂停后 B 可 start: %v", err)
	}
	// complete 不受单 in_progress 影响(快速销单)
	c := mustCreate(t, s, "任务 C")
	if _, err := s.Transit("complete", c, ""); err != nil {
		t.Fatalf("pending 直接 complete 应允许: %v", err)
	}
}

// TestBlockedByGateAndRefs blockedBy:start 前置依赖须 completed;悬空引用被拒;
// complete 解锁后 start 成功;被依赖任务 delete 拒绝。
func TestBlockedByGateAndRefs(t *testing.T) {
	s := newStore(t)
	dep := mustCreate(t, s, "前置:打通装配")
	main := mustCreate(t, s, "主任务", dep)
	// start main 被依赖阻塞
	if _, err := s.Transit("start", main, ""); err == nil || !strings.Contains(err.Error(), dep) {
		t.Fatalf("依赖未完成应拒绝 start: %v", err)
	}
	// 悬空引用 create 拒绝
	if _, err := s.Create("坏任务", "", "", "", nil, []string{"ghost"}); err == nil {
		t.Fatal("悬空 blockedBy 应拒绝")
	}
	// 完成 dep 后 start main 成功
	if _, err := s.Transit("complete", dep, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transit("start", main, ""); err != nil {
		t.Fatalf("依赖完成后可 start: %v", err)
	}
	// delete main 会破坏 dep?不,是 dep 被 main 依赖:删 dep 应拒绝
	if err := s.Delete(dep); err == nil {
		t.Fatal("被依赖任务 delete 应拒绝")
	}
	// 先解除依赖(update removeBlockedBy)再删 dep
	if _, err := s.Update(main, "", "", "", "", nil, nil, []string{dep}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(dep); err != nil {
		t.Fatalf("解除依赖后可删: %v", err)
	}
}

// TestBlockedByCycle update 加依赖成环拒绝。
func TestBlockedByCycle(t *testing.T) {
	s := newStore(t)
	a := mustCreate(t, s, "A")
	b := mustCreate(t, s, "B")
	if _, err := s.Update(a, "", "", "", "", nil, []string{b}, nil); err != nil {
		t.Fatal(err)
	}
	// b 依赖 a → 环
	if _, err := s.Update(b, "", "", "", "", nil, []string{a}, nil); err == nil {
		t.Fatal("A←B←A 成环应拒绝")
	}
	// 自依赖拒绝
	if _, err := s.Update(a, "", "", "", "", nil, []string{a}, nil); err == nil {
		t.Fatal("自依赖应拒绝")
	}
}

// TestUpdateFieldsAndNoStatusChange update 改字段/依赖;status 不被 update 改变。
func TestUpdateFieldsAndNoStatusChange(t *testing.T) {
	s := newStore(t)
	id := mustCreate(t, s, "原主题")
	tk, err := s.Update(id, "新主题", "详细描述", "推进中", "gah", map[string]any{"k": "v"}, []string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Subject != "新主题" || tk.Description != "详细描述" || tk.ActiveForm != "推进中" ||
		tk.Owner != "gah" || tk.Metadata["k"] != "v" {
		t.Fatalf("update 字段应生效: %+v", tk)
	}
	if tk.Status != StatusPending {
		t.Fatalf("update 不应改状态: %s", tk.Status)
	}
	if tk.ID != id {
		t.Fatal("id 不可变")
	}
}

// TestTombstoneAndBadLines delete 墓碑:list 不可见、Get 仍可见(核对);坏行容忍。
func TestTombstoneAndBadLines(t *testing.T) {
	s := newStore(t)
	id := mustCreate(t, s, "待删任务")
	if err := s.Delete(id); err != nil {
		t.Fatal(err)
	}
	if vs := s.List(""); len(vs) != 0 {
		t.Fatalf("delete 后 list 应为空: %v", vs)
	}
	if tk, ok := s.Get(id); !ok || !tk.Deleted || tk.Subject != "待删任务" {
		t.Fatalf("Get 应可见墓碑: %+v ok=%v", tk, ok)
	}
	// 再次 delete 拒绝(幂等拒绝语义)
	if err := s.Delete(id); err == nil {
		t.Fatal("重复 delete 应拒绝")
	}
	// 坏行 + 注释行容忍
	tk2 := mustCreate(t, s, "有效任务")
	path := filepath.Join(s.root, sdkProjectKey()+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, "{broken")
	fmt.Fprintln(f, "# 人工注释行")
	f.Close()
	vs := s.List("")
	if len(vs) != 1 || vs[0].ID != tk2 {
		t.Fatalf("坏行容忍后应只剩有效任务: %v", vs)
	}
}

func sdkProjectKey() string {
	return sdk.ProjectKeyFromCwd()
}

// TestToolExecute 工具面 action 分发与校验。
func TestToolExecute(t *testing.T) {
	tl := &Tool{store: newStore(t)}
	// 缺 subject
	if out, _ := tl.Execute(nil, `{"action":"create"}`); out.(map[string]any)["error"] == nil {
		t.Fatalf("create 缺 subject 应 error: %v", out)
	}
	out, err := tl.Execute(nil, `{"action":"create","subject":"写单测"}`)
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["id"].(string)
	// 未知 action
	if out, _ := tl.Execute(nil, `{"action":"nope"}`); out.(map[string]any)["error"] == nil {
		t.Fatal("未知 action 应 error")
	}
	// list 可见;start;get 详情;list status 过滤
	if out, _ := tl.Execute(nil, `{"action":"list"}`); len(out.([]View)) != 1 {
		t.Fatalf("list 应 1 条: %v", out)
	}
	if out, _ := tl.Execute(nil, `{"action":"start","id":"`+id+`"}`); out.(map[string]any)["status"] != StatusInProgress {
		t.Fatalf("start 应 in_progress: %v", out)
	}
	if out, _ := tl.Execute(nil, `{"action":"list","status":"in_progress"}`); len(out.([]View)) != 1 {
		t.Fatalf("list 过滤 in_progress 应 1 条: %v", out)
	}
	if out, _ := tl.Execute(nil, `{"action":"get","id":"`+id+`"}`); out.(Task).Subject != "写单测" {
		t.Fatalf("get 应返回详情: %v", out)
	}
	// Definition
	def := tl.Definition()
	if def.Name != "todo" {
		t.Fatalf("工具名应为 todo: %s", def.Name)
	}
	if !strings.Contains(def.Description, "start") {
		t.Fatal("描述应含 action 用法")
	}
}

// TestConcurrentOps 并发 create/start:全部落盘、单 in_progress 不被击穿。
func TestConcurrentOps(t *testing.T) {
	s := newStore(t)
	const n = 25
	var wg sync.WaitGroup
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tk, err := s.Create(fmt.Sprintf("并发任务 %d", i), "", "", "", nil, nil)
			if err != nil {
				t.Errorf("并发 create: %v", err)
				return
			}
			ids[i] = tk.ID
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			t.Fatalf("并发下 id 应唯一: %v", ids)
		}
		seen[id] = true
	}
	if vs := s.List(""); len(vs) != n {
		t.Fatalf("应全部落盘 %d: %d", n, len(vs))
	}
	// 并发 start 全部:仅一个成功(in_progress),其余拒绝(单 in_progress 击穿防护)
	ok := 0
	for i := 0; i < n; i++ {
		if _, err := s.Transit("start", ids[i], ""); err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("并发全部 start 后仅 1 个 in_progress,实际 %d", ok)
	}
}

// TestFileShape 落盘 jsonl 每行可解析且为 Task 记录(人工可编辑三特性)。
func TestFileShape(t *testing.T) {
	s := newStore(t)
	id := mustCreate(t, s, "形状检查")
	b, err := os.ReadFile(filepath.Join(s.root, sdkProjectKey()+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var last Task
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		if err := json.Unmarshal([]byte(line), &last); err != nil {
			t.Fatalf("行应可解析: %q: %v", line, err)
		}
	}
	if last.ID != id || last.Status != StatusPending {
		t.Fatalf("最后一行应为任务记录: %+v", last)
	}
}
