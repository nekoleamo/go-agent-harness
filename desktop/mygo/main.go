// gah 桌面壳 · mygo 0.10.0 webview 模式。
//
// 与 Wails 壳同口径的四条主链路:
//  1. 壳挑一个空闲回环端口,经 GAH_WEB_ADDR 交给 sidecar `gah --profile web`;
//  2. 轮询 /api/state 就绪 → 主窗口导航到 `http://127.0.0.1:<port>/?shell=desktop`;
//  3. 页面仍由 gah 自己提供(同源直连,SSE/WS/cookie 全不动),壳侧能力走**自建控制面
//     HTTP API**(control.go:回环 + 一次性 token + 只放行 gah 那个源),不绑 Go 方法 ——
//     保住「页面 JS(含 UI 插件)拿不到壳能力凭据」这条边界;
//  4. 退出链:关窗 = 收进托盘;退出 = POST /api/shutdown → 等端口释放 → 超时强杀。
//
// 与 Wails 的语义差异(踩过,写在这里):
//   - mygo 里 `OnClose` 的 PreventDefault 会**取消 App.Quit()**(prepareQuit 逐个 close),
//     所以退出链必须先置 quitting 再 Quit();
//   - mygo 没有框架级 PanicHandler:框架回调里的 panic 抓不到,只能在自己的回调里兜;
//   - mygo 没有导出开机自启 API:notify.go 里自己实现(mac LaunchAgent / win Run 键)。
package main

import (
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
)

// version 由构建时 -ldflags "-X main.version=..." 注入(发行版跟 tag 走)。
var version = "dev"

// firstFrameFallback 前端首帧就绪的最长等待(与 Wails 壳同值)。
const firstFrameFallback = 1200 * time.Millisecond

// firstFrameSettle 前端报「已挂载」之后留多少给浏览器合成一帧。
const firstFrameSettle = 40 * time.Millisecond

// noApp 是个占位:mygo 的应用对象是包级单例(App),没有 `s.app` 这层间接。
// 保留它只是为了让从 Wails 壳搬过来的代码在 diff 里更好读(见 control.go 的注释)。
var app = mygo.App

// shell 桌面壳的运行态(单实例,全字段在 Run 之前填好后再起协程)。
type shell struct {
	window *mygo.Window
	tray   *mygo.Tray
	ctl    *controlPlane

	addr     string      // gah web 的回环地址
	gahToken string      // gah 的 data.auth_token(空 = 未开 token 模式)
	ready    atomic.Bool // 界面是否已就绪(就绪前退出要铺诊断页)
	mu       sync.Mutex  // 保护 sc(tray 退出与退出链会并发取)
	sc       *sidecarProc
	quitting atomic.Bool
	notifier *notifier // 系统通知

	flasher  windowFlasher // 窗口闪烁(窗口创建后注入)
	flashDur time.Duration // 闪烁收尾时长(0 = 不收尾,测试用)

	noticeSvcMissing atomic.Bool // 未装配通知:只记一次
	// 下列字段全部在 Run 之前写好、之后只读 —— 控制面 handler 会并发读它们。
	plan     runtimePlan
	planErr  error
	dataRoot string
	portable bool
	updater  *updater

	trayCheckItem *mygo.MenuItem // 托盘「检查更新…」
	panel         *updatePanel   // 原生更新面板(懒建;托盘不可用时为 nil)
	mini          *miniPanel     // 概览浮层(懒建;同理)

	updateSeenMu sync.Mutex
	updateSeen   string

	uiReady     chan struct{}
	uiReadyOnce sync.Once
	windowShown atomic.Bool
	updateBusy  atomic.Bool
	updateSeq   atomic.Uint64
}

