// Package tui 提供 ui-tui-app 的界面实现(bubbletea v2 + lipgloss)。
// 状态机与渲染为纯逻辑,可脱离终端单测;bubbletea 壳仅做事件分发。
package tui

import (
	"strings"
	"time"
	"unicode"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Line 会话流展示行。
type Line struct {
	Kind      string // user|assistant|tool|meta|error
	Text      string
	Full      string // S2.2:折叠行全文(tool 结果超长时存原文,展开切换)
	Streaming bool   // assistant 流式增量中
}

// State TUI 展示状态(事件驱动,线程安全由调用方保证)。
type State struct {
	Lines          []Line
	Running        bool
	Model          string
	Profile        string
	Error          string
	Input          string
	Cursor         int
	LastTool       string
	Sandbox        string         // 沙箱档位显示(read-only|workspace-write|full-access)
	PendingConfirm string         // 非空 = 有待确认的危险操作(确认弹层)
	Suggestions    []string       // 输入 / 前缀时的命令提示(注册表过滤结果,渲染于输入行下方)
	Pick           *Pick          // 非空 = 交互式选择器激活(↑/↓ 移动,Enter 应用)
	PickDismissed  bool           // Esc/断点后抑制自动激活,直至输入变化
	QuitArmed      bool           // 双按退出武装中:第一次 Ctrl+C(输入为空)后待第二次确认(输入行提示)
	SpinnerIdx     int            // 思考动画帧索引(回合运行中 tick 推进)
	Workspace      string         // 当前工作区显示(启动时 cwd 目录名)
	Thinking       string         // 思考等级显示(off 空;Tab/Shift+Tab 切换)
	Session        string         // 当前会话标签(显示名优先,无名称回退 id;空 = 未命名主会话;状态栏)
	Stats          sdk.UsageStats // 会话 token 统计(回合结束刷新;状态栏显示使用率/缓存命中率)

	// ScrollOffset 会话流上滚物理行数(0 = 跟随最新;>0 = 浏览历史),渲染时钳制。
	ScrollOffset int

	// sessionWin 最近一次渲染的会话流可视窗口行数(渲染写回;滚动条命中/拖动用,
	// 与渲染 scrollMetrics 同几何——避免估算偏差导致点不到滑块)。0 = 未渲染。
	sessionWin int

	// 鼠标划选(拖选复制):选区以全局物理行 + rune 列(0 基,不含 pad)描述。
	// 渲染按 selRange 反色高亮;释放后保留高亮(已复制),再次按下/Esc 清除。
	SelActive bool
	SelRow0   int // 按下点行
	SelCol0   int // 按下点列
	SelRow1   int // 当前(拖动)行
	SelCol1   int // 当前(拖动)列

	// 滚动条展示增强:回底指示/hover 高亮/auto-hide。
	// BarShownAt 最近一次滚动条相关交互时刻(滚轮/拖动/回底);渲染判定
	// time.Since 超过 barHideDelay 且非 hover 时隐藏滚动条。HoverBar 鼠标悬停轨道列。
	BarShownAt time.Time
	HoverBar   bool

	// 会话内搜索:SearchQuery 非空 = 搜索激活。SearchHits 为命中逻辑行(Lines 索引,
	// 大小写不敏感子串匹配原文),SearchIdx 为当前定位命中(跳转/高亮)。
	SearchQuery string
	SearchHits  []int
	SearchIdx   int

	// flatN 最近一次渲染展平出的会话流物理行数(含折行;ScrollBy 上限钳制用,渲染后刷新)。
	flatN int

	// S2.2 工具结果行折叠:foldOpen 记录已展开的逻辑行(lineIdx → true)。
	// 默认折叠(摘要单行显示);鼠标单击结果行展开查看完整内容(Line.Full)。
	FoldOpen map[int]bool

	// S1.3 输入增强:历史/undo/编辑快捷键。
	// CmdHistory 本进程已提交的斜杠命令(命令不入会话 Lines,单独记录补历史源;
	// 普通 user 消息经 Lines 提取)。histActive+histIdx = 历史翻页态(false 未翻),
	// histDraft = 开始翻页前的输入草稿(HistNext 越过最新一条后还原)。undo/redo 为
	// 输入框文本快照栈(编辑前入栈;提交/清空即清栈;相邻同型编辑合并为一个 undo 步)。
	CmdHistory []string
	histActive bool
	histCur    int      // 当前显示条目在 histRev 中的下标(0 = 最新一条)
	histRev    []string // 翻页开始重建的历史(最新在前);草稿态不活跃时为空
	histDraft  string
	undo       []ustep
	redo       []ustep
	lastEdit   time.Time
	lastKind   byte
}

// ustep 输入撤销步(文本 + 光标位置快照)。
type ustep struct {
	text string
	cur  int
}

// ApplySessionEvent 把会话事件推进到展示状态(纯逻辑,可测)。
func (s *State) ApplySessionEvent(ev *sdk.SessionEvent) {
	switch ev.Kind {
	case sdk.EventUserMessage:
		if u, ok := ev.Payload.(sdk.UserMessage); ok {
			s.Lines = append(s.Lines, Line{Kind: "user", Text: u.Content})
		}
	case sdk.EventAssistantChunk:
		if cev, ok := ev.Payload.(sdk.LLMStreamEvent); ok && cev.Delta != "" {
			s.appendStreaming(cev.Delta)
		}
	case sdk.EventAssistantMessage:
		if a, ok := ev.Payload.(sdk.AssistantMessage); ok {
			s.finishStreaming(a.Content)
			// 工具行不在 assistant 消息处铺(避免与随后的 EventToolCall 双写重复):
			// 模型声明调用后由 agent-loop 逐条发 EventToolCall/EventToolResult,
			// 工具行只在那两处生成(S2.1 去重)。a.ToolCalls 仅保留于会话日志。
		}
	case sdk.EventToolCall:
		if tc, ok := ev.Payload.(sdk.ToolCallEvent); ok {
			s.Lines = append(s.Lines, Line{Kind: "tool", Text: toolCallText(sdk.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})})
			s.LastTool = tc.Name // 状态栏"执行工具"提示
		}
	case sdk.EventToolResult:
		if r, ok := ev.Payload.(sdk.ToolResultEvent); ok {
			s.LastTool = "" // 工具完成:回“思考中”

			kind := "tool"
			label := "✓"
			full := r.Content
			if r.Error != "" {
				kind = "error"
				label = "✗"
				full = r.Error
			}
			// 摘要行(S2.1 单行截断);完整内容存 Full(上限防爆,会话日志仍是事实源)。
			// S2.2 折叠:结果行可点击展开/收起(见 state.foldOpen)。
			sum := full
			if len(sum) > 160 {
				sum = sum[:160] + "…"
			}
			sum = strings.ReplaceAll(sum, "\n", " ") // 摘要强制单行(多段折行由展开查看)
			if len(sum) > 160 {
				sum = sum[:160] + "…"
			}
			stored := full
			if len(stored) > foldFullLimit {
				stored = stored[:foldFullLimit] + "…(截断,完整见会话日志)"
			}
			s.Lines = append(s.Lines, Line{Kind: kind, Text: label + " " + r.Name + ": " + sum, Full: stored})
		}
	case sdk.EventTurnEnd:
		s.Lines = append(s.Lines, Line{Kind: "meta", Text: "—— 轮次结束 ——"})
		s.LastTool = ""
	case sdk.EventAgentError:
		if err, ok := ev.Payload.(error); ok {
			s.Error = err.Error()
			s.Lines = append(s.Lines, Line{Kind: "error", Text: "agent error: " + err.Error()})
		}
	}
}

