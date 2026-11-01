// Package toolask 提供 tool-ask 插件(P3 语义交互 seam):注册 ask_user_question 工具,
// 让模型在需要用户决策时提出结构化问题(单选/多选/自由文本)并等待回答。
// 进程内装配(同 tool-auto-plan 先例):问题经 ctx.question(host-confirm-fusion 提供,
// 广播 web/tui 渠道,首答生效)呈现;未装配提问通道时工具显式报错(不静默假答)。
//
// 两种形状(同一工具,互斥):
//   - **单题**(老形状):prompt + options + multiple + free_text;
//   - **整卷**(questionnaire):questions[] + 可选 title,每题带 label/hint/
//     options[{value,label,description}]/multi_select/allow_other/allow_chat。
//     渠道支持整卷 ⇒ 一次问完(tab/确认页);否则 sdk 自动逐题降级(功能不丢)。
package toolask

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 tool-ask。
type Plugin struct {
	c sdk.Ctx
}

func (p *Plugin) Name() string { return "tool-ask" }

// Start 注册 ask_user_question 工具(ctx.question 运行期现取——未装配 fusion 的单 UI
// profile 下工具仍可注册,调用时给明确错误提示)。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	p.c = c
	return tools.Register(&Tool{c: c}), nil
}

// Tool ask_user_question 工具实现。
type Tool struct {
	c sdk.Ctx
}

// questionInput 整卷里的一题(模型侧 schema 形状;description 落到 sdk 的 Desc)。
type questionInput struct {
	ID          string        `json:"id"`
	Label       string        `json:"label"`
	Prompt      string        `json:"prompt"`
	Hint        string        `json:"hint"`
	Options     []optionInput `json:"options"`
	MultiSelect bool          `json:"multi_select"`
	AllowOther  *bool         `json:"allow_other"`
	AllowChat   *bool         `json:"allow_chat"`
}

// optionInput 一个选项:value 机器可读、label 展示、description 说明。
type optionInput struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Definition 工具 schema(单题快捷态与整卷两形状;互斥性在 Execute 校验)。
//
// 有意不写 required:老形状要求 prompt、整卷形状没有 prompt,一条 required 会把
// 整卷挡在 schema 校验外(模型看到的是"调用失败",而不是"参数不对")。
// 描述刻意写短:工具 schema 每轮都发,体积门在 tests/tool_schema_budget_test.go。
func (t *Tool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: "ask_user_question",
		Description: "向用户提结构化问题并等待回答。单题用 prompt+options;一次问清多件事用 questions[]" +
			"(每题 label 作 tab 短标签、hint 写代价/为什么问;选项 2-4 个,推荐项在 label 末尾标「(推荐)」)。" +
			"只用于无法从上下文推断的决策;用户选「聊聊这个」时转对话,不要重发整卷。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt":    map[string]any{"type": "string", "description": "单题:题干(与 questions 互斥)"},
				"options":   map[string]any{"type": "array", "description": "单题:选项", "items": optionSchema()},
				"multiple":  map[string]any{"type": "boolean", "description": "单题:多选"},
				"free_text": map[string]any{"type": "boolean", "description": "单题:允许自由文本"},
				"title":     map[string]any{"type": "string", "description": "整卷:标题"},
				"questions": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":           map[string]any{"type": "string", "description": "卷内唯一标识"},
							"label":        map[string]any{"type": "string", "description": "tab 短标签"},
							"prompt":       map[string]any{"type": "string", "description": "题干"},
							"hint":         map[string]any{"type": "string", "description": "题干下灰字(代价/为什么问)"},
							"options":      map[string]any{"type": "array", "items": optionSchema()},
							"multi_select": map[string]any{"type": "boolean", "description": "多选"},
							"allow_other":  map[string]any{"type": "boolean", "description": "给「自己输入」"},
							"allow_chat":   map[string]any{"type": "boolean", "description": "给「聊聊这个」"},
						},
					},
				},
			},
		},
	}
}

