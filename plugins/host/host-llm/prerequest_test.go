// llm_prerequest_test.go:`llm/pre-request` 扩展点与 ThinkingSet 判据(第八十六批)。
//
// 为什么值得单测钉住:这个扩展点是"按角色换模型"的唯一通道,而它发在 LLM 服务的必经路径上 ——
// emit 顺序(早于取锁/早于模型解析)、veto 语义、以及"显式 off 不被会话档回填"三条,
// 任何一条错了都不会报错,只会让人看到一个与预期不符的模型。
package hostllm

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	corectx "github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// recorder 记录它实际收到的请求(适配器侧看到的就是"真正发出去的")。
type recorder struct {
	got *sdk.LLMRequest
}

func (r *recorder) Name() string { return "rec" }
func (r *recorder) Complete(_ context.Context, req *sdk.LLMRequest, _ func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	r.got = req
	return &sdk.LLMResponse{}, nil
}

// newSvcWithBus 造一个带真实 bus 的服务(单测里搭真实 ctx/bus 是仓库既定做法)。
func newSvcWithBus(t *testing.T, model string, thinking sdk.ThinkingLevel) (*Service, sdk.Ctx, *recorder) {
	t.Helper()
	lg := slog.New(slog.DiscardHandler)
	bus := event.New(lg)
	c := corectx.New(lg, bus)
	rec := &recorder{}
	s := &Service{c: c, adapters: map[string]sdk.LLMAdapter{"rec": rec}, order: []string{"rec"},
		model: model, thinking: thinking}
	return s, c, rec
}

func TestPreRequestWaterfallRewritesRequest(t *testing.T) {
	s, c, rec := newSvcWithBus(t, "session-model", sdk.ThinkingOff)
	dw := c.Subscribe(sdk.EventLLMPreRequest, func(_ context.Context, ev *sdk.Event) error {
		req, ok := ev.Payload.(*sdk.LLMRequest)
		if !ok {
			t.Fatalf("载荷应是 *sdk.LLMRequest,得到 %T", ev.Payload)
		}
		req.Model = "role-model"
		return nil
	})
	defer dw()

	if _, err := s.Complete(context.Background(), &sdk.LLMRequest{}, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if rec.got == nil || rec.got.Model != "role-model" {
		t.Fatalf("适配器应收到被改写的模型,得到 %+v", rec.got)
	}
}

// 监听器给模型 ⇒ 即使会话没配模型也能跑(emit 早于"还没有配置模型"检查)。
func TestPreRequestModelWorksWithoutSessionModel(t *testing.T) {
	s, c, rec := newSvcWithBus(t, "", sdk.ThinkingOff)
	dw := c.Subscribe(sdk.EventLLMPreRequest, func(_ context.Context, ev *sdk.Event) error {
		ev.Payload.(*sdk.LLMRequest).Model = "role-model"
		return nil
	})
	defer dw()

	if _, err := s.Complete(context.Background(), &sdk.LLMRequest{}, nil); err != nil {
		t.Fatalf("角色给了模型就不该再报\"还没有配置模型\": %v", err)
	}
	if rec.got.Model != "role-model" {
		t.Fatalf("适配器应收到 role-model,得到 %q", rec.got.Model)
	}
	// 对照:没人给模型时会话也为空 ⇒ 仍应有那条可操作的报错
	s2, _, _ := newSvcWithBus(t, "", sdk.ThinkingOff)
	if _, err := s2.Complete(context.Background(), &sdk.LLMRequest{}, nil); err == nil {
		t.Fatal("会话与监听器都没给模型时应报错")
	}
}

// waterfall veto:监听器返回 error ⇒ 请求被阻断,且**不**打适配器。
func TestPreRequestVetoStopsRequest(t *testing.T) {
	s, c, rec := newSvcWithBus(t, "session-model", sdk.ThinkingOff)
	boom := errors.New("该角色不允许在此模型上运行")
	dw := c.Subscribe(sdk.EventLLMPreRequest, func(context.Context, *sdk.Event) error { return boom })
	defer dw()

	_, err := s.Complete(context.Background(), &sdk.LLMRequest{}, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("veto 错误应原样上冒,得到 %v", err)
	}
	if rec.got != nil {
		t.Fatalf("被阻断的请求不该到达适配器,得到 %+v", rec.got)
	}
}

// ThinkingSet 三态:显式 off 压过会话档;未设置则用会话档;非 Off 的旧写法照旧生效。
func TestThinkingSetRespected(t *testing.T) {
	cases := []struct {
		name string
		req  sdk.LLMRequest
		want sdk.ThinkingLevel
	}{
		{"显式 off:不被会话 high 回填", sdk.LLMRequest{Thinking: sdk.ThinkingOff, ThinkingSet: true}, sdk.ThinkingOff},
		{"未设置:用会话档", sdk.LLMRequest{}, sdk.ThinkingHigh},
		{"旧写法(非 Off 且未置标记):显式值保持", sdk.LLMRequest{Thinking: sdk.ThinkingMedium}, sdk.ThinkingMedium},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, rec := newSvcWithBus(t, "m", sdk.ThinkingHigh)
			req := tc.req
			if _, err := s.Complete(context.Background(), &req, nil); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if rec.got.Thinking != tc.want {
				t.Fatalf("思考档应为 %v,得到 %v", tc.want, rec.got.Thinking)
			}
		})
	}
}

// 无 bus(既有单测直接构造 &Service{})不得 panic:本服务必须容忍 c == nil。
func TestNilCtxNoPanic(t *testing.T) {
	rec := &recorder{}
	s := &Service{adapters: map[string]sdk.LLMAdapter{"rec": rec}, order: []string{"rec"}, model: "m"}
	if _, err := s.Complete(context.Background(), &sdk.LLMRequest{}, nil); err != nil {
		t.Fatalf("c == nil 时应照常工作: %v", err)
	}
	if rec.got == nil || rec.got.Model != "m" {
		t.Fatalf("适配器应收到会话模型,得到 %+v", rec.got)
	}
}
