// notify:系统通知的薄适配层(mygo notification)。
//
// 与 Wails 壳的差异:mygo 的 Show **自己管授权**(macOS 首次发送时弹授权,并把用户答案
// 作为 ErrNotificationsDenied 返回),所以 Wails 壳那套「Check → 请求一次 → 缓存状态」
// 的状态机整段不需要了 —— 少一个会失同步的缓存,也就少一类「日志写已发、屏幕没有」。
package main

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/egoist/mygo"
)

// notificationsSupported 通知能不能用。
//
// macOS 上通知要求**有效的 bundle identifier**(NSBundle mainBundle),裸二进制开发态没有。
// mygo 同样要求打包应用,所以非 .app 运行一律明确禁用并留一行日志,不静默。
func notificationsSupported() bool {
	if runtime.GOOS != "darwin" {
		return runtime.GOOS == "windows"
	}
	// 「在 .app 里」不等于「能被通知中心认」:没有 CFBundleIdentifier 的包会让
	// UNUserNotificationCenter 抛异常把进程打死(见 bundle_darwin.go)。
	return bundleIdentifier() != ""
}

// notifier 通知适配器(只包一个方法面)。
//
// send 是**给单测的注入点**:nil = 真发系统通知。为什么留这个口子:通知的失败语义
// (未授权/被系统拒)在真机上不可复现,只能靠假系统钉住「闪烁与通知独立成败」这条口径。
type notifier struct {
	seq     atomic.Uint64
	granted atomic.Bool                // 只信「系统明确说已授权」;false 一律当「未确认」
	send    func(string, string) error // nil = 走 mygo 真通知
}

// newNotifier 建适配器。
func newNotifier() *notifier { return &notifier{} }

// native 发一条系统通知。
func (n *notifier) native(title, body string) error {
	if n == nil {
		return fmt.Errorf("通知服务未装配")
	}
	if n.send != nil {
		err := n.send(title, body)
		if err == nil {
			n.granted.Store(true)
		}
		return err
	}
	opts := mygo.NotificationOptions{
		ID:    fmt.Sprintf("gah-%d-%d", nowUnixMilli(), n.seq.Add(1)),
		Title: title,
		Body:  body,
	}
	err := mygo.NewNotification(opts).Show()
	if err == nil {
		n.granted.Store(true)
	}
	return err
}

// logNotifyPermission 启动时把通知可用性记进壳日志。
func (n *notifier) logNotifyPermission() {
	shellLogf("通知授权状况: 首次发送时由系统申请(未授权会返回明确错误)")
}

// notifyExplain 把通知失败翻成人话(macOS 的 UNErrorDomain error 1 对用户零信息量)。
func notifyExplain(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if errors.Is(err, mygo.ErrNotificationsDenied) || strings.Contains(strings.ToLower(msg), "denied") {
		return msg + "\n\n" + notAuthorizedHint
	}
	return msg
}

// notAuthorizedHint 未授权时的可执行说明。
//
// 「列表里没有 gah」那条不是凑数:macOS 只在应用**真的申请过**权限之后才把它登记进
// 通知中心,没这一项就是「一次都没申请成功」,用户在列表里翻是白费功夫。
const notAuthorizedHint = "gah 在系统里没有通知权限:系统设置 → 通知 → gah,把「允许通知」打开。\n" +
	"如果那个列表里根本没有 gah,说明这次运行没能向系统申请到权限(壳日志里「通知」几行有记录)。"
