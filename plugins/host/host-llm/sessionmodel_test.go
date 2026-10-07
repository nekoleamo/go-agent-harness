package hostllm

// 会话级模型 / 思考档(第一百一十六批):在 `llm/pre-request` 扩展点上按会话填进去。
//
// 这组测试钉三件事:
//  ① 两个会话各设各的 ⇒ 各拿各的(多页签同时跑时不能串);
//  ② **顺序无关**:已填好的请求不被覆盖(角色的覆盖就是走这条路 —— 它先填,会话级让位,
//     所以"会话级优先"这条规则不靠订阅顺序);
//  ③ 模型名本地不可用时不填 + 告警一次(宁可回落到全局模型,也不要整轮请求直接失败)。

import (
	"context"
	"log/slog"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// prefsStub 最小会话服务:只回答"这个会话显式设了什么"。
type prefsStub struct {
	sdk.CwdSessions
	bySession map[string]sdk.SessionPrefs
}

func (p *prefsStub) SessionPrefsOf(id string) sdk.SessionPrefs { return p.bySession[id] }

func startWithPrefs(t *testing.T, by map[string]sdk.SessionPrefs) (*Service, *prefsStub) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if err := c.Provide("ctx.cwdSessions", &prefsStub{bySession: by}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var llm sdk.LLMService
	if err := c.Inject("ctx.llm", &llm); err != nil {
		t.Fatal(err)
	}
	return llm.(*Service), &prefsStub{bySession: by}
}

func TestSessionModelAndThinkingAreFilled(t *testing.T) {
	s, _ := startWithPrefs(t, map[string]sdk.SessionPrefs{
		"A": {Model: "model-A", Thinking: "high"},
		"B": {Model: "model-B", Thinking: "off"},
	})
	// 未设模型的服务:没有可用目录 ⇒ 一律当可用(不误拦)
	ra := &sdk.LLMRequest{}
	_ = s.onPreRequest(sdk.WithSessionContext(ctxBG(), "A"), &sdk.Event{Payload: ra})
	if ra.Model != "model-A" || ra.Thinking.String() != "high" || !ra.ThinkingSet {
		t.Fatalf("会话 A 应填 model-A/high,得 model=%q thinking=%q set=%v", ra.Model, ra.Thinking, ra.ThinkingSet)
	}
	rb := &sdk.LLMRequest{}
	_ = s.onPreRequest(sdk.WithSessionContext(ctxBG(), "B"), &sdk.Event{Payload: rb})
	if rb.Model != "model-B" || rb.Thinking.String() != "off" {
		t.Fatalf("会话 B 应填 model-B/off,得 model=%q thinking=%q", rb.Model, rb.Thinking)
	}
	if rb.ThinkingSet != true {
		t.Fatal("off 也必须显式标记(否则会被会话级 thinking 回填)")
	}
}

// 顺序无关:请求里已经有值(角色先填了)就别动。
func TestSessionPrefsDoNotOverrideFilled(t *testing.T) {
	s, _ := startWithPrefs(t, map[string]sdk.SessionPrefs{"A": {Model: "session-model", Thinking: "high"}})
	req := &sdk.LLMRequest{Model: "role-model"}
	_ = s.onPreRequest(sdk.WithSessionContext(ctxBG(), "A"), &sdk.Event{Payload: req})
	if req.Model != "role-model" {
		t.Fatalf("已填的模型不该被覆盖,得 %q", req.Model)
	}
	// 思考档同理:显式设置过就不动
	req2 := &sdk.LLMRequest{Thinking: sdk.ThinkingLow, ThinkingSet: true}
	_ = s.onPreRequest(sdk.WithSessionContext(ctxBG(), "A"), &sdk.Event{Payload: req2})
	if req2.Thinking != sdk.ThinkingLow {
		t.Fatalf("已显式设置的思考档不该被覆盖,得 %q", req2.Thinking)
	}
}

// 会话没设 → 一个字都不填(交回全局兜底:s.model / s.thinking)。
func TestSessionPrefsUnsetLeavesRequestAlone(t *testing.T) {
	s, _ := startWithPrefs(t, map[string]sdk.SessionPrefs{"A": {Model: "m"}})
	req := &sdk.LLMRequest{}
	_ = s.onPreRequest(sdk.WithSessionContext(ctxBG(), "B"), &sdk.Event{Payload: req})
	if req.Model != "" || req.ThinkingSet {
		t.Fatalf("没设过的会话不该注入任何东西,得 model=%q thinkingSet=%v", req.Model, req.ThinkingSet)
	}
}

// 本地判为不可用的模型名不填(回落全局继续跑),而不是让整轮请求直接失败。
func TestUnusableSessionModelIsNotInjected(t *testing.T) {
	s, _ := startWithPrefs(t, map[string]sdk.SessionPrefs{"A": {Model: "不存在的模型"}})
	s.catalog = catalogStub{known: "only-model"} // 本地目录只认这一个
	req := &sdk.LLMRequest{}
	_ = s.onPreRequest(sdk.WithSessionContext(ctxBG(), "A"), &sdk.Event{Payload: req})
	if req.Model != "" {
		t.Fatalf("本地不可用的模型不该被注入,得 %q", req.Model)
	}
}

// catalogStub sdk.ModelCatalog 桩:只认 known 这一个模型名。
type catalogStub struct{ known string }

func (c catalogStub) KnownModel(model string) bool { return model == c.known }

func ctxBG() context.Context { return context.Background() }
