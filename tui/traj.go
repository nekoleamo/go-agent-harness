package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// S-P0-1 TUI 侧轨迹/可观测视图模型(input = 同一份会话事件账本,只做纯聚合)。
// 与 Web 侧 web-src/src/traj.ts 同源同口径(Web 端先行交付,TUI 端为同一事件的另一个 presenter):
// 不订阅额外通道、不新增后端契约、不落盘。前端只呈现"过程与成本"(回合 → 步 → 工具),
// 不复制消息正文——会话内容仍看流视图(轨迹不做成第二个会话流)。
//
// 纪律(与 Web 一致):
//   - 时长只用事件自带 TS 计算。**进行中的 turn/step/tool 不给时长**(显示"进行中"),
//     不拿本地时钟补齐(否则陈旧回合会被编造出越来越大的时长)。
//   - 工具归属 = 它所在 step(事件里没有更细的嵌套关系,不臆造层级)。
//   - turn 边界由 user/message(起点)与 turn/end(终点)派生;turn/start 是可选标记
//     (旧会话没有它),不作为必要输入。
//
// 数字口径:TUI 沿用本端既有 fmtK(状态栏同源,1024 进制);Web fmtTok 走 1000 进制,
// 两端各自与所在端一致,不在轨迹里另起一套。
type TrajTool struct {
	ID       string
	Name     string
	Args     string
	Status   string // called | ok | err
	Error    string
	OutBytes int
	CallTS   time.Time
	ResultTS time.Time
}

type TrajStep struct {
	Seq     uint64
	TS      time.Time
	EndTS   time.Time
	ToolIDs []string
}

type TrajUsage struct {
	Model      string
	Prompt     int
	Completion int
	Cached     int
	Requests   int
}

type TrajTurn struct {
	Index     int
	Seq       uint64
	TS        time.Time
	EndTS     time.Time
	Reason    string // turn/end 载荷(done|cancelled|max_steps|…;空 = 进行中)
	Steps     []*TrajStep
	ToolIDs   []string
	Tools     map[string]*TrajTool
	Usage     TrajUsage
	User      string
	Assistant string
}

// Traj 轨迹模型。Turns 用指针切片(cur 需跨 append 稳定指向同一回合)。
// lastSeq 用于去重:重放路径(ApplyReplay 提前返回 turn/end 后仍交给 ApplySessionEvent)
// 会让同一事件进入两次,按会话 seq 单调性丢弃重复。
type Traj struct {
	Turns   []*TrajTurn
	cur     *TrajTurn
	lastSeq uint64
}

// NewTraj 空轨迹。
func NewTraj() *Traj { return &Traj{} }

// Reset 清空(会话切换)。
func (t *Traj) Reset() {
	t.Turns = nil
	t.cur = nil
	t.lastSeq = 0
}

func newTrajTurn(index int, seq uint64, ts time.Time, user string) *TrajTurn {
	return &TrajTurn{
		Index: index, Seq: seq, TS: ts, User: user,
		Tools: map[string]*TrajTool{},
	}
}

// ensure 取当前回合;不存在时按需开隐式回合(只有过程帧、没有 user/message 的会话)。
func (t *Traj) ensure(ts time.Time, seq uint64) *TrajTurn {
	if t.cur == nil {
		turn := newTrajTurn(len(t.Turns)+1, seq, ts, "")
		t.Turns = append(t.Turns, turn)
		t.cur = turn
	}
	return t.cur
}

func lastTrajStep(turn *TrajTurn) *TrajStep {
	if len(turn.Steps) == 0 {
		return nil
	}
	return turn.Steps[len(turn.Steps)-1]
}

// findTool 跨回合回溯定位工具(结果帧可能晚于回合结束到达,如取消后收尾)。
func (t *Traj) findTool(id string) *TrajTool {
	for i := len(t.Turns) - 1; i >= 0; i-- {
		if tool, ok := t.Turns[i].Tools[id]; ok {
			return tool
		}
	}
	return nil
}

