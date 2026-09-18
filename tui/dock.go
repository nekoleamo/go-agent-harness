// dock.go:S-P0-3 TUI 后台坞(任务/子代理常驻折叠行)。
//
// 目标(对标 Hermes Agent 的实时子代理坞 / Codex App 的并行 agent 面):
// 后台任务与子代理运行时,状态栏常驻一行「后台 N 运行中: <最新>」,不必先敲命令才知道有东西在跑。
//
// 分工:
//   - 数据面 = 宿主命令 /jobs(host-internal-commands):list/output/kill,三端同源;
//   - 环境面 = 本文件:状态栏折叠行 + 1s 节拍刷新(仅在「回合运行中或存在任务」时续拍,空闲零开销)。
//
// 刷新节拍只在 Running 或 Total>0 时续拍:空闲且无任务时链自动停;
// 下一次 submit 由 App 主动 kick 一帧重新起链(见 app.go submit / agentDoneMsg)。
package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// DockInfo 后台坞状态(计数 + 最新一条运行中任务摘要 + 展开列表行快照)。
type DockInfo struct {
	Running int    // 运行中的任务+子代理数
	Total   int    // 记录总数(含已完成)
	Latest  string // 最新一条运行中任务摘要(命令/任务描述;空 = 无)
	Rows    []DockRow
}

// DockRow 坞列表一行(展开态选择列表;动作仍交宿主命令 /jobs,见 App.dockOpenSelected 等)。
type DockRow struct {
	Kind    string // agent | job
	ID      string
	State   string // running | done | failed | killed ...
	Summary string
	Dur     string // 耗时(已完成用 Done-Created、运行中用 now-Created;不可知为空)

	running bool
	created time.Time
}

// maxDockRows 展开列表最大行数(超出滚动窗口);超过即用 pickWindow 以选区为锚滚动。
const maxDockRows = 6

// dockRows 从两个宿主服务组装坞行(运行中优先,其次新创建在前)。
// 仅取选择列表所需的**薄投影**(id/状态/摘要/耗时):输出渲染与终止动作均走宿主命令
// /jobs(单一事实源),TUI 不重复实现。两个服务均未装配 → nil(不占位)。
func dockRows(js sdk.JobService, fo sdk.FanoutService) []DockRow {
	now := time.Now()
	var rows []DockRow
	if js != nil {
		for _, j := range js.List() {
			sum := j.Command
			if sum == "" {
				if j.Result != nil {
					sum = fmt.Sprintf("%v", j.Result)
				} else {
					sum = "(函数任务)"
				}
			}
			if j.Error != "" {
				sum += " ← " + j.Error
			}
			rows = append(rows, DockRow{
				Kind: "job", ID: j.ID, State: string(j.State), Summary: sum,
				Dur:     dockDur(j.CreatedAt, j.DoneAt, string(j.State) == "running", now),
				running: j.State == sdk.JobRunning, created: j.CreatedAt,
			})
		}
	}
	if fo != nil {
		for _, a := range fo.ListAgents() {
			sum := a.Input
			if a.Error != "" {
				sum += " ← " + a.Error
			}
			rows = append(rows, DockRow{
				Kind: "agent", ID: a.ID, State: string(a.State), Summary: sum,
				Dur:     dockDur(a.CreatedAt, time.Time{}, string(a.State) == "running", now),
				running: string(a.State) == "running", created: a.CreatedAt,
			})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].running != rows[j].running {
			return rows[i].running
		}
		return rows[i].created.After(rows[j].created)
	})
	return rows
}

// dockDur 行耗时(不可知 = 空串;与状态栏时长同口径 fmtDur)。
func dockDur(created, done time.Time, running bool, now time.Time) string {
	if created.IsZero() {
		return ""
	}
	end := done
	if end.IsZero() {
		if !running {
			return ""
		}
		end = now
	}
	if end.Before(created) {
		return ""
	}
	return fmtDur(end.Sub(created))
}

