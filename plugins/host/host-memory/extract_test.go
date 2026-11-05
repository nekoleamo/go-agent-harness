package hostmemory

// 自动生成的宿主侧契约(P1)。
//
// 钉的是五件会"悄悄错"的事:
//   ① 规则闸不命中就不发请求(成本闸门在这儿,不在别处);
//   ② 高置信白名单类型真的落进记忆,低置信进候选,凭据既不进记忆也不进候选;
//   ③ 失败(llm/parse)如实记进账本,且**不改动**已有记忆(失败不等于"没有新事实");
//   ④ 冷却/关闭时一次请求都不发;
//   ⑤ Disposer 之后 worker 真的退出(注册即副作用,卸载即撤销)。

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/internal/memory"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubLLM 最小 LLMService(只用来喂提取器的返回值/错误)。
type stubLLM struct {
	mu      sync.Mutex
	calls   int
	content string
	err     error
	// gate 非 nil 时 Complete 先等它(用于测 Disposer 的等待行为)。
	gate chan struct{}
}

func (s *stubLLM) RegisterAdapter(sdk.LLMAdapter) sdk.Disposer { return func() {} }
func (s *stubLLM) SetModel(string)                             {}
func (s *stubLLM) Model() string                               { return "stub-model" }
func (s *stubLLM) List() []string                              { return []string{"stub"} }
func (s *stubLLM) SetProvider(string, string) error            { return nil }
func (s *stubLLM) UnsetProvider(string) error                  { return nil }
func (s *stubLLM) ResetProvider() error                        { return nil }
func (s *stubLLM) ListModels() ([]sdk.ModelInfo, error)        { return nil, nil }
func (s *stubLLM) ProviderInfo() (string, string, bool)        { return "", "", false }
func (s *stubLLM) SetThinking(sdk.ThinkingLevel)               {}
func (s *stubLLM) Thinking() sdk.ThinkingLevel                 { return sdk.ThinkingOff }

func (s *stubLLM) Complete(ctx context.Context, _ *sdk.LLMRequest,
	_ func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	s.mu.Lock()
	s.calls++
	gate := s.gate
	s.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	return &sdk.LLMResponse{Message: sdk.LLMMessage{Role: sdk.RoleAssistant, Content: s.content},
		FinishReason: sdk.FinishReasonStop}, nil
}

func (s *stubLLM) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// startExtractEnv 装配 host-system-prompt + host-memory(带给定 llm)。
func startExtractEnv(t *testing.T, data map[string]any) (sdk.Ctx, *Service, *stubLLM) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	ll := &stubLLM{content: `{"items":[]}`}
	if err := c.Provide("ctx.llm", sdk.LLMService(ll)); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	man := &sdk.Manifest{Data: data}
	if _, err := (&Plugin{}).Start(c, man); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {}) // Disposer 由各用例按需调用
	var ms sdk.MemoryService
	if err := c.Inject("ctx.memory", &ms); err != nil {
		t.Fatal(err)
	}
	return c, ms.(*Service), ll
}

func observe(t *testing.T, svc *Service, d sdk.TurnDigest) {
	t.Helper()
	var ex sdk.MemoryExtractor
	if err := svc.c.Inject("ctx.memoryExtract", &ex); err != nil || ex == nil {
		t.Fatalf("ctx.memoryExtract 未装配: %v", err)
	}
	if err := ex.ObserveTurn(context.Background(), d); err != nil {
		t.Fatalf("投递失败: %v", err)
	}
}

