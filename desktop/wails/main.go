// gah 桌面壳(Go + Wails v3)—— 替换原 Tauri v2(Rust)壳。
//
// 为什么换:壳逻辑回到 Go(与 gah 同语言、可正常单测、不再需要 Rust 工具链),
// 托盘/菜单/通知/自启/单实例/更新器/打包都由 v3 框架内置。
// 背景与取舍见 docs/DESKTOP_FEASIBILITY.md §11。
//
// 与旧壳一致的四条主链路:
//  1. 壳挑一个空闲回环端口,经 GAH_WEB_ADDR 交给 sidecar `gah --profile web`;
//  2. 轮询 /api/state 就绪 → 主窗口导航到 `http://127.0.0.1:<port>/?shell=desktop`;
//  3. 页面仍由 gah 自己提供(同源直连,SSE/WS/cookie 全不动);
//  4. 退出链:POST /api/shutdown → 等端口释放 → 超时强杀;壳被强杀时 sidecar 靠 stdin EOF 自己退。
//
// 关键差异(旧壳没有的一层):**v3.0 里从外部 URL 加载的页面拿不到 bindings**
// (页面源是 gah 自己,不是 wails:// 或 wails.localhost),所以壳侧能力不再走 IPC,
// 改由壳自建的**本地控制面 HTTP API**(见 control.go:回环 + 一次性 token + 只放行
// gah 那个源)提供。页面侧经 URL fragment 拿到控制面端口与 token。
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

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// version 由构建时 -ldflags "-X main.version=..." 注入(发行版跟 tag 走)。
var version = "dev"

// shell 桌面壳的运行态(单实例,全字段在 main 里填好后再起协程)。
type shell struct {
	app      *application.App
	window   *application.WebviewWindow
	ctl      *controlPlane
	addr     string      // gah web 的回环地址
	gahToken string      // gah 的 data.auth_token(空 = 未开 token 模式)
	ready    atomic.Bool // 界面是否已就绪(就绪前退出要铺诊断页)
	mu       sync.Mutex  // 保护 sc(tray 退出与 OnShutdown 会并发取)
	sc       *sidecarProc
	quitting atomic.Bool
	notifier *notifier // 系统通知(未装配时为 nil,调用方需容忍)

	noticeSvcMissing atomic.Bool // 宿主未装配 ctx.notices:只记一次
	// 下列字段全部在 app.Run() 之前写好、之后只读 —— 控制面 handler 会并发读它们,
	// 所以**不能**在 boot() 协程里再改(那是一次真实的 data race:备份端点可能读到半个值)。
	plan     runtimePlan      // 本次运行落点(由 resolveRuntime 在启动前算好)
	planErr  error            // 落点解析失败的原因(有窗口后再铺诊断页)
	dataRoot string           // = plan.dataRoot(便于 handlers 直接取)
	portable bool             // 便携模式:只查不装(Windows 不允许覆盖运行中的程序)
	updater  *desktopProvider // 更新源(未装配时为 nil ⇒ 控制面如实报不可用)

	trayCheckItem *application.MenuItem // 托盘「检查更新…」(自动检查用它)
	updateBusy    atomic.Bool           // 检查更新进行中(前端轮询 update-state 用)
	updateSeq     atomic.Uint64         // 检查轮次(前端据此判断"这一轮有结论了")
}

