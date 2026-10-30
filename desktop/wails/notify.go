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

// notificationService 只取实际用到的三个方法。
//
// 抽成接口是为了单测能注入假系统(wails 的 *notifications.NotificationService 是具体
// 类型直连 C,拿它写不出授权状态机的用例;而状态机正是 2026-10-30 那次「通知一次都没弹过」
// 的现场,必须有用例钉住)。
type notificationService interface {
	CheckNotificationAuthorization() (bool, error)
	RequestNotificationAuthorization() (bool, error)
	SendNotification(notifications.NotificationOptions) error
}

// notifier 通知适配器(包一层,便于 shell 只依赖一个方法面)。
type notifier struct {
	svc     notificationService
	seq     atomic.Uint64
	granted atomic.Bool // 只信「系统明确说已授权」;false 一律当「未确认」
	asked   atomic.Bool // 本次运行是否已向系统请求过一次授权
}

// ensureAuthorization 发送前的唯一前置动作:没授权就去要。
//
// 这是 2026-10-30 真机翻车的现场:老代码在启动时把「Check 返回 false」当成**终态**缓存
// (auth=2)并跳过请求,于是 macOS 上 notDetermined 的应用第一次 addNotificationRequest
// 直接返回 UNErrorDomain error 1 —— 用户**从没见过授权弹窗**,只看到托盘弹框
// 「通知发送失败:Error: The operation couldn't be completed. (UNErrorDomain error 1.)」。
// 教训:CheckNotificationAuthorization 只回「是否已授权」,它分不出
// notDetermined / denied,所以 false 只说明「不能假定已授权」,不说明「没救了」。
func (n *notifier) ensureAuthorization() {
	ok, err := n.svc.CheckNotificationAuthorization()
	switch {
	case err != nil:
		shellLogf("通知授权查询失败: %v", err)
		n.granted.Store(false)
	case ok:
		n.granted.Store(true)
		return
	default:
		n.granted.Store(false)
	}
	// 每次发送前都复查一次上面那步(用户在系统设置里刚打开开关,下一次就能生效);
	// 但**请求**每次运行只做一次:denied 时 requestAuthorization 秒回 false 不弹窗,
	// 反复请也没意义,只会多几次跨进程往返。
	if !n.asked.CompareAndSwap(false, true) {
		return
	}
	granted, rerr := n.svc.RequestNotificationAuthorization()
	shellLogf("通知授权请求: granted=%v err=%v", granted, rerr)
	n.granted.Store(granted)
}

// native 发一条系统通知。
func (n *notifier) native(title, body string) error {
	if n == nil || n.svc == nil {
		return fmt.Errorf("通知服务未装配")
	}
	n.ensureAuthorization()
	opts := notifications.NotificationOptions{
		ID:    fmt.Sprintf("gah-%d-%d", nowUnixMilli(), n.seq.Add(1)),
		Title: title,
		Body:  body,
	}
	return n.svc.SendNotification(opts)
}

// explain 把系统错误翻成用户能照着做的话(macOS 的 UNErrorDomain error 1 对用户零信息量)。
func (n *notifier) explain(err error) error {
	if err == nil {
		return nil
	}
	if n == nil || n.granted.Load() {
		return err
	}
	return fmt.Errorf("%w\n\n%s", err, notAuthorizedHint)
}

// notAuthorizedHint 未授权时的可执行说明。
//
// 「列表里没有 gah」那条不是凑数:macOS 只在应用**真的申请过**权限之后才把它登记进
// 通知中心,所以没这一项就是「一次都没申请成功」,用户在列表里翻是白费功夫。
const notAuthorizedHint = "gah 在系统里没有通知权限:系统设置 → 通知 → gah,把「允许通知」打开。\n" +
	"如果那个列表里根本没有 gah,说明这次运行没能向系统申请到权限(壳日志里「通知授权」几行有记录)。"

// logNotifyPermission 启动时把通知授权状况记进壳日志(旧 Rust 壳同款)。
//
// 只落日志、**不改判定状态**:见 ensureAuthorization 的说明,这里的「false」
// 既可能是没请求过也可能是被拒,缓存成终态就是那次翻车的直接成因。
func (n *notifier) logNotifyPermission() {
	if n == nil || n.svc == nil {
		return
	}
	ok, err := n.svc.CheckNotificationAuthorization()
	switch {
	case err != nil:
		shellLogf("通知授权状况: 查询失败(%v)", err)
	case ok:
		n.granted.Store(true)
		shellLogf("通知授权状况: 已授权")
	default:
		shellLogf("通知授权状况: 未授权(可能是还没申请过,也可能被拒;首次发送时申请)")
	}
}
