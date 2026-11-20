// updatepanel:托盘「检查更新…」的原生面板(mygo `ui`,不走 webview、不走阻塞弹窗)。
//
// 为什么要有它(2026-11-20):更新下载最长 30 分钟,原先只有两个阻塞 MessageBox ——
// 用户看不到进度、也没法取消;而「检查更新…」是托盘菜单里最后一个还在用系统弹窗的入口。
//
// 边界(方案见 docs/DESKTOP_FEASIBILITY.md §17):
//   - 数据只来自壳自己的 updater(不读 gah-data、不碰 sidecar 的 HTTP API);
//   - 判定复用 installGate / installPrecheck / explainUpdateError,与控制面
//     `/shell/update-install` 是**同一份**,不另写一套;
//   - 删除本文件 + tray.go 那一处菜单项即回到「对话框」形态(随时可丢)。
package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// panelAccent 与 web 端同一支强调色(web-src/src/style.css 的 DeepSeek 蓝)。
const panelAccent = "#4176e6"

// releasePageURL 便携版/被挡住时的下载落点(与 README 里给用户的那句话一致)。
const releasePageURL = "https://github.com/nekoleamo/go-agent-harness/releases/latest"

// 面板阶段。与 updater 的五态**不是一回事**:updater 只有「下载/装」两段,
// 面板还要表示「检查中 / 已是最新 / 被准入判定挡住」这些只发生在面板里的态。
const (
	panelIdle        = "idle"
	panelChecking    = "checking"
	panelCurrent     = "current"
	panelAvailable   = "available"
	panelDownloading = "downloading"
	panelReady       = "ready"
	panelFailed      = "failed"
	panelBlocked     = "blocked"
)

// panelSnap 面板某一刻要显示的东西(view 只读它,不碰 shell)。
type panelSnap struct {
	phase   string
	version string // 远端版本(available/downloading/ready 用)
	notes   string // 更新说明(available 时展示,过长截断)
	message string // 出错/被挡住的人话
	expect  string // 用户看到并确认的版本(安装前对账)
	written int64  // 已下载字节
	total   int64  // 总字节(<=0 表示未知)
}

// updatePanel 托盘锚定的更新面板。零值不可用,一律经 newUpdatePanel 构造。
type updatePanel struct {
	sh   *shell
	win  *mygo.Window
	tray *mygo.Tray

	// 副作用出口:为 nil 时走 shell 上的真实实现。单测注入假实现后
	// 只验「哪个按钮点了会触发哪件事」,不碰网络、不碰文件。
	doCheck    func()
	doInstall  func(expect string)
	doRestart  func()
	doClose    func()
	doDownload func()

	mu     sync.Mutex
	snap   panelSnap
	cancel context.CancelFunc // 下载期可取消
	// busy 检查/安装进行中。防连点:备份那几秒按钮还在(阶段还没切),连点会做两次备份、
	// 起两份安装器 —— 一次发布替换两份文件正是最难排查的那类现场。
	busy atomic.Bool
}

// tryBegin 抢占「进行中」标记;已经在进行就返回 false(调用方直接退出)。
func (p *updatePanel) tryBegin() bool { return p.busy.CompareAndSwap(false, true) }

// end 放开标记。
func (p *updatePanel) end() { p.busy.Store(false) }

// newUpdatePanel 建面板(**懒**建:第一次用到才建 flyout,避免启动期多点一个窗口)。
// tray 为 nil 时返回 nil(托盘起不来的降级路径:面板一样没有落点)。
func newUpdatePanel(sh *shell, tray *mygo.Tray) *updatePanel {
	if tray == nil {
		return nil
	}
	p := &updatePanel{sh: sh, tray: tray, snap: panelSnap{phase: panelIdle}}
	return p
}

// newFlyout 建 flyout 本体。必须在 App 就绪后调用(由第一次 Show 触发)。
func (p *updatePanel) newFlyout() (*mygo.Window, error) {
	if p.win != nil {
		return p.win, nil
	}
	if p.tray == nil {
		return nil, fmt.Errorf("托盘不可用,更新面板没有锚点")
	}
	p.win = mygo.NewFlyout(mygo.FlyoutOptions{
		Tray:       p.tray,
		Placement:  mygo.PlacementBottom,
		Popover:    true, // macOS 走 NSPopover(材质/圆角/箭头/点外关闭);其它平台无变化
		Shadow:     true,
		Focusable:  true,
		FitContent: true,
		MinWidth:   320,
		MaxWidth:   420,
		Content:    ui.View(p.view),
	})
	return p.win, nil
}

