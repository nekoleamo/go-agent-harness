// 会话级偏好的端到端验证(第一百一十六批)。
//
// 这里不测单个组件,测**一条真实链路**:装配 base bundle(含 host-roles / host-cwd-sessions /
// policy-guard / host-llm),两个会话各设各的偏好,然后跑真回合 —— 模型覆盖、沙箱拦截、
// 用量分账必须各归各的。
//
// 为什么必须端到端:这一批的每一步单独测都过,合起来仍可能串 —— 最典型的就是"装配时按全局
// 取了一次值,之后切会话不重取"。白盒测不到这种,所以留给真链路。

package tests

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// prefProbe 记录每次请求的模型/思考档(用来断言"角色按会话覆盖了模型")。
type prefProbe struct {
	mu       sync.Mutex
	models   []string
	seesSess map[string]bool // 请求时读到的会话 id
}

func (p *prefProbe) Name() string { return "pref-probe" }

func (p *prefProbe) Complete(ctx context.Context, req *sdk.LLMRequest, _ func(sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	p.mu.Lock()
	p.models = append(p.models, req.Model)
	p.seesSess[sdk.SessionFromContext(ctx)] = true
	p.mu.Unlock()
	// 带 usage:否则 loop 不会记 usage 事件,用量分桶就无从验证(这是端到端该覆盖的路径)
	return &sdk.LLMResponse{
		Message:      sdk.LLMMessage{Role: sdk.RoleAssistant, Content: "好"},
		FinishReason: sdk.FinishReasonStop,
		Usage:        sdk.Usage{PromptTokens: 100, CompletionTokens: 10},
	}, nil
}

// prefsEnv 装配带 probe 适配器的宿主。
func prefsEnv(t *testing.T, ws string) (sdk.Ctx, *prefProbe) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-system-prompt"},
		{ID: "host-skills"},
		{ID: "host-agent-loop"},
		{ID: "host-roles"},
		{ID: "host-cwd-sessions"},
		{ID: "host-usage-stats"},
		{ID: "policy-guard", Data: map[string]any{"approval": "smart", "sandbox": "workspace-write", "sync": true}},
		{ID: "tool-files"},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := base.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	if _, err := bus.Emit(context.Background(), "cwd/workspace-switched", ws, sdk.Emit); err != nil {
		t.Fatal(err)
	}
	var llm sdk.LLMService
	if err := c.Inject("ctx.llm", &llm); err != nil {
		t.Fatal(err)
	}
	probe := &prefProbe{seesSess: map[string]bool{}}
	llm.RegisterAdapter(probe)
	llm.SetModel("global-model")
	return c, probe
}

