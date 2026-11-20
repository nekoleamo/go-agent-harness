// attention:把「需要人回来」的信号落到**壳层**(窗口闪烁 + 系统通知)。
//
// 为什么判据不在这里:页面已经判断过「窗口在后台」与「这是一次新的提问/审批」——
// 壳只负责呈现,与 notice.go 的「判据只在宿主一处」同一条纪律。这里也**不做去重**:
// 重连补推的帧由页面按 id 去重,重复闪烁/通知才真让人烦。
//
// 为什么闪烁与通知都要:窗口闪烁在 macOS 是 Dock 图标弹跳、Windows 是任务栏闪烁,
// 但「应用完全隐藏/全屏别的 app」时这两种都可能被忽略;系统通知则留在通知中心。
// 两者**独立成败** —— 通知未授权不该把闪烁一起吞掉(反过来也一样)。
package main

import (
	"runtime"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
)

// attentionFlashDur 一次提醒的闪烁时长:到点主动收尾,不让桌面图标闪到天荒地老
// (Windows 的任务栏闪烁会一直持续到窗口获得焦点,macOS 的弹跳只做一次)。
const attentionFlashDur = 5 * time.Second

// windowFlasher 只取 Flash(bool):mygo 的 *Window 经 mygoFlasher 满足,单测注假实现。
type windowFlasher interface {
	Flash(enabled bool)
}

// mygoFlasher 把「提醒用户」落到 mygo 的窗口闪烁 + Dock 弹跳上。
//
// 为什么要两个:单独一个都可能被忽略 —— Windows 的任务栏闪烁在窗口完全被遮住时看不见,
// macOS 的 Dock 弹跳在应用已在前台时也不明显。
type mygoFlasher struct {
	win  *mygo.Window
	boot atomic.Int64 // 非零 = 有一次 Dock 弹跳还没收尾
}

// Flash 开/关窗口闪烁(顺带在 macOS 上让 Dock 图标弹跳一次)。
func (f *mygoFlasher) Flash(on bool) {
	if f == nil || f.win == nil {
		return
	}
	f.win.FlashFrame(on)
	if runtime.GOOS != "darwin" || !mygo.App.IsReady() {
		return // 非 mac 没有 Dock 弹跳;未就绪时调它会阻塞等主线程
	}
	if on {
		f.boot.Store(int64(mygo.App.Dock.Bounce(true)))
		return
	}
	if id := f.boot.Swap(0); id != 0 {
		mygo.App.Dock.CancelBounce(int(id))
	}
}

// attentionResult 一次「注意」的落地结果(控制面原样回给页面,便于自证与排查)。
type attentionResult struct {
	Flashed  bool   `json:"flashed"`
	Notified bool   `json:"notified"`
	Error    string `json:"error,omitempty"`
}

// attention 请求用户注意:窗口闪烁 + 系统通知。
// 两者都不可用(开发态无通知服务、也无窗口)时返回零值,由控制面按 503 如实上报。
func (s *shell) attention(title, body string) attentionResult {
	if title == "" {
		title = "gah 需要你的回应"
	}
	var out attentionResult
	if s.flasher != nil {
		s.flasher.Flash(true)
		out.Flashed = true
		if s.flashDur > 0 {
			// 定时收尾:窗口拿到焦点后系统自己会停,这里兜住"用户一直不理"的情况。
			time.AfterFunc(s.flashDur, func() { s.flasher.Flash(false) })
		}
	}
	if err := s.sendNotification(title, body); err != nil {
		out.Error = err.Error()
	} else {
		out.Notified = true
	}
	return out
}
