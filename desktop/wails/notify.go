// notify:系统通知的薄适配层(Wails v3 notifications service)。
//
// 与旧壳的关键差异:v3 的 SendNotification **返回真实错误**(未授权 / 未注册进通知中心
// 都会如实报),而 tauri-plugin-notification 把 show() 丢进 async 任务并丢弃错误 ——
// 旧壳 2026-09-22 真机就是这样「日志写已发、屏幕什么都没有」,只能绕过插件直调
// notify-rust 才拿到真实结果。迁移到 v3 后那段绕路可以删掉。
package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// notificationsSupported 通知服务能不能注册。
//
// macOS 上 notifications 服务在 Startup 里要求**有效的 bundle identifier**
// (NSBundle mainBundle),否则 `app.Run()` 直接失败退出 —— 裸二进制跑(开发态)就是这样,
// 2026-10-29 实测:整个应用起不来,报 "notifications require a valid bundle identifier"。
// 所以只有真正在 .app 包里运行时才注册;开发态明确禁用并留一行日志,不静默。
func notificationsSupported() bool {
	if runtime.GOOS != "darwin" {
		return true
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.Contains(exe, ".app/Contents/MacOS/")
}

// notifier 通知适配器(包一层,便于 shell 只依赖一个方法面)。
type notifier struct {
	svc  *notifications.NotificationService
	seq  atomic.Uint64
	auth atomic.Int32 // 0=未查 1=已授权 2=未授权
}

// native 发一条系统通知。
func (n *notifier) native(title, body string) error {
	if n == nil || n.svc == nil {
		return fmt.Errorf("通知服务未装配")
	}
	// macOS 首次需要用户授权;未授权时 SendNotification 会返回错误 —— 如实上报。
	if n.auth.Load() == 0 {
		ok, err := n.svc.CheckNotificationAuthorization()
		switch {
		case err != nil:
			shellLogf("通知授权查询失败: %v", err)
			n.auth.Store(2)
		case ok:
			n.auth.Store(1)
		default:
			// 主动请求一次(系统弹窗只在第一次真正出现,之后不会再打扰)。
			granted, rerr := n.svc.RequestNotificationAuthorization()
			shellLogf("通知授权请求: granted=%v err=%v", granted, rerr)
			if granted {
				n.auth.Store(1)
			} else {
				n.auth.Store(2)
			}
		}
	}
	opts := notifications.NotificationOptions{
		ID:    fmt.Sprintf("gah-%d-%d", nowUnixMilli(), n.seq.Add(1)),
		Title: title,
		Body:  body,
	}
	return n.svc.SendNotification(opts)
}

// logNotifyPermission 启动时把通知授权状况记进壳日志(旧 Rust 壳同款)。
// 真机上「通知没弹」时,这一行能立刻区分「没权限」还是「发了但被静音」。
func (n *notifier) logNotifyPermission() {
	if n == nil || n.svc == nil {
		return
	}
	ok, err := n.svc.CheckNotificationAuthorization()
	switch {
	case err != nil:
		shellLogf("通知授权状况: 查询失败(%v)", err)
	case ok:
		n.auth.Store(1)
		shellLogf("通知授权状况: 已授权")
	default:
		n.auth.Store(2)
		shellLogf("通知授权状况: 未授权(首次发送时会请求;用户拒绝后通知不会弹出)")
	}
}