// openUpdatePanel 壳侧入口:懒建面板、接上进度钩子、开面板(并自动查一次)。
//
// 托盘不可用(tray == nil)时面板没有锚点:只记日志,不退回阻塞弹窗 ——
// 降级状态下的结论由页面设置面板里的「检查更新」提供。
func (s *shell) openUpdatePanel() {
	if s.panel == nil {
		s.panel = newUpdatePanel(s, s.tray)
		if s.panel == nil {
			shellLogf("更新面板不可用(托盘未创建),更新检查留在页面设置面板")
			return
		}
		if s.updater != nil {
			s.updater.setProgressHook(s.panel.progress)
		}
	}
	s.panel.open()
}

// open 显示面板;若还停在 idle 就自动开始一次检查(点「检查更新…」来的预期)。
func (p *updatePanel) open() {
	win, err := p.newFlyout()
	if err != nil {
		shellLogf("更新面板: %v", err)
		p.sh.notifyDialog("更新面板不可用", err.Error())
		return
	}
	win.Show()
	// 位置/尺寸留痕:浮层锚点错位(gap/多显示器)或 FitContent 未收敛时,这是唯一证据。
	// 必须**延迟取一次**:原生内容的首帧还在量内容,紧随 Show 的 bounds 是还没拟合的大小。
	shellLogf("更新面板: 已显示 bounds=%+v", win.Bounds())
	go func() {
		// 退出竞争:窗口已销毁时取 bounds 会 panic,一行日志不值得拖垮退出链。
		defer func() { _ = recover() }()
		// 两次采样:600ms 是「检查中」的拟合结果,2s 之后通常已切到 available(带说明+按钮),
		// 两行一起看才知道 FitContent 有没有随阶段重排。
		for _, d := range []time.Duration{600 * time.Millisecond, 2 * time.Second} {
			time.Sleep(d)
			shellLogf("更新面板: 收敛后 bounds=%+v", win.Bounds())
		}
	}()
	p.mu.Lock()
	idle := p.snap.phase == panelIdle || p.snap.phase == panelFailed
	p.mu.Unlock()
	if idle {
		p.runCheck()
	}
}

// runCheck 走副作用出口(单测可替换)。
func (p *updatePanel) runCheck() {
	if p.doCheck != nil {
		p.doCheck()
		return
	}
	go p.check()
}

func (p *updatePanel) runInstall(expect string) {
	if p.doInstall != nil {
		p.doInstall(expect)
		return
	}
	go p.install(expect)
}

func (p *updatePanel) runRestart() {
	if p.doRestart != nil {
		p.doRestart()
		return
	}
	if p.sh == nil || p.sh.updater == nil {
		return
	}
	if err := p.sh.updater.Restart(context.Background()); err != nil {
		shellLogf("更新面板: 重启失败: %v", err)
	}
}

// runDownload 打开发行页(便携版自动更新被拒时的唯一出路)。
func (p *updatePanel) runDownload() {
	if p.doDownload != nil {
		p.doDownload()
		return
	}
	if p.sh == nil {
		return
	}
	if err := p.sh.openExternal(releasePageURL); err != nil {
		shellLogf("更新面板: 打开下载页失败: %v", err)
		p.sh.notifyDialog("打不开浏览器", err.Error())
	}
}

func (p *updatePanel) runClose() {
	if p.doClose != nil {
		p.doClose()
		return
	}
	if p.win != nil {
		p.win.Hide()
	}
}

// check 只查不装(2026-10-03 的决定),结论落到面板而不是弹窗。
func (p *updatePanel) check() {
	if !p.tryBegin() {
		return
	}
	defer p.end()
	p.set(func(s *panelSnap) {
		s.phase = panelChecking
		s.message = ""
	})
	sh := p.sh
	if sh == nil || sh.updater == nil {
		p.set(func(s *panelSnap) {
			s.phase = panelFailed
			s.message = "更新器未装配(壳日志里有原因)。"
		})
		return
	}
	sh.setTrayChecking(true)
	defer sh.setTrayChecking(false)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	shellLogf("更新面板: 检查更新(当前 %s)", version)
	rel, err := sh.updater.Check(ctx)
	if err != nil {
		shellLogf("更新面板: 检查失败: %v", err)
		p.set(func(s *panelSnap) {
			s.phase = panelFailed
			s.message = explainUpdateError(err)
		})
		return
	}
	if rel == nil {
		p.set(func(s *panelSnap) {
			s.phase = panelCurrent
			s.message = "当前版本 " + version + " 已经是最新的。"
		})
		return
	}
	shellLogf("更新面板: 发现新版本 %s", rel.Version)
	// 记下来:用户点「下载并安装」时拿它跟「他看到的那个版本」对账。
	sh.noteSeenVersion(rel.Version)
	p.set(func(s *panelSnap) {
		s.phase = panelAvailable
		s.version = rel.Version
		s.notes = rel.Notes
		s.expect = rel.Version
		s.message = ""
		s.written, s.total = 0, 0
	})
}

