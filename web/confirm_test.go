// ConfirmService 单测:审批弹层推送 → 应答回传;ctx 取消按安全默认拒绝。
package web

import (
	"context"
	"testing"
	"time"
)

func TestConfirmAnswerFlow(t *testing.T) {
	hub := NewHub()
	svc := NewConfirm(hub)
	ch, release := hub.Stream()
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan bool, 1)
	errCh := make(chan error, 1)
	go func() {
		ok, err := svc.Confirm(ctx, "危险操作?")
		if err != nil {
			errCh <- err
			return
		}
		result <- ok
	}()

	f := <-ch
	if f.Type != FrameConfirm {
		t.Fatalf("期望 confirm 帧,得 %+v", f)
	}
	req, ok := f.Payload.(*ConfirmRequest)
	if !ok {
		t.Fatalf("载荷应为 *ConfirmRequest,得 %T", f.Payload)
	}
	if req.Prompt != "危险操作?" || req.ID == "" {
		t.Fatalf("弹层内容不符 %+v", req)
	}
	svc.Answer(req.ID, true)
	select {
	case ok := <-result:
		if !ok {
			t.Fatal("应答应为 true")
		}
	case <-time.After(time.Second):
		t.Fatal("应答未回传")
	}
}

func TestConfirmCancelRejects(t *testing.T) {
	hub := NewHub()
	svc := NewConfirm(hub)
	ch, release := hub.Stream()
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	// 不应答:ctx 超时 → 拒绝(安全默认)
	errCh := make(chan error, 1)
	go func() { _, err := svc.Confirm(ctx, "无应答"); errCh <- err }()
	// 第一帧:弹层
	if f := <-ch; f.Type != FrameConfirm {
		t.Fatalf("期望 confirm 帧,得 %+v", f)
	}
	// 第二帧:**裁决帧**。不带它弹层就永远挂在界面上 —— 单 profile(web 自 Provide ctx.confirm)
	// 不经 host-confirm-fusion,没有 confirm/resolved 事件可订阅(真机反馈:
	// 「超时系统默认失败,继续进行,但弹窗仍在界面上」)。
	select {
	case f := <-ch:
		if f.Type != FrameConfirmDone {
			t.Fatalf("取消时必须推裁决帧(否则弹层残留),得 %+v", f)
		}
		d, ok := f.Payload.(*ConfirmDone)
		if !ok || d.Prompt != "无应答" || d.Err == "" {
			t.Fatalf("裁决帧应带 prompt 与 err: %#v", f.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("取消后未收到 confirmdone 帧(弹层会残留)")
	}
	if err := <-errCh; err == nil {
		t.Fatal("超时未应答应返回错误(按拒绝)")
	}
	// 应答未知 id(超时后的迟应答)不 panic
	svc.Answer("nonexistent", true)
}

// TestPendingConfirmReplayedOnConnect 无限等待的配套:连接建立时补推未决审批弹层。
// 否则审批挂起期间刷新/重开页面 ⇒ 弹层永远不出现,而审批默认不再超时 ⇒ 回合一直挂。
func TestPendingConfirmReplayedOnConnect(t *testing.T) {
	s, _ := newTestServer()
	cs := s.confirm

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, _ = cs.Confirm(ctx, "危险操作?") }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(cs.Pending()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if len(cs.Pending()) != 1 {
		t.Fatalf("应有 1 条未决确认,得 %d", len(cs.Pending()))
	}

	frames := make(chan Frame, 8)
	stop := make(chan struct{})
	go s.consumeStream(0, func(f Frame) error { frames <- f; return nil }, stop)
	defer close(stop)

	saw := false
	for i := 0; i < 4 && !saw; i++ {
		select {
		case f := <-frames:
			if f.Type != FrameConfirm {
				continue // 首帧是 baseline;其余会话帧与本用例无关
			}
			req, ok := f.Payload.(*ConfirmRequest)
			if !ok || req.Prompt != "危险操作?" || req.ID == "" {
				t.Fatalf("补推的弹层载荷不对: %#v", f.Payload)
			}
			saw = true
		case <-time.After(time.Second):
		}
	}
	if !saw {
		t.Fatal("新连接未收到未决弹层(刷新页面后审批就永远等不到人了)")
	}

	// 已裁决的不再补推
	cs.Answer(cs.Pending()[0].ID, true)
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(cs.Pending()) > 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(cs.Pending()); got != 0 {
		t.Fatalf("应答后 pending 应清空,得 %d", got)
	}
}

func TestConfirmDeny(t *testing.T) {
	hub := NewHub()
	svc := NewConfirm(hub)
	ch, release := hub.Stream()
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan bool, 1)
	go func() {
		ok, err := svc.Confirm(ctx, "拒绝?")
		if err == nil {
			result <- ok
		}
	}()
	f := <-ch
	req := f.Payload.(*ConfirmRequest)
	svc.Answer(req.ID, false)
	select {
	case ok := <-result:
		if ok {
			t.Fatal("应答应为 false")
		}
	case <-time.After(time.Second):
		t.Fatal("应答未回传")
	}
}