func main() {
	initLog(version)

	// 安装器模式:壳自己再被拉起一次,只做换文件(没有 UI、没有 sidecar)。
	if planPath, ok := installerMode(); ok {
		if err := installerMain(planPath); err != nil {
			shellLogf("安装器: 失败: %v", err)
			os.Exit(1)
		}
		return
	}
	// 上次没删掉的旧版本(Windows 上被占用,或安装器被强杀):启动时顺手清。
	pruneOldTargets()

	addr, err := pickFreeAddr()
	if err != nil {
		shellLogf("挑空闲端口失败: %v", err)
		fmt.Fprintf(os.Stderr, "gah: 挑空闲端口失败: %v\n", err)
		os.Exit(1)
	}
	sh := &shell{addr: addr, gahToken: os.Getenv("GAH_WEB_TOKEN"), uiReady: make(chan struct{})}
	if plan, perr := resolveRuntime(localDataDir(), version); perr != nil {
		sh.planErr = perr
		shellLogf("运行落点解析失败: %v", perr)
	} else {
		sh.plan = plan
		sh.dataRoot = plan.dataRoot
		sh.portable = plan.portable
	}
	shellLogf("gah web 地址: %s(token 模式: %v)", addr, sh.gahToken != "")

	if notificationsSupported() {
		sh.notifier = newNotifier()
	} else {
		shellLogf("通知: 未打包运行(macOS 需要 bundle identifier),本轮禁用系统通知")
	}
	if sh.notifier != nil {
		sh.notifier.logNotifyPermission()
	}

	// 控制面必须在窗口之前起来:页面一加载就会打 /shell/*,晚一步就是 502。
	cp, err := startControlPlane(sh)
	if err != nil {
		shellLogf("控制面启动失败: %v", err)
		fmt.Fprintf(os.Stderr, "gah: 控制面启动失败: %v\n", err)
		os.Exit(1)
	}
	sh.ctl = cp
	shellLogf("控制面: http://127.0.0.1:%s(仅放行 http://%s)", cp.port(), addr)

	// 更新器起不来不该拦住整个应用(界面/会话全不能用比没有更新更糟);
	// 但要留痕,并让控制面如实报「不可用」。
	if u, uerr := newUpdater(); uerr != nil {
		shellLogf("更新器初始化失败(本次禁用检查更新): %v", uerr)
	} else {
		sh.updater = u
	}

	// 窗口/托盘必须在 App 就绪后创建(mygo 的要求),boot 等这个信号再导航。
	windowReady := make(chan struct{})
	mygo.App.WhenReady(func() {
		defer close(windowReady)
		sh.buildWindow()
		sh.buildTray()
		sh.setTrayStarting(true)
		sh.applyDockIcon()
	})

	// 单实例:第二次启动只聚焦已有窗口(托盘常驻形态的常规做法)。
	if !wantsNewInstance() {
		mygo.App.OnSecondInstance(func([]string, string) {
			shellLogf("第二次启动:聚焦已有窗口")
			sh.focus()
		})
		// 点通知/点 Dock 图标都按「召回窗口」处理。
		mygo.App.OnNotificationClick(func(id string) {
			shellLogf("通知被点击: %s", id)
			sh.focus()
		})
	} else {
		shellLogf("以 --new-instance 启动:单实例锁由宿主数据根的租约处理")
	}

	// 退出链第一跳:托盘退出 / 系统退出都走这里 → 优雅停 sidecar。
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { sh.stopSidecar(true) })

	go sh.boot(windowReady)

	if err := mygo.App.Run(); err != nil {
		shellLogf("App.Run 返回错误: %v", err)
		fmt.Fprintf(os.Stderr, "gah: %v\n", err)
	}
	sh.stopSidecar(false) // Run 返回后的兜底
	shellLogf("=== 壳退出 ===")
}

// buildWindow 建主窗口(几何恢复 + 关窗收托盘)。
func (s *shell) buildWindow() {
	opts := mygo.WindowOptions{
		Title:  "gah",
		Width:  1200,
		Height: 800,
		// 就绪后再显:避免先闪一帧空白页(与 Wails 壳同口径)。
		Hidden: true,
	}
	if st, ok := loadWindowState(); ok && screenVisible(st) {
		opts.X, opts.Y, opts.Width, opts.Height = st.X, st.Y, st.W, st.H
		shellLogf("窗口几何: 恢复上次位置 %dx%d @ (%d,%d)", st.W, st.H, st.X, st.Y)
	}
	s.window = mygo.NewWindow(opts)
	s.flasher = &mygoFlasher{win: s.window}
	s.flashDur = attentionFlashDur
	watchWindowState(s.window)

	// 关窗 ≠ 退出:收进托盘。mygo 的 OnClose 阻止同时会取消 App.Quit(),
	// 所以「是否真退出」只看 quitting 标志(见文件头)。
	s.window.OnClose(func(e *mygo.CloseEvent) {
		if s.quitting.Load() {
			return
		}
		e.PreventDefault()
		s.window.Hide()
		shellLogf("主窗口已收进托盘(窗口保持存活,托盘「打开 gah」可再提起来)")
	})
}