// waitSnap 等 worker 把观测值写出来(提取是异步的)。
func waitSnap(t *testing.T, svc *Service, cond func(sdk.MemoryExtractSnapshot) bool) sdk.MemoryExtractSnapshot {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if snap := svc.LastExtract(); !snap.At.IsZero() && cond(snap) {
			return snap
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等不到期望的提取观测值,最后: %+v", svc.LastExtract())
	return sdk.MemoryExtractSnapshot{}
}

func userMemory(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(memory.UserPath())
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestExtractRuleMissDoesNotCallModel(t *testing.T) {
	_, svc, ll := startExtractEnv(t, nil)
	observe(t, svc, sdk.TurnDigest{SessionID: "s1", UserText: "帮我看看这个函数在哪"})
	snap := waitSnap(t, svc, func(s sdk.MemoryExtractSnapshot) bool { return true })
	if snap.RuleHit != "" {
		t.Fatalf("规则未命中应记空 RuleHit,got %+v", snap)
	}
	time.Sleep(50 * time.Millisecond)
	if n := ll.callCount(); n != 0 {
		t.Fatalf("规则未命中不该发模型请求(成本闸门),got %d 次", n)
	}
}

func TestExtractWritesHighConfidenceWhitelisted(t *testing.T) {
	_, svc, ll := startExtractEnv(t, nil)
	ll.content = `{"items":[{"text":"交付前先跑 gate.sh","kind":"convention","confidence":0.92,"cue":"以后都先跑 gate"}]}`
	observe(t, svc, sdk.TurnDigest{SessionID: "s1", UserText: "以后都先跑 gate.sh 再提交"})
	snap := waitSnap(t, svc, func(s sdk.MemoryExtractSnapshot) bool { return s.Produced > 0 })
	if snap.Written != 1 || snap.Candidate != 0 {
		t.Fatalf("白名单 + 高置信应直写,got %+v", snap)
	}
	raw := userMemory(t)
	if !strings.Contains(raw, "交付前先跑 gate.sh") {
		t.Fatalf("记忆文件应写下这条:\n%s", raw)
	}
	if !strings.Contains(raw, "kind=convention") || !strings.Contains(raw, "来源: 会话 s1") {
		t.Fatalf("直写要带元数据与来源(可追溯才可回收):\n%s", raw)
	}
}

func TestExtractLowConfidenceGoesToCandidate(t *testing.T) {
	_, svc, ll := startExtractEnv(t, nil)
	ll.content = `{"items":[{"text":"喜欢蓝灰配色","kind":"preference","confidence":0.65,"cue":"配色"}]}`
	observe(t, svc, sdk.TurnDigest{SessionID: "s1", UserText: "我比较喜欢蓝灰配色"})
	snap := waitSnap(t, svc, func(s sdk.MemoryExtractSnapshot) bool { return s.Produced > 0 })
	if snap.Candidate != 1 || snap.Written != 0 {
		t.Fatalf("中置信应进候选,got %+v", snap)
	}
	if strings.Contains(userMemory(t), "喜欢蓝灰配色") {
		t.Fatal("候选不该直接进记忆")
	}
	cands, err := os.ReadFile(memory.CandidatesPath())
	if err != nil || !strings.Contains(string(cands), "喜欢蓝灰配色") {
		t.Fatalf("候选池应有这条: %v %s", err, cands)
	}
}

func TestExtractDropsSecretEverywhere(t *testing.T) {
	_, svc, ll := startExtractEnv(t, nil)
	ll.content = `{"items":[{"text":"api_key: sk-abcdefgh1234","kind":"convention","confidence":0.99,"cue":"密钥"}]}`
	observe(t, svc, sdk.TurnDigest{SessionID: "s1", UserText: "记住这个 api_key: sk-abcdefgh1234"})
	snap := waitSnap(t, svc, func(s sdk.MemoryExtractSnapshot) bool { return s.Produced > 0 })
	if snap.Dropped != 1 || snap.Written != 0 || snap.Candidate != 0 {
		t.Fatalf("凭据应被丢弃,got %+v", snap)
	}
	if strings.Contains(userMemory(t), "sk-abcdefgh") {
		t.Fatal("凭据绝不进记忆")
	}
	if raw, err := os.ReadFile(memory.CandidatesPath()); err == nil && strings.Contains(string(raw), "sk-abcdefgh") {
		t.Fatal("凭据也不该进候选池")
	}
}

func TestExtractFailureIsRecordedNotSilent(t *testing.T) {
	_, svc, ll := startExtractEnv(t, nil)
	ll.err = fmt.Errorf("端点 500")
	observe(t, svc, sdk.TurnDigest{SessionID: "s1", UserText: "以后都用 pnpm"})
	snap := waitSnap(t, svc, func(s sdk.MemoryExtractSnapshot) bool { return s.Skipped != "" })
	if snap.Skipped != "llm" {
		t.Fatalf("失败应记 llm,got %+v", snap)
	}
	st, err := memory.LoadExtractState()
	if err != nil {
		t.Fatal(err)
	}
	if st.Requests != 1 || st.TotalFailed != 1 {
		t.Fatalf("请求与失败都要记账(成本已发生),got %+v", st)
	}
	if raw := userMemory(t); raw != "" {
		t.Fatalf("提取失败不该写入任何记忆:\n%s", raw)
	}
}

func TestExtractParseFailureSeparateFromEmpty(t *testing.T) {
	_, svc, ll := startExtractEnv(t, nil)
	ll.content = "这不是 JSON"
	observe(t, svc, sdk.TurnDigest{SessionID: "s1", UserText: "以后都用 pnpm"})
	snap := waitSnap(t, svc, func(s sdk.MemoryExtractSnapshot) bool { return s.Skipped != "" })
	if snap.Skipped != "parse" {
		t.Fatalf("解析失败应记 parse,got %+v", snap)
	}
	st, err := memory.LoadExtractState()
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalFailed != 1 || st.TotalEmpty != 0 {
		t.Fatalf("解析失败计入 failed,不能计入 empty,got %+v", st)
	}
}

func TestExtractEmptyItemsCountsAsEmptyNotFailed(t *testing.T) {
	_, svc, _ := startExtractEnv(t, nil)
	observe(t, svc, sdk.TurnDigest{SessionID: "s1", UserText: "以后都用 pnpm"})
	snap := waitSnap(t, svc, func(s sdk.MemoryExtractSnapshot) bool { return s.Skipped == "" })
	if snap.Produced != 0 || snap.Skipped != "" {
		t.Fatalf("空 items 是正常结果,got %+v", snap)
	}
	st, err := memory.LoadExtractState()
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalEmpty != 1 || st.TotalFailed != 0 {
		t.Fatalf("空结果计入 empty,got %+v", st)
	}
}

func TestExtractCooldownSkipsWithoutRequest(t *testing.T) {
	_, svc, ll := startExtractEnv(t, nil)
	observe(t, svc, sdk.TurnDigest{SessionID: "s1", UserText: "以后都用 pnpm"})
	waitSnap(t, svc, func(s sdk.MemoryExtractSnapshot) bool { return s.Skipped == "" })
	first := ll.callCount()
	if first != 1 {
		t.Fatalf("第一次应发一次请求,got %d", first)
	}
	observe(t, svc, sdk.TurnDigest{SessionID: "s1", UserText: "以后都用 pnpm 2"})
	snap := waitSnap(t, svc, func(s sdk.MemoryExtractSnapshot) bool { return s.Skipped == "cooldown" })
	if snap.Skipped != "cooldown" {
		t.Fatalf("冷却期应跳过,got %+v", snap)
	}
	if n := ll.callCount(); n != first {
		t.Fatalf("冷却期不该再发请求,got %d", n)
	}
}

func TestExtractOffSendsNothing(t *testing.T) {
	_, svc, ll := startExtractEnv(t, map[string]any{"extract": "off"})
	observe(t, svc, sdk.TurnDigest{SessionID: "s1", UserText: "以后都用 pnpm"})
	snap := waitSnap(t, svc, func(s sdk.MemoryExtractSnapshot) bool { return s.Skipped != "" })
	if snap.Skipped != "disabled" {
		t.Fatalf("extract=off 应记 disabled,got %+v", snap)
	}
	if n := ll.callCount(); n != 0 {
		t.Fatalf("off 模式不该发请求,got %d", n)
	}
	if svc.ExtractEnabled() {
		t.Fatal("ExtractEnabled 应为 false")
	}
}

func TestUnknownExtractModeFailsLoudly(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	if err := c.Provide("ctx.llm", sdk.LLMService(&stubLLM{content: "{}"})); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{"extract": "always"}}); err == nil {
		t.Fatal("未支持的 extract 模式必须显式失败")
	}
}

