// 鼠标滚轮单测:滚轮滚动会话流(上滚看历史/下滚回最新)、选择器激活时移动选项、边界钳制。
package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// mouseModel 装配:20 行历史 + 窗口高(m.h)固定。
func mouseModel(t *testing.T) *Model {
	t.Helper()
	m := &Model{state: &State{}, h: 10}
	for i := 0; i < 20; i++ {
		m.state.Lines = append(m.state.Lines, Line{Kind: "meta", Text: "L"})
	}
	return m
}

func TestMouseWheelScrollsHistory(t *testing.T) {
	m := mouseModel(t)
	// 滚轮上滚一格:offset +wheelStep(每格多行,系统手感)
	m.Update(mouseEventMsg{tea.MouseWheelMsg{Button: tea.MouseWheelUp}})
	if m.state.ScrollOffset != wheelStep {
		t.Fatalf("上滚应 +%d: %d", wheelStep, m.state.ScrollOffset)
	}
	// 高频连续事件被节流:50ms 内后续事件全部忽略(防平滑滚轮停不下来)
	for i := 0; i < 20; i++ {
		m.Update(mouseEventMsg{tea.MouseWheelMsg{Button: tea.MouseWheelUp}})
	}
	if m.state.ScrollOffset != wheelStep {
		t.Fatalf("节流后 offset 应保持 %d: %d", wheelStep, m.state.ScrollOffset)
	}
	// 超过 50ms 后下一事件生效(模拟物理停顿)
	m.lastWheel = m.lastWheel.Add(-60 * time.Millisecond)
	m.Update(mouseEventMsg{tea.MouseWheelMsg{Button: tea.MouseWheelUp}})
	if m.state.ScrollOffset != 2*wheelStep {
		t.Fatalf("节流窗口过后应 +%d: %d", wheelStep, m.state.ScrollOffset)
	}
	// 连续下滚(带节流推进)回到 0
	for i := 0; i < 10; i++ {
		m.lastWheel = m.lastWheel.Add(-60 * time.Millisecond)
		m.Update(mouseEventMsg{tea.MouseWheelMsg{Button: tea.MouseWheelDown}})
	}
	if m.state.ScrollOffset != 0 {
		t.Fatalf("回底应为 0: %d", m.state.ScrollOffset)
	}
}

// TestRenderScrollbar 会话流超窗口时渲染输出含右侧滚动条(轨道 ░/滑块 █);不超时不渲染。
func TestRenderScrollbar(t *testing.T) {
	m := mouseModel(t) // 20 行历史
	s := m.state
	s.ScrollOffset = 0
	out := Render(s, 60, 12) // 窗口小:内容超窗口
	if !strings.Contains(out, styleBarTrack.Render("░")) && !containsBar(out) {
		t.Fatal("超窗口应渲染滚动条轨道/滑块")
	}
	// 上滚后滑块应存在(琥珀 █)
	s.ScrollBy(5, 6)
	out2 := Render(s, 60, 12)
	if !containsBar(out2) {
		t.Fatal("上滚后滚动条应存在")
	}
	// 内容未超窗口:无滚动条
	s2 := &State{}
	for i := 0; i < 3; i++ {
		s2.Lines = append(s2.Lines, Line{Kind: "meta", Text: "x"})
	}
	out3 := Render(s2, 60, 12)
	if strings.Contains(out3, styleBarTrack.Render("░")) {
		t.Fatal("未超窗口不应渲染滚动条")
	}
}