// ApplyStatus 处理 agent/status(running/idle)。
func (s *State) ApplyStatus(status string) {
	switch status {
	case "running":
		s.Running = true
	case "idle":
		s.Running = false
	}
}

// SetError 设置错误(输入处理失败等)。
// ApplyConfirmPrompt 显示确认弹层。
func (s *State) ApplyConfirmPrompt(prompt string) {
	s.PendingConfirm = prompt
}

// ResolveConfirm 用户答复后清除弹层,返回决定。
func (s *State) ResolveConfirm(ok bool) bool {
	s.PendingConfirm = ""
	return ok
}

func (s *State) SetError(msg string) {
	s.Error = msg
	s.Lines = append(s.Lines, Line{Kind: "error", Text: msg})
}

func (s *State) appendStreaming(delta string) {
	if n := len(s.Lines); n > 0 && s.Lines[n-1].Kind == "assistant" && s.Lines[n-1].Streaming {
		s.Lines[n-1].Text += delta
		return
	}
	s.Lines = append(s.Lines, Line{Kind: "assistant", Text: delta, Streaming: true})
}

func (s *State) finishStreaming(final string) {
	if n := len(s.Lines); n > 0 && s.Lines[n-1].Kind == "assistant" && s.Lines[n-1].Streaming {
		s.Lines[n-1].Text = final
		s.Lines[n-1].Streaming = false
		return
	}
	if final != "" {
		s.Lines = append(s.Lines, Line{Kind: "assistant", Text: final})
	}
}

