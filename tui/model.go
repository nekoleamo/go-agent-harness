// bubbletea 壳:事件分发与输入处理;渲染逻辑在 render.go,状态推进在 state.go。
package tui

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// sessionEventMsg 会话事件推送(经 session/event 广播)。
type sessionEventMsg struct{ ev *sdk.SessionEvent }

type statusMsg struct{ status string }

// mouseEventMsg 鼠标事件转发消息。⚠️ View.OnMouse 不能原样返回 MouseMsg:
// MouseWheelMsg/MouseClickMsg 等实现 MouseMsg 接口,tea 系统层 case MouseMsg 会再次捕获
// 同一个消息→无限重发死循环(实测 24 万/秒滚轮风暴、界面失控)。必须包装成自定义类型。
type mouseEventMsg struct{ ev tea.MouseMsg }

type agentDoneMsg struct{ err error }

type disarmQuitMsg struct{} // 双按退出武装超时解除(tea.Tick 单次延迟发送)

type barHideMsg struct{} // 滚动条 auto-hide:最近交互超时后触发重绘隐藏(tea.Tick 单次)

type confirmMsg struct{ prompt string }

// Model 实现 tea.Model。
type Model struct {
	state *State
	w, h  int
	quit  bool

	quitArmed bool // 双按退出武装中:第一次 Ctrl+C(输入为空)后待第二次确认

	lastWheel time.Time // 滚轮事件节流:kitty 平滑滚轮/触摸板一次手势可发成百上千事件,
	// 不经节流会“滚一下停不下来”(每事件都滚)→ 限制处理频率(wheelThrottle)。
	wheelGesture int      // 本滚轮手势累计事件数(超时未滚或方向反转 = 新手势清零)
	wheelDir     int      // 本滚轮手势方向(1=上,-1=下;方向反转即新手势——防 cap 挡住用户换向)
	dragBar      bool     // 滚动条滑块拖动中(命中滑块按下;motion 按比例跟手)
	dragY        int      // 拖动锚点 Y(按下时鼠标在会话流区的行号)
	dragOff      int      // 拖动锚点 ScrollOffset(按下时)
	selRows      []string // 鼠标划选期间展平行文本缓存(press 时取,避免 motion 高频重复 flatten)
	selLineIdx   []int    // 与 selRows 平行的逻辑行索引(press 缓存;单击折叠命中用)
	selMoved     bool     // 本次划选是否有位移(释放时判定点击 vs 拖动)
	skipView     bool     // 本 Update 未改渲染输入:View 返回缓存,快速消化滚轮事件风暴
	// (风暴事件逐个进队列,即使节流丢弃也走完整 Update→View;缓存让丢弃事件几乎零开销,
	// 键盘/Ctrl+C 不必在成百滚轮事件后排长队)。
	cacheContent   string // View 缓存(仅 skipView 命中时复用)
	cacheSet       bool
	cacheW, cacheH int

	onSubmit        func(input string)               // 普通输入提交(注入)
	onCommand       func(cmd string) error           // 命令处理(注入)
	onConfirm       func(ok bool)                    // 确认答复(注入;见 app.Confirm)
	onCancel        func()                           // 取消进行中的回合(注入;Esc 触发)
	hints           func(prefix string) []sdk.Option // 命令选项(注入;前缀=去掉 / 后的输入)
	levels          func(name string) []sdk.ArgLevel // 命令参数级定义(注入;枚举/自由级)
	onThinkingCycle func(dir int)                    // Tab/Shift+Tab 思考等级循环(注入:dir=1 前进,-1 后退)
	onStats         func() sdk.UsageStats            // 会话 token 统计拉取(注入;回合结束刷新状态栏)
}

// spinInterval 思考动画帧间隔。
type spinnerMsg struct{}

const spinInterval = 120 * time.Millisecond

// quitConfirmWindow 双按退出确认窗口:第一次 Ctrl+C(输入为空)武装后,
// 窗口内再按一次才彻底退出;超时未按自动解除(防误触)。
const quitConfirmWindow = 2 * time.Second

// 滚轮节流与手势上限:
//   - wheelStep:滚轮一格滚动行数(步进小=滚动更细腻/丝滑;↑/↓ 逐行精确浏览不受影响);
//   - wheelThrottle:两次实际滚动最小间隔(平滑滚轮/触摸板高频事件;越短帧率越高越丝滑,
//     33Hz×2 行 ≈ 66 行/s — 与 50ms×3 行同吞吐但视觉平滑一倍);
//   - gestureReset:距上次实际滚动超过该时长 = 新的一次手势(累计清零);
//   - gestureCap:单次连续手势(含触控板惯性/长滑)最多处理事件数,超出即丢弃——
//     防“滚一下一直滚、无法打断”(约 gestureCap×wheelStep 行/手势)。
//   - barHideDelay:滚动条交互后静止超时(隐藏;鼠标悬停期间不隐藏)。
const (
	wheelStep     = 2
	wheelThrottle = 30 * time.Millisecond
	gestureReset  = 350 * time.Millisecond
	gestureCap    = 24
	barHideDelay  = 1500 * time.Millisecond
)