// TestArrowKeysScroll ↑/↓ 输入光标头/尾(历史浏览走滚轮/滚动条/PgUp);Home/End 等价;
// 选择器激活时箭头仍为选项移动。
func TestArrowKeysScroll(t *testing.T) {
	m := mouseModel(t)
	// 输入框有文本时:↑ → 光标行首,↓ → 光标行尾(不滚动会话流)
	m.state.Input = "abc"
	m.state.Cursor = 2
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.state.Cursor != 0 {
		t.Fatalf("↑ 应光标到输入行首: %d", m.state.Cursor)
	}
	if m.state.ScrollOffset != 0 {
		t.Fatalf("↑ 不应滚动会话流: %d", m.state.ScrollOffset)
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.state.Cursor != 3 {
		t.Fatalf("↓ 应光标到输入行尾: %d", m.state.Cursor)
	}
	if m.state.ScrollOffset != 0 {
		t.Fatalf("↓ 不应滚动会话流: %d", m.state.ScrollOffset)
	}
	// Home/End 等价(输入光标头/尾)
	m.state.Cursor = 1
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyHome})
	if m.state.Cursor != 0 {
		t.Fatalf("Home 应光标到头: %d", m.state.Cursor)
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	if m.state.Cursor != 3 {
		t.Fatalf("End 应光标到尾: %d", m.state.Cursor)
	}
	// 输入为空:↑/↓ 光标钳制为 0(无副作用)
	m.state.Input = ""
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.state.Cursor != 0 {
		t.Fatalf("空输入 ↑ 应 no-op: %d", m.state.Cursor)
	}
	// 选择器激活:箭头只移选项不滚动
	m2 := mouseModel(t)
	m2.state.Pick = &Pick{Items: []sdk.Option{{Value: "a"}, {Value: "b"}, {Value: "c"}, {Value: "d"}}}
	m2.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if p := m2.state.Pick; p.Cursor != 0 || m2.state.ScrollOffset != 0 {
		t.Fatal("选择器激活时 ↑ 只移选项不滚动")
	}
	m2.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if p := m2.state.Pick; p.Cursor != 1 {
		t.Fatal("选择器激活时 ↓ 移选项")
	}
}

// containsBar 输出是否含轨道或滑块字符(ANSI 包裹下直接查字符)。
func containsBar(out string) bool {
	return strings.Contains(out, "░") || strings.Contains(out, "█")
}

// TestWheelGestureCap 单次连续手势滚动有上限:一次滚轮风暴同手势最多滚 gestureCap 行,
// 超出即丢弃——防“滚一下一直滚、无法打断”;手势结束(>gestureReset 未滚)复位后可继续。
func TestWheelGestureCap(t *testing.T) {
	m := &Model{state: &State{}, h: 12}
	for i := 0; i < 60; i++ {
		m.state.Lines = append(m.state.Lines, Line{Kind: "meta", Text: "L"})
	}
	// 已滚 gestureCap-1(同手势内),下一次事件应滚满 cap
	m.wheelGesture = gestureCap - 1
	m.lastWheel = time.Now().Add(-60 * time.Millisecond)
	m.Update(mouseEventMsg{tea.MouseWheelMsg{Button: tea.MouseWheelUp}})
	if m.state.ScrollOffset != wheelStep || m.wheelGesture != gestureCap {
		t.Fatalf("应滚满 cap: off=%d gesture=%d", m.state.ScrollOffset, m.wheelGesture)
	}
	// 同手势再滚:达上限 → 丢弃(不滚动、skipView)
	m.lastWheel = time.Now().Add(-60 * time.Millisecond)
	m.Update(mouseEventMsg{tea.MouseWheelMsg{Button: tea.MouseWheelUp}})
	if m.state.ScrollOffset != wheelStep {
		t.Fatalf("cap 后不应再滚: off=%d", m.state.ScrollOffset)
	}
	if !m.skipView {
		t.Fatal("cap 丢弃应置 skipView(事件零开销消化)")
	}
	// 手势结束(>gestureReset 未滚)复位,可继续滚
	m.lastWheel = time.Now().Add(-400 * time.Millisecond)
	m.Update(mouseEventMsg{tea.MouseWheelMsg{Button: tea.MouseWheelUp}})
	if m.state.ScrollOffset != 2*wheelStep {
		t.Fatalf("新手势应继续滚动: got %d", m.state.ScrollOffset)
	}
	if m.wheelGesture != 1 {
		t.Fatalf("新手势累计应清零: got %d", m.wheelGesture)
	}
}

// TestWheelThrottleSkipView 节流丢弃的滚轮事件置 skipView(不触发重渲染),
// 让风暴事件快速消化(键盘/Ctrl+C 不被成百滚轮事件阻塞排队)。
func TestWheelThrottleSkipView(t *testing.T) {
	m := &Model{state: &State{}, h: 10}
	for i := 0; i < 20; i++ {
		m.state.Lines = append(m.state.Lines, Line{Kind: "meta", Text: "L"})
	}
	m.Update(mouseEventMsg{tea.MouseWheelMsg{Button: tea.MouseWheelUp}}) // 首事件:实际滚动
	if m.skipView {
		t.Fatal("实际滚动不应置 skipView")
	}
	m.Update(mouseEventMsg{tea.MouseWheelMsg{Button: tea.MouseWheelUp}}) // 50ms 内:节流丢弃
	if !m.skipView {
		t.Fatal("节流丢弃应置 skipView(View 返回缓存快速消化)")
	}
	// View 命中缓存不崩溃,且 skipView 复位(下个 Update 正常全量渲染)
	_ = m.View()
	if m.skipView {
		t.Fatal("View 后 skipView 应复位")
	}
}

// TestWheelDirectionReverseResets 方向反转 = 新手势:上滚达手势上限后立即下滚必须响应,
// 不被 cap 卡住(用户换向滚“卡死/抖动”回归);同向继续则正常累计。
func TestWheelDirectionReverseResets(t *testing.T) {
	m := &Model{state: &State{}, h: 12}
	for i := 0; i < 60; i++ {
		m.state.Lines = append(m.state.Lines, Line{Kind: "meta", Text: "L"})
	}
	// 上滚 5 格建立 offset
	for i := 0; i < 5; i++ {
		m.lastWheel = time.Now().Add(-60 * time.Millisecond)
		m.Update(mouseEventMsg{tea.MouseWheelMsg{Button: tea.MouseWheelUp}})
	}
	if m.state.ScrollOffset != 5*wheelStep {
		t.Fatalf("上滚 5 格: off=%d", m.state.ScrollOffset)
	}
	// 模拟长上滚已满手势上限(dir 仍上)
	m.wheelGesture = gestureCap
	m.wheelDir = 1
	// 反转下滚:方向变 → 新手势清零 → 立即滚,不被 cap 卡住
	m.lastWheel = time.Now().Add(-60 * time.Millisecond)
	m.Update(mouseEventMsg{tea.MouseWheelMsg{Button: tea.MouseWheelDown}})
	if m.state.ScrollOffset != 4*wheelStep {
		t.Fatalf("方向反转应立即可滚(不被 cap 卡住): off=%d", m.state.ScrollOffset)
	}
	if m.wheelGesture != 1 || m.wheelDir != -1 {
		t.Fatalf("反转后应新手势: gesture=%d dir=%d", m.wheelGesture, m.wheelDir)
	}
	// 同向下滚继续累计(不因方向判断误重置)
	m.lastWheel = time.Now().Add(-60 * time.Millisecond)
	m.Update(mouseEventMsg{tea.MouseWheelMsg{Button: tea.MouseWheelDown}})
	if m.state.ScrollOffset != 3*wheelStep || m.wheelGesture != 2 {
		t.Fatalf("同向继续应累计: off=%d gesture=%d", m.state.ScrollOffset, m.wheelGesture)
	}
}

// TestScrollbarDrag 滚动条:点按滑块 → 锚定拖动,offset 按比例映射(滑块跟手);
// 点轨道空白 → 整页翻向该侧;非 bar 列按下不触发。
func TestScrollbarDrag(t *testing.T) {
	m := &Model{state: &State{}, w: 80, h: 24}
	for i := 0; i < 60; i++ {
		m.state.Lines = append(m.state.Lines, Line{Kind: "meta", Text: "L"})
	}
	m.state.flatN = 60
	win := m.h - 3 // 21
	bar := m.scrollbarCol()
	if bar != 77 {
		t.Fatalf("bar 列应为 w-3=77: %d", bar)
	}
	thumb, top, ok := m.scrollbarGeom(60, win)
	if !ok || thumb < 1 {
		t.Fatalf("应可滚: thumb=%d", thumb)
	}
	// 初始 offset=0(最新):滑块在轨道底 [top, top+thumb)
	if m.state.ScrollOffset != 0 || top != win-thumb {
		t.Fatalf("初始滑块应在底部: top=%d (want %d)", top, win-thumb)
	}
	// 点按滑块中下部 → 锚定拖动,offset 不变
	grabY := top + thumb - 2
	m.Update(mouseEventMsg{tea.MouseClickMsg(tea.Mouse{X: bar, Y: grabY, Button: tea.MouseLeft})})
	if !m.dragBar || m.state.ScrollOffset != 0 {
		t.Fatalf("抓滑块应锚定且不跳转: drag=%v off=%d", m.dragBar, m.state.ScrollOffset)
	}
	// 上拖到轨道中部:offset 按比例(滑块跟手),非 1:1 内容行
	m.Update(mouseEventMsg{tea.MouseMotionMsg(tea.Mouse{X: bar, Y: win / 2, Button: tea.MouseLeft})})
	mid := m.state.ScrollOffset
	if mid <= 0 || mid >= 60-win {
		t.Fatalf("拖到中部应有中间 offset: %d", mid)
	}
	// 拖到轨道顶(y=0)→ 最早历史(maxOff)
	m.Update(mouseEventMsg{tea.MouseMotionMsg(tea.Mouse{X: bar, Y: 0, Button: tea.MouseLeft})})
	if m.state.ScrollOffset != 60-win {
		t.Fatalf("拖到顶应最早历史: off=%d (want %d)", m.state.ScrollOffset, 60-win)
	}
	m.Update(mouseEventMsg{tea.MouseReleaseMsg(tea.Mouse{})})
	if m.dragBar {
		t.Fatal("release 应结束拖动")
	}
	// 轨道空白(滑块下方,取非底行的空白区)点击 → 向下整页;
	// 注:底行(win-1)在 offset>0 时为回底指示列(点击回底),故用 win-2 测翻页。
	m.Update(mouseEventMsg{tea.MouseClickMsg(tea.Mouse{X: bar, Y: win - 2, Button: tea.MouseLeft})})
	want := 60 - win - win
	if want < 0 {
		want = 0
	}
	if m.state.ScrollOffset != want {
		t.Fatalf("滑块下方空白应向下翻页: off=%d (want %d)", m.state.ScrollOffset, want)
	}
	// 非 bar 列按下不触发拖动
	m2 := &Model{state: &State{}, w: 80, h: 24}
	m2.state.flatN = 60
	m2.Update(mouseEventMsg{tea.MouseClickMsg(tea.Mouse{X: 10, Y: 10, Button: tea.MouseLeft})})
	if m2.dragBar || m2.state.ScrollOffset != 0 {
		t.Fatalf("非滚动条列按下不应进入拖动: drag=%v off=%d", m2.dragBar, m2.state.ScrollOffset)
	}
}

// TestBarAutoHide 滚动条 auto-hide:交互后静止超时且非悬停 → 隐藏;初始/悬停保持显示。
func TestBarAutoHide(t *testing.T) {
	s := &State{}
	for i := 0; i < 60; i++ {
		s.Lines = append(s.Lines, Line{Kind: "meta", Text: "L"})
	}
	// 初始未交互(IsZero)→ 显示(轨道 ░ 在 bar 列)
	out := Render(s, 60, 20)
	if !strings.Contains(out, "░") {
		t.Fatal("初始应显示滚动条轨道")
	}
	// 交互后(新近)→ 显示
	s.BarShownAt = time.Now()
	s.ScrollOffset = 5
	out2 := Render(s, 60, 20)
	if !strings.Contains(out2, "░") {
		t.Fatal("交互后应显示滚动条轨道")
	}
	// 静止超时且非悬停 → 隐藏(bar 列无轨道/指示;输入光标 █ 与滑块同字符,不以此判)
	s.BarShownAt = time.Now().Add(-2 * time.Second)
	s.HoverBar = false
	out3 := Render(s, 60, 20)
	if strings.Contains(out3, "░") || strings.Contains(out3, "▼") {
		t.Logf("out3=%q", out3)
		t.Fatal("超时非悬停应隐藏滚动条")
	}
	// 悬停中(即使超时)→ 保持显示
	s.HoverBar = true
	out4 := Render(s, 60, 20)
	if !strings.Contains(out4, "░") {
		t.Fatal("悬停应保持滚动条显示")
	}
}

