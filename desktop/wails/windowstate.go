// windowstate:记住主窗口的位置与尺寸(下次打开回到原处)。
//
// 为什么值得做:现在每次都是 1200×800 居中 —— 多屏用户每次都要把窗口拖回去。
// 实现要点(都是"不做会出怪事"的地方):
//  1. 存盘落在**壳自己的数据目录**(不是 gah 的 GAH_HOME,也不是仓库),与壳日志同级;
//  2. 恢复前要校验「这块区域还在某块屏幕上」:拔掉外接屏之后,原坐标可能落在不存在的区域,
//     窗口会跑到看不见的地方,用户以为"程序没打开";
//  3. 移动/缩放是高频事件,写盘要**去抖**(500ms),否则拖一次窗口写几十次文件。
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// windowStateFile 状态文件名(与 gah-shell.log 同在本地数据目录)。
const windowStateFile = "window.json"

// minWindowSize 低于这个尺寸的状态视为损坏(不恢复,免得开出一个针眼窗口)。
const minWindowSize = 400

// winState 窗口几何。
type winState struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// windowStatePath 状态文件路径。
func windowStatePath() string { return filepath.Join(localDataDir(), windowStateFile) }

// loadWindowState 读上次的窗口几何;文件缺失/损坏一律当"没有"(不抛错、不阻塞启动)。
func loadWindowState() (winState, bool) {
	b, err := os.ReadFile(windowStatePath())
	if err != nil {
		return winState{}, false
	}
	var st winState
	if err := json.Unmarshal(b, &st); err != nil {
		shellLogf("窗口状态解析失败(按默认尺寸打开): %v", err)
		return winState{}, false
	}
	if st.W < minWindowSize || st.H < minWindowSize {
		return winState{}, false
	}
	return st, true
}

// screenVisible 判断某块区域是否还落在当前任一屏幕的工作区内(防"窗口跑到不存在的屏幕")。
//
// 判据刻意宽松:只要窗口左上角 + 一小块可见区域在屏幕内即可 —— 用户故意把窗口
// 拖到半出屏是合法用法,不该被"纠正"。
func screenVisible(app *application.App, st winState) bool {
	if app == nil || app.Screen == nil {
		return true // 拿不到屏幕信息就别拦
	}
	screens := app.Screen.GetAll()
	if len(screens) == 0 {
		return true
	}
	const margin = 80 // 至少要露出 80px,否则用户抓不到它
	for _, sc := range screens {
		wa := sc.WorkArea
		if st.X+margin < wa.X+wa.Width && st.X+wa.Width-margin > wa.X &&
			st.Y+margin < wa.Y+wa.Height && st.Y+wa.Height-margin > wa.Y {
			return true
		}
	}
	return false
}

// windowStateSaver 去抖写盘(移动/缩放事件很密)。
type windowStateSaver struct {
	mu    sync.Mutex
	timer *time.Timer
	win   *application.WebviewWindow
}

// schedule 500ms 内的多次调用只写一次。
func (s *windowStateSaver) schedule() {
	if s == nil || s.win == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(500*time.Millisecond, s.flush)
}

// flush 真正落盘(读当前位置/尺寸)。
func (s *windowStateSaver) flush() {
	x, y := s.win.Position()
	w, h := s.win.Size()
	st := winState{X: x, Y: y, W: w, H: h}
	if st.W < minWindowSize || st.H < minWindowSize {
		return // 最小化等状态下读到的尺寸不可信,别把它写进去
	}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(windowStatePath()), 0o755); err != nil {
		return
	}
	// 原子写:先写临时文件再 rename,免得崩溃时留下半个 JSON。
	tmp := windowStatePath() + ".new"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, windowStatePath())
}

// watchWindowState 挂上移动/缩放事件(去抖写盘)。
func watchWindowState(app *application.App, w *application.WebviewWindow) *windowStateSaver {
	saver := &windowStateSaver{win: w}
	w.OnWindowEvent(events.Common.WindowDidMove, func(*application.WindowEvent) { saver.schedule() })
	w.OnWindowEvent(events.Common.WindowDidResize, func(*application.WindowEvent) { saver.schedule() })
	return saver
}