// optionSchema 选项的共享 schema(单题与整卷同形)。
func optionSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value":       map[string]any{"type": "string", "description": "返回值(短、机器可读)"},
			"label":       map[string]any{"type": "string", "description": "展示名(缺省 value)"},
			"description": map[string]any{"type": "string", "description": "灰字说明(可省)"},
		},
		"required": []string{"value"},
	}
}

// input 工具入参(两形状共用;互斥由 Execute 校验)。
type input struct {
	Prompt    string               `json:"prompt"`
	Options   []sdk.QuestionOption `json:"options"`
	Multiple  bool                 `json:"multiple"`
	FreeText  bool                 `json:"free_text"`
	Title     string               `json:"title"`
	Questions []questionInput      `json:"questions"`
}

// Execute 调用提问通道:单题走 Ask(与历史行为逐字一致),整卷走 AskQuestionnaire。
func (t *Tool) Execute(ctx context.Context, argsJSON string) (any, error) {
	var in input
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return nil, fmt.Errorf("ask_user_question: 参数解析失败: %w", err)
	}
	single := strings.TrimSpace(in.Prompt) != ""
	switch {
	case single && len(in.Questions) > 0:
		return nil, fmt.Errorf("ask_user_question: prompt 与 questions 互斥(单题给 prompt,整卷给 questions)")
	case !single && len(in.Questions) == 0:
		return nil, fmt.Errorf("ask_user_question: 缺少 prompt 或 questions")
	}
	var qs sdk.QuestionService
	if t.c == nil {
		return nil, fmt.Errorf("ask_user_question: 提问通道未装配")
	}
	if err := t.c.Inject("ctx.question", &qs); err != nil {
		return nil, fmt.Errorf("ask_user_question: 提问通道未装配(%v;需 host-confirm-fusion 与至少一个 UI 渠道)", err)
	}
	if single {
		return t.askSingle(ctx, qs, in)
	}
	return t.askQuestionnaire(ctx, qs, in)
}

// askSingle 单题路径(老形状,行为不变)。
func (t *Tool) askSingle(ctx context.Context, qs sdk.QuestionService, in input) (any, error) {
	ans, err := qs.Ask(ctx, sdk.Question{
		Prompt:   in.Prompt,
		Options:  in.Options,
		Multiple: in.Multiple,
		FreeText: in.FreeText,
	})
	if err != nil {
		return nil, fmt.Errorf("ask_user_question: 提问失败: %w", err)
	}
	out, _ := json.Marshal(ans)
	return string(out), nil
}

// askQuestionnaire 整卷路径:先校验(重复 id / 空题干 / 空选项),再交给 sdk 归一化+派发。
func (t *Tool) askQuestionnaire(ctx context.Context, qs sdk.QuestionService, in input) (any, error) {
	qn, err := buildQuestionnaire(in)
	if err != nil {
		return nil, err
	}
	ans, err := sdk.AskQuestionnaire(ctx, qs, qn)
	if err != nil {
		return nil, fmt.Errorf("ask_user_question: 整卷提问失败: %w", err)
	}
	out, _ := json.Marshal(buildResult(qn, ans))
	return string(out), nil
}

