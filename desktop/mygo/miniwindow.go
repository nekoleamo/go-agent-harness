// miniwindow:托盘锚定的「概览」浮层 —— 关窗收进托盘之后,唯一能回答
// 「服务还在吗 / 现在在跑吗 / 哪个会话」的地方。
//
// 数据只来自 gah 自己的 HTTP API(`/api/state` 与 `/api/notices`,壳已有 token
// 与 cookie 先例,见 notice.go),不读 gah-data、不复制会话逻辑 ——
// 与 updatepanel 同一条边界(docs/DESKTOP_FEASIBILITY.md §17)。
//
// 轮询只在浮层**可见时**跑(OnShow 起、OnHide 停):隐藏的浮层不该有网络开销。
package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// miniPollInterval 可见期间的轮询间隔(/api/state 是高频端点,2s 与页面状态栏同量级)。
const miniPollInterval = 2 * time.Second

// miniNoticeLimit 浮层里最多摆几条提示(多了该去主窗口看)。
const miniNoticeLimit = 3

// miniSnap 浮层某一刻要显示的东西。
type miniSnap struct {
	reachable bool   // 最近一次 /api/state 拿到了没有
	running   bool   // 当前(主)作用域在跑回合
	session   string // 会话显示名(空 = 主会话)
	model     string
	role      string
	version   string
	others    int          // 另有几个会话在跑(running_sessions 去掉主会话)
	notices   []noticeItem // 最近的 warn/error
	at        time.Time    // 最近一次成功刷新的时刻
	// restarting「重启服务」进行中:重启期间状态必然取不到,别显示成「服务失联」。
	restarting bool
}

// miniState 是 /api/state 里本浮层用到的子集(其余字段不关心,不解码)。
type miniState struct {
	Running  bool   `json:"running"`
	Model    string `json:"model"`
	Role     string `json:"role"`
	RoleName string `json:"role_name"`
	Version  string `json:"version"`
	Session  *struct {
		Name string `json:"name"`
	} `json:"session"`
	RunningSessions []string `json:"running_sessions"`
}

// parseMiniState 解出浮层需要的字段。形状不符就 ok=false(不猜)。
func parseMiniState(raw []byte) (miniSnap, bool) {
	var v miniState
	if err := json.Unmarshal(raw, &v); err != nil {
		return miniSnap{}, false
	}
	s := miniSnap{
		reachable: true,
		running:   v.Running,
		model:     v.Model,
		role:      v.RoleName,
		version:   v.Version,
	}
	if v.Session != nil {
		s.session = v.Session.Name
	}
	if s.role == "" {
		s.role = v.Role
	}
	// 「另有几个在跑」:running_sessions 里的会话键按作用域算。
	// 与主作用域无关:空键是主会话,其余都是「别的窗口」。
	for _, k := range v.RunningSessions {
		if k != "" {
			s.others++
		}
	}
	return s, true
}