// dockPanelLines 展开坞面板(纯函数,S-P0-3 剩余切片):表头(动作提示/停止确认)+ 行窗口。
// 收起态(DockOpen=false)返回 nil(不占行高);行数计入主区高度扣除(见 render.go)。
func dockPanelLines(s *State, width int) []string {
	if !s.DockOpen {
		return nil
	}
	rows := s.DockRows
	running := 0
	for _, r := range rows {
		if r.running {
			running++
		}
	}
	head := fmt.Sprintf("后台坞 %d 运行中 / %d 条", running, len(rows))
	if len(rows) == 0 {
		head = "后台坞:无记录"
	}
	var lines []string
	if s.DockArm {
		// 停止武装(防误触):高亮提示再按一次 x/Enter 才真发 kill
		head += " " + styleError.Render(fmt.Sprintf("⚠ 再按 x/Enter 确认停止 %s(Esc 取消)", dockSelID(s)))
	} else {
		head += "  " + styleMeta.Render("↑/↓ 选择 · Enter/o 看输出 · s 定向(发输入框内容) · x 停止 · Esc/F6 收起")
	}
	lines = append(lines, styleMeta.Render(" "+head))
	if len(rows) == 0 {
		lines = append(lines, styleMeta.Render("   (无运行中或历史后台任务:子代理/后台任务启动后自动出现)"))
		return lines
	}
	sel := s.DockSel
	start, end := pickWindow(len(rows), sel, maxDockRows)
	for i := start; i < end; i++ {
		r := rows[i]
		mark := "  "
		if i == sel {
			mark = "▸ "
		}
		// 行:标记 + [种类] id 状态 耗时 摘要(固定列宽便于扫读)
		body := fmt.Sprintf("[%-5s] %-14s %-8s %6s  %s",
			r.Kind, truncateVisible(r.ID, 14), truncateVisible(r.State, 8), r.Dur, r.Summary)
		body = truncateVisible(body, width-4) // 摘要撑满时不吃掉右边界
		if i == sel {
			lines = append(lines, stylePick.Render(mark+body))
		} else if r.running {
			lines = append(lines, styleBusy.Render(mark+body))
		} else {
			lines = append(lines, styleMeta.Render(mark+body))
		}
	}
	if end < len(rows) || start > 0 {
		lines = append(lines, styleMeta.Render(fmt.Sprintf("   …另有 %d 条(↑/↓ 滚动;全部明细 /jobs)", len(rows)-(end-start))))
	}
	return lines
}

// dockSelID 当前选中行 id(无选中/空列表 = "-";确认提示文案用)。
func dockSelID(s *State) string {
	if s.DockSel < 0 || s.DockSel >= len(s.DockRows) {
		return "-"
	}
	return s.DockRows[s.DockSel].ID
}

// dockInterval 坞刷新节拍。1s 足够(任务状态变化不要求逐帧精度),
// 又比事件驱动多订阅一条链路简单:节拍自带「运行中→结束」的收敛,无需感知 job/done。
const dockInterval = time.Second

// dockTickMsg 坞刷新节拍(自续链:条件满足才续发)。
type dockTickMsg struct{}

// dockTick 生成一帧节拍命令。
func dockTick() tea.Cmd {
	return tea.Tick(dockInterval, func(time.Time) tea.Msg { return dockTickMsg{} })
}

// startDockTick S-P0-3:在回合开始时顺手起坞刷新链(幂等);nil = 链已在跑。
// 由 Update 返回该 Cmd(不得从 Update 内直调 program.Send——bubbletea 的 msgs
// 通道无缓冲,Send 在 Update 协程内会自锁)。
func (m *Model) startDockTick() tea.Cmd {
	if m.dockTicking {
		return nil
	}
	m.dockTicking = true
	return dockTick()
}

// refreshDock 刷新一帧;返回是否续拍。
// 续拍条件:仍有运行中的任务/子代理,或回合仍在运行(回合中随时可能出现新任务),
// 或坞展开中(用户正在看 → 即使全已完成也需保持列表最新)。
// 已完成记录不构成续拍理由——JobService 保留历史记录,否则链永不停(每秒空转重绘);
// 链停时清空折叠行(已结束计数无环境价值,明细走 /jobs)。
func (m *Model) refreshDock() bool {
	if m.onDock != nil {
		m.state.Dock = m.onDock()
	}
	// 选区钳制:行数变少(任务结束/回收)时不能越界
	if n := len(m.state.Dock.Rows); m.state.DockSel >= n {
		m.state.DockSel = n - 1
	}
	if m.state.DockSel < 0 {
		m.state.DockSel = 0
	}
	if m.state.Dock.Running > 0 || m.state.Running || m.state.DockOpen {
		return true
	}
	m.state.Dock = DockInfo{}
	return false
}

// dockLabel 折叠行文案(纯函数,便于测):
//   - 运行中 → 「后台 2 运行中: npm test」(摘要超宽截断);
//   - 全部结束 → 「后台 3 条已结束」(提示可回看,不占视觉权重)。
//     无记录 → 空串(不占位)。
func dockLabel(d DockInfo) string {
	if d.Total <= 0 {
		return ""
	}
	if d.Running <= 0 {
		return fmt.Sprintf("后台 %d 条已结束", d.Total)
	}
	s := fmt.Sprintf("后台 %d 运行中", d.Running)
	if d.Latest != "" {
		s += ": " + truncateVisible(d.Latest, 22)
	}
	return s
}

// —— 展开态按键(模态)——

