package tui

// 状态栏的待办项(D2 的另一半 · 2026-10-23)。
//
// 为什么要有:待办工具早就落盘了,但 TUI 里**看不见** —— 只能问模型"还有几条没做完"
// (Web 有看板,TUI 没有)。状态栏给一个常驻信号(「待办 1/3」),细节走 /todo。
//
// 口径:无待办 ⇒ **隐身**(没建过单不占位);刷新时机 = 回合结束 + 文件 mtime 变了,
// **不是每帧读盘**(待办由工具/外部进程写,状态栏每帧渲染,每帧读盘是低频指标付高频成本)。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/todostore"
)

// TestStatuslineTodoItem 待办进度渲染 + 无待办隐身。
func TestStatuslineTodoItem(t *testing.T) {
	s := &State{TodoDone: 1, TodoTotal: 3}
	if out := stripANSI(statuslineItem(s, "todo")); !strings.Contains(out, "1/3") {
		t.Fatalf("待办项应显示进度,got %q", out)
	}
	if out := statuslineItem(&State{}, "todo"); out != "" {
		t.Fatalf("无待办时该项应隐身,got %q", out)
	}
	// 合法项 + 默认集合(基线默认要覆盖全部可用项,否则用户不动 /statusline 就看不到)
	var inTokens, inDefault bool
	for _, tok := range statuslineTokens {
		if tok == "todo" {
			inTokens = true
		}
	}
	for _, tok := range defaultStatusline {
		if tok == "todo" {
			inDefault = true
		}
	}
	if !inTokens || !inDefault {
		t.Fatalf("todo 应既在 statuslineTokens 也在默认集合里(tokens=%v default=%v)",
			inTokens, inDefault)
	}
	if statuslineTokenDesc["todo"] == "" {
		t.Fatal("todo 缺说明(/statusline 要靠它自述)")
	}
}

// TestTodoCountsReadsStoreAndCaches mtime 未变时不重放(第二次 changed=false)。
//
// 这条守的是"低频指标不付高频成本":每次回合结束都会调 todoCounts,
// 若每次都重放+排序,会话一长就是白烧 CPU;而**跳过了又必须还能更新**,
// 所以第二种情形(文件被改写)必须 changed=true 且拿到新计数。
func TestTodoCountsReadsStoreAndCaches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	// GAH_HOME 变了 ⇒ 项目 key 也要跟着变(todostore 用 sdk.Home 派生路径)
	dir := filepath.Join(home, "todos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := todostore.Path("")
	// ts 每次写都**严格递增 1s**(而不是"现在 + 行数秒"):后者的偏移取决于行数,连续两次
	// 同样行数的写会落在同一个秒级值上,只靠两次 time.Now() 的亚秒差区分 —— Windows CI 的
	// 临时卷上这个差会被抹平,症状是「墓碑不该计数」那条断言恒红(v0.7.2 发版途中实撞,
	// rerun 即绿 = 环境相关,不是产品缺陷)。
	ts := time.Now()
	write := func(lines ...string) {
		body := strings.Join(lines, "\n") + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		ts = ts.Add(time.Second)
		if err := os.Chtimes(path, ts, ts); err != nil {
			t.Fatal(err)
		}
		// 环境的时间戳粒度比 1s 还粗(FAT/exFAT 一类 2s 粒度卷)⇒ "mtime 变了要重读"
		// 这条根本测不到。显式跳过并说清原因,别让它报成产品缺陷(与只读目录类用例同款纪律)。
		if st, err := os.Stat(path); err != nil || !st.ModTime().Equal(ts) {
			t.Skipf("文件系统时间戳粒度不足以区分 1s(写成 %v,读回 %v):跳过缓存失效断言",
				ts, st.ModTime())
		}
	}
	write(
		`{"id":"1","subject":"甲","status":"completed","ts":1}`,
		`{"id":"2","subject":"乙","status":"pending","ts":2}`,
	)

	a := &App{}
	done, total, changed := a.todoCounts()
	if !changed || done != 1 || total != 2 {
		t.Fatalf("首次读取应有变化且计数正确,got done=%d total=%d changed=%v", done, total, changed)
	}
	if done, total, changed := a.todoCounts(); changed || done != 1 || total != 2 {
		t.Fatalf("mtime 未变应命中缓存(changed=false),got done=%d total=%d changed=%v",
			done, total, changed)
	}
	// 推进一条:缓存必须失效并读到新值(否则状态栏会永远停在旧进度)
	write(
		`{"id":"1","subject":"甲","status":"completed","ts":1}`,
		`{"id":"2","subject":"乙","status":"completed","ts":3}`,
	)
	if done, total, changed := a.todoCounts(); !changed || done != 2 || total != 2 {
		t.Fatalf("mtime 变了应重读,got done=%d total=%d changed=%v", done, total, changed)
	}
	// 全部删除(墓碑):计数归零 ⇒ 状态栏该项隐身
	write(
		`{"id":"1","subject":"甲","status":"deleted","del":true,"ts":4}`,
		`{"id":"2","subject":"乙","status":"deleted","del":true,"ts":4}`,
	)
	if done, total, changed := a.todoCounts(); !changed || done != 0 || total != 0 {
		t.Fatalf("墓碑不该计数,got done=%d total=%d changed=%v", done, total, changed)
	}
}