func main() {
	initLog(version)

	addr, err := pickFreeAddr()
	if err != nil {
		shellLogf("挑空闲端口失败: %v", err)
		fmt.Fprintf(os.Stderr, "gah: 挑空闲端口失败: %v\n", err)
		os.Exit(1)
	}
	sh := &shell{addr: addr, gahToken: os.Getenv("GAH_WEB_TOKEN")}
	// 运行落点(sidecar 从哪来、数据根在哪)在起控制面之前算清楚:
	// ① handlers 会读 dataRoot/portable,晚写就是 data race;
	// ② 解析失败也要让控制面/诊断页能如实报出来,而不是半个壳在跑。
	if plan, perr := resolveRuntime(localDataDir(), version); perr != nil {
		sh.planErr = perr
		shellLogf("运行落点解析失败: %v", perr)
	} else {
		sh.plan = plan
		sh.dataRoot = plan.dataRoot
		sh.portable = plan.portable
	}
	shellLogf("gah web 地址: %s(token 模式: %v)", addr, sh.gahToken != "")

	// 通知服务:macOS 只在 .app 内可注册(见 notify.go 的说明),否则整个应用起不来。
	var svcs []application.Service
	if notificationsSupported() {
		// 分开存:wails 要的是具体类型(application.NewService 的 *T 泛型),
		// notifier 只认接口(为了单测能注假系统)。
		svc := notifications.New()
		sh.notifier = &notifier{svc: svc}
		svcs = append(svcs, application.NewService(svc))
	} else {
		shellLogf("通知: 未打包运行(macOS 需要 bundle identifier),本轮禁用系统通知")
	}
	if sh.notifier != nil {
		sh.notifier.logNotifyPermission()
	}

	newInstance := wantsNewInstance()
	if newInstance {
		shellLogf("以 --new-instance 启动:不注册单实例(与既有实例各自独立)")
	}
	app := application.New(application.Options{
		Name:        "gah",
		Description: "gah 桌面壳",
		Icon:        appIconPNG, // 窗口/任务栏图标(= 裸二进制跑时的 Dock 图标兜底)
		Services:    svcs,
		// panic 一律落壳日志:GUI 进程没有终端,崩了就是窗口凭空消失,
		// 没有日志的话用户能提供的信息为零(旧壳 installPanicLog 的等价)。
		PanicHandler: func(pd *application.PanicDetails) {
			shellLogf("panic: %v", pd.Error)
			shellLogf("stack:\n%s", pd.FullStackTrace)
		},
		Mac: application.MacOptions{
			// 关掉主窗口不退出:gah 驻留托盘(后台任务/计划仍在跑),与旧壳一致。
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		SingleInstance: singleInstanceOptions(newInstance, sh),
		// 退出链的第一跳:托盘退出/关窗退出都走这里 → 优雅停 sidecar。
		OnShutdown: func() { sh.stopSidecar(true) },
	})
	sh.app = app

	cp, err := startControlPlane(sh)
	if err != nil {
		// 控制面起不来 = 壳侧能力(选目录/导出/更新)全废。显式失败,不静默降级。
		shellLogf("控制面启动失败: %v", err)
		fmt.Fprintf(os.Stderr, "gah: 控制面启动失败: %v\n", err)
		os.Exit(1)
	}
	sh.ctl = cp
	shellLogf("控制面: http://127.0.0.1:%s(仅放行 http://%s)", cp.port(), addr)

	// 窗口几何:上次的位置/尺寸优先(校验过"还在某块屏幕上"才用,见 windowstate.go)。
	opts := application.WebviewWindowOptions{
		Name:   "main",
		Title:  "gah",
		Width:  1200,
		Height: 800,
		// 就绪后再显示:避免先闪一帧空白页(旧壳同理)。
		Hidden: true,
		// **故意关掉**:Wails 的原生 drop 会拦截系统拖拽并改成 FilesDropped 事件,
		// 而 gah 前端已经有 HTML5 拖拽(拿到的是真正的 File 对象,直接走 /api/attachments)。
		// 开着等于把已经能用的功能换成需要壳中转的版本。
		EnableFileDrop: false,
	}
	if st, ok := loadWindowState(); ok && screenVisible(app, st) {
		opts.X, opts.Y = st.X, st.Y
		opts.Width, opts.Height = st.W, st.H
		opts.InitialPosition = application.WindowXY
		shellLogf("窗口几何: 恢复上次位置 %dx%d @ (%d,%d)", st.W, st.H, st.X, st.Y)
	}
	sh.window = app.Window.NewWithOptions(opts)
	// 关窗口 ≠ 销毁窗口:否则召回只能靠重启(见 windowclose.go 的真机报告)。
	sh.interceptMainWindowClose(sh.window)
	watchWindowState(app, sh.window)
	sh.buildTray()

	if err := sh.initUpdater(); err != nil {
		// 更新器起不来不该拦住整个应用(界面/会话全不能用比没有更新更糟);
		// 但要留痕,并让控制面如实报「不可用」。
		shellLogf("更新器初始化失败(本次禁用检查更新): %v", err)
		sh.updater = nil
	}

	// 启动链放协程:spawn sidecar → 等就绪 → 导航 → 显示窗口。
	// 就绪前失败不 panic:把原因铺在窗口里,比白屏闪退有用得多。
	go sh.boot()

	if err := app.Run(); err != nil {
		shellLogf("app.Run 返回错误: %v", err)
		fmt.Fprintf(os.Stderr, "gah: %v\n", err)
	}
	sh.stopSidecar(false) // Run 返回后的兜底:确保 sidecar 不留在后台
	shellLogf("=== 壳退出 ===")
}

// boot 启动 sidecar 并在就绪后把窗口导航到 gah 的页面。
func (s *shell) boot() {
	if s.planErr != nil {
		s.showDiag("找不到运行文件", []string{
			s.planErr.Error(),
			"开发态:先跑 bash scripts/gen-desktop-wails.sh(或设 GAH_SIDECAR 指向已构建的 gah)。",
		})
		return
	}
	plan := s.plan
	shellLogf("运行落点: bin=%s data_root=%s external=%v staged=%v portable=%v",
		plan.bin, plan.dataRoot, plan.external, plan.staged, plan.portable)
	setShellNotices(plan.notices)
	for _, n := range plan.notices {
		// 便携形态说明「一次讲清就够」:走系统通知,拿不到通知权限就退回日志
		// (不能因为通知不可用就把这条事实吞掉 —— 用户需要知道数据在程序目录里)。
		s.announce("gah 便携模式", n)
	}
	sc, err := startSidecar(plan.bin, s.addr, s.gahToken, "127.0.0.1:"+s.ctl.port(), s.ctl.token)
	if err != nil {
		shellLogf("%v", err)
		s.showDiag("gah 服务未能启动", []string{err.Error(), "壳日志: " + shellLogPath()})
		return
	}
	s.mu.Lock()
	s.sc = sc
	s.mu.Unlock()
	shellLogf("sidecar 已启动 pid=%d", sc.cmd.Process.Pid)

	// 进程退出监听:就绪前退出 → 立刻铺诊断页(别让用户干等 12 秒探活超时)。
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
	s.window.SetURL(u)
	s.window.Show()
	s.window.Focus()
	go s.startNoticePoller()
	go s.startHealthWatch()
	// 升级冒烟脚本入口(旧壳同款):启动后自动走一次检查,让「发出去的包能不能升级」
	// 可以脚本化验证。默认关闭,不打扰用户。
	if secs := updateAutoCheckSeconds(); secs > 0 {
		shellLogf("GAH_SHELL_UPDATE_AUTOCHECK=%d: %ds 后自动检查更新", secs, secs)
		go func() {
			time.Sleep(time.Duration(secs) * time.Second)
			s.checkUpdateFromTray(s.trayCheckItem)
		}()
	}
}

// shellURL 主窗口地址:`?shell=desktop` 让 web UI 走桌面壳布局;fragment 里带控制面
// 端口与 token(gah 自己的 token 必须放**最后** —— 引导页的正则是 `token=` 取到末端)。
func (s *shell) shellURL() string {
	// 凭据不再进 URL:控制面地址与令牌经 env 交给 sidecar(/shell/* 同源代理),
	// 于是 fragment 只剩 gah 自己的 token(引导页换 cookie 用)。
	base := "http://" + s.addr + "/?shell=desktop"
	if s.gahToken != "" {
		return base + "#token=" + url.QueryEscape(s.gahToken)
	}
	return base
}

// redactURL 把 fragment 里的 token 打码后再进日志(日志是要让用户发回来的)。
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

// focus 把已有窗口提到前台(托盘点击 / 二次启动 / Dock 点击)。
func (s *shell) focus() {
	if s.window == nil {
		return
	}
	// 最小化/全屏时 makeKeyAndOrderFront 提不起来 —— 先还原再提。
	s.window.Restore()
	s.window.Show()
	s.window.Focus()
	// 用户按过 Cmd+H(整个应用被隐藏)时,activate 不会自动 unhide。
	activateApp()
}

// stopSidecar 走优雅退出链(幂等)。fromShutdown=true 表示由 OnShutdown 触发。
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
		s.window.SetHTML(html)
		s.window.Show()
		s.window.Focus()
	}
}

