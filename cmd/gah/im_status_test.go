// `gah im --status` 探活退出码单测(E4):在线/未连接/凭证无效/装配缺失。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubConn 连接状态桩。
type stubConn struct {
	st   sdk.IMConnectStatus
	spec sdk.IMConnectSpec
}

func (s *stubConn) ConnectSpec() sdk.IMConnectSpec                              { return s.spec }
func (s *stubConn) StartConnect(_ context.Context) (sdk.IMConnectStatus, error) { return s.st, nil }
func (s *stubConn) SubmitConfig(_ context.Context, _ map[string]string) (sdk.IMConnectStatus, error) {
	return s.st, nil
}
func (s *stubConn) ConnectStatus() sdk.IMConnectStatus { return s.st }

func TestIMStatusReportExitCodes(t *testing.T) {
	cases := []struct {
		name string
		st   sdk.IMConnectStatus
		want int
	}{
		{"在线", sdk.IMConnectStatus{Channel: "wechat", Phase: sdk.IMPhaseDone, Account: "…1234"}, imExitOnline},
		{"未登录", sdk.IMConnectStatus{Channel: "wechat", Phase: sdk.IMPhaseIdle, Detail: "未登录"}, imExitOffline},
		{"凭证失效", sdk.IMConnectStatus{Channel: "qq", Phase: sdk.IMPhaseFailed, Error: "校验 access_token 失败: 401 invalid appid"}, imExitBadCreds},
		{"校验中", sdk.IMConnectStatus{Channel: "qq", Phase: sdk.IMPhaseValidating}, imExitOffline},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := &stubCtx{s: map[string]any{"ctx.imChannels": &stubConn{st: c.st}}}
			if got := imStatusReport(ctx, "", true); got != c.want {
				t.Fatalf("退出码 = %d, want %d", got, c.want)
			}
		})
	}
}

func TestIMStatusReportNoChannel(t *testing.T) {
	ctx := &stubCtx{s: map[string]any{}}
	if got := imStatusReport(ctx, "", false); got != imExitNoChannel {
		t.Fatalf("未装配应 5,得 %d", got)
	}
}

func TestIMStatusReportChannelMismatch(t *testing.T) {
	ctx := &stubCtx{s: map[string]any{"ctx.imChannels": &stubConn{st: sdk.IMConnectStatus{Channel: "wechat", Phase: sdk.IMPhaseDone}}}}
	if got := imStatusReport(ctx, "qq", false); got != imExitNoChannel {
		t.Fatalf("渠道不符应 5,得 %d", got)
	}
}

// stubCtx 最小 sdk.Ctx(仅 services)。
type stubCtx struct{ s map[string]any }

func (c *stubCtx) Provide(string, any) error { return nil }
func (c *stubCtx) Inject(k string, out any) error {
	v, ok := c.s[k]
	if !ok {
		return fmt.Errorf("ctx: service %q not provided", k)
	}
	switch p := out.(type) {
	case *sdk.IMConnectService:
		*p = v.(sdk.IMConnectService)
	default:
		return fmt.Errorf("ctx: 未支持的注入目标 %T", out)
	}
	return nil
}
func (c *stubCtx) Subscribe(string, sdk.AnyListener) sdk.Disposer { return func() {} }
func (c *stubCtx) Emit(_ context.Context, _ string, _ any, _ sdk.DispatchMode) (any, error) {
	return nil, nil
}
func (c *stubCtx) Logger() *slog.Logger { return slog.Default() }
