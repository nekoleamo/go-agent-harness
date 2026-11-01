// 整卷问卷端到端(真实装配 tool-ask + host-confirm-fusion + host-agent-loop + mock LLM):
//
//	① 模型一次调用 questions[] → 融合派给**整卷渠道** → 作答 JSON 回模型(含 summary/answers);
//	② 只有单题能力的渠道 → 融合逐题降级,答案仍按题序组装;
//	③ 「聊聊这个」→ 结果里带 chat 与「不要重发整卷」的 note;
//	④ 事件面:整卷路径只发 questionnaire/requested↔resolved(不叠加每题一对 question/*
//	   在整卷路径上;降级路径反过来) —— 订阅方据此不会被双发。
package tests

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/bundles/base"
	confirmfusion "github.com/nekoleamo/go-agent-harness/bundles/confirm-fusion"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// qnToolCall 模型侧一次整卷调用(两题:单选 + 自由文本)。
const qnToolCall = `{"tools":[{"name":"ask_user_question","args":"{\"title\":\"本轮待确认\",\"questions\":[{\"id\":\"scope\",\"label\":\"Scope\",\"prompt\":\"范围到哪?\",\"hint\":\"决定切片\",\"options\":[{\"value\":\"web\",\"label\":\"只做 Web\",\"description\":\"最快\"},{\"value\":\"all\",\"label\":\"三端\"}]},{\"id\":\"note\",\"label\":\"补充\",\"prompt\":\"还有什么?\"}]}"}]}`

// qnPresenterE2E 整卷渠道桩:记录收到的整卷,回固定作答。
type qnPresenterE2E struct {
	mu  sync.Mutex
	got []sdk.Questionnaire
	ans sdk.QuestionnaireAnswer
}

func (p *qnPresenterE2E) PresentQuestionnaire(_ context.Context, qn sdk.Questionnaire) (<-chan sdk.QuestionnaireAnswer, func(), error) {
	p.mu.Lock()
	p.got = append(p.got, qn)
	ans := p.ans
	p.mu.Unlock()
	ch := make(chan sdk.QuestionnaireAnswer, 1)
	ch <- ans
	return ch, func() {}, nil
}

// PresentQuestion 整卷渠道也实现单题呈现(融合注册表要求 QuestionPresenter)。
func (p *qnPresenterE2E) PresentQuestion(context.Context, sdk.Question) (<-chan sdk.QuestionAnswer, func(), error) {
	ch := make(chan sdk.QuestionAnswer, 1)
	return ch, func() {}, nil
}

func (p *qnPresenterE2E) last() (sdk.Questionnaire, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.got) == 0 {
		return sdk.Questionnaire{}, false
	}
	return p.got[len(p.got)-1], true
}

// singlePresenterE2E 只实现单题呈现(模拟 ACP/老 UI 插件):验证逐题降级。
// 作答按题面区分(第 1 题给选项值、第 2 题给手写),这样断言能证明"答案按题序组装"。
type singlePresenterE2E struct {
	got []sdk.Question
}

func (p *singlePresenterE2E) PresentQuestion(_ context.Context, q sdk.Question) (<-chan sdk.QuestionAnswer, func(), error) {
	p.got = append(p.got, q)
	ch := make(chan sdk.QuestionAnswer, 1)
	if len(q.Options) > 0 {
		ch <- sdk.QuestionAnswer{Values: []string{"web"}, Index: 1}
	} else {
		ch <- sdk.QuestionAnswer{Text: "补充一句", Custom: true}
	}
	return ch, func() {}, nil
}

// buildQuestionnaireEnv 装配最小 base + 整卷通路,返回 ctx / 事件收集 / mock 脚本。
func buildQuestionnaireEnv(t *testing.T, script string) (sdk.Ctx, *[]string) {
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
		{ID: "host-confirm-fusion"},
		{ID: "tool-ask"},
		{ID: "llm-mock", Data: map[string]any{"script": script}},
		{ID: "host-agent-loop"},
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
	// 融合 bundle 不在 base 里(它是 profile 级能力):ctx.question 由它 Provide。
	if err := confirmfusion.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })

	var mu sync.Mutex
	var names []string
	for _, n := range []string{sdk.EventQuestionnaireRequested, sdk.EventQuestionnaireResolved, sdk.EventQuestionRequested, sdk.EventQuestionResolved} {
		name := n
		c.Subscribe(name, func(context.Context, *sdk.Event) error {
			mu.Lock()
			names = append(names, name)
			mu.Unlock()
			return nil
		})
	}
	return c, &names
}

// toolResults 会话账本里的工具结果正文(模型可见面)。
func toolResults(t *testing.T, c sdk.Ctx) []string {
	t.Helper()
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range sessions.DeriveMessages() {
		if m.Role == sdk.RoleTool {
			out = append(out, m.Content)
		}
	}
	return out
}

// TestQuestionnaireEndToEndSheet 整卷渠道:一次问完,作答 JSON 回模型。
func TestQuestionnaireEndToEndSheet(t *testing.T) {
	c, names := buildQuestionnaireEnv(t, `[`+qnToolCall+`,{"text":"收到,开始做。"}]`)
	var qs sdk.QuestionService
	if err := c.Inject("ctx.question", &qs); err != nil {
		t.Fatal(err)
	}
	p := &qnPresenterE2E{ans: sdk.QuestionnaireAnswer{Answers: []sdk.QuestionAnswer{
		{Values: []string{"web"}, Index: 1},
		{Text: "记得写测试", Custom: true},
	}}}
	defer qs.RegisterQuestioner("test", p)()

	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	if err := loop.Run(context.Background(), "问几个问题"); err != nil {
		t.Fatalf("回合失败: %v", err)
	}

	// ① 渠道收到的是**归一化**整卷:整卷 id、题 id/序号/标签、默认 allow_other/allow_chat
	qn, ok := p.last()
	if !ok {
		t.Fatal("整卷渠道未收到整卷")
	}
	if qn.ID == "" || len(qn.Questions) != 2 {
		t.Fatalf("整卷载荷不符: %+v", qn)
	}
	if qn.Questions[0].Index != 1 || qn.Questions[1].Label != "补充" || qn.Questions[0].Hint != "决定切片" {
		t.Fatalf("题元数据不符(序号/标签/hint): %+v", qn.Questions)
	}
	if !qn.Questions[0].AllowOther || !qn.Questions[0].AllowChat {
		t.Fatal("allow_other/allow_chat 默认应为 true")
	}
	if qn.Questions[0].Options[0].Label != "只做 Web" || qn.Questions[0].Options[0].Desc != "最快" {
		t.Fatalf("选项 label/description 未透传: %+v", qn.Questions[0].Options)
	}

	// ② 作答 JSON 回模型(工具结果正文)
	// 结果正文在会话账本里是**字符串化的 JSON**(引号带转义),所以这里按无歧义的片段断言:
	// 摘要口径(Scope=web)+ 手写答案 + 结构化字段名。
	res := strings.Join(toolResults(t, c), "\n")
	for _, want := range []string{"summary", "Scope=web", "记得写测试", "index"} {
		if !strings.Contains(res, want) {
			t.Fatalf("工具结果应含 %q: %s", want, res)
		}
	}

	// ③ 事件面:整卷路径只发 questionnaire/*(不叠加每题一对 question/*)
	mu := namesLock(names)
	if mu.count(sdk.EventQuestionnaireRequested) != 1 || mu.count(sdk.EventQuestionnaireResolved) != 1 {
		t.Fatalf("整卷事件应各一条: %v", mu.all())
	}
	if mu.count(sdk.EventQuestionRequested) != 0 || mu.count(sdk.EventQuestionResolved) != 0 {
		t.Fatalf("整卷路径不应再发单题事件: %v", mu.all())
	}
}

// TestQuestionnaireEndToEndFallback 只有单题能力的渠道:融合逐题降级,答案仍按题序组装。
func TestQuestionnaireEndToEndFallback(t *testing.T) {
	c, names := buildQuestionnaireEnv(t, `[`+qnToolCall+`,{"text":"收到。"}]`)
	var qs sdk.QuestionService
	if err := c.Inject("ctx.question", &qs); err != nil {
		t.Fatal(err)
	}
	p := &singlePresenterE2E{}
	defer qs.RegisterQuestioner("legacy", p)()

	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	if err := loop.Run(context.Background(), "问几个问题"); err != nil {
		t.Fatalf("降级回合失败: %v", err)
	}
	if len(p.got) != 2 {
		t.Fatalf("应逐题问 2 次,实际 %d", len(p.got))
	}
	if p.got[0].Prompt != "范围到哪?" || p.got[1].Prompt != "还有什么?" {
		t.Fatalf("逐题顺序应与题序一致: %+v", p.got)
	}
	res := strings.Join(toolResults(t, c), "\n")
	if !strings.Contains(res, "补充一句") || !strings.Contains(res, "Scope=web") {
		t.Fatalf("逐题答案应按题序组装进结果: %s", res)
	}
	// 降级路径:每题一对 question/* 事件仍在(老订阅方不掉线),整卷事件也各一条
	mu := namesLock(names)
	if mu.count(sdk.EventQuestionRequested) != 2 || mu.count(sdk.EventQuestionResolved) != 2 {
		t.Fatalf("降级路径应有每题一对 question 事件: %v", mu.all())
	}
	if mu.count(sdk.EventQuestionnaireRequested) != 1 {
		t.Fatalf("整卷事件仍应有一条: %v", mu.all())
	}
}

// TestQuestionnaireEndToEndChat 「聊聊这个」:结果带 chat + 明确要求转对话。
func TestQuestionnaireEndToEndChat(t *testing.T) {
	c, _ := buildQuestionnaireEnv(t, `[`+qnToolCall+`,{"text":"我们聊聊优先级。"}]`)
	var qs sdk.QuestionService
	if err := c.Inject("ctx.question", &qs); err != nil {
		t.Fatal(err)
	}
	p := &qnPresenterE2E{ans: sdk.QuestionnaireAnswer{Chat: "scope", Answers: []sdk.QuestionAnswer{{}, {}}}}
	defer qs.RegisterQuestioner("test", p)()

	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	if err := loop.Run(context.Background(), "问几个问题"); err != nil {
		t.Fatalf("回合失败: %v", err)
	}
	res := strings.Join(toolResults(t, c), "\n")
	if !strings.Contains(res, "chat") || !strings.Contains(res, "scope") {
		t.Fatalf("结果应带 chat 题 id: %s", res)
	}
	if !strings.Contains(res, "不要重发") {
		t.Fatalf("chat 提示应要求转对话而不是重发整卷: %s", res)
	}
}

// eventNames 事件名只读视图。回合 Run 返回后不再有写入(fusion 的 emit 是同步的),
// 所以这里不加锁;真需要并发时再引入收集器 —— 不为一次断言造一层同步机器。
type eventNames []string

func namesLock(p *[]string) eventNames { return eventNames(*p) }

func (e eventNames) all() []string { return append([]string(nil), e...) }

func (e eventNames) count(name string) int {
	n := 0
	for _, v := range e {
		if v == name {
			n++
		}
	}
	return n
}