// pickNotices 从提示流里挑出值得摆出来的(只 warn/error),取最后几条。
func pickNotices(items []noticeItem, limit int) []noticeItem {
	out := make([]noticeItem, 0, limit)
	for _, it := range items {
		if it.Level != "warn" && it.Level != "error" {
			continue
		}
		out = append(out, it)
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// miniPanel 概览浮层。
type miniPanel struct {
	sh   *shell
	win  *mygo.Window
	tray *mygo.Tray

	// 数据/副作用出口:为 nil 时走 shell 上的真实实现(单测注入假实现)。
	getState  func() ([]byte, bool)
	notices   func(since uint64) (noticePage, bool)
	doOpen    func()
	doNew     func()
	doRestart func()

	mu     sync.Mutex
	snap   miniSnap
	cursor uint64
	stop   chan struct{}
	// restarting 防连点:两次「重启服务」会并发停/起 sidecar,端口与进程都可能打架。
	restarting atomic.Bool
}

// newMiniPanel 建浮层描述(**懒**建,第一次用才建 flyout)。
func newMiniPanel(sh *shell, tray *mygo.Tray) *miniPanel {
	if tray == nil {
		return nil
	}
	return &miniPanel{sh: sh, tray: tray}
}

// openMiniWindow 壳侧入口(托盘菜单「概览…」)。
func (s *shell) openMiniWindow() {
	if s.mini == nil {
		s.mini = newMiniPanel(s, s.tray)
		if s.mini == nil {
			shellLogf("概览浮层不可用(托盘未创建)")
			return
		}
	}
	s.mini.open()
}

// open 显示浮层并开始轮询。
func (p *miniPanel) open() {
	win, err := p.newFlyout()
	if err != nil {
		shellLogf("概览浮层: %v", err)
		p.sh.notifyDialog("概览浮层不可用", err.Error())
		return
	}
	win.Show()
	p.startPolling()
	shellLogf("概览浮层: 已显示 bounds=%+v", win.Bounds())
	// 紧随 Show 的尺寸是**没拟合完**的(与更新面板同一条坑),再采一次收敛值。
	go func() {
		defer func() { _ = recover() }() // 退出竞争时窗口已销毁
		time.Sleep(600 * time.Millisecond)
		shellLogf("概览浮层: 收敛后 bounds=%+v", win.Bounds())
	}()
}

func (p *miniPanel) newFlyout() (*mygo.Window, error) {
	if p.win != nil {
		return p.win, nil
	}
	if p.tray == nil {
		return nil, fmt.Errorf("托盘不可用,概览浮层没有锚点")
	}
	p.win = mygo.NewFlyout(mygo.FlyoutOptions{
		Tray:       p.tray,
		Placement:  mygo.PlacementBottom,
		Popover:    true,
		Shadow:     true,
		Focusable:  true,
		FitContent: true,
		Content:    ui.View(p.view),
	})
	// 收起来就停轮询:隐藏的浮层没有网络开销。
	p.win.OnHide(p.stopPolling)
	return p.win, nil
}

// startPolling 起轮询(重复调用是幂等的)。
func (p *miniPanel) startPolling() {
	p.mu.Lock()
	if p.stop != nil {
		p.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	p.stop = stop
	p.mu.Unlock()

	go func() {
		p.pollOnce() // 先刷一次,别让用户看到空白
		t := time.NewTicker(miniPollInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				p.pollOnce()
			}
		}
	}()
}

// stopPolling 停轮询(幂等)。
func (p *miniPanel) stopPolling() {
	p.mu.Lock()
	stop := p.stop
	p.stop = nil
	p.mu.Unlock()
	if stop != nil {
		close(stop)
	}
}

// pollOnce 刷一次状态与提示。
func (p *miniPanel) pollOnce() {
	getState := p.getState
	if getState == nil && p.sh != nil {
		getState = p.sh.getState
	}
	fetch := p.notices
	if fetch == nil && p.sh != nil {
		fetch = p.sh.fetchNotices
	}

	var next miniSnap
	if getState != nil {
		if raw, ok := getState(); ok {
			if s, ok := parseMiniState(raw); ok {
				next = s
				next.at = time.Now()
			}
		}
	}
	if fetch != nil {
		p.mu.Lock()
		since := p.cursor
		p.mu.Unlock()
		if page, ok := fetch(since); ok {
			p.mu.Lock()
			if page.MaxID > p.cursor {
				p.cursor = page.MaxID
			}
			p.mu.Unlock()
			next.notices = pickNotices(page.Items, miniNoticeLimit)
		}
	}
	// 提示沿用上一次的(这一轮没取到不该清空),状态则以本轮为准。
	p.mu.Lock()
	prev := p.snap
	p.mu.Unlock()
	next.restarting = p.restarting.Load()
	if next.reachable && !prev.reachable {
		shellLogf("概览浮层: 已连上服务 running=%v model=%s", next.running, next.model)
	}
	if !next.reachable && prev.reachable {
		shellLogf("概览浮层: 拿不到服务状态,界面切到「服务失联」")
	}
	if next.notices == nil {
		next.notices = prev.notices
	}
	p.set(next)
}

// set 写快照并请主线程重画。
func (p *miniPanel) set(s miniSnap) {
	p.mu.Lock()
	p.snap = s
	p.mu.Unlock()
	if p.win != nil {
		p.win.Update(func() {})
	}
}

// setRestarting 只改快照里的 restarting 位(不整体覆盖:提示与状态要留着)。
func (p *miniPanel) setRestarting(on bool) {
	p.mu.Lock()
	s := p.snap
	s.restarting = on
	p.mu.Unlock()
	p.set(s)
}

// view 画一帧(只读快照,单测可直接喂 ui.NewTester)。
func (p *miniPanel) view(c *ui.Context) {
	t := *c.Theme().WithAccent(ui.Hex(panelAccent))
	t.Radius = 10
	c.SetTheme(&t)

	p.mu.Lock()
	s := p.snap
	p.mu.Unlock()

	root := ui.Column(c).Padding(16).Gap(10).Width(360)
	if !c.Vibrancy() {
		root.Background(t.Background)
	}
	root.Children(func() {
		// 状态行:圆点 + 一句话。
		ui.Row(c).Gap(8).AlignItems(ui.Baseline).Children(func() {
			ui.Text(c, "●").FontSize(11).TextColor(miniStatusColor(t, s))
			ui.Text(c, miniStatusLine(s)).Bold()
		})
		ui.Divider(c)

		if s.reachable {
			ui.Text(c, "会话: "+miniSessionName(s)).TextColor(t.TextMuted)
			ui.Text(c, "模型: "+orDash(s.model)+miniRoleSuffix(s)).TextColor(t.TextMuted)
			if s.others > 0 {
				ui.Text(c, fmt.Sprintf("另有 %d 个会话在跑", s.others)).TextColor(t.TextMuted)
			}
		} else {
			ui.Text(c, "拿不到服务状态。可试「重启服务」,或打开 gah 看诊断页。").Wrap().TextColor(t.TextMuted)
		}

		if len(s.notices) > 0 {
			ui.Divider(c)
			for _, n := range s.notices {
				ui.Text(c, noticeTitle(n)).MaxLines(1).TextColor(miniNoticeColor(t, n))
				ui.Text(c, noticeBody(n)).MaxLines(2).TextColor(t.TextMuted)
			}
		}

		ui.Divider(c)
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, "打开 gah").Clicked() {
				p.runOpen()
			}
			if ui.Button(c, "新会话").Clicked() {
				p.runNew()
			}
			if ui.Button(c, "重启服务").Clicked() {
				p.runRestart()
			}
		})
	})
}

