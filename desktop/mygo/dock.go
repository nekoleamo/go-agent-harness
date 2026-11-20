// dock:Dock 图标「弹一下」的跨平台薄封装。
//
// mygo 自带 App.Dock.Bounce(macOS),所以 Wails 壳那 58 行 purego/ObjC 直打运行时
// (NSInformationalRequest + unhide + activateIgnoringOtherApps)整段删掉了 —— 这是
// §13.2 记的「净收益」那条。非 mac 平台空实现(Windows 的任务栏提示走窗口闪烁)。
package main

import (
	"runtime"

	"github.com/egoist/mygo"
)

// bounceDock 让 Dock 图标弹一下(通知被静音/聚焦助手挡掉时,这是唯一还剩的提示通道)。
func bounceDock() {
	if runtime.GOOS != "darwin" {
		return
	}
	// App 未就绪(单测/启动早期)时调 Bounce 会阻塞等主线程 —— 那里没有事件循环,
	// 会直接把调用方挂住(实测:notice_test 卡 120s)。没有应用就没有 Dock,跳过是对的。
	if !mygo.App.IsReady() {
		return
	}
	// false = informational:弹一次就停,不一直弹到用户点开。
	mygo.App.Dock.Bounce(false)
}
