// 角色携带模型/思考档:llm/pre-request 监听器的判据(第八十六批)。
//
// 直接调监听器而不过 bus:要钉的是"注入规则"本身(只填空/让位/不可用回退),
// bus 投递语义已有 host-llm 侧的单测覆盖。
package hostroles

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeCatalog 假 LLM:支持 sdk.ModelCatalog(控制"模型能不能接")。
type fakeCatalog struct {
	known map[string]bool
	calls []string
	mu    sync.Mutex
}

func (f *fakeCatalog) RegisterAdapter(sdk.LLMAdapter) sdk.Disposer { return func() {} }
func (f *fakeCatalog) SetModel(string)                             {}
func (f *fakeCatalog) Model() string                               { return "" }
func (f *fakeCatalog) List() []string                              { return nil }
func (f *fakeCatalog) SetProvider(string, string) error            { return nil }
func (f *fakeCatalog) UnsetProvider(string) error                  { return nil }
func (f *fakeCatalog) ResetProvider() error                        { return nil }
func (f *fakeCatalog) ProviderInfo() (string, string, bool)        { return "", "", false }
func (f *fakeCatalog) ListModels() ([]sdk.ModelInfo, error)        { return nil, nil }
func (f *fakeCatalog) SetThinking(sdk.ThinkingLevel)               {}
func (f *fakeCatalog) Thinking() sdk.ThinkingLevel                 { return sdk.ThinkingOff }
func (f *fakeCatalog) ListAllModels() []sdk.ProviderModelList      { return nil }
func (f *fakeCatalog) Complete(context.Context, *sdk.LLMRequest, func(sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	return &sdk.LLMResponse{}, nil
}
func (f *fakeCatalog) KnownModel(m string) bool {
	f.mu.Lock()
	f.calls = append(f.calls, m)
	f.mu.Unlock()
	return f.known[m]
}

// fakeNotices 记下发出去的提示(0 号 Count 便于断言"只告警一次")。
type fakeNotices struct {
	mu   sync.Mutex
	got  []sdk.Notice
	next uint64
}

func (f *fakeNotices) Publish(n sdk.Notice) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.got = append(f.got, n)
	return f.next
}
func (f *fakeNotices) List(since uint64) sdk.NoticePage { return sdk.NoticePage{} }
func (f *fakeNotices) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

// newRoleModelSvc 造一个只带角色缓存的服务(其余依赖按需注入)。
func newRoleModelSvc(t *testing.T, spec sdk.RoleSpec, llm sdk.LLMService, nt sdk.NoticeService) *Service {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	lg := slog.New(slog.DiscardHandler)
	c := ctx.New(lg, event.New(lg))
	if llm != nil {
		if err := c.Provide("ctx.llm", llm); err != nil {
			t.Fatal(err)
		}
	}
	if nt != nil {
		if err := c.Provide("ctx.notices", nt); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{c: c, specs: map[string]sdk.RoleSpec{spec.ID: spec}, active: spec.ID}
	return svc
}

func emitReq(t *testing.T, svc *Service, req *sdk.LLMRequest) *sdk.LLMRequest {
	t.Helper()
	if err := svc.onLLMPreRequest(context.Background(), &sdk.Event{Name: sdk.EventLLMPreRequest, Payload: req}); err != nil {
		t.Fatalf("监听器不应返回错误(veto 会让整个会话停摆): %v", err)
	}
	return req
}

func TestOnLLMPreRequestFillsRoleModelAndThinking(t *testing.T) {
	svc := newRoleModelSvc(t, sdk.RoleSpec{ID: "finance", Name: "财务", Model: "claude-x", Thinking: "off"},
		&fakeCatalog{known: map[string]bool{"claude-x": true}}, nil)

	req := emitReq(t, svc, &sdk.LLMRequest{})
	if req.Model != "claude-x" {
		t.Fatalf("角色模型应被注入,得到 %q", req.Model)
	}
	// 角色显式 off 必须置 ThinkingSet,否则 host-llm 会用会话档回填
	if req.Thinking != sdk.ThinkingOff || !req.ThinkingSet {
		t.Fatalf("角色 thinking=off 应写成 {Off, ThinkingSet:true},得到 {%v, %v}", req.Thinking, req.ThinkingSet)
	}
}

func TestOnLLMPreRequestYieldsToExplicitValues(t *testing.T) {
	svc := newRoleModelSvc(t, sdk.RoleSpec{ID: "finance", Model: "claude-x", Thinking: "high"},
		&fakeCatalog{known: map[string]bool{"claude-x": true}}, nil)

	// 别处已显式指定模型/思考档 → 角色让位(只填空,不掠夺)
	req := emitReq(t, svc, &sdk.LLMRequest{Model: "other", Thinking: sdk.ThinkingLow, ThinkingSet: true})
	if req.Model != "other" || req.Thinking != sdk.ThinkingLow {
		t.Fatalf("显式值应被保留,得到 model=%q thinking=%v", req.Model, req.Thinking)
	}
}

func TestOnLLMPreRequestNoopWithoutDeclaration(t *testing.T) {
	svc := newRoleModelSvc(t, sdk.RoleSpec{ID: "plain"}, &fakeCatalog{}, nil)
	req := emitReq(t, svc, &sdk.LLMRequest{})
	if req.Model != "" || req.Thinking != sdk.ThinkingOff || req.ThinkingSet {
		t.Fatalf("未声明应与本功能上线前完全一致,得到 %+v", req)
	}
	// 未启用角色(基线)同样不该动请求
	svc.active = ""
	if req := emitReq(t, svc, &sdk.LLMRequest{}); req.Model != "" {
		t.Fatalf("基线不该注入模型,得到 %q", req.Model)
	}
}

func TestOnLLMPreRequestUnusableModelWarnsOnceAndFallsBack(t *testing.T) {
	cat := &fakeCatalog{known: map[string]bool{}} // 谁都不认识
	nt := &fakeNotices{}
	svc := newRoleModelSvc(t, sdk.RoleSpec{ID: "finance", Name: "财务", Model: "ghost-model", Thinking: "high"}, cat, nt)

	req := emitReq(t, svc, &sdk.LLMRequest{})
	if req.Model != "" {
		t.Fatalf("不可用的角色模型不该注入,得到 %q", req.Model)
	}
	// 思考档与模型可用性无关,必须照常生效
	if req.Thinking != sdk.ThinkingHigh || !req.ThinkingSet {
		t.Fatalf("模型不可用不该连累思考档,得到 {%v, %v}", req.Thinking, req.ThinkingSet)
	}
	// 热路径上每轮都会进来:告警只能一次
	emitReq(t, svc, &sdk.LLMRequest{})
	emitReq(t, svc, &sdk.LLMRequest{})
	if n := nt.count(); n != 1 {
		t.Fatalf("不可用告警应恰好发一次,实际 %d 条", n)
	}
	if nt.got[0].Level != sdk.NoticeWarn {
		t.Fatalf("不可用应是 warn 级,得到 %v", nt.got[0].Level)
	}
}

// 拿不到 ctx.llm / 实现不含 ModelCatalog(单测里的假 LLM)时一律放行 —— 不做"我以为不可用"的误拦。
func TestOnLLMPreRequestPassesThroughWhenCatalogUnavailable(t *testing.T) {
	svc := newRoleModelSvc(t, sdk.RoleSpec{ID: "finance", Model: "claude-x"}, nil, nil)
	if req := emitReq(t, svc, &sdk.LLMRequest{}); req.Model != "claude-x" {
		t.Fatalf("无目录能力时应放行注入,得到 %q", req.Model)
	}
}