func (m *Model) Init() tea.Cmd {
	// 首帧即启动 tick(回合未运行时 Update 不再续发,自动停)
	return tea.Every(spinInterval, func(time.Time) tea.Msg { return spinnerMsg{} })
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case sessionEventMsg:
		m.state.ApplySessionEvent(msg.ev)
	case statusMsg:
		m.state.ApplyStatus(msg.status)
	case agentDoneMsg:
		// 回合结束:刷新 token 统计(上下文使用率/缓存命中率,状态栏)
		if m.onStats != nil {
			m.state.Stats = m.onStats()
		}
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				// 用户主动取消(Esc):提示而非报错
				m.state.Lines = append(m.state.Lines, Line{Kind: "meta", Text: "回合已取消"})
			} else {
				m.state.SetError("回合失败: " + msg.err.Error())
			}
		}
		m.state.Running = false
		// P4-1 消息队列:回合成功结束且有排队 → 自动发下一条(每次一条,保证会话串行)。
		// 取消(Esc)与回合失败不续发——用户意图停止/需先处理,队列保留供 Alt+Up/Esc 取回。
		if msg.err == nil && len(m.state.Queue) > 0 && m.onSubmit != nil {
			m.submitQueuedNext()
		}
	case confirmMsg:
		m.state.ApplyConfirmPrompt(msg.prompt)
	case spinnerMsg:
		// 思考动画:仅回合运行中续发 tick(空闲停,不浪费重绘)
		if m.state.Running {
			m.state.SpinnerIdx++
			return m, tea.Every(spinInterval, func(time.Time) tea.Msg { return spinnerMsg{} })
		}
	case tea.PasteMsg:
		// bracketed paste:整段插入(终端 Cmd+V/中键粘贴);与字符输入同语义
		m.state.PickDismissed = false
		m.state.InsertText(msg.Content)
		m.syncHints()
	case mouseEventMsg:
		// 鼠标事件(经 View.OnMouse 包装转发到 Update,见 mouseEventMsg 注释)
		switch e := msg.ev.(type) {
		case tea.MouseWheelMsg:
			m.handleWheel(e.Button)
		case tea.MouseClickMsg:
			m.handleMousePress(e.Mouse())
		case tea.MouseMotionMsg:
			m.handleMouseMotion(e.Mouse())
		case tea.MouseReleaseMsg:
			m.handleMouseRelease(e.Mouse())
		}
		cmd = m.markBar() // 滚动条显示计时重置并排 auto-hide tick(渲染按时间/hover 判定隐藏)
	case barHideMsg:
		// 仅触发重绘:渲染按 BarShownAt/HoverBar 判定滚动条隐藏(消息本身无状态变更)
	case editorDoneMsg:
		// 外部编辑器(Ctrl+G)结束:读回结果回填输入框(tea.ExecProcess 自动临时退出
		// alt-screen 交还终端给编辑器;恢复后收到本消息)
		m.finishExternal(msg)
	case tea.KeyMsg:
		cmd = m.handleKey(msg) // Ctrl+C 武装时携带超时解除命令
	case disarmQuitMsg:
		// 双按退出超时:自动解除武装(再按一次已不再退出,回到初始态)
		m.disarmQuit()
	}
	if m.quit {
		return m, tea.Quit
	}
	return m, cmd
}

func (m *Model) View() tea.View {
	// 缓存:滚轮风暴丢弃事件(skipView)未改渲染输入 → 直接复用上次渲染结果,
	// 免去每次全量 Render/flatten(风暴数百事件逐个重渲染会拖慢事件队列,键盘/Ctrl+C 排队)。
	content := m.cacheContent
	if !(m.skipView && m.cacheSet && m.w == m.cacheW && m.h == m.cacheH) {
		content = Render(m.state, m.w, m.h)
		m.cacheContent = content
		m.cacheSet = true
		m.cacheW, m.cacheH = m.w, m.h
	}
	m.skipView = false
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion // 启用鼠标:滚轮滚动会话流(点击/释放/滚轮)
	// v2 鼠标事件须经 View.OnMouse 转发才送达 Update;但绝不能原样返回 MouseMsg
	// (MouseWheelMsg 等实现 MouseMsg 接口,tea 系统层 case MouseMsg 会再次捕获同一消息,
	// 无限重发死循环——实测 24 万/秒滚轮风暴)。包装成自定义类型 mouseEventMsg 绕过。
	v.OnMouse = func(msg tea.MouseMsg) tea.Cmd {
		return func() tea.Msg { return mouseEventMsg{msg} }
	}
	return v
}