// TestSessionPrefsEndToEnd 两个会话各设各的偏好,跑真回合验证三件事各归各的。
func TestSessionPrefsEndToEnd(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)

	// 两个角色:各自声明不同的模型
	for _, r := range []struct{ id, model string }{{"ro", "model-readonly"}, {"rw", "model-write"}} {
		d := filepath.Join(home, "roles", r.id)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		yaml := "id: " + r.id + "\nname: " + r.id + "\ndescription: 测试角色\nmodel: " + r.model + "\n"
		if err := os.WriteFile(filepath.Join(d, "role.yaml"), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "AGENTS.md"), []byte("角色 "+r.id+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	c, probe := prefsEnv(t, ws)

	var cs sdk.CwdSessions
	if err := c.Inject("ctx.cwdSessions", &cs); err != nil {
		t.Fatal(err)
	}
	type setter interface {
		SetSessionPrefs(id string, p sdk.SessionPrefs) error
	}
	st, ok := cs.(setter)
	if !ok {
		t.Fatal("host-cwd-sessions 未实现会话偏好写入(缺 SessionPrefsSource 扩展)")
	}
	// 两个会话:用 Spawn 造(不切换当前会话)
	var d sdk.SessionDir
	if err := c.Inject("ctx.sessionDir", &d); err != nil {
		t.Fatal(err)
	}
	sa, err := d.Spawn()
	if err != nil {
		t.Fatal(err)
	}
	sb, err := d.Spawn()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionPrefs(sa, sdk.SessionPrefs{Role: "ro", Sandbox: "read-only"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionPrefs(sb, sdk.SessionPrefs{Role: "rw"}); err != nil {
		t.Fatal(err)
	}

	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	runner, ok := loop.(sdk.SessionRunner)
	if !ok {
		t.Fatal("未实现 sdk.SessionRunner")
	}
	var wg sync.WaitGroup
	for _, id := range []string{sa, sb} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_ = runner.RunInSession(context.Background(), id, "任务-"+id)
		}(id)
	}
	wg.Wait()

	// ① 模型覆盖按会话:两次请求的模型必须不同(各按各的角色)
	probe.mu.Lock()
	got := append([]string(nil), probe.models...)
	sawSessions := len(probe.seesSess)
	probe.mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("应两次请求,得 %d(%v)", len(got), got)
	}
	if got[0] == got[1] {
		t.Fatalf("两个会话的模型覆盖应不同(角色按会话),实得都是 %q", got[0])
	}
	if !strings.Contains(got[0]+got[1], "model-readonly") || !strings.Contains(got[0]+got[1], "model-write") {
		t.Fatalf("两个角色声明的模型都该出现,实得 %v", got)
	}
	if sawSessions < 2 {
		t.Fatalf("回合 ctx 应带会话 id(两个会话各一个),实得 %d", sawSessions)
	}

	// ② 用量按会话分桶:两个会话各跑过一轮,统计不许串
	var us sdk.UsageStatsService
	if err := c.Inject("ctx.usageStats", &us); err != nil {
		t.Fatal(err)
	}
	ss, ok := us.(sdk.SessionUsageStats)
	if !ok {
		t.Fatal("host-usage-stats 未实现 sdk.SessionUsageStats(按会话取统计)")
	}
	if ss.StatsFor(sa).Requests == 0 || ss.StatsFor(sb).Requests == 0 {
		t.Fatalf("两个会话的请求数应各自 ≥1,得 %d/%d", ss.StatsFor(sa).Requests, ss.StatsFor(sb).Requests)
	}

	// ③ 偏好持久化(恢复对话的前提):重新装配一个宿主读同一份 meta
	c2, _ := prefsEnv(t, ws)
	var cs2 sdk.CwdSessions
	if err := c2.Inject("ctx.cwdSessions", &cs2); err != nil {
		t.Fatal(err)
	}
	src, ok := cs2.(sdk.SessionPrefsSource)
	if !ok {
		t.Fatal("重装配后仍应能读会话偏好")
	}
	if got := src.SessionPrefsOf(sa); got.Role != "ro" || got.Sandbox != "read-only" {
		t.Fatalf("重装配后应读回会话偏好(恢复对话即靠它),得 %+v", got)
	}
}

// TestSessionSandboxTierEndToEnd 会话 A 设只读 ⇒ 它的写被拒;会话 B 不受影响。
func TestSessionSandboxTierEndToEnd(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("GAH_HOME", t.TempDir())
	c, _ := prefsEnv(t, ws)

	var sb sdk.Sandbox
	if err := c.Inject("ctx.sandbox", &sb); err != nil {
		t.Fatal(err)
	}
	vf, ok := sb.(sdk.PathValidatorFor)
	if !ok {
		t.Fatal("沙箱未实现 sdk.PathValidatorFor(按会话校验写路径)")
	}
	var cs sdk.CwdSessions
	if err := c.Inject("ctx.cwdSessions", &cs); err != nil {
		t.Fatal(err)
	}
	var d sdk.SessionDir
	if err := c.Inject("ctx.sessionDir", &d); err != nil {
		t.Fatal(err)
	}
	type st interface {
		SetSessionPrefs(id string, p sdk.SessionPrefs) error
	}
	setter := cs.(st)
	roID, err := d.Spawn()
	if err != nil {
		t.Fatal(err)
	}
	if err := setter.SetSessionPrefs(roID, sdk.SessionPrefs{Sandbox: "read-only"}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(ws, "x.txt")
	ctxRO := sdk.WithSessionContext(context.Background(), roID)
	if err := vf.ValidatePathFor(ctxRO, target); err == nil {
		t.Fatal("会话设了只读,写必须被拒")
	}
	// 另一个会话(没设)⇒ 跟随全局(workspace-write)⇒ 写工作区内放行
	if err := vf.ValidatePathFor(sdk.WithSessionContext(context.Background(), "no-such-session"), target); err != nil {
		t.Fatalf("没设过的会话应跟随全局,写工作区内应放行: %v", err)
	}
}