// Push 消费一帧会话事件(实时与重放共用)。载荷缺失不 panic:轨迹宁少不错。
func (t *Traj) Push(ev *sdk.SessionEvent) {
	if ev == nil {
		return
	}
	if ev.Seq != 0 {
		if ev.Seq <= t.lastSeq {
			return // 重复投递(重放二次进入)/乱序旧帧
		}
		t.lastSeq = ev.Seq
	}
	switch ev.Kind {
	case sdk.EventUserMessage:
		content := ""
		if um, ok := ev.Payload.(sdk.UserMessage); ok {
			content = um.Content
		}
		cur := t.cur
		// turn/start 已开回合(user/message 前发该帧):回填用户消息,不再开新回合;
		// 否则(旧会话无 turn/start)= 以用户消息为回合起点。
		if cur != nil && cur.User == "" && len(cur.Steps) == 0 && len(cur.ToolIDs) == 0 &&
			cur.Assistant == "" && cur.EndTS.IsZero() {
			cur.User = content
			cur.Seq = ev.Seq
			cur.TS = ev.TS
			return
		}
		turn := newTrajTurn(len(t.Turns)+1, ev.Seq, ev.TS, content)
		t.Turns = append(t.Turns, turn)
		t.cur = turn
	case sdk.EventTurnStart:
		if t.cur == nil { // 已有 cur(user/message 先行)不重复开
			turn := newTrajTurn(len(t.Turns)+1, ev.Seq, ev.TS, "")
			t.Turns = append(t.Turns, turn)
			t.cur = turn
		}
	case sdk.EventTurnEnd:
		turn := t.ensure(ev.TS, ev.Seq)
		turn.EndTS = ev.TS
		if reason, ok := ev.Payload.(string); ok && reason != "" {
			turn.Reason = reason
		} else {
			turn.Reason = "done"
		}
		t.cur = nil
	case sdk.EventStepStart:
		turn := t.ensure(ev.TS, ev.Seq)
		turn.Steps = append(turn.Steps, &TrajStep{Seq: ev.Seq, TS: ev.TS})
	case sdk.EventStepEnd:
		turn := t.cur
		if turn == nil {
			return
		}
		if step := lastTrajStep(turn); step != nil && step.EndTS.IsZero() {
			step.EndTS = ev.TS
		}
	case sdk.EventToolCall:
		tc, ok := ev.Payload.(sdk.ToolCallEvent)
		if !ok || tc.ID == "" {
			return
		}
		turn := t.ensure(ev.TS, ev.Seq)
		step := lastTrajStep(turn)
		if step == nil { // 无 step 的工具调用(异常流/裁剪日志):补隐式 step,防丢失归属
			step = &TrajStep{Seq: ev.Seq, TS: ev.TS}
			turn.Steps = append(turn.Steps, step)
		}
		tool := &TrajTool{
			ID: tc.ID, Name: tc.Name, Args: tc.Arguments,
			Status: "called", CallTS: ev.TS,
		}
		turn.Tools[tool.ID] = tool
		turn.ToolIDs = append(turn.ToolIDs, tool.ID)
		step.ToolIDs = append(step.ToolIDs, tool.ID)
	case sdk.EventToolResult:
		tr, ok := ev.Payload.(sdk.ToolResultEvent)
		if !ok {
			return
		}
		tool := t.findTool(tr.CallID)
		if tool == nil {
			return
		}
		tool.ResultTS = ev.TS
		if tr.Error != "" {
			tool.Status = "err"
			tool.Error = tr.Error
			tool.OutBytes = len(tr.Error)
		} else {
			tool.Status = "ok"
			tool.OutBytes = len(tr.Content)
		}
	case sdk.EventAssistantMessage:
		am, ok := ev.Payload.(sdk.AssistantMessage)
		if !ok || am.Content == "" || t.cur == nil {
			return
		}
		if t.cur.Assistant == "" {
			t.cur.Assistant = am.Content
		} else {
			t.cur.Assistant += "\n\n" + am.Content
		}
	case sdk.EventUsage:
		ue, ok := ev.Payload.(sdk.UsageEvent)
		if !ok || t.cur == nil {
			return
		}
		t.cur.Usage.Requests++
		t.cur.Usage.Prompt += ue.Usage.PromptTokens
		t.cur.Usage.Completion += ue.Usage.CompletionTokens
		t.cur.Usage.Cached += ue.Usage.CachedTokens
		if ue.Model != "" {
			t.cur.Usage.Model = ue.Model
		}
	default:
		// assistant/chunk(流式增量)等:以落定事件为准,轨迹侧不累积
	}
}

