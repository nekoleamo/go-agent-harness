// TUI 融合 Present 单测(P3):多待答通道广播 + cancel 幂等 + Confirm 走 Present。
package tui

import (
	"context"
	"testing"
	"time"
)

// TestAppPresentBroadcast Present 登记待答 → confirmResult 广播全部通道。
func TestAppPresentBroadcast(t *testing.T) {
	a := commandTestApp()
	ch1, c1, err := a.Present(context.Background(), "确认1")
	if err != nil {
		t.Fatal(err)
	}
	defer c1()
	ch2, c2, err := a.Present(context.Background(), "确认2")
	if err != nil {
		t.Fatal(err)
	}
	defer c2()
	a.confirmResult(true)
	for i, ch := range []<-chan bool{ch1, ch2} {
		select {
		case ok := <-ch:
			if !ok {
				t.Fatalf("通道 %d 应收到批准", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("通道 %d 未收到应答(广播失效)", i)
		}
	}
	// 广播后 pending 清空(cancel 幂等无副作用)
	a.pendMu.Lock()
	n := len(a.pending)
	a.pendMu.Unlock()
	if n != 0 {
		t.Fatalf("应答后 pending 应清空,got %d", n)
	}
	c1()
	c2()
}

// TestAppConfirmViaPresenter Confirm 经 Present 路径(融合改造后)仍可用。
func TestAppConfirmViaPresenter(t *testing.T) {
	a := commandTestApp()
	done := make(chan bool, 1)
	go func() {
		ok, err := a.Confirm(context.Background(), "危险操作")
		if err != nil {
			done <- false
			return
		}
		done <- ok
	}()
	// 等 Present 登记(Confirm 内),再回填拒绝
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.pendMu.Lock()
		n := len(a.pending)
		a.pendMu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Confirm 未登记待答")
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.confirmResult(false)
	select {
	case ok := <-done:
		if ok {
			t.Fatal("应返回拒绝")
		}
	case <-time.After(time.Second):
		t.Fatal("Confirm 未返回")
	}
}

// TestAppPresentCancel 无应答时 cancel 清理待答(超时/放弃路径)。
func TestAppPresentCancel(t *testing.T) {
	a := commandTestApp()
	_, cancel, err := a.Present(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	a.pendMu.Lock()
	n := len(a.pending)
	a.pendMu.Unlock()
	if n != 1 {
		t.Fatalf("Present 后应 1 个待答,got %d", n)
	}
	cancel()
	a.pendMu.Lock()
	n = len(a.pending)
	a.pendMu.Unlock()
	if n != 0 {
		t.Fatalf("cancel 后待答应清理,got %d", n)
	}
}