// install 下载并安装。准入与备份走 shell.installPrecheck(与控制面同一份判定)。
func (p *updatePanel) install(expect string) {
	if !p.tryBegin() {
		return
	}
	defer p.end()
	sh := p.sh
	if sh == nil || sh.updater == nil {
		return
	}
	if expect == "" {
		expect = sh.seenVersion()
	}
	// 先把阶段切到「下载中」再准入:否则备份那几秒按钮还亮着(连点会把下面的
	// busy 守卫当摆设,而守卫只挡得住协程层)。总量未知时进度条走不确定长度态。
	p.set(func(s *panelSnap) {
		s.phase = panelDownloading
		s.written, s.total = 0, 0
	})
	action, msg, err := sh.installPrecheck(expect)
	switch {
	case action == installPortableOnly:
		shellLogf("更新面板: 便携版拒绝自动安装")
		p.set(func(s *panelSnap) {
			s.phase = panelBlocked
			s.message = msg
		})
		return
	case action == installVersionChanged:
		shellLogf("更新面板: 版本已漂移(确认的是 %s,现在是 %s),不安装", expect, sh.seenVersion())
		p.set(func(s *panelSnap) {
			s.phase = panelBlocked
			s.message = msg
		})
		return
	case err != nil:
		shellLogf("更新面板: 升级前备份失败(中止升级): %v", err)
		p.set(func(s *panelSnap) {
			s.phase = panelFailed
			s.message = err.Error()
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	p.mu.Lock()
	p.cancel = cancel
	p.mu.Unlock()
	defer func() {
		cancel()
		p.mu.Lock()
		p.cancel = nil
		p.mu.Unlock()
	}()

	shellLogf("更新面板: 开始下载安装更新")
	if err := sh.updater.DownloadAndInstall(ctx); err != nil {
		if ctx.Err() != nil {
			shellLogf("更新面板: 下载已取消")
			p.set(func(s *panelSnap) {
				s.phase = panelAvailable
				s.message = "已取消下载。"
				s.written, s.total = 0, 0
			})
			return
		}
		shellLogf("更新面板: 安装失败: %v", err)
		p.set(func(s *panelSnap) {
			s.phase = panelFailed
			s.message = explainUpdateError(err)
		})
		return
	}
	v := sh.seenVersion()
	shellLogf("更新面板: 更新已就绪(%s),等用户重启", v)
	p.set(func(s *panelSnap) {
		s.phase = panelReady
		s.version = v
		s.message = ""
	})
}

// cancelDownload 取消下载(下载期才有意义)。
func (p *updatePanel) cancelDownload() {
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// set 改面板状态并请主线程重画(可从任意协程调用)。
func (p *updatePanel) set(fn func(*panelSnap)) {
	p.mu.Lock()
	fn(&p.snap)
	p.mu.Unlock()
	p.repaint()
}

// progress 下载进度回调(由 updater 的钩子推动;可从任意协程调用)。
func (p *updatePanel) progress(written, total int64) {
	p.set(func(s *panelSnap) {
		if s.phase == panelDownloading {
			s.written, s.total = written, total
		}
	})
}

func (p *updatePanel) repaint() {
	if p.win != nil {
		p.win.Update(func() {})
	}
}

// view 画一帧。只读快照,不碰 shell —— 单测可以直接拿它喂 ui.NewTester。
func (p *updatePanel) view(c *ui.Context) {
	t := *c.Theme().WithAccent(ui.Hex(panelAccent))
	t.Radius = 10
	c.SetTheme(&t)

	p.mu.Lock()
	s := p.snap
	p.mu.Unlock()

	// **宽度自己钉死**:FitContent 量的是内容的 max-content,长说明会把浮层撑到几百点,
	// 且实测 MinWidth/MaxWidth 对 native 内容的拟合不生效(macOS 2026-11-20:
	// 给 Min 320/Max 420 仍得到 446 宽)。所以宽定 360、高度交给 FitContent。
	root := ui.Column(c).Padding(16).Gap(10).Width(360)
	// Popover 材质下不自己画底(否则会把系统材质盖住);非材质形态才铺背景。
	if !c.Vibrancy() {
		root.Background(t.Background)
	}
	root.Children(func() {
		ui.Text(c, panelTitle(s)).FontSize(15).Bold()
		p.body(c, t, s)
	})
}

// panelTitle 各阶段的一句话标题。
func panelTitle(s panelSnap) string {
	switch s.phase {
	case panelChecking:
		return "正在检查更新…"
	case panelCurrent:
		return "已是最新"
	case panelAvailable:
		return "发现新版本 " + s.version
	case panelDownloading:
		return "正在下载 " + s.version
	case panelReady:
		return "更新已就绪"
	case panelFailed:
		return "更新失败"
	case panelBlocked:
		return "无法自动更新"
	default:
		return "更新"
	}
}

// body 阶段正文与按钮。按钮回调一律经 run* 出口(不在 view 里直接起网络/写盘)。
func (p *updatePanel) body(c *ui.Context, t ui.Theme, s panelSnap) {
	switch s.phase {
	case panelChecking:
		ui.Spinner(c)
		ui.Text(c, "正在跟更新源核对版本…").TextColor(t.TextMuted)

	case panelCurrent:
		ui.Text(c, s.message).TextColor(t.TextMuted)
		if ui.Button(c, "再次检查").Clicked() {
			p.runCheck()
		}

	case panelAvailable:
		if s.notes != "" {
			// 短说明直接铺开(固定 96 高会在下面留一条空白带);长说明才进滚动区。
			if len([]rune(s.notes)) <= 240 {
				ui.Text(c, s.notes).Wrap().TextColor(t.TextMuted)
			} else {
				ui.Scroll(c).Height(96).Children(func() {
					ui.Text(c, truncateRunes(s.notes, 600)).TextColor(t.TextMuted)
				})
			}
		} else {
			ui.Text(c, "这个版本没带更新说明。").TextColor(t.TextMuted)
		}
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, "下载并安装").Clicked() {
				p.runInstall(s.expect)
			}
			if ui.Button(c, "稍后").Clicked() {
				p.runClose()
			}
		})

	case panelDownloading:
		ui.Progress(c, progressRatio(s))
		ui.Text(c, progressLine(s)).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, "取消下载").Clicked() {
				p.cancelDownload()
			}
		})

	case panelReady:
		ui.Text(c, "新版本 "+s.version+" 已就绪,重启后生效。").TextColor(t.TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, "立即重启").Clicked() {
				p.runRestart()
			}
			if ui.Button(c, "稍后").Clicked() {
				p.runClose()
			}
		})

	case panelBlocked:
		ui.Text(c, truncateRunes(s.message, 600)).Wrap().TextColor(t.TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, "去下载").Clicked() {
				p.runDownload()
			}
			if ui.Button(c, "关闭").Clicked() {
				p.runClose()
			}
		})

	case panelFailed:
		ui.Scroll(c).Height(96).Children(func() {
			ui.Text(c, truncateRunes(s.message, 600)).TextColor(t.TextMuted)
		})
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, "重试").Clicked() {
				p.runCheck()
			}
			if ui.Button(c, "关闭").Clicked() {
				p.runClose()
			}
		})

	default:
		ui.Text(c, "当前版本 "+version).TextColor(t.TextMuted)
		if ui.PrimaryButton(c, "检查更新").Clicked() {
			p.runCheck()
		}
	}
}

// progressRatio 进度比例;总量未知时返回 -1(Progress 的「不确定长度」)。
func progressRatio(s panelSnap) float64 {
	if s.total <= 0 {
		return -1
	}
	r := float64(s.written) / float64(s.total)
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

// progressLine 「已下载 3.2 / 18.0 MB」这类人话。
func progressLine(s panelSnap) string {
	if s.total <= 0 {
		return fmt.Sprintf("已下载 %s", mb(s.written))
	}
	return fmt.Sprintf("已下载 %s / %s", mb(s.written), mb(s.total))
}

func mb(n int64) string {
	if n <= 0 {
		return "0 MB"
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

// truncateRunes 按字符截断(中文按字算,不切半个).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
