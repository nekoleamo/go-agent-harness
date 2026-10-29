// mainthread:所有直接落到 AppKit/Win32 的调用都必须回主线程。
//
// 为什么需要这一层:Wails v3 的菜单回调是在**独立 goroutine** 里跑的
// (menuitem.go handleClick → go func()),而 MenuItem 的 SetLabel/SetEnabled/SetChecked
// 直接调平台 API(darwin 上是 C.setMenuItemLabel),**不像 Window.Show/SetURL 那样自己
// 走 InvokeSync**。于是「在菜单回调里改菜单文字」= 在非主线程碰 AppKit ——
// 未定义行为,症状是偶发崩溃或界面不刷新,而代码看起来完全正常。
//
// InvokeSync 自身有主线程快路径(dispatchOnMainThread 先判断 isOnMainThread),
// 所以从主线程调用也不会死锁,可以无脑包一层。
package main

import "github.com/wailsapp/wails/v3/pkg/application"

func setMenuItemLabel(item *application.MenuItem, label string) {
	if item == nil {
		return
	}
	application.InvokeSync(func() { item.SetLabel(label) })
}

func setMenuItemEnabled(item *application.MenuItem, enabled bool) {
	if item == nil {
		return
	}
	application.InvokeSync(func() { item.SetEnabled(enabled) })
}

func setMenuItemChecked(item *application.MenuItem, checked bool) {
	if item == nil {
		return
	}
	application.InvokeSync(func() { item.SetChecked(checked) })
}