// handleWheel 滚轮事件(经 View.OnMouse 包装的 mouseEventMsg 送达)。
// 节流(wheelThrottle)防高频事件逐格滚;单次手势上限(gestureCap)防平滑滚轮/触摸板
// 惯性长滑“滚不停、无法打断”;方向反转 = 新手势(换向立即响应,不被 cap 卡住)。
func (m *Model) handleWheel(btn tea.MouseButton) {
	now := time.Now()
	// 节流:一次手势可发大量事件,50ms 内只滚一次。被节流丢弃的事件仍占 Update 队列——
	// 标记 skipView(本次未改渲染输入),View 返回缓存让风暴事件近乎零开销地快速消化。
	if !m.lastWheel.IsZero() && now.Sub(m.lastWheel) < wheelThrottle {
		m.skipView = true
		return
	}
	if btn != tea.MouseWheelUp && btn != tea.MouseWheelDown {
		return // 仅处理上下滚(水平滚轮无绑定)
	}
	dir := 1
	if btn == tea.MouseWheelDown {
		dir = -1
	}
	// 手势边界:距上次实际滚动超过 gestureReset(新一次手势)或方向反转
	// (用户换向滚——上滚 24 行后立即下滚必须响应,不被手势上限卡住)→ 累计清零。
	if now.Sub(m.lastWheel) > gestureReset || (m.wheelDir != 0 && dir != m.wheelDir) {
		m.wheelGesture = 0
		m.wheelDir = dir
	}
	// 选择器激活:滚轮移动选项(不参与会话流滚动手势)。
	if m.state.Pick != nil {
		m.lastWheel = now
		if dir > 0 && m.state.Pick.Cursor > 0 {
			m.state.Pick.Cursor--
		} else if dir < 0 && m.state.Pick.Cursor < len(m.state.Pick.Items)-1 {
			m.state.Pick.Cursor++
		}
		return
	}
	// 单次手势滚动上限:同向连续手势最多滚 gestureCap 行,超出即丢弃剩余事件——
	// 防平滑滚轮/触摸板惯性一次手势“滚不停、无法打断”。
	if m.wheelGesture >= gestureCap {
		m.skipView = true
		return
	}
	m.lastWheel = now
	m.wheelGesture++
	m.wheelDir = dir
	if dir > 0 {
		m.state.ScrollBy(wheelStep, m.h-4) // 上滚看历史(一格 wheelStep 行,系统手感)
	} else {
		m.state.ScrollBy(-wheelStep, m.h-4) // 下滚回最新
	}
}

// scrollbarCol 滚动条所在列(渲染:内容 colW=m.w-3 宽 + 1 空格 + bar 贴右缘,bar 在 w-2)。
// 命中区取 bar 及前一空格列(w-3..w-2),便于点按。
func (m *Model) scrollbarCol() int {
	c := m.w - 3
	if c < 0 {
		return 0
	}
	return c
}

// scrollbarGeom 会话流窗口高 win 与滚动条几何(与渲染 scrollMetrics 同式):
// 内容行 total、窗口 win → 滑块高 thumb、当前滑块顶 top(0=轨道顶)。total<=win 返回不可滚。
func (m *Model) scrollbarGeom(total, win int) (thumb, top int, ok bool) {
	maxOff := total - win
	if maxOff <= 0 {
		return 0, 0, false
	}
	thumb = win * win / total
	if thumb < 1 {
		thumb = 1
	}
	if thumb > win {
		thumb = win
	}
	off := m.state.ScrollOffset
	if off < 0 {
		off = 0
	}
	if off > maxOff {
		off = maxOff
	}
	top = (maxOff - off) * (win - thumb) / maxOff
	if top > win-thumb {
		top = win - thumb
	}
	if top < 0 {
		top = 0
	}
	return thumb, top, true
}

// handleBarPress 滚动条按下:命中滑块 → 记录锚点进入拖动(offset 不变,拖动按比例跟手);
// 点轨道空白 → 整页翻向该侧(不进入拖动,滑块仍可单独抓住)。
func (m *Model) handleBarPress(mo tea.Mouse) {
	win := m.state.sessionWin // 渲染实际会话窗口高(与滚动条渲染同几何;未渲染回退估算)
	if win < 2 {
		win = m.h - 3
	}
	if win < 2 || mo.Y < 0 || mo.Y >= win {
		return
	}
	if mo.X < m.scrollbarCol() {
		return // 未命中滚动条列
	}
	// 回底指示:浏览历史(offset>0)且点击底行 → 回最新
	if m.state.ScrollOffset > 0 && mo.Y == win-1 {
		m.state.ScrollOffset = 0
		return
	}
	total := m.state.flatN
	thumb, top, ok := m.scrollbarGeom(total, win)
	if !ok {
		return // 内容不足窗口:无滚动条
	}
	switch {
	case mo.Y >= top && mo.Y < top+thumb:
		// 抓住滑块:锚点记录,offset 保持,拖动时滑块跟手
		m.dragBar = true
		m.dragY = mo.Y
		m.dragOff = m.state.ScrollOffset
	default:
		// 轨道空白:整页翻向点击侧(向上=看更早,向下=回最新)
		page := win
		if page < 4 {
			page = 4
		}
		if mo.Y < top {
			m.state.ScrollOffset += page // 点击滑块上方:向上翻页(更早历史)
		} else {
			m.state.ScrollOffset -= page // 点击滑块下方:向下翻页(回最新)
		}
		maxOff := total - win
		if m.state.ScrollOffset < 0 {
			m.state.ScrollOffset = 0
		}
		if m.state.ScrollOffset > maxOff {
			m.state.ScrollOffset = maxOff
		}
	}
}

