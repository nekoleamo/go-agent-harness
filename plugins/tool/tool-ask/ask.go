// Package toolask 提供 tool-ask 插件(P3 语义交互 seam):注册 ask_user_question 工具,
// 让模型在需要用户决策时提出结构化问题(单选/多选/自由文本)并等待回答。
// 进程内装配(同 tool-auto-plan 先例):问题经 ctx.question(host-confirm-fusion 提供,
// 广播 web/im/tui 渠道,首答生效)呈现;未装配提问通道时工具显式报错(不静默假答)。
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

// Definition 工具 schema(单选/多选/自由文本)。
func (t *Tool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: "ask_user_question",
		Description: "向用户提出结构化问题并等待回答(单选/多选/自由文本)。" +
			"仅用于需要用户决策、无法从上下文推断的场景(如选择方案/环境、确认参数);" +
			"不要用它做普通寒暄或可自行判断的事。options 省略则为自由文本提问。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{"type": "string", "description": "问题正文"},
				"options": map[string]any{
					"type":        "array",
					"description": "可选项(省略 = 自由文本提问)",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"value": map[string]any{"type": "string", "description": "返回值(机器可读的短值)"},
							"desc":  map[string]any{"type": "string", "description": "展示说明(可省)"},
						},
						"required": []string{"value"},
					},
				},
				"multiple":  map[string]any{"type": "boolean", "description": "是否多选"},
				"free_text": map[string]any{"type": "boolean", "description": "是否允许自由文本作答(有选项时也允许)"},
			},
			"required": []string{"prompt"},
		},
	}
}

// Execute 调用提问通道。
func (t *Tool) Execute(ctx context.Context, argsJSON string) (any, error) {
	var in struct {
		Prompt   string             `json:"prompt"`
		Options  []sdk.QuestionOption `json:"options"`
		Multiple bool               `json:"multiple"`
		FreeText bool               `json:"free_text"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return nil, fmt.Errorf("ask_user_question: 参数解析失败: %w", err)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return nil, fmt.Errorf("ask_user_question: prompt 不能为空")
	}
	var qs sdk.QuestionService
	if t.c == nil {
		return nil, fmt.Errorf("ask_user_question: 提问通道未装配")
	}
	if err := t.c.Inject("ctx.question", &qs); err != nil {
		return nil, fmt.Errorf("ask_user_question: 提问通道未装配(%v;需 host-confirm-fusion 与至少一个 UI 渠道)", err)
	}
	ans, err := qs.Ask(ctx, sdk.Question{
		Prompt:   in.Prompt,
		Options:  in.Options,
		Multiple: in.Multiple,
		FreeText: in.FreeText,
	})
	if err != nil {
		return nil, fmt.Errorf("ask_user_question: 提问失败: %w", err)
	}
	// 结构化结果回模型(JSON 便于解析)
	out, _ := json.Marshal(ans)
	return string(out), nil
}
