//go:build darwin

// macOS 原生小动作(AppKit 一行能做完、Wails v3 没暴露的那些)。
//
// 两个都跟「窗口不在用户眼前」有关:
//   - bounceDock:通知被系统静音/聚焦助手挡掉时唯一还剩的提示通道(旧 Rust 壳同款);
//   - activateApp:托盘/Dock 召回主窗口时,先把应用从 Cmd+H 隐藏态拉出来。
//
// 这里直接打 ObjC 运行时(purego/objc),不再要一个 cgo 文件 —— 于是壳在 mac 上也能
// CGO_ENABLED=0(配合 Wails v3.1 的 `-tags purego` 后端;来龙去脉见
// docs/DESKTOP_FEASIBILITY.md §11.14)。cgo 后端下同一份代码照样可用:AppKit 已在进程里。
//
// 语义与旧 cgo 版逐条对齐(不是「行为差不多」):
//   - NSInformationalRequest = 10:跳一下就自己结束,不会像 NSCriticalRequest(0) 那样持续弹;
//   - 必须先 unhide 再 activate:按过 Cmd+H 后只 activate 不会把窗口拉回来。
package main

import (
	"github.com/ebitengine/purego/objc"
	"github.com/wailsapp/wails/v3/pkg/application"
)

var (
	selSharedApplication      = objc.RegisterName("sharedApplication")
	selRequestUserAttention   = objc.RegisterName("requestUserAttention:")
	selUnhide                 = objc.RegisterName("unhide:")
	selActivateIgnoringOthers = objc.RegisterName("activateIgnoringOtherApps:")
)

// nsApp 返回 NSApplication 单例。只在主线程调用(下面两个函数都用 onMain 保证)。
func nsApp() objc.ID {
	return objc.ID(objc.GetClass("NSApplication")).Send(selSharedApplication)
}

// onMain 把 AppKit 调用丢回主线程。单测里没有 Wails 应用(globalApplication 为 nil),
// 此时直接跳过 —— InvokeSync 会解引用这个全局,在测试进程里必 nil panic。
func onMain(fn func()) {
	if application.Get() == nil {
		return
	}
	application.InvokeSync(fn)
}

// bounceDock 让 Dock 图标跳一下(用户点一下应用即取消)。
func bounceDock() {
	onMain(func() {
		nsApp().Send(selRequestUserAttention, uintptr(10))
	})
}

// activateApp 把整个应用拿到前台(含 Cmd+H 隐藏态)。
func activateApp() {
	onMain(func() {
		app := nsApp()
		app.Send(selUnhide, objc.ID(0))
		app.Send(selActivateIgnoringOthers, true)
	})
}