// handleBarDrag 拖动(按住滑块移动):offset 按锚点 + 鼠标位移 × 比例映射——
// 滑块视觉位移与鼠标 1:1 跟手(内容行远多于窗口时亦然)。
func (m *Model) handleBarDrag(mo tea.Mouse) {
	if !m.dragBar {
		return
	}
	win := m.state.sessionWin
	if win < 2 {
		win = m.h - 3
	}
	total := m.state.flatN
	maxOff := total - win
	thumb, _, ok := m.scrollbarGeom(total, win)
	if !ok || win-thumb < 1 {
		return
	}
	// 鼠标位置钳制到轨道内,拖出窗口仍继续(释放才停)
	y := mo.Y
	if y < 0 {
		y = 0
	}
	if y > win-1 {
		y = win - 1
	}
	// 滑块行程 (win-thumb) 对应内容全行程 maxOff:比例 = maxOff/(win-thumb)
	ratio := float64(maxOff) / float64(win-thumb)
	off := float64(m.dragOff) + float64(m.dragY-y)*ratio
	if off < 0 {
		off = 0
	}
	if off > float64(maxOff) {
		off = float64(maxOff)
	}
	m.state.ScrollOffset = int(off + 0.5)
}

// mouseColW 内容列宽(与渲染同算式 colW=w-3)。
func (m *Model) mouseColW() int {
	c := m.w - 3
	if c < 8 {
		c = 8
	}
	return c
}

// mouseRowAt 会话区鼠标 y → 全局物理行号(越界钳到窗口内;无内容返回 -1)。
func (m *Model) mouseRowAt(y int) int {
	win := m.state.sessionWin
	if win < 2 {
		win = m.h - 3
	}
	if y < 0 {
		y = 0
	}
	if y >= win {
		y = win - 1
	}
	total := m.state.flatN
	if total <= 0 {
		return -1
	}
	if total <= win {
		return y
	}
	off := m.state.ScrollOffset
	if off > total-win {
		off = total - win
	}
	if off < 0 {
		off = 0
	}
	return total - win - off + y
}

// colAt 鼠标 x(0 基终端列)→ rune 列(0 基;text 为展平行文本无 pad,双宽字符按列宽)。
func colAt(text string, x int) int {
	if x <= 0 || text == "" {
		return 0
	}
	cw, idx := 0, 0
	for _, r := range text {
		w := runeCols(r)
		if w == 0 {
			idx++
			continue
		}
		if cw+w > x {
			break
		}
		cw += w
		idx++
	}
	n := len([]rune(text))
	if idx > n {
		idx = n
	}
	return idx
}

// handleMousePress 鼠标按下:bar 列走滚动条;内容区(非选择器)开始划选——
// 清除旧选区、缓存展平行文本(拖动不高频重算)、记起点。
func (m *Model) handleMousePress(mo tea.Mouse) {
	if mo.X >= m.scrollbarCol() {
		m.handleBarPress(mo)
		return
	}
	if m.state.Pick != nil {
		return // 选择器激活:内容区不划选(避免与选项交互混淆)
	}
	m.state.SelActive = false
	m.selMoved = false
	m.selRows = nil
	m.selLineIdx = nil
	rows := flattenViewLines(m.state, m.mouseColW())
	m.selRows = make([]string, len(rows))
	m.selLineIdx = make([]int, len(rows))
	for i, p := range rows {
		m.selRows[i] = p.text
		m.selLineIdx[i] = p.lineIdx
	}
	r := m.mouseRowAt(mo.Y)
	if r < 0 || r >= len(m.selRows) {
		m.selRows = nil
		m.selLineIdx = nil
		return
	}
	m.state.SelRow0, m.state.SelCol0 = r, colAt(m.selRows[r], mo.X)
	m.state.SelRow1, m.state.SelCol1 = m.state.SelRow0, m.state.SelCol0
	m.state.SelActive = true
}

// handleMouseMotion 拖动/悬停:划选中更新选区末端;滚动条拖动中滚滚动条;
// 否则更新滚动条悬停状态(bar 列且会话区 → hover,auto-hide 期间保持显示)。
func (m *Model) handleMouseMotion(mo tea.Mouse) {
	if m.state.SelActive {
		if m.selRows == nil {
			return
		}
		r := m.mouseRowAt(mo.Y)
		if r >= len(m.selRows) {
			r = len(m.selRows) - 1
		}
		if r < 0 {
			return
		}
		c := colAt(m.selRows[r], mo.X)
		if r != m.state.SelRow0 || c != m.state.SelCol0 {
			m.selMoved = true
		}
		m.state.SelRow1, m.state.SelCol1 = r, c
		return
	}
	if m.dragBar {
		m.handleBarDrag(mo)
		m.state.HoverBar = true
		return
	}
	// 悬停判定:bar 列且会话流区
	win := m.state.sessionWin
	if win < 2 {
		win = m.h - 3
	}
	m.state.HoverBar = mo.X >= m.scrollbarCol() && mo.Y >= 0 && mo.Y < win
}

// markBar 滚动条交互计时:重置显示计时并返回 auto-hide tick 命令(渲染按时间/HoverBar 判定隐藏)。
func (m *Model) markBar() tea.Cmd {
	m.state.BarShownAt = time.Now()
	return tea.Tick(barHideDelay, func(time.Time) tea.Msg { return barHideMsg{} })
}