// —— 度量(进行中一律返回 false / 不编造) ——

func trajElapsed(from, to time.Time) (int64, bool) {
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return 0, false
	}
	return to.Sub(from).Milliseconds(), true
}

// TurnMS 回合时长(未结束 = 第二返回值为 false)。
func (t *TrajTurn) TurnMS() (int64, bool) { return trajElapsed(t.TS, t.EndTS) }

// StepMS 步时长(未结束 = false)。
func (s *TrajStep) StepMS() (int64, bool) { return trajElapsed(s.TS, s.EndTS) }

// ToolMS 工具耗时(调用到结果;结果未回 = false)。
func (t *TrajTool) ToolMS() (int64, bool) { return trajElapsed(t.CallTS, t.ResultTS) }

// TrajStats 单回合计数(与 Web turnStats 同字段)。
type TrajStats struct {
	Steps   int
	Tools   int
	Failed  int
	Pending int // 未回填结果的工具数
	Tokens  int // prompt + completion
	MS      int64
	HasMS   bool
}

// Stats 回合计数。
func (t *TrajTurn) Stats() TrajStats {
	st := TrajStats{Steps: len(t.Steps), Tools: len(t.ToolIDs)}
	for _, id := range t.ToolIDs {
		tool := t.Tools[id]
		if tool == nil {
			continue
		}
		switch tool.Status {
		case "err":
			st.Failed++
		case "called":
			st.Pending++
		}
	}
	st.Tokens = t.Usage.Prompt + t.Usage.Completion
	st.MS, st.HasMS = t.TurnMS()
	return st
}

// TrajOverview 顶部概览(回合数/总时长/累计 token 与缓存)。
type TrajOverview struct {
	Turns   int
	Running bool
	MS      int64
	HasMS   bool // 全部回合已结束才有总时长(有进行中回合 = 不给,不编造)
	Tokens  int
	Cached  int
}

// Overview 汇总概览。
func (t *Traj) Overview() TrajOverview {
	ov := TrajOverview{Turns: len(t.Turns), Running: t.cur != nil, HasMS: true}
	var ms int64
	for _, turn := range t.Turns {
		if d, ok := turn.TurnMS(); ok {
			ms += d
		} else {
			ov.HasMS = false
		}
		ov.Tokens += turn.Usage.Prompt + turn.Usage.Completion
		ov.Cached += turn.Usage.Cached
	}
	ov.MS = ms
	return ov
}

// —— 格式化(轨迹视图内部口径;Web 侧对应 fmtMs/fmtBytes/clip) ——

func trajDur(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	secs := float64(ms) / 1000
	if secs < 60 {
		return fmtDur(time.Duration(ms) * time.Millisecond) // 复用 TUI 既有秒口径
	}
	return fmt.Sprintf("%dm%02ds", int(secs)/60, int(secs)%60)
}

func trajBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/1024/1024)
	}
}

// trajClip 单行摘要(多行折叠为空格;按显示宽度截断,CJK 安全)。
func trajClip(s string, max int) string {
	one := strings.Join(strings.Fields(s), " ")
	if one == "" {
		return ""
	}
	return truncWidthRunes(one, max)
}

func trajCachePct(over TrajOverview) int {
	if over.Tokens <= 0 || over.Cached <= 0 {
		return 0
	}
	return over.Cached * 100 / over.Tokens
}

