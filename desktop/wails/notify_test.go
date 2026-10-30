package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// fakeNotify 假系统通知服务:authorized 由用例摆布,send 只在未授权时按真机行为报错。
type fakeNotify struct {
	authorized bool // 系统当前的授权状态
	checkErr   error
	requestRes bool // requestAuthorization 返回的 granted
	requestErr error
	sendErr    error

	checks, requests, sends int
}

func (f *fakeNotify) CheckNotificationAuthorization() (bool, error) {
	f.checks++
	return f.authorized, f.checkErr
}

func (f *fakeNotify) RequestNotificationAuthorization() (bool, error) {
	f.requests++
	if f.requestErr != nil {
		return false, f.requestErr
	}
	if f.requestRes {
		f.authorized = true
	}
	return f.requestRes, nil
}

func (f *fakeNotify) SendNotification(notifications.NotificationOptions) error {
	f.sends++
	// 真机行为:未授权时 addNotificationRequest 一律 UNErrorDomain error 1。
	if !f.authorized {
		return errors.New("Error: The operation couldn't be completed. (UNErrorDomain error 1.)")
	}
	return f.sendErr
}

// TestNotifyRequestsAuthorizationBeforeFirstSend 首次发送必须先要权限。
// 反向验证:让 native 跳过 ensureAuthorization(老代码缓存「未授权」就直接跳过请求),本用例必红。
func TestNotifyRequestsAuthorizationBeforeFirstSend(t *testing.T) {
	f := &fakeNotify{requestRes: true}
	n := &notifier{svc: f}

	if err := n.native("回合已完成", "看这里"); err != nil {
		t.Fatalf("要过权限后应该发得出去,得到错误: %v", err)
	}
	if f.requests != 1 {
		t.Fatalf("首次发送应请求一次授权,实际 %d 次", f.requests)
	}
	if f.sends != 1 {
		t.Fatalf("应发出 1 条通知,实际 %d", f.sends)
	}
	if !n.granted.Load() {
		t.Fatal("授权成功后应记下已授权")
	}
}

// TestNotifyRequestsOnlyOnce 用户拒绝后不再反复请求(denied 时不会弹窗,请求本身没意义),
// 但发送照做 —— 用户刚在系统设置里打开开关时,下一次发送要能立刻成功。
func TestNotifyRequestsOnlyOnce(t *testing.T) {
	f := &fakeNotify{requestRes: false}
	n := &notifier{svc: f}

	if err := n.native("回合已完成", "第一次"); err == nil {
		t.Fatal("未授权时发送应返回真实错误,不能假装成功")
	}
	if err := n.native("回合已完成", "第二次"); err == nil {
		t.Fatal("未授权时第二次发送也应报错")
	}
	if f.requests != 1 {
		t.Fatalf("一次运行只该请求一次授权,实际 %d 次", f.requests)
	}
}

// TestNotifySeesLaterGrant 用户去系统设置打开开关后,下一次发送就能成功(不再需要请求)。
func TestNotifySeesLaterGrant(t *testing.T) {
	f := &fakeNotify{requestRes: false}
	n := &notifier{svc: f}

	if err := n.native("a", "b"); err == nil {
		t.Fatal("未授权时应报错")
	}
	f.authorized = true // 用户去系统设置里打开了开关
	if err := n.native("a", "b"); err != nil {
		t.Fatalf("授权状态变了就该发得出去,得到: %v", err)
	}
	if f.requests != 1 {
		t.Fatalf("已授权后不该再请求,实际 %d 次", f.requests)
	}
}

// TestNotifySkipsRequestWhenAlreadyAuthorized 已授权时一次查询就够,不去打扰系统。
func TestNotifySkipsRequestWhenAlreadyAuthorized(t *testing.T) {
	f := &fakeNotify{authorized: true}
	n := &notifier{svc: f}

	if err := n.native("a", "b"); err != nil {
		t.Fatalf("已授权应发得出去: %v", err)
	}
	if f.requests != 0 {
		t.Fatalf("已授权不该请求授权,实际 %d 次", f.requests)
	}
}

// TestLogPermissionDoesNotSuppressRequest 这是 2026-10-30 真机事故的复盘用例:
// 启动时那行「通知授权状况」查询到 false 时**不能**把状态定死,否则第一次发送
// 会跳过请求、直接被系统拒(用户从没见过授权弹窗)。
// 反向验证:让 logNotifyPermission 重新缓存终态(老代码 auth=2),本用例必红。
func TestLogPermissionDoesNotSuppressRequest(t *testing.T) {
	f := &fakeNotify{requestRes: true} // 装完还没申请过 → Check 返回 false
	n := &notifier{svc: f}

	n.logNotifyPermission() // 启动时那行日志
	if err := n.native("回合已完成", "看这里"); err != nil {
		t.Fatalf("启动时查询到未授权不该屏蔽后续请求: %v", err)
	}
	if f.requests != 1 {
		t.Fatalf("首次发送仍要请求授权,实际 %d 次", f.requests)
	}
}

// TestExplainUnauthorizedHint 未授权时错误文案要给出「去哪儿开」,不能只丢系统原文。
func TestExplainUnauthorizedHint(t *testing.T) {
	f := &fakeNotify{}
	n := &notifier{svc: f}
	err := n.native("a", "b")
	if err == nil {
		t.Fatal("未授权应报错")
	}
	msg := n.explain(err).Error()
	if !strings.Contains(msg, "系统设置") {
		t.Fatalf("未授权文案应指路系统设置,实际:%s", msg)
	}
	// 已授权时的其他错误(比如被静音/系统忙)不该被这段提示污染。
	// granted 只在发送路径上刷新,所以先用一次成功发送把它带上来(真机也是紧跟着 explain)。
	f.authorized = true
	if err := n.native("a", "b"); err != nil {
		t.Fatalf("授权后应发得出去: %v", err)
	}
	other := n.explain(errors.New("别的原因"))
	if strings.Contains(other.Error(), "系统设置") {
		t.Fatalf("已授权时的错误不该套未授权文案:%s", other)
	}
}
