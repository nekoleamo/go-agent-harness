// 托盘与托盘菜单(旧壳里由 Rust 侧承担的那部分)。
//
// 菜单项与旧壳对齐:打开 gah / 新会话窗口 / 新实例 / 开机自启(勾选,点击后回写真实状态)/
// 检查更新(只查不装,结论用原生弹层)/ 测试通知 / 关于 / 重新启动服务 / 退出。
//
// mygo 的菜单回调在主线程上执行(框架内部 onMain),不需要 Wails 那层 InvokeSync 包装。
package main

import (
	"runtime"

	"github.com/egoist/mygo"
)

// 托盘「检查更新」两态文案。
const (
	checkIdleText   = "检查更新…"
	checkBusyText   = "检查更新中…"
	startingPrefix  = "启动中…"
	trayTooltipName = "gah"
)

// buildTray 建托盘与菜单。必须在 App 就绪后调用(在 WhenReady 里)。
func (s *shell) buildTray() {
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "打开 gah", Click: func(*mygo.MenuItem, *mygo.Window) { s.focus() }},
		{Label: "新会话窗口", Click: func(*mygo.MenuItem, *mygo.Window) {
			id, err := s.openSessionWindow()
			if err != nil {
				shellLogf("托盘: 新会话窗口失败: %v", err)
				s.notifyDialog("无法新建会话窗口", err.Error())
				return
			}
			shellLogf("托盘: 已开新会话窗口 %s", id)
		}},
		{Label: "概览…", Click: func(*mygo.MenuItem, *mygo.Window) {
			shellLogf("托盘: 打开概览浮层")
			s.openMiniWindow()
		}},
		{Label: "新实例…", Click: func(*mygo.MenuItem, *mygo.Window) { s.spawnNewInstance() }},
		{Type: mygo.MenuItemSeparator},

		// 勾选态由 mygo 在调回调**之前**翻转(与 Wails v3 同语义),回调里读到的是
		// 用户点出来的新状态;之后一律回写成**真实**状态,否则用户点完看不出变化。
		{Label: "开机自启", Type: mygo.MenuItemCheckbox, Checked: s.autostartEnabled(),
			Click: func(item *mygo.MenuItem, _ *mygo.Window) {
				want := item.IsChecked()
				err := s.setAutostart(want)
				got := s.autostartEnabled()
				item.SetChecked(got)
				shellLogf("开机自启: want=%v err=%v 实际=%v", want, err, got)
				if err != nil {
					s.notifyDialog("开机自启设置失败", err.Error())
					return
				}
				if want && !got {
					s.notifyDialog("开机自启需要在系统里批准",
						"gah 已登记为登录项,但系统还没放行:系统设置 → 通用 → 登录项,\n在「允许在后台」里打开 gah。")
				}
			}},

		// 菜单项要能被改成「检查更新中…」并置灰,所以留一个可操作的句柄。
		{ID: checkIdleID(), Label: checkIdleText, Click: func(*mygo.MenuItem, *mygo.Window) {
			// 2026-11-20 起走原生面板(带进度/可取消),不再弹阻塞对话框。
			s.openUpdatePanel()
		}},

		{Label: "测试系统通知", Click: func(*mygo.MenuItem, *mygo.Window) {
			if err := s.sendNotification("gah 通知测试", "看到这条说明系统通知通道是通的"); err != nil {
				shellLogf("测试通知失败: %v", err)
				s.notifyDialog("通知发送失败", notifyExplain(err))
				return
			}
			shellLogf("测试通知已发送")
		}},
		{Label: "关于 gah", Click: func(*mygo.MenuItem, *mygo.Window) { s.showAbout() }},
		{Label: "重新启动服务", Click: func(*mygo.MenuItem, *mygo.Window) {
			shellLogf("托盘: 重新启动服务")
			go func() {
				if err := s.restartSidecar(); err != nil {
					shellLogf("重启服务失败: %v", err)
					s.notifyDialog("重启服务失败", err.Error())
					return
				}
				s.notifyDialog("已重启", "后台服务已重新启动,界面已重新加载。")
			}()
		}},
		{Type: mygo.MenuItemSeparator},
		{Label: "退出", Click: func(*mygo.MenuItem, *mygo.Window) {
			shellLogf("托盘: 退出")
			s.quit()
		}},
	})

	tray, err := mygo.NewTray(mygo.TrayOptions{
		Icon:           trayIcon(),
		IconIsTemplate: isTemplateIcon(),
		ToolTip:        trayTooltipName,
		Menu:           menu,
	})
	if err != nil {
		// 托盘起不来不该让整个壳失去救生口:窗口与控制面仍在,「退出」只能靠 Cmd+Q。
		shellLogf("托盘创建失败: %v", err)
		return
	}
	s.tray = tray
	s.trayCheckItem = menu.ItemByID(checkIdleID())
	tray.OnClick(func() { s.focus() })
}

// checkIdleID 「检查更新」菜单项的 ID(便于 ItemByID 取句柄)。
func checkIdleID() string { return "tray-check-update" }

// isTemplateIcon 托盘图是否为模板图(mac 需要;win 用彩色图)。
func isTemplateIcon() bool { return runtime.GOOS == "darwin" }

// quit 退出链:先置 quitting(否则 OnClose 的 PreventDefault 会取消 Quit),再停 sidecar、退出。
func (s *shell) quit() {
	if s.quitting.Swap(true) {
		return // 已经在退
	}
	// 概览浮层的轮询先停:退出过程中不该再发请求(它也不该成为孤儿协程)。
	if s.mini != nil {
		s.mini.stopPolling()
	}
	s.stopSidecar(true)
	mygo.App.Quit()
}

// setTrayStarting 启动期的菜单栏提示(在显示窗口时撒掉)。
func (s *shell) setTrayStarting(on bool) {
	if s.tray == nil {
		return
	}
	tip := trayTooltipName
	label := ""
	if on {
		label = startingPrefix
		tip = "gah 正在启动"
	}
	s.tray.SetTitle(label)
	s.tray.SetToolTip(tip)
	if on {
		shellLogf("托盘: 启动中(首帧就绪前窗口保持隐藏)")
	} else {
		shellLogf("托盘: 启动中已撒掉")
	}
}

// setTrayChecking 把「检查更新」菜单项改成进行中/空闲(进行中置灰,避免连点)。
func (s *shell) setTrayChecking(busy bool) {
	if s.trayCheckItem == nil {
		return
	}
	if busy {
		s.trayCheckItem.SetLabel(checkBusyText)
		s.trayCheckItem.SetEnabled(false)
	} else {
		s.trayCheckItem.SetLabel(checkIdleText)
		s.trayCheckItem.SetEnabled(true)
	}
}

// notifyDialog 用系统弹框说明一件事(托盘点击后没有别的反馈渠道)。
func (s *shell) notifyDialog(title, body string) {
	_, _ = mygo.Dialog.Message(mygo.MessageOptions{
		Parent:  s.window,
		Type:    mygo.MessageInfo,
		Title:   title,
		Message: body,
	})
}