// buildQuestionnaire 把模型入参转成 sdk.Questionnaire(校验 + 默认值)。
//
// 默认值口径(与 pi 一致):allow_other / allow_chat 缺省 true(教育模型多给逃生口,
// 而不是让它每題都要显式声明);multi_select 缺省 false。
func buildQuestionnaire(in input) (sdk.Questionnaire, error) {
	qn := sdk.Questionnaire{Title: strings.TrimSpace(in.Title), Questions: make([]sdk.Question, 0, len(in.Questions))}
	seen := make(map[string]bool, len(in.Questions))
	for i, q := range in.Questions {
		prompt := strings.TrimSpace(q.Prompt)
		if prompt == "" {
			return sdk.Questionnaire{}, fmt.Errorf("ask_user_question: 第 %d 题缺少 prompt", i+1)
		}
		id := strings.TrimSpace(q.ID)
		if id != "" {
			if seen[id] {
				return sdk.Questionnaire{}, fmt.Errorf("ask_user_question: 卷内题 id 重复: %q", id)
			}
			seen[id] = true
		}
		opts := make([]sdk.QuestionOption, 0, len(q.Options))
		for j, o := range q.Options {
			value := strings.TrimSpace(o.Value)
			label := strings.TrimSpace(o.Label)
			if value == "" && label == "" {
				return sdk.Questionnaire{}, fmt.Errorf("ask_user_question: 第 %d 题第 %d 个选项缺少 value", i+1, j+1)
			}
			if value == "" {
				value = label // 模型只给 label 时用它当返回值(不静默丢选项)
			}
			opts = append(opts, sdk.QuestionOption{Value: value, Label: label, Desc: strings.TrimSpace(o.Description)})
		}
		qq := sdk.Question{
			ID:         id,
			Label:      strings.TrimSpace(q.Label),
			Prompt:     prompt,
			Hint:       strings.TrimSpace(q.Hint),
			Options:    opts,
			Multiple:   q.MultiSelect,
			AllowOther: boolOr(q.AllowOther, true),
			AllowChat:  boolOr(q.AllowChat, true),
		}
		// 纯自由文本题(无选项):自己输入 = 唯一作答路径,无需伪选项
		if len(opts) == 0 {
			qq.AllowOther = false
			qq.FreeText = true
		}
		qn.Questions = append(qn.Questions, qq)
	}
	return sdk.NormalizeQuestionnaire(qn), nil
}

// qnAnswerOut 单题作答的模型可见形状(与 sdk.QuestionAnswer 同名字段,便于模型对照入参)。
type qnAnswerOut struct {
	ID     string   `json:"id"`
	Label  string   `json:"label,omitempty"`
	Values []string `json:"values,omitempty"`
	Text   string   `json:"text,omitempty"`
	Custom bool     `json:"custom,omitempty"`
	Index  int      `json:"index,omitempty"`
}

// qnResultOut 整卷结果(模型直接读 JSON;summary 给人/日志看)。
type qnResultOut struct {
	Title     string        `json:"title,omitempty"`
	Cancelled bool          `json:"cancelled,omitempty"`
	Chat      string        `json:"chat,omitempty"`
	Note      string        `json:"note,omitempty"`
	Summary   string        `json:"summary"`
	Answers   []qnAnswerOut `json:"answers"`
}

// buildResult 组装回模型的整卷结果(顺序与题序一致;未答显式留空)。
func buildResult(qn sdk.Questionnaire, ans sdk.QuestionnaireAnswer) qnResultOut {
	out := qnResultOut{
		Title:     qn.Title,
		Cancelled: ans.Cancelled,
		Chat:      ans.Chat,
		Summary:   sdk.QuestionnaireSummary(qn, ans),
		Answers:   make([]qnAnswerOut, 0, len(qn.Questions)),
	}
	for i, q := range qn.Questions {
		var a sdk.QuestionAnswer
		if i < len(ans.Answers) {
			a = ans.Answers[i]
		}
		out.Answers = append(out.Answers, qnAnswerOut{
			ID: q.ID, Label: q.Label, Values: a.Values, Text: a.Text, Custom: a.Custom, Index: a.Index,
		})
	}
	switch {
	case ans.Chat != "":
		label := ans.Chat
		for _, q := range qn.Questions {
			if q.ID == ans.Chat && q.Label != "" {
				label = q.Label
				break
			}
		}
		out.Note = fmt.Sprintf("用户选择就「%s」展开讨论:不要重发整卷问卷,直接就该题继续对话(已收集的答案见 answers)。", label)
	case ans.Cancelled:
		out.Note = "用户取消了问卷,未作答任何题;需要时再问,不要重复连发。"
	}
	return out
}

// boolOr 取 *bool 值,缺省 fallback。
func boolOr(p *bool, fallback bool) bool {
	if p == nil {
		return fallback
	}
	return *p
}