// searchRun 执行会话内搜索:匹配 Lines 原文(大小写不敏感子串),记录命中逻辑行、
// 定位首个命中;无命中自动退出。返回描述文本(命令回执)。
func (m *Model) searchRun(q string) string {
	m.state.SearchQuery = q
	m.state.SearchHits = nil
	m.state.SearchIdx = 0
	m.skipView = false
	if q == "" {
		return "搜索已清除"
	}
	ql := strings.ToLower(q)
	for i, ln := range m.state.Lines {
		if strings.Contains(strings.ToLower(ln.Text), ql) {
			m.state.SearchHits = append(m.state.SearchHits, i)
		}
	}
	if len(m.state.SearchHits) == 0 {
		m.state.SearchQuery = "" // 无命中:退出搜索态
		return "搜索 \"" + q + "\":无命中"
	}
	m.searchGoto(m.state.SearchHits[0])
	return fmt.Sprintf("搜索 \"%s\":命中 %d 行(n/N 循环跳转,F3 下一处,Esc 退出)", q, len(m.state.SearchHits))
}

// searchGoto 定位到命中逻辑行:其展平首个物理行放窗口顶(可看下文)。
func (m *Model) searchGoto(lineIdx int) {
	rows := flattenViewLines(m.state, m.mouseColW())
	first := -1
	for i, p := range rows {
		if p.lineIdx == lineIdx {
			first = i
			break
		}
	}
	if first < 0 {
		return
	}
	win := m.state.sessionWin
	if win < 2 {
		win = m.h - 3
	}
	maxOff := len(rows) - win
	if maxOff < 0 {
		maxOff = 0
	}
	if first > maxOff {
		first = maxOff
	}
	m.state.ScrollOffset = first
	m.skipView = false
}

// searchJump 循环跳转下一/上一命中(搜索激活且非空命中时)。
func (m *Model) searchJump(next bool) {
	if m.state.SearchQuery == "" || len(m.state.SearchHits) == 0 {
		return
	}
	n := len(m.state.SearchHits)
	if next {
		m.state.SearchIdx = (m.state.SearchIdx + 1) % n
	} else {
		m.state.SearchIdx = (m.state.SearchIdx - 1 + n) % n
	}
	m.searchGoto(m.state.SearchHits[m.state.SearchIdx])
}

// handleMouseRelease 释放:划选有位移 → 复制选中文本(OSC52)并保留高亮;
// 单击(无位移)→ 清除选区;滚动条拖动 → 结束。
func (m *Model) handleMouseRelease(mo tea.Mouse) {
	if m.state.SelActive {
		if m.selMoved {
			if text := m.selectedText(); text != "" {
				writeClipboardOSC52(text)
			}
		} else {
			// 单击:若命中可折叠结果行(全文 Full)则切换展开/收起(几何变化),否则清除选区。
			idx := -1
			if m.selLineIdx != nil {
				r := m.state.SelRow0
				if r >= 0 && r < len(m.selLineIdx) {
					idx = m.selLineIdx[r]
				}
			}
			if idx >= 0 && idx < len(m.state.Lines) && m.state.Lines[idx].Full != "" && m.state.ToggleFold(idx) {
				m.skipView = false // 几何变化:强制重渲染(不再复用缓存)
			}
			m.state.SelActive = false // 单击清除(不论是否 toggle)
		}
		m.selRows = nil
		m.selLineIdx = nil
		m.selMoved = false
		return
	}
	if m.dragBar {
		m.dragBar = false
	}
}

// selectedText 依选区(行+列,反向拖动已归一)提取选中文本,行间用 \n 拼接。
func (m *Model) selectedText() string {
	if m.selRows == nil {
		return ""
	}
	a, b := m.state.SelRow0, m.state.SelRow1
	cA, cB := m.state.SelCol0, m.state.SelCol1
	if a > b {
		a, b = b, a
		cA, cB = cB, cA
	}
	if a < 0 {
		a = 0
	}
	if a >= len(m.selRows) {
		return ""
	}
	if b >= len(m.selRows) {
		b = len(m.selRows) - 1
	}
	var out []string
	for i := a; i <= b; i++ {
		rs := []rune(m.selRows[i])
		c0, c1 := 0, len(rs)
		if i == a {
			c0 = cA
		}
		if i == b {
			c1 = cB
		}
		if c0 < 0 {
			c0 = 0
		}
		if c1 > len(rs) {
			c1 = len(rs)
		}
		if c0 > c1 {
			c0 = c1
		}
		out = append(out, string(rs[c0:c1]))
	}
	return strings.Join(out, "\n")
}

// writeClipboardOSC52 经 OSC52 将文本写入系统剪贴板(kitty/iTerm2 等支持)。
// 全局函数变量便于单测替换捕获。
var writeClipboardOSC52 = func(text string) {
	b64 := base64.StdEncoding.EncodeToString([]byte(text))
	fmt.Fprintf(os.Stdout, "\x1b]52;c;%s\x07", b64)
}

// handleEscape Esc 键处理:运行中取消当前回合(有划选时先清除选区)。
func (m *Model) handleEscape() {
	if m.state.Running && m.onCancel != nil {
		m.onCancel()
		m.state.Lines = append(m.state.Lines, Line{Kind: "meta", Text: "正在取消回合…"})
	}
}