// InsertText 光标处插入一段文本(粘贴支持;bracketed paste 单行化:
// 命令行语义,换行转空格——API key/URL 复制常带尾换行,防破坏渲染)。
func (s *State) InsertText(text string) {
	text = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(text)
	t := []rune(text)
	if len(t) == 0 {
		return
	}
	s.snapshotUndo('t')
	b := []rune(s.Input)
	out := make([]rune, 0, len(b)+len(t))
	out = append(out, b[:s.Cursor]...)
	out = append(out, t...)
	out = append(out, b[s.Cursor:]...)
	s.Input = string(out)
	s.Cursor += len(t)
}

// InsertRune 输入字符。
func (s *State) InsertRune(r rune) {
	s.snapshotUndo('i')
	b := []rune(s.Input)
	b = append(b[:s.Cursor], append([]rune{r}, b[s.Cursor:]...)...)
	s.Input = string(b)
	s.Cursor++
}

// CursorLeft/Right 光标左右移动(边界钳制)。
func (s *State) CursorLeft() {
	if s.Cursor > 0 {
		s.Cursor--
	}
}

func (s *State) CursorRight() {
	n := len([]rune(s.Input))
	if s.Cursor < n {
		s.Cursor++
	}
}

// CursorHome/End 光标跳输入框头/尾。
func (s *State) CursorHome() { s.Cursor = 0 }
func (s *State) CursorEnd()  { s.Cursor = len([]rune(s.Input)) }

// Delete 删除光标处字符(末尾 no-op)。
func (s *State) Delete() {
	n := len([]rune(s.Input))
	if s.Cursor >= n {
		return
	}
	s.snapshotUndo('d')
	b := []rune(s.Input)
	s.Input = string(append(b[:s.Cursor], b[s.Cursor+1:]...))
}

// Backspace 删除光标前一字符。
func (s *State) Backspace() {
	if s.Cursor <= 0 || s.Input == "" {
		return
	}
	s.snapshotUndo('b')
	b := []rune(s.Input)
	s.Input = string(append(b[:s.Cursor-1], b[s.Cursor:]...))
	s.Cursor--
}

// ClearInput 提交/清空后复位输入区:文本与光标清空,undo/redo 栈清空(提交即
// 丢弃撤销历史),历史翻页指针复位(草稿不再需要)。
func (s *State) ClearInput() {
	s.Input = ""
	s.Cursor = 0
	s.undo = nil
	s.redo = nil
	s.histActive = false
	s.histCur = 0
	s.histRev = nil
	s.histDraft = ""
}

// —— S1.3 输入增强:历史(Ctrl+P/N)、undo/redo(Ctrl+Z/Ctrl+Shift+Z)、
// kill(Ctrl+K/U)、按词移动(Alt+←/→)。状态机纯逻辑,可脱离终端单测。 ——

// RecordCmd 记录一条已提交的斜杠命令(历史源补充;命令不入会话 Lines)。
// 上限 200 条,超出丢最旧。
func (s *State) RecordCmd(cmd string) {
	if cmd == "" || !strings.HasPrefix(cmd, "/") {
		return
	}
	s.CmdHistory = append(s.CmdHistory, cmd)
	if len(s.CmdHistory) > 200 {
		s.CmdHistory = append([]string(nil), s.CmdHistory[len(s.CmdHistory)-200:]...)
	}
}

// histList 输入历史源(时间序,去相邻重复):会话 user 消息(Lines,含跨会话重放)
// + 本进程已提交命令(CmdHistory,较新置后)。
func (s *State) histList() []string {
	var out []string
	last := ""
	add := func(t string) {
		if t == "" || t == last {
			return
		}
		last = t
		out = append(out, t)
	}
	for _, ln := range s.Lines {
		if ln.Kind == "user" && !ln.Streaming {
			add(ln.Text)
		}
	}
	for _, c := range s.CmdHistory {
		add(c)
	}
	return out
}

// HistPrev Ctrl+P 上一条历史:首次记录当前输入为草稿并重建历史(最新在前);
// 已到最老则停在原地。
func (s *State) HistPrev() bool {
	if !s.histActive {
		hist := s.histList()
		if len(hist) == 0 {
			return false
		}
		s.histDraft = s.Input
		s.histActive = true
		s.histCur = -1
		s.histRev = make([]string, len(hist))
		for i, h := range hist {
			s.histRev[len(hist)-1-i] = h // 反转:最新在前
		}
	}
	if s.histCur+1 >= len(s.histRev) {
		return false // 已到最老
	}
	s.histCur++
	s.setFromHist(s.histRev[s.histCur])
	return true
}