// 副作用出口(单测可替换)。
func (p *miniPanel) runOpen() {
	if p.doOpen != nil {
		p.doOpen()
		return
	}
	if p.sh != nil {
		p.sh.focus()
	}
}

func (p *miniPanel) runNew() {
	if p.doNew != nil {
		p.doNew()
		return
	}
	if p.sh == nil {
		return
	}
	go func() {
		if _, err := p.sh.openSessionWindow(); err != nil {
			shellLogf("概览浮层: 新会话窗口失败: %v", err)
			p.sh.notifyDialog("无法新建会话窗口", err.Error())
		}
	}()
}

func (p *miniPanel) runRestart() {
	// 守卫放在副作用出口**之前**:连点两次重启会并发停/起 sidecar(端口与进程都会打架)。
	if !p.restarting.CompareAndSwap(false, true) {
		return
	}
	p.setRestarting(true)
	done := func() {
		p.restarting.Store(false)
		p.setRestarting(false)
	}
	if p.doRestart != nil {
		p.doRestart()
		done()
		return
	}
	if p.sh == nil {
		done()
		return
	}
	go func() {
		defer done()
		if err := p.sh.restartSidecar(); err != nil {
			shellLogf("概览浮层: 重启服务失败: %v", err)
			p.sh.notifyDialog("重启服务失败", err.Error())
			return
		}
		p.pollOnce()
	}()
}

// ---- 纯函数(单测重点) ----

func miniStatusLine(s miniSnap) string {
	switch {
	case s.restarting:
		// 重启期间 /api/state 必然取不到;说成「服务失联」会让人以为崩了。
		return "正在重启服务…"
	case !s.reachable:
		return "服务失联"
	case s.running:
		return "回合进行中"
	default:
		return "空闲"
	}
}

func miniStatusColor(t ui.Theme, s miniSnap) ui.Color {
	switch {
	case !s.reachable:
		return t.Danger
	case s.running:
		return t.Success
	default:
		return t.TextMuted
	}
}

func miniSessionName(s miniSnap) string {
	if s.session != "" {
		return s.session
	}
	return "主会话"
}

func miniRoleSuffix(s miniSnap) string {
	if s.role == "" {
		return ""
	}
	return " · 角色 " + s.role
}

func miniNoticeColor(t ui.Theme, n noticeItem) ui.Color {
	if n.Level == "error" {
		return t.Danger
	}
	return t.Warning
}

func noticeTitle(n noticeItem) string { return titleOf(n) }
func noticeBody(n noticeItem) string  { return firstLine(bodyOf(n)) }

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
