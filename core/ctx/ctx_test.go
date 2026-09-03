package ctx

import (
	"context"
	"log/slog"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func newCtx(t *testing.T) *Ctx {
	t.Helper()
	return New(slog.New(slog.DiscardHandler), event.New(slog.New(slog.DiscardHandler)))
}

type fakeTools struct{ n int }

func (f *fakeTools) Run() int { return f.n }

func TestProvideInjectRoundTrip(t *testing.T) {
	c := newCtx(t)
	svc := &fakeTools{n: 42}
	if err := c.Provide("ctx.tools", svc); err != nil {
		t.Fatal(err)
	}
	var got *fakeTools
	if err := c.Inject("ctx.tools", &got); err != nil {
		t.Fatal(err)
	}
	if got != svc || got.n != 42 {
		t.Fatalf("注入结果不一致:got=%+v", got)
	}
}

func TestProvideDuplicateFails(t *testing.T) {
	c := newCtx(t)
	_ = c.Provide("ctx.tools", &fakeTools{})
	if err := c.Provide("ctx.tools", &fakeTools{}); err == nil {
		t.Fatal("重复注册应报错")
	}
}

func TestInjectMissingFailsExplicitly(t *testing.T) {
	c := newCtx(t)
	var got *fakeTools
	if err := c.Inject("ctx.missing", &got); err == nil {
		t.Fatal("缺失服务应显式失败,不静默")
	}
}

func TestInjectTypeMismatchFails(t *testing.T) {
	c := newCtx(t)
	_ = c.Provide("ctx.tools", &fakeTools{})
	var got *int // 类型不匹配
	if err := c.Inject("ctx.tools", &got); err == nil {
		t.Fatal("类型不匹配应报错")
	}
}

func TestCtxEmitAndSubscribe(t *testing.T) {
	c := newCtx(t)
	var seen any
	c.Subscribe("evt", func(c context.Context, ev *sdk.Event) error {
		seen = ev.Payload
		return nil
	})
	got, err := c.Emit(context.Background(), "evt", 7, sdk.Bail)
	if err != nil {
		t.Fatal(err)
	}
	ev := got.(*sdk.Event)
	if seen != 7 || ev.Payload != 7 {
		t.Fatalf("事件载荷不一致:seen=%v got=%v", seen, ev.Payload)
	}
}

func TestListServiceKeys(t *testing.T) {
	c := newCtx(t)
	_ = c.Provide("ctx.a", &fakeTools{})
	_ = c.Provide("ctx.b", &fakeTools{})
	if len(c.ListServiceKeys()) != 2 {
		t.Fatalf("服务键数不符:got %v", c.ListServiceKeys())
	}
}