// Render 渲染轨迹报告(文本 pager 用;纯函数,不触碰状态)。
func (t *Traj) Render() []string {
	var out []string
	out = append(out, "轨迹 · 可观测(本机事件账本派生:turn → step → tool;时长只取事件时间戳)")
	out = append(out, "")
	if len(t.Turns) == 0 {
		return append(out, "暂无轨迹:本会话还没有回合事件(空回合不记)。")
	}
	over := t.Overview()
	head := fmt.Sprintf("概览  %d 回合 · 总时长 %s · %s tok",
		over.Turns, trajTotalDur(over), fmtK(over.Tokens))
	if pct := trajCachePct(over); pct > 0 {
		head += fmt.Sprintf(" · 缓存 %s(%d%%)", fmtK(over.Cached), pct)
	}
	if over.Running {
		head += " · 最后回合进行中"
	}
	out = append(out, head, "")
	for i := len(t.Turns) - 1; i >= 0; i-- {
		out = append(out, renderTrajTurn(t.Turns[i])...)
	}
	out = append(out, "说明  进行中的 turn/step/tool 不给时长(不拿本地时钟补齐);",
		"      工具归属按所在 step;用量按 session/usage 事件累加;正文内容见会话流视图。")
	return out
}

func trajTotalDur(over TrajOverview) string {
	if !over.HasMS {
		return "计算中(有回合未结束)"
	}
	return trajDur(over.MS)
}

func renderTrajTurn(turn *TrajTurn) []string {
	st := turn.Stats()
	state := "进行中"
	if !turn.EndTS.IsZero() {
		state = turn.Reason
		if state == "" {
			state = "done"
		}
		if st.HasMS {
			state = trajDur(st.MS) + " (" + state + ")"
		}
	}
	line := fmt.Sprintf("#%-3d %s · %d 步 · %d 工具", turn.Index, state, st.Steps, st.Tools)
	if st.Failed > 0 || st.Pending > 0 {
		line += fmt.Sprintf("(失败 %d · 未回填 %d)", st.Failed, st.Pending)
	}
	if st.Tokens > 0 {
		line += fmt.Sprintf(" · %s tok", fmtK(st.Tokens))
	}
	if turn.Usage.Model != "" {
		line += " · " + turn.Usage.Model
	}
	out := []string{line}
	if u := trajClip(turn.User, 60); u != "" {
		out = append(out, "      提问  "+u)
	}
	for i, step := range turn.Steps {
		head := fmt.Sprintf("      步 %d  ", i+1)
		if d, ok := step.StepMS(); ok {
			head += fmt.Sprintf("%s · %d 工具", trajDur(d), len(step.ToolIDs))
		} else {
			head += fmt.Sprintf("进行中 · %d 工具", len(step.ToolIDs))
		}
		out = append(out, head)
		out = append(out, renderTrajStepTools(turn, step)...)
	}
	if len(turn.Tools) > 0 && len(turn.Steps) == 0 {
		out = append(out, renderTrajStepTools(turn, nil)...)
	}
	return out
}

// renderTrajStepTools 一条工具一行:状态 + 耗时/出参体积 + 名称 + 参数摘要。
// step 为 nil 表示无 step 归属的工具(按回合内出现顺序铺)。
func renderTrajStepTools(turn *TrajTurn, step *TrajStep) []string {
	ids := turn.ToolIDs
	if step != nil {
		ids = step.ToolIDs
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		tool := turn.Tools[id]
		if tool == nil {
			continue
		}
		status := map[string]string{"ok": "ok", "err": "err", "called": "…"}[tool.Status]
		// 名称紧跟状态(左端可辨识);指标(耗时/体积/错误)在中,参数摘要末尾(超宽由 pager 截断)。
		row := fmt.Sprintf("            %-3s %-14s", status, tool.Name)
		var meta []string
		if d, ok := tool.ToolMS(); ok {
			meta = append(meta, trajDur(d))
		}
		switch {
		case tool.Status == "err":
			if e := trajClip(tool.Error, 40); e != "" {
				meta = append(meta, e)
			}
		case tool.OutBytes > 0:
			meta = append(meta, trajBytes(tool.OutBytes))
		}
		if len(meta) > 0 {
			row += "  " + strings.Join(meta, "  ")
		}
		if a := trajClip(tool.Args, 48); a != "" {
			row += "  " + a
		}
		out = append(out, row)
	}
	return out
}
