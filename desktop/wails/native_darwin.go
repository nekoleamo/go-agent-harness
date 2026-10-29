//go:build darwin

// macOS 原生小动作(AppKit 一行能做完、Wails v3 没暴露的那些)。
//
// 目前只有「Dock 图标弹一下」:通知被系统静音/聚焦助手开着时,这是唯一还剩的提示通道
// (旧 Rust 壳的 bounceDock,真机上确实救过一次「任务失败但通知没弹」)。
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
*/
import "C"

// bounceDock 让 Dock 图标跳一下(用户点一下应用即取消)。
func bounceDock() { C.gahBounceDock() }
