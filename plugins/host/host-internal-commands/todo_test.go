package hostintcmd

// /todo 命令的用例(D2 的另一半 · 2026-10-23)。
//
// 要钉的是:
//  1. 列出待办:状态词/标记/完成度都在,顺序是"进行中 > 待办 > 已完成";
//  2. 按状态过滤;
//  3. 详情按 id;
//  4. 墓碑(deleted)**不进列表也不进计数**(删掉的任务不该还占一条);
//  5. 没建过单 ⇒ 明确说"暂无待办",不报错也不装空;
//  6. **不经工具流水线**:ctx.tools 完全不装配也能跑(否则会触发审批、并往会话里
//     塞一条模型没请求过的工具结果)。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/todostore"
)

// seedTodos 往 $GAH_HOME/todos/<project-key>.jsonl 写几行(追加式,同 id 后者胜)。
func seedTodos(t *testing.T, home string, lines ...string) string {
	t.Helper()
	t.Setenv("GAH_HOME", home)
	path := todostore.Path("")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCmdTodoList(t *testing.T) {
	home := t.TempDir()
	seedTodos(t, home,
		`{"id":"t1","subject":"写用例","status":"pending","ts":1}`,
		`{"id":"t2","subject":"改代码","activeForm":"正在改代码","status":"in_progress","ts":2}`,
		`{"id":"t3","subject":"读文档","status":"completed","ts":3}`,
		`{"id":"t4","subject":"已废弃","status":"deleted","del":true,"ts":4}`,
	)
	c, cmds := buildEnv(t)
	startCmds(t, c)
	out, err := run(t, cmds, "todo")
	if err != nil {
		t.Fatal(err)
	}
	// 三种状态各一条 + 完成度(墓碑不计 ⇒ 1/3 而不是 1/4)
	for _, want := range []string{"进行中", "待办", "已完成", "1/3"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
	// 进行中优先显示 activeForm(它写的是"正在做什么")
	if !strings.Contains(out, "正在改代码") {
		t.Errorf("进行中应显示 activeForm:\n%s", out)
	}
	// 墓碑不该出现
	if strings.Contains(out, "已废弃") {
		t.Errorf("墓碑不该出现在列表里:\n%s", out)
	}
	// 顺序:进行中(t2)在待办(t1)之前
	if strings.Index(out, "正在改代码") > strings.Index(out, "写用例") {
		t.Errorf("进行中应排在待办之前:\n%s", out)
	}
}

func TestCmdTodoFilterAndDetail(t *testing.T) {
	home := t.TempDir()
	seedTodos(t, home,
		`{"id":"t1","subject":"写用例","status":"pending","ts":1}`,
		`{"id":"t2","subject":"改代码","description":"把 /todo 加上","status":"in_progress","blockedBy":["t9"],"ts":2}`,
	)
	c, cmds := buildEnv(t)
	startCmds(t, c)

	out, err := run(t, cmds, "todo", "pending")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "写用例") || strings.Contains(out, "改代码") {
		t.Errorf("状态过滤失效:\n%s", out)
	}

	detail, err := run(t, cmds, "todo", "t2")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"把 /todo 加上", "t9"} {
		if !strings.Contains(detail, want) {
			t.Errorf("详情缺少 %q:\n%s", want, detail)
		}
	}
}

// TestCmdTodoDetailRichAndFilters 详情全覆盖(负责人/元数据/进行时)+ 过滤空态 + all。
//
// 这些分支都在 todo.go 里(不是纯排版),漏测就让包覆盖率掉到棘轮线下 —— 说明
// "命令的每个可答问题都得有一条用例"。
func TestCmdTodoDetailRichAndFilters(t *testing.T) {
	home := t.TempDir()
	seedTodos(t, home,
		`{"id":"t1","subject":"写用例","status":"pending","ts":1}`,
		`{"id":"t2","subject":"改代码","description":"把 /todo 加上","activeForm":"正在改 /todo","owner":"我","metadata":{"k":1},"status":"in_progress","ts":2}`,
	)
	c, cmds := buildEnv(t)
	startCmds(t, c)

	detail, err := run(t, cmds, "todo", "t2")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"进行时:正在改 /todo", "负责人:我", "元数据:1 项"} {
		if !strings.Contains(detail, want) {
			t.Errorf("详情缺少 %q:\n%s", want, detail)
		}
	}

	// 过滤到空:不能说"暂无待办"(那会被读成"一条都没有"),要指名是哪个状态
	empty, err := run(t, cmds, "todo", "completed")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(empty, "已完成") {
		t.Errorf("过滤空态应点名状态:\n%s", empty)
	}

	// all = 不过滤(含已完成)
	all, err := run(t, cmds, "todo", "all")
	if err != nil {
		t.Fatal(err)
	}
	// 进行中的那条显示 activeForm(不是标题)—— 与列表口径一致
	if !strings.Contains(all, "写用例") || !strings.Contains(all, "正在改 /todo") {
		t.Errorf("all 应列出全部:\n%s", all)
	}
}

func TestCmdTodoEmptyAndUnknown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home) // 目录不存在 = 还没建过单
	c, cmds := buildEnv(t)
	startCmds(t, c)
	out, err := run(t, cmds, "todo")
	if err != nil {
		t.Fatalf("没建过单不是错误: %v", err)
	}
	if !strings.Contains(out, "暂无待办") {
		t.Errorf("空态应明确说暂无待办:\n%s", out)
	}
	if _, err := run(t, cmds, "todo", "nope"); err == nil {
		t.Fatal("不存在的任务应显式报错")
	}
}
