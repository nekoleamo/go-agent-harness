//go:build darwin

// macOS 原生小动作(AppKit 一行能做完、Wails v3 没暴露的那些)。
//
// 两个都跟「窗口不在用户眼前」有关:
//   - bounceDock:通知被系统静音/聚焦助手挡掉时唯一还剩的提示通道(旧 Rust 壳同款);
//   - activateApp:托盘/Dock 召回主窗口时,先把应用从 Cmd+H 隐藏态拉出来。
package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa

#import <Cocoa/Cocoa.h>

static void gahBounceDock(void) {
	// 必须回主线程:AppKit 的 UI 调用不在主线程上是未定义行为。
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp requestUserAttention:NSInformationalRequest];
	});
}

static void gahActivateApp(void) {
	// unhide:Cmd+H 隐藏整个应用后,单纯 activate 不会把窗口拉回来。
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp unhide:nil];
		[NSApp activateIgnoringOtherApps:YES];
	});
}
*/
import "C"

// bounceDock 让 Dock 图标跳一下(用户点一下应用即取消)。
func bounceDock() { C.gahBounceDock() }

// activateApp 把整个应用拿到前台(含 Cmd+H 隐藏态)。
func activateApp() { C.gahActivateApp() }