// HistNext Ctrl+N 下一条历史:越过最新一条后还原开始翻页前的草稿。
func (s *State) HistNext() bool {
	if !s.histActive {
		return false
	}
	if s.histCur == 0 { // 回到最新边界:还原草稿并退出翻页态
		s.histActive = false
		s.Input = s.histDraft
		s.Cursor = len([]rune(s.Input))
		return true
	}
	s.histCur--
	s.setFromHist(s.histRev[s.histCur])
	return true
}

// setFromHist 历史条目填入输入框(光标到尾;置 PickDismissed 防 / 开头自动激活选择器)。
func (s *State) setFromHist(t string) {
	s.Input = t
	s.Cursor = len([]rune(t))
	s.PickDismissed = true
}

// snapshotUndo 编辑动作前入栈(相邻同型 800ms 内合并为一个 undo 步,防逐字符爆栈);
// 任何新编辑使 redo 失效。
func (s *State) snapshotUndo(kind byte) {
	now := time.Now()
	if s.lastKind == kind && now.Sub(s.lastEdit) < 800*time.Millisecond {
		return
	}
	s.pushUndo(s.Input, s.Cursor)
	s.lastKind = kind
	s.lastEdit = now
	s.redo = nil
}

// pushUndo 入撤销栈(上限 256,超出丢最旧)。
func (s *State) pushUndo(text string, cur int) {
	if len(s.undo) >= 256 {
		s.undo = append([]ustep(nil), s.undo[1:]...)
	}
	s.undo = append(s.undo, ustep{text: text, cur: cur})
}

// Undo Ctrl+Z:回退一个编辑步(当前文本进 redo 栈)。无栈返回 false。
func (s *State) Undo() bool {
	if len(s.undo) == 0 {
		return false
	}
	s.redo = append(s.redo, ustep{text: s.Input, cur: s.Cursor})
	last := s.undo[len(s.undo)-1]
	s.undo = s.undo[:len(s.undo)-1]
	s.Input = last.text
	s.Cursor = last.cur
	s.PickDismissed = true
	return true
}

// Redo Ctrl+Shift+Z:重做被撤销的编辑步。
func (s *State) Redo() bool {
	if len(s.redo) == 0 {
		return false
	}
	last := s.redo[len(s.redo)-1]
	s.redo = s.redo[:len(s.redo)-1]
	s.pushUndo(s.Input, s.Cursor)
	s.Input = last.text
	s.Cursor = last.cur
	s.PickDismissed = true
	return true
}

// KillToEnd Ctrl+K:删除光标到行尾,返回是否有删除。
func (s *State) KillToEnd() bool {
	n := len([]rune(s.Input))
	if s.Cursor >= n {
		return false
	}
	s.snapshotUndo('k')
	r := []rune(s.Input)
	s.Input = string(r[:s.Cursor])
	return true
}

// KillToStart Ctrl+U:删除光标到行首,返回是否有删除。
func (s *State) KillToStart() bool {
	if s.Cursor <= 0 {
		return false
	}
	s.snapshotUndo('u')
	r := []rune(s.Input)
	s.Input = string(r[s.Cursor:])
	s.Cursor = 0
	return true
}

// WordLeft Alt+←:光标按词左移(跨过紧邻空白再跨一个词)。
func (s *State) WordLeft() {
	r := []rune(s.Input)
	i := s.Cursor
	if i <= 0 {
		return
	}
	i--
	for i > 0 && unicode.IsSpace(r[i]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(r[i-1]) {
		i--
	}
	s.Cursor = i
}

// WordRight Alt+→:光标移动到下一词词首——当前在词中先走到词尾,
// 再跨过空白停在词首(词内或词后均前移一词)。
func (s *State) WordRight() {
	r := []rune(s.Input)
	n := len(r)
	i := s.Cursor
	if i >= n {
		return
	}
	if !unicode.IsSpace(r[i]) {
		for i < n && !unicode.IsSpace(r[i]) {
			i++ // 词中:先走到词尾
		}
	}
	for i < n && unicode.IsSpace(r[i]) {
		i++ // 跨过空白
	}
	s.Cursor = i
}

// toolCallText 工具调用展示行。
func toolCallText(tc sdk.ToolCall) string {
	args := tc.Arguments
	if len(args) > 80 {
		args = args[:80] + "…"
	}
	return "⚙ " + tc.Name + " " + args
}

// foldFullLimit Line.Full 存储上限(超长工具结果防 TUI 内存/渲染爆;会话日志是事实源)。
const foldFullLimit = 4096

// ToggleFold 切换工具结果行的展开/折叠(仅结果行有 Full 时有效)。返回切换是否生效。
func (s *State) ToggleFold(lineIdx int) bool {
	if lineIdx < 0 || lineIdx >= len(s.Lines) {
		return false
	}
	if s.Lines[lineIdx].Full == "" {
		return false // 无全文(未折叠行/调用行):不响应
	}
	if s.FoldOpen == nil {
		s.FoldOpen = map[int]bool{}
	}
	s.FoldOpen[lineIdx] = !s.FoldOpen[lineIdx]
	return true
}

// foldOpenOf 逻辑行是否展开(结果行 Full 非空默认折叠)。
func (s *State) foldOpenOf(lineIdx int) bool {
	return s.FoldOpen != nil && s.FoldOpen[lineIdx]
}

// ApplyReplay 重放历史事件到展示层(/session switch 切换会话后)。
// 多轮结构:跨轮时插一条细分隔线(轻量 meta,替代全宽“轮次结束”行——重放几十轮
// 若每轮都铺全宽分隔仍显吵;细线只标轮界,实时回合保留原“轮次结束”分隔感)。
func (s *State) ApplyReplay(ev *sdk.SessionEvent) {
	if ev.Kind == sdk.EventTurnEnd {
		return
	}
	// 新一轮 user 消息且当前已有内容且末行不是分隔线 → 插细分隔线(轮界)。
	if ev.Kind == sdk.EventUserMessage && len(s.Lines) > 0 {
		if last := s.Lines[len(s.Lines)-1]; last.Kind != "meta" {
			s.Lines = append(s.Lines, Line{Kind: "meta", Text: turnDivider})
		}
	}
	s.ApplySessionEvent(ev)
}

// turnDivider 轮次细分隔线(弱化 meta;跨轮结构一眼可分)。
const turnDivider = "· ─ ─ ─ ·"

// visible 滚动窗口:按 ScrollOffset 取最近 n 行(offset=0 跟随最新;>0 上滚看历史),钳制。
func (s *State) visible(n int) []Line {
	total := len(s.Lines)
	if total == 0 || n <= 0 {
		return nil
	}
	win := n
	if win > total {
		win = total // 窗口大于总行数:全显(防负索引)
	}
	offset := s.ScrollOffset
	maxOff := total - win
	if offset > maxOff {
		offset = maxOff
	}
	s.ScrollOffset = offset // 钳制写回(渲染与状态一致)
	return s.Lines[total-offset-win : total-offset]
}

// ScrollBy 会话流相对滚动:delta>0 上滚看历史,delta<0 回底部;height=当前窗口高(钳制用)。
// 滚动以物理行为单位(与可见窗口一致;上限用最近展平行数 flatN,首次未知时退化按 Lines)。
func (s *State) ScrollBy(delta, height int) {
	if height < 1 {
		height = 1
	}
	s.ScrollOffset += delta
	if s.ScrollOffset < 0 {
		s.ScrollOffset = 0
	}
	lim := s.flatN - height
	if lim < 0 {
		lim = len(s.Lines) - height // 展平未知(未渲染/流式追加中):按逻辑行数粗钳,渲染时精确
	}
	if lim > 0 && s.ScrollOffset > lim {
		s.ScrollOffset = lim
	}
}

// RecentLines 供测试:返回当前展示行文本。
func (s *State) RecentLines(n int) []string {
	out := make([]string, 0, len(s.Lines))
	for _, l := range s.visible(n) {
		out = append(out, l.Kind+": "+l.Text)
	}
	return out
}

// InputText 命令判定:以 / 开头。
func (s *State) IsCommand() bool {
	return strings.HasPrefix(s.Input, "/")
}

// searchHitLine 逻辑行是否命中搜索;SearchIdx 当前命中是否该行。
func (s *State) searchHitLine(lineIdx int) bool {
	if s.SearchQuery == "" {
		return false
	}
	for _, h := range s.SearchHits {
		if h == lineIdx {
			return true
		}
	}
	return false
}

// searchCurLine 当前定位命中逻辑行(-1 无)。
func (s *State) searchCurLine() int {
	if s.SearchQuery == "" || s.SearchHits == nil {
		return -1
	}
	if s.SearchIdx < 0 || s.SearchIdx >= len(s.SearchHits) {
		return -1
	}
	return s.SearchHits[s.SearchIdx]
}