// handleDockKey S-P0-3 坞面板按键(模态:非本面板按键不透传输入框,草稿文本仍在 s.Input)。
// 返回 tea.Cmd:仅「收起后恢复节拍/重新起链」路径需要(其余 nil)。
// 注意:本函数在 Update 协程内执行,严禁 program.Send;打开浮层直接写 m.state.Doc
// (与 PagerMsg 分支同一语义:已构造好的浮层直接入栈)。
func (m *Model) handleDockKey(k tea.Key) tea.Cmd {
	key := k.Code
	switch key {
	case tea.KeyEscape, tea.KeyF6, 'q', 'Q':
		m.closeDock()
		return nil
	case tea.KeyUp, 'k':
		if m.state.DockSel > 0 {
			m.state.DockSel--
		}
		m.state.DockArm = false
		return nil
	case tea.KeyDown, 'j':
		if m.state.DockSel < len(m.state.DockRows)-1 {
			m.state.DockSel++
		}
		m.state.DockArm = false
		return nil
	case tea.KeyEnter, 'o', 'O':
		if m.state.DockArm { // 武装态下 Enter = 确认停止(与 x 同义)
			return m.dockKillSelected()
		}
		if p := m.dockOutputSelected(); p != nil {
			m.state.Doc = p // 全屏浮层:坞仍在展开态,关闭浮层后回到坞
		}
		return nil
	case 'x', 'X':
		if m.state.DockArm {
			return m.dockKillSelected()
		}
		if m.state.DockSel < len(m.state.DockRows) { // 第一次:武装(防误触;再按才真发)
			m.state.DockArm = true
		}
		return nil
	case 's', 'S':
		m.dockSteerSelected()
		return nil
	}
	return nil // 其余键忽略(模态:不落到输入框)
}

// closeDock 收起面板(草稿保持不动)。收起后:若仍有运行中任务/回合,节拍链继续;
// 否则链会在下一帧自然停(见 refreshDock 续拍条件)。
func (m *Model) closeDock() {
	m.state.DockOpen = false
	m.state.DockArm = false
}

// dockOutputSelected 打开选中项输出(经宿主命令 /jobs output,单一事实源)。
func (m *Model) dockOutputSelected() *DocPager {
	if m.onDockOutput == nil || m.state.DockSel >= len(m.state.DockRows) {
		return nil
	}
	row := m.state.DockRows[m.state.DockSel]
	p, err := m.onDockOutput(row.ID)
	if err != nil {
		m.state.Lines = append(m.state.Lines, Line{Kind: "error", Text: err.Error()})
		return nil
	}
	m.state.Lines = append(m.state.Lines, Line{Kind: "meta", Text: "查看后台输出: " + row.ID})
	return p
}

// dockSteerSelected 定向:把输入框内容发给选中子代理(M9.3 SendMessage;草稿即消息)。
// 失败(任务行/未运行/未装配)一律显式回显错误,不静默丢弃用户输入。
func (m *Model) dockSteerSelected() {
	if m.state.DockSel >= len(m.state.DockRows) || m.onDockSteer == nil {
		return
	}
	row := m.state.DockRows[m.state.DockSel]
	msg := strings.TrimSpace(m.state.Input)
	if msg == "" {
		m.state.Lines = append(m.state.Lines, Line{Kind: "error",
			Text: "定向需要先在输入框写内容(草稿不丢:F6 收起后可编辑再发)"})
		return
	}
	if err := m.onDockSteer(row.ID, msg); err != nil {
		m.state.Lines = append(m.state.Lines, Line{Kind: "error", Text: err.Error()})
		return
	}
	m.state.Lines = append(m.state.Lines, Line{Kind: "meta",
		Text: "已定向给 " + row.ID + ": " + truncateVisible(msg, 60)})
	m.state.ClearInput() // 定向 = 发出(与普通提交同语义:草稿已消费)
}

// dockKillSelected 终止选中项(经宿主命令 /jobs kill:任务优先、回退子代理;需二次确认)。
func (m *Model) dockKillSelected() tea.Cmd {
	if m.state.DockSel >= len(m.state.DockRows) || m.onDockKill == nil {
		m.state.DockArm = false
		return nil
	}
	row := m.state.DockRows[m.state.DockSel]
	out, err := m.onDockKill(row.ID)
	m.state.DockArm = false
	if err != nil {
		m.state.Lines = append(m.state.Lines, Line{Kind: "error", Text: err.Error()})
		return nil
	}
	m.state.Lines = append(m.state.Lines, Line{Kind: "meta", Text: out})
	// 立即刷新一帧(不等节拍):终止后行状态/计数马上对齐
	m.refreshDock()
	if cmd := m.startDockTick(); cmd != nil {
		return cmd
	}
	return nil
}
