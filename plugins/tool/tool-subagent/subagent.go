// Package toolsubagent 提供 tool-subagent 插件(M9.1):子代理委派工具面。
// 引擎复用声明(零新引擎、零宿主改动):宿主已有 host-fanout(ctx.fanout:
// agent/parallel/pipeline,独立子代理上下文、子会话隔离、仅结论回流父级),
// 且 GAH_CB_ADDR 回调通道已具备 fanout.agent(M6.9)。本插件只增模型面向工具面。
//
// 两消费面共存:tool-workflow(starlark 编排,程序化批量)与 tool-subagent
// (自然语言委派,自主型)共享同一 fanout seam;外部进程独立入口经回调通道驱动
// (崩溃隔离,与 workflow 语义聚类)。
//
// T1 范围:one-shot 同步委派 delegate(task) → 等子代理完成取回最终文本;
// 子代理上下文隔离(仅结论进父级)。控制组/背景带手柄归 M9.2。
package toolsubagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// agentCaller 单子代理执行函数(注入点:进程内 = ctx.fanout.Agent,
// 外部进程 = 宿主回调 fanout.agent;单测注入 stub)。窄接口避免持有全部 FanoutService。
type agentCaller func(ctx context.Context, input string) (string, error)

// Tool 实现 sdk.Tool(单 schema 多 action;T1 仅 delegate)。
type Tool struct {
	agent agentCaller // 可为 nil:宿主 ctx.fanout 未装配时 delegate 显式报错
}

// NewTool 工厂:fanout nil → delegate 报"宿主未装配 ctx.fanout"。
func NewTool(fanout sdk.FanoutService) sdk.Tool {
	if fanout == nil {
		return &Tool{agent: nil}
	}
	return &Tool{agent: fanout.Agent}
}

// NewToolFn 测试注入:直接给执行函数。
func NewToolFn(fn func(ctx context.Context, input string) (string, error)) sdk.Tool {
	return &Tool{agent: fn}
}

// Plugin 实现 tool-subagent(双轨:装配进宿主时经 ctx.tools 注册)。
type Plugin struct{}

func (p *Plugin) Name() string { return "tool-subagent" }

// Start 注册 subagent 工具(经 ctx.fanout 驱动;未装配显式报错对齐 tool-workflow)。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	var fanout sdk.FanoutService
	if err := c.Inject("ctx.fanout", &fanout); err != nil {
		return nil, err
	}
	return tools.Register(NewTool(fanout)), nil
}

func (t *Tool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: "subagent",
		Description: "委派一个独立子代理执行多步/独立任务(引擎=宿主 ctx.fanout;子代理上下文隔离," +
			"仅结论回流父级,不占父上下文)。action:" +
			"delegate(task 必填:委派目标与验收口径)→ one-shot 同步等子代理完成,返回其最终回复文本。" +
			"适合可并行拆给独立上下文的子任务(独立实现/独立评审/独立调研);轻量单步直接自己做,不必委派。" +
			"持续交互/中断/后台由控制组提供(本版无)。",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"action"},
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"delegate"}},
				"task":   map[string]any{"type": "string", "description": "delegate:委派目标(含验收口径/输入输出约束)"},
			},
		},
	}
}

// args 统一入参。
type args struct {
	Action string `json:"action"`
	Task   string `json:"task"`
}

// Execute 执行 delegate;业务失败以 map{"error":...} 回传(不中断 turn)。
func (t *Tool) Execute(ctx context.Context, raw string) (any, error) {
	var a args
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("subagent: args: %w", err)
	}
	switch a.Action {
	case "delegate":
		if strings.TrimSpace(a.Task) == "" {
			return map[string]any{"error": "subagent: delegate 需 task(委派目标)"}, nil
		}
		if t.agent == nil {
			return map[string]any{"error": "subagent: ctx.fanout 未装配(host-fanout),子代理不可用"}, nil
		}
		out, err := t.agent(ctx, strings.TrimSpace(a.Task))
		if err != nil {
			return map[string]any{"error": "subagent: 委派失败: " + err.Error()}, nil
		}
		return map[string]any{"result": out}, nil
	default:
		return map[string]any{"error": fmt.Sprintf("subagent: 未知 action %q(delegate)", a.Action)}, nil
	}
}
