// G-E3-R2:TUI IM 状态 widget 单测(映射表 + 懒解析 + 未装配/无渠道回落空串)。
package uitui

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeChannel 三能力齐备的渠道替身。
type fakeChannel struct {
	st  []sdk.IMChannelStatus
	env string
}

func (f *fakeChannel) Status() []sdk.IMChannelStatus { return f.st }
func (f *fakeChannel) ConnectSpec() sdk.IMConnectSpec {
	return sdk.IMConnectSpec{Channel: "qq", Kind: sdk.IMConnectForm}
}
func (f *fakeChannel) StartConnect(context.Context) (sdk.IMConnectStatus, error) {
	return sdk.IMConnectStatus{}, nil
}
func (f *fakeChannel) SubmitConfig(context.Context, map[string]string) (sdk.IMConnectStatus, error) {
	return sdk.IMConnectStatus{}, nil
}
func (f *fakeChannel) ConnectStatus() sdk.IMConnectStatus {
	return sdk.IMConnectStatus{Channel: "qq", Phase: sdk.IMPhaseDone, Env: f.env}
}

// statusOnly 仅实现状态能力的渠道(无连接/退出能力)。
type statusOnly struct{ st []sdk.IMChannelStatus }

func (s statusOnly) Status() []sdk.IMChannelStatus { return s.st }

// newTestCtx 构造带(可选)ctx.imChannels 的宿主上下文。
func newTestCtx(t *testing.T, svc any) sdk.Ctx {
	t.Helper()
	c := ctx.New(slog.New(slog.DiscardHandler), event.New(slog.New(slog.DiscardHandler)))
	if svc != nil {
		if err := c.Provide("ctx.imChannels", svc); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestImStatusWidgetRendersChannelState(t *testing.T) {
	c := newTestCtx(t, &fakeChannel{
		st:  []sdk.IMChannelStatus{{Channel: "wechat", State: "online", Detail: "已登录"}},
		env: "sandbox",
	})
	out := imStatusWidget(c)()
	for _, want := range []string{"●", "微信(沙箱)", "已连接"} {
		if !strings.Contains(out, want) {
			t.Fatalf("widget 缺少 %q: %q", want, out)
		}
	}
	if strings.Contains(out, "需处理") {
		t.Fatalf("无错误不应出现「需处理」: %q", out)
	}
}

func TestImStatusWidgetMultiChannelAndError(t *testing.T) {
	c := newTestCtx(t, statusOnly{st: []sdk.IMChannelStatus{
		{Channel: "wechat", State: "online"},
		{Channel: "qq", State: "configuring", Error: "凭证无效"},
	}})
	out := imStatusWidget(c)()
	if !strings.Contains(out, "微信") || !strings.Contains(out, "QQ") || !strings.Contains(out, " · ") {
		t.Fatalf("多渠道应以 · 分隔: %q", out)
	}
	if !strings.Contains(out, "未配置") || !strings.Contains(out, "需处理") {
		t.Fatalf("失败渠道应显式「未配置/需处理」: %q", out)
	}
	// 仅状态能力的渠道:不 panic,环境缺省不展示括号
	if strings.Contains(out, "(") {
		t.Fatalf("无连接能力不应展示环境: %q", out)
	}
}

func TestImStatusWidgetEmptyWithoutService(t *testing.T) {
	if got := imStatusWidget(newTestCtx(t, nil))(); got != "" {
		t.Fatalf("未装配 ctx.imChannels 应返回空串: %q", got)
	}
	if got := imStatusWidget(newTestCtx(t, statusOnly{}))(); got != "" {
		t.Fatalf("渠道列表为空应返回空串: %q", got)
	}
}

func TestImStatusWidgetMapping(t *testing.T) {
	cases := []struct {
		state, text, level, dot string
	}{
		{"online", "已连接", "ok", "●"},
		{"running", "运行中", "warn", "◐"},
		{"configuring", "未配置", "off", "○"},
		{"offline", "未连接", "off", "○"},
		{"未知", "未连接", "off", "○"},
	}
	for _, c := range cases {
		if got := imStateText(c.state); got != c.text {
			t.Fatalf("imStateText(%q)=%q want %q", c.state, got, c.text)
		}
		if got := imStateLevel(c.state); got != c.level {
			t.Fatalf("imStateLevel(%q)=%q want %q", c.state, got, c.level)
		}
		if got := imStateDot(c.level); got != c.dot {
			t.Fatalf("imStateDot(%q)=%q want %q", c.level, got, c.dot)
		}
	}
	for in, want := range map[string]string{"wechat": "微信", "qq": "QQ", "other": "other"} {
		if got := imChannelLabel(in); got != want {
			t.Fatalf("imChannelLabel(%q)=%q want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"sandbox": "沙箱", "official": "正式", "": "", "x": ""} {
		if got := imEnvLabel(in); got != want {
			t.Fatalf("imEnvLabel(%q)=%q want %q", in, got, want)
		}
	}
}

// 注入失败(未装配)不得 panic 且返回空串 —— 显式覆盖错误分支。
func TestImStatusWidgetInjectError(t *testing.T) {
	c := &errCtx{err: errors.New("ctx.imChannels 未装配")}
	if got := imStatusWidget(c)(); got != "" {
		t.Fatalf("注入失败应返回空串: %q", got)
	}
}

// errCtx Inject 恒失败的最小 Ctx 替身。
type errCtx struct{ err error }

func (e *errCtx) Provide(string, any) error { return nil }
func (e *errCtx) Inject(string, any) error  { return e.err }
func (e *errCtx) Subscribe(string, sdk.AnyListener) sdk.Disposer {
	return func() {}
}
func (e *errCtx) Emit(context.Context, string, any, sdk.DispatchMode) (any, error) {
	return nil, nil
}
func (e *errCtx) Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }
