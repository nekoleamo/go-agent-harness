package hostmemory

// 记忆宿主的测试(第一百零九批)。
//
// 钉的是三件会"悄悄错"的事:
//   ① 记忆确实进了系统提示,且**排在指令层之后**(不能覆盖角色规则与 AGENTS.md);
//   ② 关掉就不注入(prefs 跨端共享的开关位);
//   ③ 截断时**如实说明**还有多少条没进上下文 —— 不静默截断。

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func newMemCtx(t *testing.T) sdk.Ctx {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	return ctx.New(logger, event.New(logger))
}

// startService 装配 host-system-prompt + host-memory,返回 (ctx, Service)。
func startService(t *testing.T) (sdk.Ctx, *Service) {
	t.Helper()
	c := newMemCtx(t)
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{}
	if _, err := p.Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var ms sdk.MemoryService
	if err := c.Inject("ctx.memory", &ms); err != nil {
		t.Fatal(err)
	}
	return c, ms.(*Service)
}

func TestMemoryInjectedIntoSystemPrompt(t *testing.T) {
	c, svc := startService(t)
	if err := svc.Add("报告图表用蓝灰配色", ""); err != nil {
		t.Fatal(err)
	}
	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		t.Fatal(err)
	}
	msgs := sp.Assemble(nil, nil)
	text := concat(msgs)
	if !strings.Contains(text, "报告图表用蓝灰配色") {
		t.Fatalf("记忆应进系统提示:\n%s", text)
	}
	// 边界声明必须一起进去(记忆是数据不是指令,且位次低于指令层)
	for _, must := range []string{"以指令为准", "<memory>"} {
		if !strings.Contains(text, must) {
			t.Fatalf("应含边界声明 %q:\n%s", must, text)
		}
	}
}

func TestMemorySlotIsAfterInstructions(t *testing.T) {
	c, svc := startService(t)
	if err := svc.Add("记住这条", ""); err != nil {
		t.Fatal(err)
	}
	var sp sdk.SystemPromptService
	_ = c.Inject("ctx.systemPrompt", &sp)
	msgs := sp.Assemble(nil, nil)
	// 找到"指令层"与"跨会话记忆"各自落在哪条消息的更后面 —— 顺序反了记忆就能覆盖规则。
	iRule, iMem := -1, -1
	for i, m := range msgs {
		if iRule < 0 && strings.Contains(m.Content, "你是 gah") {
			iRule = i // 固定引导的实际开头(guidanceText 首句)
		}
		if iMem < 0 && strings.Contains(m.Content, "跨会话记忆") {
			iMem = i
		}
	}
	if iRule < 0 || iMem < 0 {
		t.Fatalf("应同时看到固定引导与记忆片段:rule=%d mem=%d", iRule, iMem)
	}
	if iMem < iRule {
		t.Fatalf("记忆片段应排在固定引导之后(SlotDefault),实际 rule=%d mem=%d", iRule, iMem)
	}
}

func TestDisabledMemoryNotInjected(t *testing.T) {
	c, svc := startService(t)
	_ = svc.Add("不该出现的一句", "")
	if !svc.Enabled() {
		t.Fatal("缺省应为开启")
	}
	if on := svc.SetEnabled(false); on {
		t.Fatal("SetEnabled(false) 应返回新状态 false")
	}
	var sp sdk.SystemPromptService
	_ = c.Inject("ctx.systemPrompt", &sp)
	if text := concat(sp.Assemble(nil, nil)); strings.Contains(text, "不该出现的一句") {
		t.Fatalf("关闭后不应注入:\n%s", text)
	}
	// 记忆仍在(关闭只是不注入)
	if len(svc.List()) != 1 {
		t.Fatal("关闭后记忆应仍留着")
	}
	// 开关位跨端共享(落偏好)
	if !prefs.Load().MemoryOff {
		t.Fatal("开关位应落进偏好(跨端共享)")
	}
}

func TestTruncationIsDisclosed(t *testing.T) {
	c := newMemCtx(t)
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{}
	// 预算小到只放得下一条
	if _, err := p.Start(c, &sdk.Manifest{Data: map[string]any{"budget_bytes": 340}}); err != nil {
		t.Fatal(err)
	}
	var ms sdk.MemoryService
	_ = c.Inject("ctx.memory", &ms)
	svc := ms.(*Service)
	for _, s := range []string{"第一条记忆", "第二条记忆", "第三条记忆", "第四条记忆"} {
		if err := svc.Add(s, ""); err != nil {
			t.Fatal(err)
		}
	}
	var sp sdk.SystemPromptService
	_ = c.Inject("ctx.systemPrompt", &sp)
	text := concat(sp.Assemble(nil, nil))
	if !strings.Contains(text, "预算内放进了") || !strings.Contains(text, "memory list") {
		t.Fatalf("截断时应如实说明还有多少没进上下文:\n%s", text)
	}
}

func TestRemoveAndSourceGovernance(t *testing.T) {
	_, svc := startService(t)
	_ = svc.Add("带来源的记忆", "sess-1")
	_ = svc.Add("手工写的", "")
	if len(svc.List()) != 2 {
		t.Fatalf("应有 2 条,got %d", len(svc.List()))
	}
	// 按来源整段删
	n, err := svc.RemoveBySource("sess-1")
	if err != nil || n != 1 {
		t.Fatalf("按来源删: n=%d err=%v", n, err)
	}
	if len(svc.List()) != 1 {
		t.Fatalf("应剩 1 条,got %d", len(svc.List()))
	}
	// 按序号删(展示序 = 新的在前)
	if _, err := svc.Remove(1); err != nil {
		t.Fatal(err)
	}
	if len(svc.List()) != 0 {
		t.Fatalf("应删空,got %d", len(svc.List()))
	}
	// 越界
	if _, err := svc.Remove(1); err == nil {
		t.Fatal("越界应报错")
	}
}

func TestMissingSystemPromptFailsLoudly(t *testing.T) {
	c := newMemCtx(t)
	// 不装 host-system-prompt:记忆无处注入 ⇒ 显式失败,不是静默只落盘
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err == nil {
		t.Fatal("缺系统提示服务时应显式失败")
	}
}

func concat(msgs []sdk.LLMMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}
