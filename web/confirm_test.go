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
	_, release := hub.Stream()
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	// 不应答:ctx 超时 → 拒绝(安全默认)
	if _, err := svc.Confirm(ctx, "无应答"); err == nil {
		t.Fatal("超时未应答应返回错误(按拒绝)")
	}
	// 应答未知 id(超时后的迟应答)不 panic
	svc.Answer("nonexistent", true)
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