// boot 启动 sidecar 并在就绪后把窗口导航到 gah 的页面。
func (s *shell) boot(windowReady <-chan struct{}) {
	if s.planErr != nil {
		<-windowReady
		s.showDiag("找不到运行文件", []string{
			s.planErr.Error(),
			"开发态:先跑 bash scripts/gen-desktop.sh(或设 GAH_SIDECAR 指向已构建的 gah)。",
		})
		return
	}
	plan := s.plan
	shellLogf("运行落点: bin=%s data_root=%s external=%v staged=%v portable=%v",
		plan.bin, plan.dataRoot, plan.external, plan.staged, plan.portable)
	setShellNotices(plan.notices)
	for _, n := range plan.notices {
		s.announce("gah 便携模式", n)
	}

	sc, err := startSidecar(plan.bin, s.addr, s.gahToken, "127.0.0.1:"+s.ctl.port(), s.ctl.token)
	if err != nil {
		<-windowReady
		shellLogf("%v", err)
		s.showDiag("gah 服务未能启动", []string{err.Error(), "壳日志: " + shellLogPath()})
		return
	}
	s.mu.Lock()
	s.sc = sc
	s.mu.Unlock()
	shellLogf("sidecar 已启动 pid=%d", sc.cmd.Process.Pid)

	go func() {
		err := sc.wait()
		if err != nil {
			shellLogf("sidecar 已退出: %v", err)
		} else {
			shellLogf("sidecar 已退出: 正常")
		}
		if !s.ready.Load() && !s.quitting.Load() {
			s.showDiag("gah 服务进程已退出", []string{
				"后台服务在界面就绪前就退出了;壳日志里 `sidecar:` 开头的行是它自己的输出。",
				"壳日志: " + shellLogPath(),
			})
		}
	}()

	if !waitReady(s.addr, s.gahToken, 12*time.Second) {
		<-windowReady
		shellLogf("12s 内 /api/state 未就绪")
		s.showDiag("gah 服务启动超时", []string{
			"12 秒内没有等到 /api/state 应答。常见原因:数据根不可写、端口被占、插件装配失败。",
			"壳日志: " + shellLogPath(),
		})
		return
	}
	s.ready.Store(true)
	u := s.shellURL()
	shellLogf("就绪,导航: %s", redactURL(u))
	<-windowReady
	if err := s.window.Page().LoadURL(u); err != nil {
		shellLogf("导航失败: %v", err)
		s.showDiag("无法打开界面", []string{err.Error()})
		return
	}
	// **不在这里 Show**:导航刚发起,webview 还没画任何像素。等前端首帧报就绪
	// (或兜底超时)再显窗。
	go s.waitFirstFrame()
	s.logSidecarVersion()
	go s.startNoticePoller()
	go s.startHealthWatch()
	if secs := updateAutoCheckSeconds(); secs > 0 {
		shellLogf("GAH_SHELL_UPDATE_AUTOCHECK=%d: %ds 后自动检查更新", secs, secs)
		go func() {
			time.Sleep(time.Duration(secs) * time.Second)
			s.openUpdatePanel()
		}()
	}
	if secs := miniOpenSeconds(); secs > 0 {
		shellLogf("GAH_SHELL_MINI_OPEN=%d: %ds 后自动开概览浮层", secs, secs)
		go func() {
			time.Sleep(time.Duration(secs) * time.Second)
			s.openMiniWindow()
		}()
	}
}

// shellURL 主窗口地址:`?shell=desktop` 让 web UI 走桌面壳布局;fragment 里带 gah
// 自己的 token(控制面地址与令牌经 env 交给 sidecar,/shell/* 同源代理)。
func (s *shell) shellURL() string {
	base := "http://" + s.addr + "/?shell=desktop"
	if s.gahToken != "" {
		return base + "#token=" + url.QueryEscape(s.gahToken)
	}
	return base
}

// redactURL 把 fragment 里的 token 打码后再进日志。
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.Fragment == "" {
		return raw
	}
	u.Fragment = "token=***"
	return u.String()
}

// waitFirstFrame 等前端报「首帧已画」再显窗;超过兜底就显示。
func (s *shell) waitFirstFrame() {
	select {
	case <-s.uiReady:
		time.Sleep(firstFrameSettle)
		s.showMainWindow("前端首帧就绪")
	case <-time.After(firstFrameFallback):
		shellLogf("前端 %v 内未报首帧就绪,兜底显窗(可能闪一帧窗口底色)", firstFrameFallback)
		s.showMainWindow("兜底超时")
	}
}

// markUIFirstFrame 前端报「首帧已画」(幂等)。
func (s *shell) markUIFirstFrame() {
	s.uiReadyOnce.Do(func() {
		if s.uiReady != nil {
			close(s.uiReady)
		}
	})
}

// showMainWindow 首次显示主窗口(幂等)。
func (s *shell) showMainWindow(reason string) {
	if s.window == nil || s.windowShown.Swap(true) {
		return
	}
	shellLogf("窗口显示(%s)", reason)
	s.setTrayStarting(false)
	s.window.Show()
	s.window.Focus()
	s.runDebugHooks()
}

