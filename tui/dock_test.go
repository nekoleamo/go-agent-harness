// dock_test.go:S-P0-3 TUI 后台坞(状态栏折叠行 + 刷新链)单测。
package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestDockLabel(t *testing.T) {
	cases := []struct {
		in   DockInfo
		want string
	}{
		{DockInfo{}, ""}, // 无记录不占位
		{DockInfo{Total: 3}, "后台 3 条已结束"},            // 全结束
		{DockInfo{Total: 1, Running: 1}, "后台 1 运行中"}, // 运行中无摘要
		{DockInfo{Total: 2, Running: 1, Latest: "npm test"}, "后台 1 运行中: npm test"},
	}
	for _, c := range cases {
		if got := dockLabel(c.in); got != c.want {
			t.Errorf("dockLabel(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
	// 超长摘要截断(不撑爆状态栏)
	long := dockLabel(DockInfo{Total: 1, Running: 1, Latest: strings.Repeat("命令参数", 10)})
	if !strings.HasSuffix(long, "…") || len([]rune(long)) > len([]rune("后台 1 运行中: "))+22 {
		t.Errorf("长摘要应截断: %q", long)
	}
}

// TestRefreshDockContinue:续拍只看「运行中任务」与「回合运行中」——
// 已结束的历史记录不得让链永转。
func TestRefreshDockContinue(t *testing.T) {
	m := &Model{state: &State{}}
	// 无 onDock:不 panic,按现有状态判定
	if m.refreshDock() {
		t.Errorf("空状态不应续拍")
	}
	m.state.Running = true
	if !m.refreshDock() {
		t.Errorf("回合运行中应续拍(回合内可能提交任务)")
	}
	// 运行中任务 → 续拍,并写入 onDock 返回
	m = &Model{state: &State{}, onDock: func() DockInfo { return DockInfo{Total: 2, Running: 1, Latest: "x"} }}
	if !m.refreshDock() {
		t.Errorf("有运行中任务应续拍")
	}
	if m.state.Dock.Total != 2 || m.state.Dock.Running != 1 || m.state.Dock.Latest != "x" {
		t.Errorf("Dock 未写入: %+v", m.state.Dock)
	}
	// 仅历史记录(0 运行、无回合)→ 停拍并清空折叠行
	m = &Model{state: &State{}, onDock: func() DockInfo { return DockInfo{Total: 5} }}
	if m.refreshDock() {
		t.Errorf("只剩历史记录时应停拍(否则每秒空转)")
	}
	if m.state.Dock.Total != 0 || m.state.Dock.Running != 0 || len(m.state.Dock.Rows) != 0 {
		t.Errorf("停拍应清空折叠行: %+v", m.state.Dock)
	}
}

// TestDockFinishedDuringTurn:回合内任务结束 → 显示「N 条已结束」并继续吃到回合结束。
func TestDockFinishedDuringTurn(t *testing.T) {
	m := &Model{state: &State{Running: true}, onDock: func() DockInfo { return DockInfo{Total: 2} }}
	if !m.refreshDock() {
		t.Fatalf("回合运行中应续拍")
	}
	if !strings.Contains(dockLabel(m.state.Dock), "2 条已结束") {
		t.Errorf("回合内应显示已结束计数: %+v", m.state.Dock)
	}
	m.state.Running = false
	if m.refreshDock() {
		t.Errorf("回合结束且无运行中任务应停拍")
	}
}

// TestDockTickChain:节拍处理在「有任务」时续拍、收敛后停链且解除标志。
func TestDockTickChain(t *testing.T) {
	info := DockInfo{Total: 1, Running: 1, Latest: "npm test"}
	m := &Model{state: &State{}, onDock: func() DockInfo { return info }}
	if m.startDockTick() == nil {
		t.Fatalf("首次起链应返回节拍命令")
	}
	if m.startDockTick() != nil {
		t.Fatalf("重复起链应幂等(不重复起链)")
	}
	_, cmd := m.Update(dockTickMsg{})
	if cmd == nil {
		t.Fatalf("有任务时应续拍")
	}
	// 任务结束且回合已停:链收敛停,折叠行清空
	info = DockInfo{Total: 1}
	_, cmd = m.Update(dockTickMsg{})
	if cmd != nil {
		t.Errorf("无运行中任务且空闲时应停链")
	}
	if m.dockTicking {
		t.Errorf("停链后应解除 ticking 标志")
	}
	if m.state.Dock.Total != 0 || m.state.Dock.Running != 0 || len(m.state.Dock.Rows) != 0 {
		t.Errorf("停链应清空折叠行: %+v", m.state.Dock)
	}
	// 停链后可再次起链(下一回合/下一个任务)
	if m.startDockTick() == nil {
		t.Errorf("停链后应可再次起链")
	}
}

func TestStatusLineShowsDock(t *testing.T) {
	// 无任务:不占位(基线不变)
	base := stripColor(renderStatusLine(&State{Workspace: "w"}, 80))
	if strings.Contains(base, "后台") {
		t.Errorf("无任务时不应出现坞段:\n%s", base)
	}
	// 运行中:显眼提示 + 摘要
	out := stripColor(renderStatusLine(&State{Workspace: "w", Dock: DockInfo{Total: 2, Running: 1, Latest: "go test ./..."}}, 120))
	if !strings.Contains(out, "后台 1 运行中: go test ./...") {
		t.Errorf("运行中应显示坞折叠行:\n%s", out)
	}
	// 全结束:仅计数
	out = stripColor(renderStatusLine(&State{Workspace: "w", Dock: DockInfo{Total: 3}}, 120))
	if !strings.Contains(out, "后台 3 条已结束") {
		t.Errorf("全结束应显示条数:\n%s", out)
	}
	// 与队列段共存(顺序:队列 → 坞)
	out = stripColor(renderStatusLine(&State{Workspace: "w", Queue: []string{"a"}, Dock: DockInfo{Total: 1, Running: 1}}, 120))
	if strings.Index(out, "待发 1") > strings.Index(out, "后台 1 运行中") {
		t.Errorf("队列段应在坞段之前:\n%s", out)
	}
}

// —— S-P0-3 剩余切片:坞展开列表(F6)+ 选择/看输出/定向/停止 ——

// stubJobs / stubFanout 坞行来源(仅实现被 dockRows 用到的 List/ListAgents)。
type stubJobs struct{ jobs []sdk.Job }

func (s *stubJobs) Submit(cmdline string) (string, error) { return "", nil }
func (s *stubJobs) Run(fn sdk.JobFunc) (string, error)    { return "", nil }
func (s *stubJobs) List() []sdk.Job                       { return s.jobs }
func (s *stubJobs) Output(id string) (sdk.Job, bool)      { return sdk.Job{}, false }
func (s *stubJobs) Kill(id string) error                  { return nil }

type stubFanout struct{ agents []sdk.AgentHandle }

func (s *stubFanout) Agent(context.Context, string) (string, error) { return "", nil }
func (s *stubFanout) Parallel(context.Context, []string) []sdk.FanoutResult {
	return nil
}
func (s *stubFanout) Pipeline(context.Context, []string) ([]sdk.FanoutResult, string, error) {
	return nil, "", nil
}
func (s *stubFanout) SpawnAgent(context.Context, string) (string, error) { return "", nil }
func (s *stubFanout) Fork(context.Context, string) (string, error)       { return "", nil }
func (s *stubFanout) SendMessage(id, msg string) error                   { return nil }
func (s *stubFanout) ListAgents() []sdk.AgentHandle                      { return s.agents }
func (s *stubFanout) AgentStatus(id string) (sdk.AgentHandle, bool)      { return sdk.AgentHandle{}, false }
func (s *stubFanout) KillAgent(id string) error                          { return nil }

// TestDockRowsRunningFirst 行组装:运行中优先,其次新在前;摘要/错误/耗时投影正确。
func TestDockRowsRunningFirst(t *testing.T) {
	now := time.Now()
	js := &stubJobs{jobs: []sdk.Job{
		{ID: "j-done", Command: "go test ./...", State: sdk.JobDone, CreatedAt: now.Add(-3 * time.Second), DoneAt: now.Add(-1 * time.Second)},
		{ID: "j-run", Command: "npm build", State: sdk.JobRunning, CreatedAt: now.Add(-2 * time.Second)},
		{ID: "j-err", State: sdk.JobFailed, Error: "exit 1", CreatedAt: now.Add(-5 * time.Second), DoneAt: now.Add(-4 * time.Second)},
		{ID: "j-fn", Result: 42, State: sdk.JobDone, CreatedAt: now.Add(-6 * time.Second), DoneAt: now.Add(-5 * time.Second)},
	}}
	fo := &stubFanout{agents: []sdk.AgentHandle{
		{ID: "a-run", Input: "写测试", State: sdk.AgentRunning, CreatedAt: now.Add(-time.Second)},
		{ID: "a-done", Input: "查资料", State: sdk.AgentDone, CreatedAt: now.Add(-9 * time.Second)},
	}}
	rows := dockRows(js, fo)
	if len(rows) != 6 {
		t.Fatalf("应汇总全部记录: %d", len(rows))
	}
	// 运行中优先:前两条必须是 running(新在前:a-run 比 j-run 新)
	if rows[0].ID != "a-run" || rows[1].ID != "j-run" {
		t.Fatalf("运行中应优先且新在前: %+v", rows[:2])
	}
	if !rows[0].running || rows[0].Kind != "agent" {
		t.Fatalf("agent 行投影: %+v", rows[0])
	}
	if rows[2].ID != "j-done" {
		t.Fatalf("已结束按创建时间倒序: %+v", rows[2:])
	}
	// 函数任务摘要用结果;失败任务摘要带错误
	byID := map[string]DockRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if byID["j-fn"].Summary != "42" {
		t.Errorf("函数任务应用结果做摘要: %q", byID["j-fn"].Summary)
	}
	if !strings.Contains(byID["j-err"].Summary, "exit 1") {
		t.Errorf("失败摘要应含错误: %q", byID["j-err"].Summary)
	}
	if byID["j-done"].Dur == "" || !strings.HasSuffix(byID["j-done"].Dur, "s") {
		t.Errorf("已完成行应有耗时: %q", byID["j-done"].Dur)
	}
	// 两服务全缺 → 不 panic 不产行
	if got := dockRows(nil, nil); got != nil {
		t.Fatalf("服务缺失应返回 nil: %+v", got)
	}
}

// TestDockPanelRendering 面板:收起不占行;表头计数/键位提示;运行中高亮;窗口滚动提示。
func TestDockPanelRendering(t *testing.T) {
	// 收起态:零行(几何不变)
	if lines := dockPanelLines(&State{}, 80); lines != nil {
		t.Fatalf("收起态不应占行: %v", lines)
	}
	running := DockRow{Kind: "agent", ID: "a-1", State: "running", Summary: "写测试", Dur: "3.0s", running: true}
	done := DockRow{Kind: "job", ID: "j-2", State: "done", Summary: "npm test", Dur: "1.2s"}
	s := &State{DockOpen: true, DockRows: []DockRow{running, done}}
	lines := dockPanelLines(s, 100)
	if len(lines) != 3 { // 表头 + 2 行
		t.Fatalf("面板应有表头 + 2 行: %v", lines)
	}
	head := stripColor(lines[0])
	for _, want := range []string{"后台坞 1 运行中 / 2 条", "↑/↓ 选择", "Enter/o 看输出", "s 定向", "x 停止", "F6 收起"} {
		if !strings.Contains(head, want) {
			t.Errorf("表头应含 %q: %q", want, head)
		}
	}
	// 选中行标记 + 行内容
	if !strings.Contains(stripColor(lines[1]), "▸") || !strings.Contains(stripColor(lines[1]), "[agent]") {
		t.Errorf("选中行应有标记与种类: %q", stripColor(lines[1]))
	}
	if !strings.Contains(stripColor(lines[2]), "  [job  ]") || !strings.Contains(stripColor(lines[2]), "npm test") {
		t.Errorf("非选中行应缩进显示: %q", stripColor(lines[2]))
	}
	// 停止武装:表头改提示,不再显示键位帮助
	s.DockArm = true
	head = stripColor(dockPanelLines(s, 100)[0])
	if !strings.Contains(head, "再按 x/Enter 确认停止 a-1") {
		t.Errorf("武装态应显示确认提示: %q", head)
	}
	if strings.Contains(head, "↑/↓ 选择") {
		t.Errorf("武装态不应再显示键位帮助: %q", head)
	}
	// 空列表:表头 + 明确空态行(不弹空白面板)
	empty := dockPanelLines(&State{DockOpen: true}, 80)
	if len(empty) != 2 || !strings.Contains(stripColor(empty[0]), "无记录") {
		t.Fatalf("空列表应有显式空态: %v", empty)
	}
	// 超窗滚动:选中末项时窗口跟着下移并给省略提示
	var many []DockRow
	for i := 0; i < maxDockRows+4; i++ {
		many = append(many, DockRow{Kind: "job", ID: fmt.Sprintf("j-%02d", i), State: "done"})
	}
	s2 := &State{DockOpen: true, DockRows: many, DockSel: len(many) - 1}
	lines = dockPanelLines(s2, 100)
	if len(lines) != maxDockRows+2 { // 表头 + 窗口 + 省略行
		t.Fatalf("超窗应滚动取窗: %d", len(lines))
	}
	body := stripColor(strings.Join(lines[1:], "\n"))
	if !strings.Contains(body, "j-09") || strings.Contains(body, "j-00") {
		t.Errorf("窗口应以选区为锚: %q", body)
	}
	if !strings.Contains(body, "另有") {
		t.Errorf("应提示窗口外条数: %q", body)
	}
}

// TestDockPanelKeyToggleAndNav F6 展开/收起 + ↑/↓ 选择 + 面板打开时输入框不被敲进按键。
func TestDockPanelKeyToggleAndNav(t *testing.T) {
	rows := []DockRow{
		{Kind: "agent", ID: "a1", running: true},
		{Kind: "job", ID: "j2"},
	}
	m := &Model{state: &State{DockRows: rows}}
	// F6 展开:当场拉一帧 + 起链
	m = &Model{state: &State{}, onDock: func() DockInfo { return DockInfo{Rows: rows, Total: 2, Running: 1} }}
	_, cmd := m.Update(keyMsg(tea.KeyF6))
	if !m.state.DockOpen || m.state.DockSel != 0 {
		t.Fatalf("F6 应展开并复位选区: %+v", m.state)
	}
	if cmd == nil {
		t.Errorf("展开时应起刷新链")
	}
	// ↓ 选中第二行;↑ 回到第一行
	m.state.DockRows = rows
	m.Update(keyMsg(tea.KeyDown))
	if m.state.DockSel != 1 {
		t.Fatalf("↓ 应下移选中: %d", m.state.DockSel)
	}
	m.Update(keyMsg(tea.KeyUp))
	if m.state.DockSel != 0 {
		t.Fatalf("↑ 应上移选中: %d", m.state.DockSel)
	}
	// 面板打开时普通字符不进输入框(模态;草稿文本保留)
	m.state.Input = "草稿"
	m.Update(keyMsg('z'))
	if m.state.Input != "草稿" {
		t.Fatalf("面板打开时按键不应改草稿: %q", m.state.Input)
	}
	// Esc 收起(草稿仍在)
	m.Update(keyMsg(tea.KeyEscape))
	if m.state.DockOpen || m.state.Input != "草稿" {
		t.Fatalf("Esc 应收起且草稿保持: open=%v input=%q", m.state.DockOpen, m.state.Input)
	}
}

// TestDockPanelActions 看输出(经宿主 /jobs output → pager)/ 定向(草稿发出)/ 停止(二次确认)。
func TestDockPanelActions(t *testing.T) {
	rows := []DockRow{{Kind: "agent", ID: "a1", running: true}, {Kind: "job", ID: "j2", running: true}}
	var killed []string
	var steered []string
	m := &Model{state: &State{DockOpen: true, DockRows: rows, Input: "换个思路"},
		onDockOutput: func(id string) (*DocPager, error) {
			return NewTextPager(TextPagerSpec{Title: "输出 " + id, Lines: []string{"行1", "行2"}}), nil
		},
		onDockKill:  func(id string) (string, error) { killed = append(killed, id); return "已终止 " + id, nil },
		onDockSteer: func(id, msg string) error { steered = append(steered, id+"="+msg); return nil },
		onDock:      func() DockInfo { return DockInfo{Rows: rows, Total: 2, Running: 2} },
	}
	// Enter = 看输出(浮层直接入栈,不经 program.Send)
	m.Update(keyMsg(tea.KeyEnter))
	if m.state.Doc == nil || !strings.Contains(m.state.Doc.Title, "a1") {
		t.Fatalf("Enter 应打开输出浮层: %+v", m.state.Doc)
	}
	m.state.Doc = nil
	// s = 定向(草稿发出并清空)
	m.Update(keyMsg('s'))
	if len(steered) != 1 || steered[0] != "a1=换个思路" {
		t.Fatalf("s 应定向草稿给选中子代理: %v", steered)
	}
	if m.state.Input != "" {
		t.Fatalf("定向后草稿应清空(已发出): %q", m.state.Input)
	}
	// x 第一次只武装,第二次真终止
	m.Update(keyMsg('x'))
	if !m.state.DockArm || len(killed) != 0 {
		t.Fatalf("第一次 x 应只武装: arm=%v killed=%v", m.state.DockArm, killed)
	}
	m.Update(keyMsg('x'))
	if m.state.DockArm || len(killed) != 1 || killed[0] != "a1" {
		t.Fatalf("第二次 x 应终止选中项: arm=%v killed=%v", m.state.DockArm, killed)
	}
	// 武装后按其它键解除(防误触:选了别的行不会误杀)
	m.Update(keyMsg('x'))
	m.Update(keyMsg(tea.KeyDown))
	if m.state.DockArm {
		t.Fatalf("移动选中应解除武装")
	}
	m.Update(keyMsg(tea.KeyEnter)) // 此时选中 j2(武装已解除)= 看输出,不是 kill
	if len(killed) != 1 {
		t.Fatalf("解除武装后 Enter 不应终止: %v", killed)
	}
}

// TestDockActionsDegradeExplicitly 未装配服务/任务行定向/空草稿:一律显式提示,不静默。
func TestDockActionsDegradeExplicitly(t *testing.T) {
	// 空草稿定向
	m := &Model{state: &State{DockOpen: true, DockRows: []DockRow{{Kind: "agent", ID: "a1"}}},
		onDockSteer: func(id, msg string) error { t.Fatal("空草稿不应发定向"); return nil }}
	m.Update(keyMsg('s'))
	if !hasLineKind(m.state.Lines, "error", "定向需要先在输入框写内容") {
		t.Fatalf("空草稿应显式提示: %+v", m.state.Lines)
	}
	// 任务行定向(host 侧报错)
	a := &App{model: &Model{state: &State{DockRows: []DockRow{{Kind: "job", ID: "j1"}}}}}
	if err := a.dockSteer("j1", "x"); err == nil || !strings.Contains(err.Error(), "仅适用于子代理") {
		t.Fatalf("任务行定向应显式拒绝: %v", err)
	}
	// 未装配命令/服务
	a2 := &App{model: &Model{state: &State{}}, cmds: newMemRegistry(), c: &stubCtx{svc: map[string]any{}}}
	if _, err := a2.dockOutputCmd("j1"); err == nil || !strings.Contains(err.Error(), "host-jobs") {
		t.Fatalf("命令未装配应提示所需插件: %v", err)
	}
	if _, err := a2.dockKillCmd("j1"); err == nil || !strings.Contains(err.Error(), "host-jobs") {
		t.Fatalf("命令未装配应提示所需插件: %v", err)
	}
	if err := a2.dockSteer("a1", "x"); err == nil || !strings.Contains(err.Error(), "host-fanout") {
		t.Fatalf("服务未装配应提示所需插件: %v", err)
	}
}

// TestDockPanelGeometry 面板占位计入主区高度(不压没会话流;收起恢复)。
func TestDockPanelGeometry(t *testing.T) {
	// 会话流需长到填满主区(会话超窗时主区才是满行,几何对账才有意义)
	var lines []Line
	for i := 0; i < 60; i++ {
		lines = append(lines, Line{Kind: "assistant", Text: fmt.Sprintf("第 %d 行", i)})
	}
	base := &State{Workspace: "w", Lines: lines}
	open := &State{Workspace: "w", Lines: lines, DockOpen: true, DockRows: []DockRow{
		{Kind: "agent", ID: "a1", State: "running", running: true}, {Kind: "job", ID: "j2", State: "done"},
	}}
	outBase := Render(base, 100, 30)
	outOpen := Render(open, 100, 30)
	if !strings.Contains(stripColor(outOpen), "后台坞 1 运行中 / 2 条") {
		t.Fatalf("展开态应含面板: %q", stripColor(outOpen))
	}
	if strings.Contains(stripColor(outBase), "后台坞") {
		t.Fatalf("收起态不应含面板: %q", stripColor(outBase))
	}
	// 总行数(终端高度)不变:面板占位从主区高度扣,不撑破终端
	if n1, n2 := len(strings.Split(outBase, "\n")), len(strings.Split(outOpen, "\n")); n1 != n2 {
		t.Fatalf("面板占位应从主区扣除(总行数不变): %d vs %d", n1, n2)
	}
	// 主区会话窗口高:展开态恰少面板行数(3 = 表头 + 2 行)
	if base.sessionWin != open.sessionWin+3 {
		t.Fatalf("主区应少 3 行: %d vs %d", base.sessionWin, open.sessionWin)
	}
}

// hasLineKind 断言存在指定种类且含关键字的会话行。
func hasLineKind(lines []Line, kind, sub string) bool {
	for _, l := range lines {
		if l.Kind == kind && strings.Contains(l.Text, sub) {
			return true
		}
	}
	return false
}

// keyMsg 构造按键消息(与实际按键路径同源:经 Update → handleKey)。
func keyMsg(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code}
}
