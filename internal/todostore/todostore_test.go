package todostore

// 读侧单一事实源的用例:落盘格式的容忍度与排序口径都在这里钉住。
// (写侧的状态机在 tool-todo;本包只读,所以这里没有"变更"类用例。)

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadLastWinsAndTolerance 同 id 最后一行胜 / 坏行与注释行容忍 / 无文件=空。
//
// 这三条是人工可编辑 + 外部进程追加的前提:任何一条破了,用户手改过的文件就会
// 让宿主与工具读到不一样的东西。
func TestLoadLastWinsAndTolerance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	body := strings.Join([]string{
		`{"id":"t1","subject":"旧标题","status":"pending","ts":1}`,
		`# 人工注释行`,
		`{"id":"t1","subject":"新标题","status":"in_progress","ts":2}`,
		`{"id":"t2",`,             // 断电半写:坏行
		`{"status":"pending"}`,    // 缺 id:跳过
		`{"id":"t3","status":""}`, // 缺 status:跳过
		`{"id":"t4","subject":"墓碑","status":"deleted","del":true,"ts":3}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks := Load(path)
	// t1(改过一次 ⇒ 1 条)+ t4(墓碑仍可查)= 2;坏行/缺 id/缺 status 三条全跳过。
	if len(tasks) != 2 {
		t.Fatalf("重放条数(坏行/缺字段应跳过): %d", len(tasks))
	}
	tk, ok := Find(tasks, "t1")
	if !ok || tk.Subject != "新标题" || tk.Status != StatusInProgress {
		t.Fatalf("同 id 最后一行应胜出: %+v", tk)
	}
	if got := Load(filepath.Join(dir, "nope.jsonl")); len(got) != 0 {
		t.Fatalf("无文件应为空(不是错误): %+v", got)
	}
}

// TestViewsOrderFilterTombstone 排序(进行中 > 待办 > 已完成,组内倒序)、状态过滤、
// 墓碑不进列表。
func TestViewsOrderFilterTombstone(t *testing.T) {
	tasks := []Task{
		{ID: "a", Subject: "完成1", Status: StatusCompleted, TS: 10},
		{ID: "b", Subject: "待办1", Status: StatusPending, TS: 20},
		{ID: "c", Subject: "进行", Status: StatusInProgress, TS: 1},
		{ID: "d", Subject: "待办2", Status: StatusPending, TS: 30},
		{ID: "e", Subject: "已删", Status: StatusDeleted, Deleted: true, TS: 40},
	}
	got := Views(tasks, "")
	var ids []string
	for _, v := range got {
		ids = append(ids, v.ID)
	}
	want := "c,d,b,a" // 进行中 → 待办(新→旧) → 已完成;墓碑不出现
	if strings.Join(ids, ",") != want {
		t.Fatalf("排序/墓碑口径: got %v want %s", ids, want)
	}
	if only := Views(tasks, StatusPending); len(only) != 2 {
		t.Fatalf("状态过滤: %+v", only)
	}
}

// TestPathAndTrim 路径派生与展示裁剪。
func TestPathAndTrim(t *testing.T) {
	t.Setenv("GAH_HOME", "/tmp/gah-home-x")
	p := Path("")
	if !strings.HasPrefix(p, filepath.Join("/tmp/gah-home-x", "todos")) || !strings.HasSuffix(p, ".jsonl") {
		t.Fatalf("路径应落在 $GAH_HOME/todos/<project>.jsonl: %s", p)
	}
	if Dir("/custom") != "/custom" {
		t.Fatal("root 注入优先")
	}
	if Trim("  短的  ", 10) != "短的" {
		t.Fatal("Trim 应去空白")
	}
	if got := Trim("一二三四五", 3); got != "一二三…" {
		t.Fatalf("Trim 不应切多字节: %q", got)
	}
	if StatusOf(Task{Status: StatusPending, Deleted: true}) != StatusDeleted {
		t.Fatal("墓碑状态显示应为 deleted")
	}
}
