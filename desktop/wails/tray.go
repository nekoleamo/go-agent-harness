// 托盘与托盘菜单(旧壳里由 Rust 侧承担的那部分)。
//
// 菜单项与旧壳对齐:打开 gah / 开机自启(勾选,点击后回写真实状态)/ 检查更新… / 退出。
// 「检查更新」本期是占位:更新器迁移在后续批次(见 docs/DESKTOP_FEASIBILITY.md §11.6),
// 现在点它给一句明确说明,而不是静默无反应。
package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
)

// buildTray 建托盘与菜单。必须在 app.Run() 之前调用。
func (s *shell) buildTray() {
	tray := s.app.SystemTray.New()
	tray.SetIcon(trayIcon())
	if s.app.SystemTray != nil {
		// Windows/Linux 用同一个图标即可;mac 的模板图已在 trayIcon 里选好。
	}
	tray.SetTooltip("gah")

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
	menu.AddSeparator()

	auto := menu.AddCheckbox("开机自启", s.autostartEnabled())
	auto.OnClick(func(*application.Context) {
		want := !auto.Checked()
		var err error
		if want {
			err = s.app.Autostart.Enable()
		} else {
			err = s.app.Autostart.Disable()
		}
		if err != nil {
			shellLogf("开机自启切换失败(want=%v): %v", want, err)
		}
		// 勾选态一律回写成**真实**状态:否则用户点完看不出任何变化(旧壳同一个坑)。
		got := s.autostartEnabled()
		setMenuItemChecked(auto, got)
		shellLogf("开机自启: want=%v err=%v 实际=%v", want, err, got)
	})

	// 菜单项要能被改成「检查更新中…」并置灰,所以留一个可操作的句柄。
	checkItem := menu.Add(checkIdleText)
	checkItem.OnClick(func(*application.Context) { s.checkUpdateFromTray(checkItem) })
	menu.AddSeparator()
	menu.Add("退出").OnClick(func(*application.Context) {
		shellLogf("托盘: 退出")
		s.app.Quit() // app.OnShutdown → stopSidecar(优雅退出链)
	})

	tray.SetMenu(menu)
	tray.OnClick(func() { s.focus() })
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