// htmlEscape 诊断页文本转义(不引第三方,够用即可)。
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

// trayIcon 托盘图标:mac 用模板图(随明暗主题反色),win/linux 用彩色图。
// 都是 gah 自己的图标,不再是 Wails 默认的红龙(见 assets.go)。
func trayIcon() []byte {
	if runtime.GOOS == "darwin" {
		return trayTemplatePNG
	}
	return trayColorPNG
}

// announce 尽力用系统通知告诉用户一件事;通知不可用时退回壳日志(并留一行说明)。
func (s *shell) announce(title, body string) {
	shellLogf("提示: %s — %s", title, body)
	if err := s.sendNotification(title, body); err != nil {
		shellLogf("(通知不可用,已只写日志: %v)", err)
	}
}

// wantsNewInstance 是否要求「另起一个进程」(旧壳 --new-instance)。
//
// 与「新会话窗口」是两件事:那个共用同一个 sidecar(同端口/同偏好),这个各有独立
// sidecar 与端口(共享数据根,靠宿主自己的租约与偏好锁协调)。
func wantsNewInstance() bool {
	for _, a := range os.Args[1:] {
		if a == "--new-instance" {
			return true
		}
	}
	return false
}

// singleInstanceOptions 单实例配置;--new-instance 时**不注册**(否则第二个进程会自己退出)。
func singleInstanceOptions(newInstance bool, sh *shell) *application.SingleInstanceOptions {
	if newInstance {
		return nil
	}
	return &application.SingleInstanceOptions{
		UniqueID: appID,
		OnSecondInstanceLaunch: func(application.SecondInstanceData) {
			shellLogf("第二次启动:聚焦已有窗口")
			sh.focus()
		},
	}
}

// updateAutoCheckSeconds 升级冒烟入口(旧壳同款):>0 时启动后自动检查一次。
func updateAutoCheckSeconds() int {
	if v := os.Getenv("GAH_SHELL_UPDATE_AUTOCHECK"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}
