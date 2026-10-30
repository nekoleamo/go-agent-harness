// windowclose:关主窗口 = 收进托盘,不是销毁。
//
// 为什么必须拦:wails v3 建窗时自己挂了一个 WindowClosing 监听器
// (webview_window.go),内容是 markAsDestroyed + impl.close + Window.Remove ——
// 用户点红叉,窗口对象就被**销毁并从窗口注册表里删掉**,而 s.window 还拿着那个死指针。
// 之后托盘点「打开 gah」走 focus() → Show()/Focus(),这两个方法开头都是
// `if w.isDestroyed() { return }`,静默什么都不做。表现就是 2026-10-30 那条真机报告:
// 「关闭主窗口后,状态栏和菜单栏点击打开 gah 无效」。
//
// 钩子(hook)先于监听器执行,取消事件后 wails 不再走销毁分支,我们只 Hide():
// 进程、托盘、后台任务照旧,于是 focus() 永远有一个活窗口可以提起来。
// 真正的退出只有托盘「退出」→ app.Quit(),那条链不经过 WindowClosing。
package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// interceptMainWindowClose 把主窗口的「关闭」劫持成「隐藏」。
func (s *shell) interceptMainWindowClose(w *application.WebviewWindow) {
	if w == nil {
		return
	}
	w.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		if s.quitting.Load() {
			return // 退出链:放行,让 wails 正常销毁
		}
		e.Cancel()
		w.Hide()
		shellLogf("主窗口已收进托盘(窗口保持存活,托盘「打开 gah」可再提起来)")
	})
}
