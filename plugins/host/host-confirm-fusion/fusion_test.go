// host-confirm-fusion 单测:广播注册/竞速应答/cancel 清理/无渠道安全拒绝。
package hostconfirmfusion

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubPresenter 可控应答(经 answer chan 注入)。
type stubPresenter struct {
	promptCh chan string // Present 收到的 prompt(断言)
	answer   chan bool   // 注入应答(chan 有值才应答;Close 模拟不可用)
	cancels  *int
	cancelMu *sync.Mutex
}

func (sp *stubPresenter) Present(ctx context.Context, prompt string) (<-chan bool, func(), error) {
	sp.promptCh <- prompt
	return sp.answer, func() {
		if sp.cancels != nil {
			sp.cancelMu.Lock()
			*sp.cancels++
			sp.cancelMu.Unlock()
		}
	}, nil
}

// TestFusionBroadcastAndFirstWins 广播两渠道;先应答者生效,另一渠道收到 cancel。
func TestFusionBroadcastAndFirstWins(t *testing.T) {
	f := &Fusion{presenters: make(map[string]sdk.ConfirmPresenter)}
	cancels := 0
	var cmu sync.Mutex
	a1 := make(chan bool, 1)
	p1 := &stubPresenter{promptCh: make(chan string, 1), answer: a1, cancels: &cancels, cancelMu: &cmu}
	a2 := make(chan bool, 1)
	p2 := &stubPresenter{promptCh: make(chan string, 1), answer: a2, cancels: &cancels, cancelMu: &cmu}
	d1 := f.Register("web", p1)
	d2 := f.Register("qq", p2)
	defer d1()
	defer d2()

	go func() { a2 <- true }() // 渠道 2 先应答
	ok, err := f.Confirm(context.Background(), "危险操作?")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("应答应生效")
	}
	if got := <-p1.promptCh; got != "危险操作?" {
		t.Fatalf("渠道1应收到提示: %q", got)
	}
	if got := <-p2.promptCh; got != "危险操作?" {
		t.Fatalf("渠道2应收到提示: %q", got)
	}
	// 渠道 1(未应答)应收到 cancel
	deadline := time.Now().Add(2 * time.Second)
	for cmu.Lock(); *p1.cancels == 0; {
		cmu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("未应答渠道应收到 cancel")
		}
		time.Sleep(10 * time.Millisecond)
		cmu.Lock()
	}
	cmu.Unlock()
}

// TestFusionNoPresenter 无渠道:Confirm 安全失败(取消语义,非静默放行)。
func TestFusionNoPresenter(t *testing.T) {
	f := &Fusion{presenters: make(map[string]sdk.ConfirmPresenter)}
	if _, err := f.Confirm(context.Background(), "x"); err == nil {
		t.Fatal("无渠道应报错(安全拒绝)")
	}
}

// TestFusionCtxCancel ctx 取消按拒绝处理。
func TestFusionCtxCancel(t *testing.T) {
	f := &Fusion{presenters: make(map[string]sdk.ConfirmPresenter)}
	a := make(chan bool, 1)
	f.Register("web", &stubPresenter{promptCh: make(chan string, 1), answer: a})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := f.Confirm(ctx, "x"); err == nil {
		t.Fatal("ctx 取消应返回错误(拒绝)")
	}
}

// TestFusionUnregister 注销渠道后不再被广播。
func TestFusionUnregister(t *testing.T) {
	f := &Fusion{presenters: make(map[string]sdk.ConfirmPresenter)}
	a := make(chan bool, 1)
	sp := &stubPresenter{promptCh: make(chan string, 1), answer: a}
	d := f.Register("web", sp)
	d()
	if _, err := f.Confirm(context.Background(), "x"); err == nil {
		t.Fatal("注销后应无渠道可应答")
	}
}

// TestFusionChannelList Channels 反映注册集。
func TestFusionChannelList(t *testing.T) {
	f := &Fusion{presenters: make(map[string]sdk.ConfirmPresenter)}
	f.Register("web", &stubPresenter{promptCh: make(chan string, 1)})
	f.Register("im-qq", &stubPresenter{promptCh: make(chan string, 1)})
	if got := len(f.Channels()); got != 2 {
		t.Fatalf("Channels 应 2,got %d", got)
	}
}