func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	k := msg.Key()
	// 确认弹层优先:y/n 决定(任何确认态下的键入不再进输入框,Ctrl+C 亦被忽略)
	if m.state.PendingConfirm != "" {
		switch k.Code {
		case 'y', 'Y':
			m.state.ResolveConfirm(true)
			m.onConfirm(true)
		case 'n', 'N':
			m.state.ResolveConfirm(false)
			m.onConfirm(false)
		}
		return nil
	}
	// Ctrl+C(防误触):输入中仅清空不退出;输入为空需连按两次才彻底退出——
	// 第一次武装并提示(2 秒窗口内再按退出;超时或其他任意键自动解除)。
	if k.Mod&tea.ModCtrl != 0 && (k.Code == 'c' || k.Code == 'C') {
		if m.state.Input == "" {
			if m.quitArmed {
				m.quit = true // 窗口内第二次:彻底退出
				return nil
			}
			return m.armQuit() // 第一次:武装待确认,返回超时解除命令
		} else {
			// 输入中:清空输入(不退出),同时解除可能的武装
			m.disarmQuit()
			m.state.PickDismissed = false
			m.state.ClearInput()
			m.syncHints()
		}
		return nil
	}
	// 武装期间按其他任意键:待退出状态解除(不退出;后续按键语义照常,如 Esc 照常取消回合)
	m.disarmQuit()
	// S1.3 输入增强组合键(选择器未激活时;组合键 Text 为空,不会误入文本分支):
	// Ctrl+P/N 历史、Ctrl+Z/Ctrl+Shift+Z undo/redo、Ctrl+K/U kill、Alt+←/→ 按词移动。
	if m.state.Pick == nil && k.Mod&tea.ModCtrl != 0 {
		switch k.Code {
		case 'p':
			if m.state.HistPrev() {
				m.syncHints()
			}
			return nil
		case 'n':
			if m.state.HistNext() {
				m.syncHints()
			}
			return nil
		case 'z', 'Z':
			var ok bool
			if k.Mod&tea.ModShift != 0 || k.Code == 'Z' {
				ok = m.state.Redo()
			} else {
				ok = m.state.Undo()
			}
			if ok {
				m.syncHints()
			}
			return nil
		case 'k':
			if m.state.KillToEnd() {
				m.syncHints()
			}
			return nil
		case 'u':
			if m.state.KillToStart() {
				m.syncHints()
			}
			return nil
		case 'g', 'G':
			// Ctrl+G:外部编辑器编辑整段($VISUAL/$EDITOR/nano;保存退出回填输入框)
			return m.externalEdit()
		}
	}
	if m.state.Pick == nil && k.Mod&tea.ModAlt != 0 {
		switch k.Code {
		case tea.KeyLeft:
			m.state.WordLeft()
			return nil
		case tea.KeyRight:
			m.state.WordRight()
			return nil
		case tea.KeyUp:
			// P4-1:取回最新一条排队消息到编辑区(队尾弹出;回合运行中亦可用)
			if len(m.state.Queue) > 0 {
				m.state.Input = m.state.PopQueued()
				m.state.Cursor = len([]rune(m.state.Input))
				m.state.PickDismissed = true
			}
			return nil
		}
	}
	switch k.Code {
	case tea.KeyEnter:
		// Shift+Enter:多行输入——普通输入态插入换行(选择器激活/自由向导中仍与 Enter
		// 相同:应用选项/步进,不插入换行)。
		if k.Mod&tea.ModShift != 0 && m.state.Pick == nil && m.state.Free == nil {
			m.state.InsertNewline()
			if strings.HasPrefix(m.state.Input, "/") {
				// 命令单行语义:已带换行的 / 输入退出选择器与提示(提交时拒绝,见 submit)
				m.state.Pick = nil
				m.state.PickDismissed = true
				m.state.Suggestions = nil
			} else {
				m.syncHints()
			}
			return nil
		}
		m.enter()
	case tea.KeyBackspace:
		m.state.PickDismissed = false
		if m.pickFilterBackspace() {
			return nil // 参数级选择态:退格只删过滤词(不动命令文本)
		}
		m.state.Backspace()
		m.syncHints()
	case tea.KeyDelete:
		if m.state.Pick == nil {
			m.state.Delete() // 删除光标处字符
		}
	case tea.KeyLeft:
		if m.state.Pick == nil {
			m.state.CursorLeft() // 左移(边界钳制)
		}
	case tea.KeyRight:
		if m.state.Pick == nil {
			m.state.CursorRight()
		}
	case tea.KeyUp:
		if p := m.state.Pick; p != nil {
			if p.Cursor > 0 {
				p.Cursor-- // 选择器:上移选项
			}
		} else {
			// 输入框:多行内上移一行(列意图记忆);单行退化为光标回头
			m.state.LineUp()
		}
	case tea.KeyDown:
		if p := m.state.Pick; p != nil {
			if p.Cursor < len(p.Items)-1 {
				p.Cursor++ // 选择器:下移选项
			}
		} else {
			// 输入框:多行内下移一行(列意图记忆);单行退化为光标回尾
			m.state.LineDown()
		}
	case tea.KeyHome:
		if m.state.Pick == nil {
			m.state.CursorHome() // 输入光标回头(旧 ↑ 聶责;Home 恒定语义)
		}
	case tea.KeyEnd:
		if m.state.Pick == nil {
			m.state.CursorEnd() // 输入光标回尾(旧 ↓ 聶责)
		}
	case tea.KeyPgUp:
		// 整页翻(兼容保留;箭头逐行为主通道)
		m.state.ScrollBy(m.h-4, m.h-4)
		m.markBar() // 键盘翻页同样重置滚动条显示计时
	case tea.KeyPgDown:
		// 整页翻回底部
		m.state.ScrollBy(-(m.h - 4), m.h-4)
		m.markBar()
	case tea.KeyF3:
		// 搜索激活时:F3 跳下一命中(Shift+F3 上一处)
		if m.state.SearchQuery != "" {
			if k.Mod&tea.ModShift != 0 {
				m.searchJump(false)
			} else {
				m.searchJump(true)
			}
		}
	case tea.KeyTab:
		// 仅 Shift+Tab 切换思考等级(前进循环 off→low→medium→high→off);单独 Tab 不绑定
		if k.Mod&tea.ModShift != 0 && m.onThinkingCycle != nil {
			m.onThinkingCycle(1)
		}
	case tea.KeyEscape:
		if m.pickFilterEsc() {
			// 参数级过滤词非空:Esc 先清过滤恢复全量(再按才退出选择)
		} else if m.state.Pick != nil {
			m.state.Pick = nil
			m.state.PickDismissed = true // 退出选择:保留文本,回普通输入
		} else if m.state.SearchQuery != "" {
			// 搜索激活:Esc 退出搜索(清除高亮/命中;不中断回合)
			m.state.SearchQuery = ""
			m.state.SearchHits = nil
			m.state.SearchIdx = 0
		} else if m.state.SelActive {
			// 有鼠标划选:Esc 清除选区(不中断回合)
			m.state.SelActive = false
			m.selRows = nil
			m.selLineIdx = nil
			m.selMoved = false
		} else if !m.state.Running && len(m.state.Queue) > 0 {
			// P4-1:回合结束/取消后队列有消息,取回最新一条到编辑区(逐次按 Esc 取一条)
			m.state.Input = m.state.PopQueued()
			m.state.Cursor = len([]rune(m.state.Input))
		} else {
			// Esc:中断进行中的回合(取消链:turn → LLM 流 → 工具进程)
			m.handleEscape()
		}
	default:
		if k.Text != "" {
			// 搜索激活且输入框为空:n/N 跳下一命中(不输入字符)
			if (k.Text == "n" || k.Text == "N") && m.state.SearchQuery != "" && m.state.Input == "" {
				m.searchJump(true)
				return nil
			}
			// 参数级选择器激活:直接打字 = 即时过滤选项(不写入命令文本)
			if p := m.state.Pick; p != nil && p.Level > 0 {
				m.pickFilterType(k.Text)
				return nil
			}
			m.state.PickDismissed = false
			for _, r := range k.Text {
				m.state.InsertRune(r)
			}
			m.syncHints()
		}
	}
	return nil
}