// focus 把已有窗口提到前台(托盘点击 / 二次启动 / 通知点击)。
func (s *shell) focus() {
	if s.window == nil {
		return
	}
	if !s.windowShown.Swap(true) {
		shellLogf("窗口显示(用户主动召回,首帧可能未就绪)")
		s.setTrayStarting(false)
	}
	s.window.Restore()
	s.window.Show()
	s.window.Focus()
	// 用户按过 Cmd+H(整个应用被隐藏)时,activate 不会自动 unhide。
	app.Show()
	app.Focus()
}

// stopSidecar 走优雅退出链(幂等)。fromShutdown=true 表示由退出链触发。
func (s *shell) stopSidecar(fromShutdown bool) {
	s.mu.Lock()
	sc := s.sc
	s.mu.Unlock()
	if sc == nil {
		return
	}
	if fromShutdown {
		s.quitting.Store(true)
	}
	sc.stop(s.addr, s.gahToken)
}

// showDiag 把失败原因铺到窗口里(比白屏有用),并显示窗口。
func (s *shell) showDiag(title string, lines []string) {
	body := ""
	for _, l := range lines {
		body += "<p>" + htmlEscape(l) + "</p>"
	}
	tail := shellLogTail(16)
	html := `<html><body style="font-family:-apple-system,'Segoe UI',sans-serif;padding:36px;max-width:760px;line-height:1.7;color:#1f2328">` +
		`<h2 style="margin:0 0 12px">` + htmlEscape(title) + `</h2>` + body +
		`<p style="color:#6b7280;font-size:13px">日志文件: ` + htmlEscape(shellLogPath()) + `</p>` +
		`<pre style="white-space:pre-wrap;background:#f5f5f5;padding:12px;border-radius:6px;font-size:12px">` + htmlEscape(tail) + `</pre>` +
		`<p style="color:#6b7280;font-size:13px">版本 ` + htmlEscape(version) + ` · ` + htmlEscape(runtime.GOOS+"/"+runtime.GOARCH) + `</p>` +
		`</body></html>`
	if s.window != nil {
		s.window.Page().LoadHTML(html, "")
		s.showMainWindow("诊断页")
	}
}

// htmlEscape 诊断页文本转义。
func htmlEscape(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case '&':
			out = append(out, []rune("&amp;")...)
		case '<':
			out = append(out, []rune("&lt;")...)
		case '>':
			out = append(out, []rune("&gt;")...)
		case '"':
			out = append(out, []rune("&quot;")...)
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

// announce 尽力用系统通知告诉用户一件事;不可用时退回日志。
func (s *shell) announce(title, body string) {
	shellLogf("提示: %s — %s", title, body)
	if err := s.sendNotification(title, body); err != nil {
		shellLogf("(通知不可用,已只写日志: %v)", err)
	}
}

// wantsNewInstance 是否要求「另起一个进程」(旧壳 --new-instance)。
func wantsNewInstance() bool {
	for _, a := range os.Args[1:] {
		if a == "--new-instance" {
			return true
		}
	}
	return false
}

// trayIcon 托盘图标:mac 用模板图(随明暗主题反色),win 用彩色图。
func trayIcon() []byte {
	if runtime.GOOS == "darwin" {
		return trayTemplatePNG
	}
	return trayColorPNG
}

// updateAutoCheckSeconds 升级冒烟入口:>0 时启动后自动检查一次。
func updateAutoCheckSeconds() int {
	if v := os.Getenv("GAH_SHELL_UPDATE_AUTOCHECK"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// miniOpenSeconds 真机验证用的开关:启动 N 秒后自动开概览浮层(不开则等托盘菜单)。
// 有这个开关,托盘锚定/DPI/深色这些只能人眼确认的东西才能在 CI 之外的一台机器上
// 用一条命令跑出来(与 GAH_SHELL_UPDATE_AUTOCHECK 同口径)。
func miniOpenSeconds() int {
	if v := os.Getenv("GAH_SHELL_MINI_OPEN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// applyDockIcon 把 gah 自己的图标挂到 Dock(打包运行时才有意义;裸二进制是通用图标)。
//
// Wails 壳是把图标传给 application.Options.Icon;mygo 没有窗口级图标参数,
// 但 App.Dock.SetIcon 等价够用(Windows 的任务栏图标随 exe 资源走,由打包脚本负责)。
func (s *shell) applyDockIcon() {
	if runtime.GOOS != "darwin" || bundlePath() == "" {
		return
	}
	if err := mygo.App.Dock.SetIcon(appIconPNG); err != nil {
		shellLogf("设置 Dock 图标失败(不影响使用): %v", err)
	}
}