// TestBarEndIndicator 回底指示:浏览历史(offset>0)时底行显示 ▼;最新时不显示;点击底行回底。
func TestBarEndIndicator(t *testing.T) {
	m := &Model{state: &State{}, w: 60, h: 20}
	for i := 0; i < 60; i++ {
		m.state.Lines = append(m.state.Lines, Line{Kind: "meta", Text: "L"})
	}
	m.state.flatN = 60
	m.state.sessionWin = 17
	m.state.BarShownAt = time.Now()
	// 浏览历史:有 ▼
	m.state.ScrollOffset = 5
	out := Render(m.state, 60, 20)
	if !strings.Contains(out, "▼") {
		t.Fatal("offset>0 应显示回底指示 ▼")
	}
	// 点击底行(y=win-1)回底
	bar := m.scrollbarCol()
	m.handleMousePress(tea.Mouse{X: bar, Y: m.state.sessionWin - 1})
	if m.state.ScrollOffset != 0 {
		t.Fatalf("回底指示点击应回最新: off=%d", m.state.ScrollOffset)
	}
	if m.dragBar {
		t.Fatal("回底点击不应进入拖动")
	}
	// 最新:无 ▼
	out2 := Render(m.state, 60, 20)
	if strings.Contains(out2, "▼") {
		t.Fatal("offset=0 不应有回底指示")
	}
}

// TestBarHoverAndMark 悬停状态跟踪与交互计时重置。
func TestBarHoverAndMark(t *testing.T) {
	m := &Model{state: &State{}, w: 60, h: 20}
	m.state.sessionWin = 17
	bar := m.scrollbarCol()
	// hover:bar 列且会话区
	m.handleMouseMotion(tea.Mouse{X: bar, Y: 5})
	if !m.state.HoverBar {
		t.Fatal("bar 列 motion 应置 hover")
	}
	// 非 bar 列
	m.handleMouseMotion(tea.Mouse{X: 10, Y: 5})
	if m.state.HoverBar {
		t.Fatal("非 bar 列 motion 应清 hover")
	}
	// markBar 重置计时并返回 tick 命令
	m.state.BarShownAt = time.Time{}
	cmd := m.markBar()
	if cmd == nil || m.state.BarShownAt.IsZero() {
		t.Fatal("markBar 应重置计时并返回 tick")
	}
}

// TestMouseWheelMovesPicker 滚轮在选择器激活时移动选项(不滚会话流)。
func TestMouseWheelMovesPicker(t *testing.T) {
	m := mouseModel(t)
	m.state.Pick = &Pick{Items: []sdk.Option{{Value: "a"}, {Value: "b"}, {Value: "c"}}}
	m.state.Pick.Cursor = 1
	step := func() { // 每次事件前模拟超过节流窗口(50ms)
		m.lastWheel = m.lastWheel.Add(-60 * time.Millisecond)
	}
	// 选择器激活:滚轮移动选项(不滚会话流)
	step()
	m.Update(mouseEventMsg{tea.MouseWheelMsg{Button: tea.MouseWheelDown}})
	if p := m.state.Pick; p.Cursor != 2 {
		t.Fatalf("下滚应移动选项: %d", p.Cursor)
	}
	if m.state.ScrollOffset != 0 {
		t.Fatalf("选择器滚动不应影响会话流 offset: %d", m.state.ScrollOffset)
	}
	step()
	m.Update(mouseEventMsg{tea.MouseWheelMsg{Button: tea.MouseWheelDown}})
	if p := m.state.Pick; p.Cursor != 2 {
		t.Fatalf("选项越界应钳制: %d", p.Cursor)
	}
	step()
	m.Update(mouseEventMsg{tea.MouseWheelMsg{Button: tea.MouseWheelUp}})
	step()
	m.Update(mouseEventMsg{tea.MouseWheelMsg{Button: tea.MouseWheelUp}})
	if p := m.state.Pick; p.Cursor != 0 {
		t.Fatalf("选项到头应钳制: %d", p.Cursor)
	}
}