// armQuit 第一次 Ctrl+C(输入为空):武装待退出——界面提示再按一次彻底退出;
// 返回超时命令:quitConfirmWindow 内未再按 → disarmQuitMsg 自动解除(防误触)。
func (m *Model) armQuit() tea.Cmd {
	m.quitArmed = true
	m.state.QuitArmed = true
	return tea.Tick(quitConfirmWindow, func(time.Time) tea.Msg { return disarmQuitMsg{} })
}

// disarmQuit 解除双按退出武装(超时 / 输入中按 Ctrl+C / 按其他任意键)。
func (m *Model) disarmQuit() {
	m.quitArmed = false
	m.state.QuitArmed = false
}

// enter 回车:选择器激活时应用高亮项(命令/参数级联推进);否则普通提交。
func (m *Model) enter() {
	if m.state.Pick == nil {
		m.submit()
		return
	}
	if len(m.state.Pick.Items) == 0 {
		return // 过滤无匹配:回车不提交(防误执行当前命令文本)
	}
	res := AdvanceEnter(m.state.Input, m.state.Pick, m.levels)
	m.state.Input = res.Input
	m.state.Cursor = len([]rune(res.Input))
	m.state.Pick = res.Pick
	m.state.Suggestions = res.Hints // 断点时显示“继续输入”提示
	if res.Pick == nil {
		m.state.PickDismissed = true // 断点/完成:重新输入才再激活
	}
	// 多值自由参数:断点建立逐步向导(序列 >1;单参数保持旧直接输入语义)
	if res.Pick == nil {
		if len(res.Free) > 1 {
			if f := strings.Fields(strings.TrimPrefix(res.Input, "/")); len(f) > 0 {
				m.state.Free = &freeStep{Cmd: f[0], Params: res.Free, Base: len(f), Done: 0}
			}
		} else {
			m.state.Free = nil
		}
	}
	// 不调 syncHints:新文本会被重新过滤成命令列表,覆盖推进出的参数级
	// (选择确认是用户主动操作,非输入变化;渲染直接用 Pick.Items/Hints)。
	if res.Commit {
		m.submit()
	}
}

// pickFilterType 参数级选择过滤:键入并入 Filter,从全量重算匹配子集(光标钳制)。
// All 为空时以当前 Items 视为全量(过滤词输入前保持不动)。
func (m *Model) pickFilterType(s string) {
	p := m.state.Pick
	if p.All == nil {
		p.All = p.Items
	}
	p.Filter += s
	p.Items = filterOptions(p.All, p.Filter)
	clampPickCursor(p)
}

// pickFilterBackspace 参数级选择退格:删过滤词尾字符并重算。
// 返回 true = 已消费(参数级选择态退格不编辑命令文本);false = 非参数级选择,走普通退格。
func (m *Model) pickFilterBackspace() bool {
	p := m.state.Pick
	if p == nil || p.Level <= 0 {
		return false
	}
	if p.Filter != "" {
		r := []rune(p.Filter)
		p.Filter = string(r[:len(r)-1])
		p.Items = filterOptions(p.All, p.Filter)
		clampPickCursor(p)
	}
	return true
}

