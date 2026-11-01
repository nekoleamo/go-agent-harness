// tool-ask 整卷形状单测:整卷透传/默认值/互斥校验/重复 id/结果 JSON/降级路径。
package toolask

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubQn question 通道桩:单题 + 整卷都会,记录收到的整卷。
type stubQn struct {
	stubQuestion
	gotQn sdk.Questionnaire
	ansQn sdk.QuestionnaireAnswer
	asked bool // 是否走了整卷
}

func (s *stubQn) AskQuestionnaire(_ context.Context, qn sdk.Questionnaire) (sdk.QuestionnaireAnswer, error) {
	s.asked = true
	s.gotQn = qn
	return s.ansQn, nil
}

// buildQn 装配工具并注入整卷能力桩。
func buildQn(t *testing.T) (sdk.Tool, *stubQn) {
	t.Helper()
	tools := &stubTools{reg: map[string]sdk.Tool{}}
	q := &stubQn{}
	svc := map[string]any{
		"ctx.tools":    sdk.ToolRegistry(tools),
		"ctx.question": sdk.QuestionService(q),
	}
	if _, err := (&Plugin{}).Start(&stubCtx{svc: svc}, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	return tools.reg["ask_user_question"], q
}

// decodeResult 解出工具返回的整卷结果(便于断言字段)。
func decodeResult(t *testing.T, out any) map[string]any {
	t.Helper()
	raw, ok := out.(string)
	if !ok {
		t.Fatalf("应返回 JSON 字符串: %T", out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("返回不是合法 JSON(%v): %s", err, raw)
	}
	return m
}

// TestSchemaHasBothShapes schema 同时暴露单题与整卷字段,且不设 required(否则整卷被挡)。
func TestSchemaHasBothShapes(t *testing.T) {
	tool, _ := buildQn(t)
	def := tool.Definition()
	props, _ := def.InputSchema["properties"].(map[string]any)
	for _, k := range []string{"prompt", "options", "multiple", "free_text", "title", "questions"} {
		if _, ok := props[k]; !ok {
			t.Fatalf("schema 缺 %s", k)
		}
	}
	if req, ok := def.InputSchema["required"]; ok {
		t.Fatalf("顶层不应有 required(会挡住整卷形状): %v", req)
	}
	if !strings.Contains(def.Description, "questions") {
		t.Fatalf("描述应告诉模型整卷形状: %q", def.Description)
	}
}

// TestQuestionnairePassesNormalized 整卷透传:默认 allow_other/allow_chat=true、label 补位、
// 描述落到 Desc、multi_select 落到 Multiple。
func TestQuestionnairePassesNormalized(t *testing.T) {
	tool, q := buildQn(t)
	q.ansQn = sdk.QuestionnaireAnswer{Answers: []sdk.QuestionAnswer{
		{Values: []string{"web"}, Index: 1},
		{Values: []string{"a", "b"}},
	}}
	args := `{"title":"本轮待确认","questions":[
		{"id":"scope","label":"Scope","prompt":"范围?","hint":"决定切片","options":[
			{"value":"web","label":"只做 Web（推荐）","description":"最快"},{"value":"all","label":"三端"}]},
		{"id":"what","prompt":"做哪些?","multi_select":true,"options":[
			{"value":"a","label":"A"},{"value":"b","label":"B"}]}]}`
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !q.asked {
		t.Fatal("应走整卷通道")
	}
	qn := q.gotQn
	if qn.Title != "本轮待确认" || len(qn.Questions) != 2 {
		t.Fatalf("整卷载荷不符: %+v", qn)
	}
	if q0 := qn.Questions[0]; q0.Hint != "决定切片" || !q0.AllowOther || !q0.AllowChat ||
		q0.Options[0].Label != "只做 Web（推荐）" || q0.Options[0].Desc != "最快" {
		t.Fatalf("第 1 题字段不符: %+v", q0)
	}
	if q1 := qn.Questions[1]; !q1.Multiple || q1.Label != "Q2" || q1.Index != 2 {
		t.Fatalf("第 2 题字段不符(multi/label/序号): %+v", q1)
	}
	res := decodeResult(t, out)
	if res["summary"] == "" || res["title"] != "本轮待确认" {
		t.Fatalf("结果应带摘要与标题: %+v", res)
	}
	answers, _ := res["answers"].([]any)
	if len(answers) != 2 {
		t.Fatalf("作答应逐题保留位次: %+v", answers)
	}
	first, _ := answers[0].(map[string]any)
	if first["index"].(float64) != 1 || first["label"] != "Scope" {
		t.Fatalf("作答应带序号与标签: %+v", first)
	}
}

// TestQuestionnaireChat 「聊聊这个」:结果带 chat + 明确的"别重发整卷"提示。
func TestQuestionnaireChat(t *testing.T) {
	tool, q := buildQn(t)
	q.ansQn = sdk.QuestionnaireAnswer{Answers: []sdk.QuestionAnswer{{Values: []string{"web"}}, {}}, Chat: "prio"}
	out, err := tool.Execute(context.Background(), `{"questions":[
		{"id":"scope","label":"Scope","prompt":"范围?","options":[{"value":"web"}]},
		{"id":"prio","label":"优先级","prompt":"先做哪个?","options":[{"value":"p"}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	res := decodeResult(t, out)
	if res["chat"] != "prio" {
		t.Fatalf("应回传 chat 题 id: %+v", res)
	}
	note, _ := res["note"].(string)
	if !strings.Contains(note, "优先级") || !strings.Contains(note, "不要重发") {
		t.Fatalf("chat 提示应指出题目标签且要求转对话: %q", note)
	}
}

// TestQuestionnaireCancelled 整卷取消:结果显式标 cancelled + 提示。
func TestQuestionnaireCancelled(t *testing.T) {
	tool, q := buildQn(t)
	q.ansQn = sdk.QuestionnaireAnswer{Answers: []sdk.QuestionAnswer{{}, {}}, Cancelled: true}
	out, err := tool.Execute(context.Background(), `{"questions":[{"prompt":"a","options":[{"value":"x"}]},{"prompt":"b"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	res := decodeResult(t, out)
	if res["cancelled"] != true {
		t.Fatalf("应显式标取消: %+v", res)
	}
	if note, _ := res["note"].(string); !strings.Contains(note, "取消") {
		t.Fatalf("取消应给提示: %q", note)
	}
}

// TestQuestionnaireValidation 参数校验:互斥/重复 id/空题干/空选项 —— 全部显式报错。
func TestQuestionnaireValidation(t *testing.T) {
	tool, _ := buildQn(t)
	cases := []struct{ name, args, want string }{
		{"两形状同给", `{"prompt":"x","questions":[{"prompt":"y"}]}`, "互斥"},
		{"都不给", `{}`, "缺少"},
		{"重复 id", `{"questions":[{"id":"a","prompt":"x"},{"id":"a","prompt":"y"}]}`, "重复"},
		{"空题干", `{"questions":[{"prompt":"  "}]}`, "缺少 prompt"},
		{"空选项", `{"questions":[{"prompt":"x","options":[{}]}]}`, "缺少 value"},
	}
	for _, c := range cases {
		if _, err := tool.Execute(context.Background(), c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: 应报错含 %q, got %v", c.name, c.want, err)
		}
	}
}

// TestQuestionnaireFreeTextQuestion 无选项的题:自带输入 = 唯一作答路径,不打伪选项。
func TestQuestionnaireFreeTextQuestion(t *testing.T) {
	tool, q := buildQn(t)
	q.ansQn = sdk.QuestionnaireAnswer{Answers: []sdk.QuestionAnswer{{Text: "手写"}}}
	if _, err := tool.Execute(context.Background(), `{"questions":[{"prompt":"随便说说"}]}`); err != nil {
		t.Fatal(err)
	}
	got := q.gotQn.Questions[0]
	if got.AllowOther || !got.FreeText {
		t.Fatalf("无选项题应 Only 自由文本: %+v", got)
	}
}

// TestQuestionnaireFallbackToPlain 通道只有单题能力时:逐题降级仍能返回整卷结果。
func TestQuestionnaireFallbackToPlain(t *testing.T) {
	tools, plain, _ := build(t, true) // build 注入的桩只实现 QuestionService
	plain.ans = sdk.QuestionAnswer{Values: []string{"web"}}
	tool := tools.reg["ask_user_question"]
	out, err := tool.Execute(context.Background(), `{"questions":[
		{"id":"scope","label":"Scope","prompt":"范围?","options":[{"value":"web"}]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if plain.got.Prompt != "范围?" {
		t.Fatalf("降级应以单题问出: %+v", plain.got)
	}
	res := decodeResult(t, out)
	if res["summary"] == "" {
		t.Fatalf("降级也要组装整卷结果: %+v", res)
	}
}
