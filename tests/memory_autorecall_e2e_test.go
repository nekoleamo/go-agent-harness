// 自动调取的端到端(P0):证明「第一轮写下的偏好,第二轮真的被放进模型请求」。
//
// 为什么必须端到端:插件单测只证明「Augment 返回了什么」,证明不了**模型真的看到了它** ——
// 中间还隔着 agent-loop 的组装、前缀探针、以及 ctx 的传递。DESIGN 里「记忆是否真的影响了
// 后续回答」这条登记了五周没验过,这条用例就是它的自动化版本。
//
// 三段互为证伪面:
//
//	① 第一轮请求里,记忆出现在**尾部附加块**(system 里没有 ⇒ 证明走的是自动调取,不是稳定块);
//	② 第二轮问不相关的话题 ⇒ 尾部不再追(零命中不摆空壳);
//	③ 全程前缀探针不得报 "changed" —— 附加块只追加在末尾,不参与前缀指纹。
package tests

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// captureAdapter 只记录"模型收到了什么",每次请求一句话收尾(不调工具,保持回合短)。
type captureAdapter struct {
	mu   sync.Mutex
	reqs [][]sdk.LLMMessage
}

func (a *captureAdapter) Name() string { return "capture" }

// 下面几个方法只为了让 captureAdapter 同时满足 sdk.LLMService(host-agent-loop 要的是
// 服务而不是裸适配器)。本用例不换模型、不切端点,故一律 no-op。
func (a *captureAdapter) RegisterAdapter(sdk.LLMAdapter) sdk.Disposer { return func() {} }
func (a *captureAdapter) SetModel(string)                             {}
func (a *captureAdapter) Model() string                               { return "capture" }
func (a *captureAdapter) List() []string                              { return []string{"capture"} }
func (a *captureAdapter) SetProvider(string, string) error            { return nil }
func (a *captureAdapter) UnsetProvider(string) error                  { return nil }
func (a *captureAdapter) ResetProvider() error                        { return nil }
func (a *captureAdapter) ListModels() ([]sdk.ModelInfo, error)        { return nil, nil }
func (a *captureAdapter) ProviderInfo() (string, string, bool)        { return "", "", false }
func (a *captureAdapter) SetThinking(sdk.ThinkingLevel)               {}
func (a *captureAdapter) Thinking() sdk.ThinkingLevel                 { return sdk.ThinkingOff }

func (a *captureAdapter) Complete(_ context.Context, req *sdk.LLMRequest,
	onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	a.mu.Lock()
	a.reqs = append(a.reqs, append([]sdk.LLMMessage(nil), req.Messages...))
	a.mu.Unlock()
	const text = "好的。"
	if onChunk != nil {
		if err := onChunk(sdk.LLMStreamEvent{Delta: text}); err != nil {
			return nil, err
		}
		done := sdk.LLMStreamEvent{Done: true, FinishReason: sdk.FinishReasonStop}
		done.Message = sdk.LLMMessage{Role: sdk.RoleAssistant, Content: text}
		if err := onChunk(done); err != nil {
			return nil, err
		}
	}
	return &sdk.LLMResponse{Message: sdk.LLMMessage{Role: sdk.RoleAssistant, Content: text},
		FinishReason: sdk.FinishReasonStop}, nil
}

func (a *captureAdapter) last() []sdk.LLMMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.reqs) == 0 {
		return nil
	}
	return a.reqs[len(a.reqs)-1]
}

// buildAutoRecallEnv 装配 host-memory + agent-loop,模型换成捕获适配器。
// host-memory 的稳定块预算压到最小(380 字节)⇒ 记忆装不进系统提示,只能靠自动调取。
func buildAutoRecallEnv(t *testing.T) (sdk.Ctx, *captureAdapter) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-tools"},
		{ID: "host-system-prompt"},
		{ID: "host-memory", Data: map[string]any{
			"budget_bytes": 380, "retrieve_budget": 4096, "top_k": 6,
		}},
		{ID: "host-agent-loop"},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := baseb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	// 模型适配器换成本用例的捕获实现(host-llm 未装配 ⇒ 不会被覆盖)。
	// 必须在 StartSubset **之前** Provide:host-agent-loop 的 Start 期就要 Inject ctx.llm。
	ad := &captureAdapter{}
	if err := c.Provide("ctx.llm", sdk.LLMService(ad)); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c, ad
}

func TestMemoryAutoRecallEndToEnd(t *testing.T) {
	c, ad := buildAutoRecallEnv(t)

	var ms sdk.MemoryService
	if err := c.Inject("ctx.memory", &ms); err != nil {
		t.Fatal(err)
	}
	const pref = "部署统一走 make deploy,不要手动 scp"
	// 两条:一条与第一轮相关,一条是干扰项。
	for _, s := range []string{pref, "报告配色用蓝灰,不要渐变", "提交前跑 gate.sh"} {
		if err := ms.Add(s, ""); err != nil {
			t.Fatal(err)
		}
	}

	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	if err := loop.Run(context.Background(), "这个项目怎么部署"); err != nil {
		t.Fatalf("第一轮: %v", err)
	}

	// ① 模型真的收到了这条记忆,且在**尾部附加块**里(不是系统提示)。
	req := ad.last()
	if len(req) < 2 {
		t.Fatalf("请求消息过少: %+v", req)
	}
	sys := req[0].Content
	if sys != "" && strings.Contains(sys, "make deploy") {
		t.Fatalf("稳定块预算被压到最小,系统提示不该带这条(否则本用例测不到自动调取):\n%s", sys)
	}
	tail := req[len(req)-1]
	if !strings.Contains(tail.Content, "make deploy") {
		t.Fatalf("尾部附加块应含相关记忆(模型真的看到它):\n%+v", req)
	}
	if !strings.Contains(tail.Content, "不是用户发言") {
		t.Fatalf("附加块必须标明不是用户发言(否则模型会以为用户刚改口):\n%s", tail.Content)
	}
	if strings.Contains(tail.Content, "报告配色") {
		t.Fatalf("无关记忆不该进尾部:\n%s", tail.Content)
	}

	// ② 第二轮不相关 ⇒ 不追加(零命中不摆空壳)。
	if err := loop.Run(context.Background(), "帮我看看数据库连接池怎么配"); err != nil {
		t.Fatalf("第二轮: %v", err)
	}
	req2 := ad.last()
	tail2 := req2[len(req2)-1]
	if strings.Contains(tail2.Content, "make deploy") || strings.Contains(tail2.Content, "报告配色") {
		t.Fatalf("不相关的一轮不该追记忆:\n%s", tail2.Content)
	}

	// ③ 全程前缀只增不改(附加块不参与前缀指纹,否则每步都会报 changed)。
	var probe sdk.PrefixProbe
	if err := c.Inject("ctx.prefixProbe", &probe); err != nil || probe == nil {
		t.Fatalf("ctx.prefixProbe 未装配: %v", err)
	}
	samples := probe.PrefixSamples(0)
	if len(samples) < 2 {
		t.Fatalf("样本数应 ≥2,实际 %d —— 门会变成空断言", len(samples))
	}
	for _, s := range samples {
		if s.DiffToPrev == "changed" {
			t.Fatalf("尾部附加块不该改写前缀(第 %d 次请求): %s · %s", s.Seq, s.DiffWhere, s.At)
		}
	}
}