// pickFilterEsc 参数级选择 Esc:过滤词非空则清过滤恢复全量(返回 true 已消费);
// 否则返回 false 交既有逻辑(退出选择器)。
func (m *Model) pickFilterEsc() bool {
	p := m.state.Pick
	if p == nil || p.Level <= 0 || p.Filter == "" {
		return false
	}
	p.Filter = ""
	p.Items = p.All
	clampPickCursor(p)
	return true
}

// clampPickCursor 光标钳制到当前 Items 内(空集归 0)。
func clampPickCursor(p *Pick) {
	if len(p.Items) == 0 {
		p.Cursor = 0
	} else if p.Cursor >= len(p.Items) {
		p.Cursor = len(p.Items) - 1
	}
}

// syncHints 输入以 / 开头时按当前前缀刷新选项:非空自动激活选择器;
// Esc/断点后(PickDismissed)只显示提示不激活,直至用户再次输入。
func (m *Model) syncHints() {
	input := m.state.Input
	if !strings.HasPrefix(input, "/") || m.hints == nil {
		m.state.Suggestions = nil
		m.state.Pick = nil
		return
	}
	opts := m.hints(strings.TrimPrefix(input, "/"))
	m.state.Suggestions = pickLines(opts)
	if len(opts) > 0 && !m.state.PickDismissed {
		m.state.Pick = &Pick{Items: opts}
	} else {
		m.state.Pick = nil
	}
}

// submit 提交输入:命令走 onCommand,否则走 onSubmit(异步回合)。
// 多行输入(P4-6)下普通消息可含换行;命令(/ 前缀)保持单行语义——含换行拒绝(不清空,
// 用户可修改);空/纯空白(含仅换行/空格)不发起回合。
func (m *Model) submit() {
	input := m.state.Input
	if m.freeContinue(input) {
		return // 多值自由参数逐步向导推进(命令未执行,等待下一参数输入)
	}
	if strings.HasPrefix(input, "/") && strings.ContainsRune(input, '\n') {
		m.state.Lines = append(m.state.Lines, Line{Kind: "meta",
			Text: "命令不支持多行(/ 前缀为命令;多行内容请去掉 / 作为普通消息发送)"})
		return // 保留现场,用户可退格删换行或去掉 / 后回车
	}
	m.state.ClearInput()
	m.syncHints()
	if strings.TrimSpace(input) == "" {
		return // 空/纯空白:不发起回合
	}
	if strings.HasPrefix(input, "/") {
		m.state.RecordCmd(input) // S1.3:斜杠命令入输入历史(不入会话 Lines)
		if err := m.onCommand(input); err != nil {
			m.state.SetError(err.Error())
		}
		return
	}
	// P4-1:回合运行中普通消息不启动新回合 → 入队(状态栏显“待发 N”,回合结束自动发送);
	// 空闲 Enter 正常提交。命令不入队(即时执行保持现状)。
	if m.state.Running {
		m.state.Enqueue(input)
		return
	}
	m.onSubmit(input)
}

// submitQueuedNext 自动发送队列下一条(回合成功结束后调用;Running 由 onSubmit 置位)。
func (m *Model) submitQueuedNext() {
	t := m.state.Dequeue()
	if t == "" {
		return
	}
	m.onSubmit(t)
}

// freeContinue 多值自由参数向导(仅 Free 启用,见 freeStep):命令序列未收齐时不执行——
// 本次回车带新词 = 步进并提示下一步;无新词在尾可选步 = 跳过执行、在必填步 = 等待继续输入。
// 返回 true = 已拦截(submit 未执行);false = 可直接执行(向导未启用/已收齐/命令已变更)。
func (m *Model) freeContinue(input string) bool {
	f := m.state.Free
	if f == nil || len(f.Params) == 0 {
		return false
	}
	fields := strings.Fields(strings.TrimPrefix(input, "/"))
	if len(fields) == 0 || fields[0] != f.Cmd {
		m.state.Free = nil // 命令已变更:脱离向导
		return false
	}
	d := len(fields) - f.Base // 已输入自由词数
	if d < 0 {
		d = 0
	}
	advanced := d > f.Done // 本次回车是否带来新自由词(决定尾可选步空回车=跳过的判据)
	switch {
	case advanced:
		if d >= len(f.Params) {
			m.state.Free = nil // 全部(或一次多词超量)填完:执行(Run 自校验)
			return false
		}
		f.Done = d // 步进到下一待填参数
	case d < f.Done:
		f.Done = d // 用户退格删词:回退步数
	}
	if f.Done >= len(f.Params) {
		m.state.Free = nil
		return false
	}
	idx := f.Done
	// 仅"无新词回车"落在尾可选步 = 跳过执行;刚带词推进到此步只提示(等待输入/再空回车跳过)
	if !advanced && d == f.Done && idx == len(f.Params)-1 && strings.HasSuffix(f.Params[idx], "?") {
		m.state.Free = nil
		return false
	}
	m.state.Suggestions = freeStepHints(m.state.Input, f.Params, idx) // 提示当前步
	return true
}
