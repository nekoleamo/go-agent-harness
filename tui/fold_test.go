// S2.2 折叠交互单测:结果行存 Full、ToggleFold 状态机、视图展平几何、单击命中切换。
package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// toolResultLine 构造一条带全文的 tool 结果行(模拟 EventToolResult 处理后的 Lines 状态)。
func toolResultLine(t *testing.T, content string) *State {
	t.Helper()
	s := &State{}
	s.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventToolResult,
		Payload: sdk.ToolResultEvent{CallID: "c1", Name: "shell", Content: content}})
	if len(s.Lines) != 1 || s.Lines[0].Full == "" {
		t.Fatalf("结果行应存 Full: %+v", s.Lines)
	}
	return s
}

// TestResultStoresFull 长结果 → 摘要(Text 截断)+ 全文(Full)均保留。
func TestResultStoresFull(t *testing.T) {
	long := strings.Repeat("数据行\n", 50) // 多段长结果
	s := toolResultLine(t, long)
	ln := s.Lines[0]
	if !strings.Contains(ln.Text, "…") || !strings.Contains(ln.Text, "shell") {
		t.Fatalf("摘要应截断含工具名: %q", ln.Text)
	}
	if ln.Full != long {
		t.Fatalf("Full 应存完整内容: got %d want %d", len(ln.Full), len(long))
	}
}

// TestFoldFullLimit 超长保护:Full 存储有上限(防爆),尾部标注。
func TestFoldFullLimit(t *testing.T) {
	big := strings.Repeat("A", foldFullLimit+500)
	s := toolResultLine(t, big)
	if len(s.Lines[0].Full) > foldFullLimit+100 {
		t.Fatalf("Full 应受上限: %d", len(s.Lines[0].Full))
	}
}

// TestToggleFold 默认折叠(无 mark)→ toggle 展开(FoldOpen true)→ toggle 再收起。
func TestToggleFold(t *testing.T) {
	s := toolResultLine(t, strings.Repeat("长内容", 100))
	if s.foldOpenOf(0) {
		t.Fatal("初始应折叠")
	}
	if !s.ToggleFold(0) || !s.foldOpenOf(0) {
		t.Fatal("toggle 应展开")
	}
	if !s.ToggleFold(0) || s.foldOpenOf(0) {
		t.Fatal("再次 toggle 应收起")
	}
	// 越界/非结果行不生效
	if s.ToggleFold(99) {
		t.Fatal("越界行 toggle 不应生效")
	}
	s2 := &State{}
	s2.Lines = []Line{{Kind: "assistant", Text: "无全文"}}
	if s2.ToggleFold(0) {
		t.Fatal("无 Full 行不应可折叠")
	}
}

// TestFoldViewGeometry 视图几何:折叠=摘要单行;展开=全文多物理行(lineIdx 归属一致)。
func TestFoldViewGeometry(t *testing.T) {
	s := toolResultLine(t, "第一段\n第二段\n第三段")
	folded := flattenViewLines(s, 60)
	if len(folded) != 1 {
		t.Fatalf("折叠应单物理行: %d", len(folded))
	}
	if !folded[0].first || folded[0].lineIdx != 0 {
		t.Fatalf("首物理行标记 first: %+v", folded[0])
	}
	s.ToggleFold(0) // 展开
	open := flattenViewLines(s, 60)
	if len(open) != 3 {
		t.Fatalf("展开应为 3 物理行(3 段): %d", len(open))
	}
	for i, r := range open {
		if r.lineIdx != 0 {
			t.Fatalf("展开物理行归属逻辑行 0: %+v", r)
		}
		if i == 0 && !r.first {
			t.Fatal("展开首行应 first")
		}
		if i > 0 && r.first {
			t.Fatal("非首行不应 first")
		}
	}
}

// TestFoldMark 渲染标记:折叠 ▲ / 展开 ▼ / 非折叠行无标记。
func TestFoldMark(t *testing.T) {
	s := toolResultLine(t, strings.Repeat("x", 300))
	rows := flattenViewLines(s, 80)
	if rows[0].text == "" {
		t.Fatal("行应有文本")
	}
	out := renderSessionRow(rows[0], s, 0)
	if !strings.Contains(out, "▲") {
		t.Fatalf("折叠行应带 ▲ 提示: %q", out)
	}
	s.ToggleFold(0)
	rows = flattenViewLines(s, 80)
	out = renderSessionRow(rows[0], s, 0)
	if !strings.Contains(out, "▼") {
		t.Fatalf("展开行应带 ▼ 提示: %q", out)
	}
	// 非折叠行(assistant 无 Full)无标记
	s2 := &State{}
	s2.Lines = []Line{{Kind: "assistant", Text: "普通回复"}}
	r := flattenViewLines(s2, 80)
	if strings.Contains(renderSessionRow(r[0], s2, 0), "▲") {
		t.Fatal("非折叠行不应带折叠标记")
	}
}

// TestFoldClickToggle 集成:press 行缓存 lineIdx → 单击 toggle 展开(FoldOpen)。
func TestFoldClickToggle(t *testing.T) {
	m := &Model{}
	m.state = &State{}
	m.state.Lines = []Line{{Kind: "tool", Text: "✓ shell: 摘要…", Full: "完整内容"}}
	// 模拟 press 缓存(直接填充,等价 handleMousePress 结果)
	rows := flattenViewLines(m.state, 80)
	m.selRows = make([]string, len(rows))
	m.selLineIdx = make([]int, len(rows))
	for i, p := range rows {
		m.selRows[i] = p.text
		m.selLineIdx[i] = p.lineIdx
	}
	m.state.SelActive = true
	m.state.SelRow0, m.state.SelCol0 = 0, 1
	m.state.SelRow1, m.state.SelCol1 = 0, 1
	m.selMoved = false
	m.handleMouseRelease(tea.Mouse{})
	if !m.state.foldOpenOf(0) {
		t.Fatal("单击折叠行应展开")
	}
	// 普通行单击不 toggle
	m2 := &Model{state: &State{}}
	m2.state.Lines = []Line{{Kind: "assistant", Text: "hello"}, {Kind: "tool", Text: "✓ x: y", Full: "f"}}
	rows2 := flattenViewLines(m2.state, 80)
	m2.selRows = make([]string, len(rows2))
	m2.selLineIdx = make([]int, len(rows2))
	for i, p := range rows2 {
		m2.selRows[i] = p.text
		m2.selLineIdx[i] = p.lineIdx
	}
	m2.state.SelActive = true
	m2.state.SelRow0, m2.state.SelCol0 = 0, 0
	m2.state.SelRow1, m2.state.SelCol1 = 0, 0
	m2.selMoved = false
	m2.handleMouseRelease(tea.Mouse{})
	if m2.state.foldOpenOf(0) {
		t.Fatal("无 Full 行单击不应 toggle")
	}
}
