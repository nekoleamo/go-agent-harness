// tool-ask 单测:注册/正常提问(单选/多选/自由文本)/未装配通道显式错误/参数校验。
package toolask

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubCtx 最小 sdk.Ctx(服务容器 + 空事件总线)。
type stubCtx struct{ svc map[string]any }

func (c *stubCtx) Provide(k string, v any) error { c.svc[k] = v; return nil }
func (c *stubCtx) Inject(k string, out any) error {
	v, ok := c.svc[k]
	if !ok {
		return fmt.Errorf("ctx: service %q not provided", k)
	}
	rv := reflect.ValueOf(out).Elem()
	rv.Set(reflect.ValueOf(v))
	return nil
}
func (c *stubCtx) Subscribe(string, sdk.AnyListener) sdk.Disposer { return func() {} }
func (c *stubCtx) Emit(context.Context, string, any, sdk.DispatchMode) (any, error) {
	return nil, nil
}
func (c *stubCtx) Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// stubTools 收集注册的工具。
type stubTools struct{ reg map[string]sdk.Tool }

func (t *stubTools) Register(tool sdk.Tool) sdk.Disposer {
	t.reg[tool.Definition().Name] = tool
	return func() { delete(t.reg, tool.Definition().Name) }
}
func (t *stubTools) List() []sdk.ToolDefinition {
	out := make([]sdk.ToolDefinition, 0, len(t.reg))
	for _, v := range t.reg {
		out = append(out, v.Definition())
	}
	return out
}
func (t *stubTools) Get(name string) (sdk.ToolDefinition, bool) {
	v, ok := t.reg[name]
	if !ok {
		return sdk.ToolDefinition{}, false
	}
	return v.Definition(), true
}
func (t *stubTools) Execute(ctx context.Context, name, args string) (*sdk.ToolResult, error) {
	v, ok := t.reg[name]
	if !ok {
		return nil, fmt.Errorf("tool %s not found", name)
	}
	out, err := v.Execute(ctx, args)
	if err != nil {
		return nil, err
	}
	return &sdk.ToolResult{Content: fmt.Sprint(out)}, nil
}

// stubQuestion 记录提问并回固定答案。
type stubQuestion struct {
	got sdk.Question
	ans sdk.QuestionAnswer
	err error
}

func (s *stubQuestion) Ask(_ context.Context, q sdk.Question) (sdk.QuestionAnswer, error) {
	s.got = q
	return s.ans, s.err
}
func (s *stubQuestion) RegisterQuestioner(string, sdk.QuestionPresenter) sdk.Disposer {
	return func() {}
}

func build(t *testing.T, withQuestion bool) (*stubTools, *stubQuestion, error) {
	t.Helper()
	tools := &stubTools{reg: map[string]sdk.Tool{}}
	svc := map[string]any{"ctx.tools": sdk.ToolRegistry(tools)}
	var q *stubQuestion
	if withQuestion {
		q = &stubQuestion{}
		svc["ctx.question"] = sdk.QuestionService(q)
	}
	c := &stubCtx{svc: svc}
	_, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	return tools, q, err
}

// TestRegister 工具注册:名称与 schema 必备字段。
func TestRegister(t *testing.T) {
	tools, _, err := build(t, true)
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := tools.reg["ask_user_question"]
	if !ok {
		t.Fatal("应注册 ask_user_question")
	}
	def := tool.Definition()
	if !strings.Contains(def.Description, "结构化问题") {
		t.Fatalf("描述应说明用途: %q", def.Description)
	}
	props, _ := def.InputSchema["properties"].(map[string]any)
	for _, k := range []string{"prompt", "options", "multiple", "free_text"} {
		if _, ok := props[k]; !ok {
			t.Fatalf("schema 缺 %s: %+v", k, props)
		}
	}
}

// TestExecuteOptionQuestion 单选提问:参数透传 + JSON 结构化作答。
func TestExecuteOptionQuestion(t *testing.T) {
	tools, q, _ := build(t, true)
	q.ans = sdk.QuestionAnswer{Values: []string{"prod"}}
	tool := tools.reg["ask_user_question"]
	out, err := tool.Execute(context.Background(), `{"prompt":"部署到哪?","options":[{"value":"dev","desc":"开发"},{"value":"prod","desc":"生产"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if q.got.Prompt != "部署到哪?" || len(q.got.Options) != 2 || q.got.Options[1].Value != "prod" {
		t.Fatalf("提问载荷不符: %+v", q.got)
	}
	got, _ := out.(string)
	if !strings.Contains(got, `"prod"`) {
		t.Fatalf("应回结构化作答 JSON: %q", got)
	}
}

// TestExecuteFreeText 多选 + 自由文本标记透传。
func TestExecuteFreeText(t *testing.T) {
	tools, q, _ := build(t, true)
	q.ans = sdk.QuestionAnswer{Text: "自定义回答"}
	tool := tools.reg["ask_user_question"]
	if _, err := tool.Execute(context.Background(), `{"prompt":"说说看","multiple":true,"free_text":true}`); err != nil {
		t.Fatal(err)
	}
	if !q.got.Multiple || !q.got.FreeText || len(q.got.Options) != 0 {
		t.Fatalf("标记应透传: %+v", q.got)
	}
}

// TestExecuteNoChannel 未装配 ctx.question:显式错误(不静默假答)。
func TestExecuteNoChannel(t *testing.T) {
	tools, _, err := build(t, false)
	if err != nil {
		t.Fatal(err)
	}
	tool := tools.reg["ask_user_question"]
	_, err = tool.Execute(context.Background(), `{"prompt":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "未装配") {
		t.Fatalf("未装配应显式报错: %v", err)
	}
}

// TestExecuteBadArgs 参数校验:坏 JSON / 空 prompt。
func TestExecuteBadArgs(t *testing.T) {
	tools, _, _ := build(t, true)
	tool := tools.reg["ask_user_question"]
	if _, err := tool.Execute(context.Background(), `{bad`); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
	if _, err := tool.Execute(context.Background(), `{"prompt":"  "}`); err == nil {
		t.Fatal("空 prompt 应报错")
	}
}

// TestExecuteAskError 通道错误透传(如 ctx 取消)。
func TestExecuteAskError(t *testing.T) {
	tools, q, _ := build(t, true)
	q.err = context.Canceled
	tool := tools.reg["ask_user_question"]
	if _, err := tool.Execute(context.Background(), `{"prompt":"x"}`); err == nil || !strings.Contains(err.Error(), "提问失败") {
		t.Fatalf("通道错误应透传: %v", err)
	}
}