// TestExtractorDisposerStopsWorker 注册即副作用、卸载即撤销:Disposer 必须让 worker 退出
// 且不阻塞(队列里的东西不再处理)。
func TestExtractorDisposerStopsWorker(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	ll := &stubLLM{content: `{"items":[]}`, gate: make(chan struct{})}
	if err := c.Provide("ctx.llm", sdk.LLMService(ll)); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	d, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	var ms sdk.MemoryService
	if err := c.Inject("ctx.memory", &ms); err != nil {
		t.Fatal(err)
	}
	svc := ms.(*Service)

	// 先让 worker 卡在一次请求上(gate 未关闭),再卸载:Disposer 应等它退出。
	observe(t, svc, sdk.TurnDigest{SessionID: "s1", UserText: "以后都用 pnpm"})
	time.Sleep(50 * time.Millisecond)
	close(ll.gate) // 放行请求,worker 回到 select
	done := make(chan struct{})
	go func() { d(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Disposer 阻塞:worker 未退出")
	}
}

// countRememberCalls 的口径:只数 memory 工具的 remember(recall/list/forget 不算),
// 且按工具调用逐条计 —— 一次回合调两次 remember 就是 2。
func TestCountRememberCalls(t *testing.T) {
	msgs := []sdk.LLMMessage{
		{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{
			{Name: "memory", Arguments: `{"action":"remember","text":"a"}`},
			{Name: "memory", Arguments: `{"action":"recall","query":"a"}`},
			{Name: "shell", Arguments: `{"command":"echo hi"}`},
		}},
		{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{
			{Name: "memory", Arguments: `{"action":"remember","text":"b"}`},
		}},
		{Role: sdk.RoleUser, Content: "不算"},
	}
	if got := countRememberCalls(msgs); got != 2 {
		t.Fatalf("应只数 remember 2 次,got %d", got)
	}
	if got := countRememberCalls(nil); got != 0 {
		t.Fatalf("空会话应为 0,got %d", got)
	}
}
