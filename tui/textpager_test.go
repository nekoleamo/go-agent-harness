// S-P1-1 纯文本 pager(/diff 的 patch 浮层)+ diff/open 订阅单测。
// 复用 pager 的滚动/搜索能力,只额外验证:行原样保留(不重排 diff)、+/-/@@ 轻染色、状态行提示。
package tui

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

const samplePatch = `plugins/host/host-jobs/jobs.go
本次会话 2 次改动:+5 −1 行

#2 edit 10:00:01  +4 −1
@@ -1,3 +1,6 @@
 a
-b
+B1
+B2
+
#3 edit 10:00:02  +1 −0
@@ -9,2 +9,3 @@
+x
`

func TestNewTextPagerKeepsLines(t *testing.T) {
	p := NewTextPager(TextPagerSpec{
		Title: "jobs.go", Format: "diff", Lines: strings.Split(strings.TrimRight(samplePatch, "\n"), "\n"),
		Status: "2 次改动 · 来自捕获的写操作(不依赖 git)", Diff: true,
	})
	if got := strings.Join(p.Lines, "\n"); got != strings.TrimRight(samplePatch, "\n") {
		t.Fatalf("diff 行必须逐字保留(不重排、不丢行):\n%s", got)
	}
	// 状态行只在宽度够时渲染(pager 既有取舍:操作提示优先);截断提示同时落在 patch 正文里
	out := p.Render(140, 24)
	for _, want := range []string{"jobs.go", "[diff]", "@@ -1,3 +1,6 @@", "+B1", "-b", "来自捕获的写操作", "行 · q/Esc 关闭"} {
		if !strings.Contains(out, want) {
			t.Errorf("渲染缺少 %q:\n%s", want, out)
		}
	}
	// 空内容给占位行(不留空白屏)
	if e := NewTextPager(TextPagerSpec{Title: "x"}); len(e.Lines) != 1 || e.Lines[0] != "(无内容)" {
		t.Fatalf("空内容占位: %v", e.Lines)
	}
}

// TestTextPagerDiffColoring diff 行轻染色:行首 +/-/@@ 走 diffToolRow 同一套词色。
func TestTextPagerDiffColoring(t *testing.T) {
	p := NewTextPager(TextPagerSpec{Title: "x", Lines: []string{"@@ -1 +1 @@", "+added", "-removed", " context"}, Diff: true})
	if got := p.renderLine(1, 80); got == styleMeta.Render("+added") {
		t.Errorf("新增行与上下文行不应同色: %q", got)
	}
	if got := p.renderLine(2, 80); got == styleMeta.Render("-removed") {
		t.Errorf("删除行与上下文行不应同色: %q", got)
	}
	// 非 diff 模式(Diff=false)全部走弱化样式
	plain := NewTextPager(TextPagerSpec{Title: "x", Lines: []string{"+added"}})
	if plain.renderLine(0, 80) != styleMeta.Render("+added") {
		t.Errorf("Diff=false 时不应给 diff 染色: %q", plain.renderLine(0, 80))
	}
	// 搜索命中优先于 diff 染色(用户显式意图)
	p.Search = "added"
	p.Hits = []int{1}
	if !strings.Contains(p.renderLine(1, 80), "\x1b[") {
		t.Error("搜索命中应高亮")
	}
}

// TestPagerMsgOpensOverlay Model 收到 PagerMsg 后设浮层并吃掉视图刷新标记。
func TestPagerMsgOpensOverlay(t *testing.T) {
	m := &Model{state: &State{}}
	m.state.Doc = nil
	m.skipView = true
	m.Update(PagerMsg{Pager: NewTextPager(TextPagerSpec{Title: "jobs.go", Lines: []string{"@@ -1 +1 @@", "+x"}, Diff: true})})
	if m.state.Doc == nil {
		t.Fatal("PagerMsg 应打开浮层")
	}
	if m.state.Doc.Title != "jobs.go" || !m.state.Doc.Diff {
		t.Fatalf("pager 字段: %+v", m.state.Doc)
	}
	if m.skipView {
		t.Error("打开浮层后应清除 skipView(下一帧要重绘)")
	}
	// nil 载荷不得把已有浮层清掉(nil = 清单意图,调用方本该拦下,这里兜底)
	m.Update(PagerMsg{})
	if m.state.Doc == nil {
		t.Fatal("nil 载荷不应清空浮层")
	}
}

// TestNewDiffPager 订阅侧口径:带 patch 才弹 pager,清单意图返回 nil。
func TestNewDiffPager(t *testing.T) {
	got := NewDiffPager(sdk.DiffOpenEvent{Path: "/w/a.go", Title: "a.go", Diff: samplePatch, Changes: 2, Truncated: true})
	if got == nil {
		t.Fatal("带 patch 应弹 pager")
	}
	if got.Title != "a.go" || !got.Diff || got.Format != "diff" {
		t.Fatalf("pager 参数: %+v", got)
	}
	if !strings.Contains(got.Status, "patch 已截断") || !strings.Contains(got.Status, "2 次改动") || !strings.Contains(got.Status, "不依赖 git") {
		t.Fatalf("状态行应含截断/次数/来源: %q", got.Status)
	}
	if len(got.Lines) == 0 || got.Lines[0] != "plugins/host/host-jobs/jobs.go" {
		t.Fatalf("patch 行应原样透传: %v", got.Lines)
	}
	// 单次改动不显示「N 次改动」;无 Title 回落 Path
	one := NewDiffPager(sdk.DiffOpenEvent{Path: "/w/b.go", Diff: "@@ -1 +1 @@\n+y\n"})
	if strings.Contains(one.Status, "次改动") || one.Title != "/w/b.go" {
		t.Fatalf("单次改动/无标题回落: %+v", one)
	}
	if got := NewDiffPager(sdk.DiffOpenEvent{}); got != nil {
		t.Fatalf("清单意图(无 patch)不应弹窗: %+v", got)
	}
}
