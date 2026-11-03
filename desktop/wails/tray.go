// 托盘与托盘菜单(旧壳里由 Rust 侧承担的那部分)。
//
// 菜单项与旧壳对齐:打开 gah / 新会话窗口 / 新实例 / 开机自启(勾选,点击后回写真实状态)/
// 检查更新(只查不装,结论用原生弹层)/ 测试通知 / 关于 / 重新启动服务 / 退出。
// 注:菜单项的文字与置灰变更必须经 mainthread.go 的包装回主线程(v3 的菜单回调在独立 goroutine 里)。
package main

import (
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// buildTray 建托盘与菜单。必须在 app.Run() 之前调用。
func (s *shell) buildTray() {
	tray := s.app.SystemTray.New()
	tray.SetIcon(trayIcon())
	if s.app.SystemTray != nil {
		// Windows/Linux 用同一个图标即可;mac 的模板图已在 trayIcon 里选好。
	}
	tray.SetTooltip("gah")
	// 启动期给个信号:首帧就绪前窗口是隐的(避免闪底),这期间用户看不到任何反应,
	// 菜单栏文字是唯一不增加「等待」的反馈渠道。
	// ⚠ 设文字必须等 Run 之后(见 onAppStarted):buildTray 跑在 app.Run() 之前,
	// 那时调 InvokeSync 直接 nil 崩(2026-11-03 实测)。
	s.tray = tray

	menu := s.app.Menu.New()
	menu.Add("打开 gah").OnClick(func(*application.Context) { s.focus() })
	menu.Add("新会话窗口").OnClick(func(*application.Context) {
		id, err := s.openSessionWindow()
		if err != nil {
			shellLogf("托盘: 新会话窗口失败: %v", err)
			s.notifyDialog("无法新建会话窗口", err.Error())
			return
		}
		shellLogf("托盘: 已开新会话窗口 %s", id)
	})
	menu.Add("新实例…").OnClick(func(*application.Context) { s.spawnNewInstance() })
	menu.AddSeparator()

	auto := menu.AddCheckbox("开机自启", s.autostartEnabled())
	auto.OnClick(func(*application.Context) {
		// 别再取反:v3 在调回调**之前**已经把勾选态翻成用户点的那个值
		// (menuitem.go handleClick 先 `m.checked = !m.checked` 再发回调)。
		// 2026-10-30 真机就是被这里坑了:老代码 `want := !auto.Checked()` 每次都反着来,
		// 于是「点自启 ⇒ 执行 Disable ⇒ 勾选态回写 false」,表现为点了毫无反应。
		want := auto.Checked()
		var err error
		if want {
			err = s.app.Autostart.Enable()
		} else {
			err = s.app.Autostart.Disable()
		}
		// 勾选态一律回写成**真实**状态:否则用户点完看不出任何变化(旧壳同一个坑)。
		got := s.autostartEnabled()
		setMenuItemChecked(auto, got)
		shellLogf("开机自启: want=%v err=%v 实际=%v", want, err, got)
		if err != nil {
			// 托盘点击后没有别的反馈渠道:失败只在日志里等于让用户以为程序坏了。
			s.notifyDialog("开机自启设置失败", err.Error())
			return
		}
		if want && !got {
			// macOS 13+ 走 SMAppService:注册成功但系统把它挂成「待批准」时,
			// IsEnabled 仍是 false —— 真正生效的开关在系统设置里,不在我们的菜单上。
			s.notifyDialog("开机自启需要在系统里批准",
				"gah 已登记为登录项,但系统还没放行:系统设置 → 通用 → 登录项,\n在「允许在后台」里打开 gah。")
		}
	})

	// 菜单项要能被改成「检查更新中…」并置灰,所以留一个可操作的句柄。
	checkItem := menu.Add(checkIdleText)
	checkItem.OnClick(func(*application.Context) { s.checkUpdateFromTray(checkItem) })
	s.trayCheckItem = checkItem

	menu.Add("测试系统通知").OnClick(func(*application.Context) {
		// 真机上「通知到底通没通」只能靠人点一下看:失败在界面上完全不可见。
		if err := s.sendNotification("gah 通知测试", "看到这条说明系统通知通道是通的"); err != nil {
			shellLogf("测试通知失败: %v", err)
			// 原始系统错误对用户没有可执行信息(UNErrorDomain error 1 什么也没说),
			// 未授权时补上「去哪儿开」以及「列表里没有 gah 意味着什么」。
			s.notifyDialog("通知发送失败", s.notifier.explain(err).Error())
			return
		}
		shellLogf("测试通知已发送")
	})
	menu.Add("关于 gah").OnClick(func(*application.Context) { s.showAbout() })
	menu.Add("重新启动服务").OnClick(func(*application.Context) {
		shellLogf("托盘: 重新启动服务")
		go func() {
			if err := s.restartSidecar(); err != nil {
				shellLogf("重启服务失败: %v", err)
				s.notifyDialog("重启服务失败", err.Error())
				return
			}
			s.notifyDialog("已重启", "后台服务已重新启动,界面已重新加载。")
		}()
	})
	menu.AddSeparator()
	menu.Add("退出").OnClick(func(*application.Context) {
		shellLogf("托盘: 退出")
		s.app.Quit() // app.OnShutdown → stopSidecar(优雅退出链)
	})

	tray.SetMenu(menu)
	tray.OnClick(func() { s.focus() })
}

// onAppStarted 在「应用真的跑起来之后」执行一次 f。
//
// 为什么不能直接调:buildTray 跑在 app.Run() **之前**,那时 wails 的主线程调度器还没
// 初始化,`application.InvokeSync` 会 nil panic —— 2026-11-03 实测就是启动即崩
// (`dispatchOnMainThread` 空指针,栈顶就是 setTrayStarting)。所以这类托盘操作挂在各平台的
// 「启动完成」事件上;三个平台的事件名不同,也不存在统一封装。
func (s *shell) onAppStarted(f func()) {
	if s.app == nil || s.app.Event == nil {
		return
	}
	var ev events.ApplicationEventType
	switch runtime.GOOS {
	case "darwin":
		ev = events.Mac.ApplicationDidFinishLaunching
	case "windows":
		ev = events.Windows.ApplicationStarted
	case "linux":
		ev = events.Linux.ApplicationStartup
	default:
		return // 没对应事件的平台就不提示 —— 宁可少个提示,也不在 Run 之前碰平台 UI
	}
	s.app.Event.OnApplicationEvent(ev, func(*application.ApplicationEvent) { f() })
}

// setTrayStarting 启动期的菜单栏提示(在显示窗口时撒掉)。
//
// 为什么不只改 tooltip:tooltip 要把鼠标悬到图标上才看得到,而这段等待(首次启动要
// 解压内嵌 sidecar)用户看不到窗口也看不到任何反应。macOS 的 SetLabel 是图标旁的
// 文字,一眼可见;其他平台 label 语义不同,它不显示也无害。
// 必须回主线程 —— 与菜单文字同一条纪律(见 mainthread.go)。
func (s *shell) setTrayStarting(on bool) {
	if s.tray == nil {
		return
	}
	tray := s.tray
	tip := "gah"
	label := ""
	if on {
		label = "启动中…"
		tip = "gah 正在启动"
	}
	application.InvokeSync(func() {
		tray.SetLabel(label)
		tray.SetTooltip(tip)
	})
	// 日志里能看到这一步,「启动中」到底有没有生效不用猜(真机反馈时很关键)。
	if on {
		shellLogf("托盘: 启动中(首帧就绪前窗口保持隐藏)")
	} else {
		shellLogf("托盘: 启动中已撒掉")
	}
}

// autostartEnabled 读真实自启状态(读失败按「未启用」显示并留痕)。
func (s *shell) autostartEnabled() bool {
	on, err := s.app.Autostart.IsEnabled()
	if err != nil {
		shellLogf("读取开机自启状态失败: %v", err)
		return false
	}
	return on
}

// notifyDialog 用系统弹框说明一件事(托盘点击后没有别的反馈渠道)。
func (s *shell) notifyDialog(title, body string) {
	d := s.app.Dialog.Info().SetTitle(title).SetMessage(body)
	if s.window != nil {
		d.AttachToWindow(s.window)
	}
	d.Show()
}
