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
// 子代理上下文隔离(仅结论进父级)。
// T2 范围:后台会话控制面——spawn(task) 后台启动带句柄(不阻塞当前回合),
// agents()/agent_status(id)/agent_kill(id) 查询与终止;fork(带父上下文后台启动)、
// send_message(向运行中的子代理注入消息,回复经 agent_status 的 messages 可读)。
package toolsubagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Tool 实现 sdk.Tool(单 schema 多 action;T1 delegate 同步,T2 加后台控制面)。
type Tool struct {
	fanout sdk.FanoutService // 可为 nil:宿主 ctx.fanout 未装配时显式报错
}

// NewTool 工厂:fanout nil → 调用报"宿主未装配 ctx.fanout"。
func NewTool(fanout sdk.FanoutService) sdk.Tool {
	return &Tool{fanout: fanout}
}

// NewToolFn 测试注入:仅 delegate 同步路径(后台控制面动作将报未装配 fanout)。
func NewToolFn(fn func(ctx context.Context, input string) (string, error)) sdk.Tool {
	return &Tool{fanout: &fnOnly{agent: fn}}
}

// fnOnly 只具 delegate 能力的 fanout 壳(单测隔离控制面依赖)。
type fnOnly struct {
	agent func(ctx context.Context, input string) (string, error)
}

func (o *fnOnly) Agent(ctx context.Context, input string) (string, error) { return o.agent(ctx, input) }
func (o *fnOnly) Parallel(context.Context, []string) []sdk.FanoutResult   { return nil }
func (o *fnOnly) Pipeline(context.Context, []string) ([]sdk.FanoutResult, string, error) {
	return nil, "", fmt.Errorf("未装配 fanout")
}
func (o *fnOnly) SpawnAgent(context.Context, string) (string, error) {
	return "", fmt.Errorf("未装配 fanout")
}
func (o *fnOnly) Fork(context.Context, string) (string, error) {
	return "", fmt.Errorf("未装配 fanout")
}
func (o *fnOnly) SendMessage(string, string) error           { return fmt.Errorf("未装配 fanout") }
func (o *fnOnly) ListAgents() []sdk.AgentHandle              { return nil }
func (o *fnOnly) AgentStatus(string) (sdk.AgentHandle, bool) { return sdk.AgentHandle{}, false }
func (o *fnOnly) KillAgent(string) error                     { return fmt.Errorf("未装配 fanout") }

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
			"delegate(task 必填:委派目标与验收口径)→ one-shot 同步等子代理完成,返回其最终回复文本;" +
			"适合可并行拆给独立上下文的子任务(独立实现/独立评审/独立调研);轻量单步直接自己做,不必委派。" +
			"spawn(task)→ 后台启动子代理(不阻塞当前回合),返回 agent_id 句柄——长任务/需并行推进时用;" +
			"agents()→ 全部后台会话(状态 running/done/failed/killed);agent_status(agent_id)→ 单个状态与结果;" +
			"fork(task)→ 带父上下文的子代理:继承当前会话已发生的历史 + task,后台启动(句柄同 spawn);" +
			"agents()→ 全部后台会话(状态 running/done/failed/killed);agent_status(agent_id)→ 单个状态与" +
			"结果,含 send_message 对话记录(messages);agent_kill(agent_id)→ 终止运行中的后台子代理;" +
			"send_message(agent_id, message)→ 向运行中的子代理注入消息(追加为输入,子代理继续执行并回复)。" +
			"isolate=\"worktree\"(可配 delegate/spawn/fork)→ **隔离运行**:子代理在独立 git worktree 里工作" +
			"(自有目录与分支),改动不落主工作区 —— 多子代理并行改同一批文件时用;回包含 worktree 路径/分支。" +
			"非 git 仓库或未启用 host-worktrees 会**显式报错**(不会静默退化为在主工作区执行)。",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []any{"action"},
			"properties": map[string]any{
				"action":   map[string]any{"type": "string", "enum": []string{"delegate", "spawn", "fork", "agents", "agent_status", "agent_kill", "send_message"}},
				"task":     map[string]any{"type": "string", "description": "delegate/spawn/fork:委派目标(含验收口径/输入输出约束)"},
				"agent_id": map[string]any{"type": "string", "description": "agent_status/agent_kill/send_message:后台会话句柄"},
				"message":  map[string]any{"type": "string", "description": "send_message:注入给运行中子代理的消息内容"},
				"isolate": map[string]any{"type": "string", "description": "隔离运行:仅支持 \"worktree\"(可配 delegate/spawn/fork)—— 子代理在独立 git worktree " +
					"里工作(自有目录与分支),改动不落主工作区;适合多子代理并行改同一批文件的场景。" +
					"非 git 仓库/未启用 host-worktrees 时显式报错(不会静默退化为非隔离);worktree 默认保留," +
					"结果与句柄都回传路径(需合并/丢弃时用 /worktree 查看或回收)"},
			},
		},
	}
}

// args 统一入参。
type args struct {
	Action  string `json:"action"`
	Task    string `json:"task"`
	AgentID string `json:"agent_id"`
	Message string `json:"message"`
	Isolate string `json:"isolate"`
}

// isolateKind 唯一的隔离模式(其余值显式报错,防“拼错就静默不隔离”)。
const isolateKind = "worktree"

// Execute 执行 action;业务失败以 map{"error":...} 回传(不中断 turn)。
func (t *Tool) Execute(ctx context.Context, raw string) (any, error) {
	var a args
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("subagent: args: %w", err)
	}
	if t.fanout == nil {
		return map[string]any{"error": "subagent: ctx.fanout 未装配(host-fanout),子代理不可用"}, nil
	}
	iso := strings.TrimSpace(a.Isolate)
	if iso != "" && iso != isolateKind {
		return map[string]any{"error": fmt.Sprintf("subagent: isolate 仅支持 %q(收到 %q)", isolateKind, iso)}, nil
	}
	switch a.Action {
	case "delegate":
		if strings.TrimSpace(a.Task) == "" {
			return map[string]any{"error": "subagent: delegate 需 task(委派目标)"}, nil
		}
		if iso != "" {
			res, err := t.runIsolated(ctx, sdk.WorktreeRun{Input: a.Task, Sync: true})
			if err != nil {
				return map[string]any{"error": "subagent: 隔离委派失败: " + err.Error()}, nil
			}
			return isolatedResult(res, res.Text), nil
		}
		out, err := t.fanout.Agent(ctx, strings.TrimSpace(a.Task))
		if err != nil {
			return map[string]any{"error": "subagent: 委派失败: " + err.Error()}, nil
		}
		return map[string]any{"result": out}, nil
	case "spawn":
		if strings.TrimSpace(a.Task) == "" {
			return map[string]any{"error": "subagent: spawn 需 task(委派目标)"}, nil
		}
		if iso != "" {
			res, err := t.runIsolated(ctx, sdk.WorktreeRun{Input: a.Task})
			if err != nil {
				return map[string]any{"error": "subagent: 隔离启动失败: " + err.Error()}, nil
			}
			return isolatedResult(res, ""), nil
		}
		id, err := t.fanout.SpawnAgent(ctx, strings.TrimSpace(a.Task))
		if err != nil {
			return map[string]any{"error": "subagent: 后台启动失败: " + err.Error()}, nil
		}
		return map[string]any{"agent_id": id, "state": "running"}, nil
	case "fork":
		if strings.TrimSpace(a.Task) == "" {
			return map[string]any{"error": "subagent: fork 需 task(委派目标)"}, nil
		}
		if iso != "" {
			res, err := t.runIsolated(ctx, sdk.WorktreeRun{Input: a.Task, Fork: true})
			if err != nil {
				return map[string]any{"error": "subagent: 隔离 fork 失败: " + err.Error()}, nil
			}
			return isolatedResult(res, ""), nil
		}
		id, err := t.fanout.Fork(ctx, strings.TrimSpace(a.Task))
		if err != nil {
			return map[string]any{"error": "subagent: fork 失败: " + err.Error()}, nil
		}
		return map[string]any{"agent_id": id, "state": "running"}, nil
	case "send_message":
		if iso != "" {
			return map[string]any{"error": "subagent: isolate 仅适用于 delegate/spawn/fork"}, nil
		}
		if strings.TrimSpace(a.AgentID) == "" {
			return map[string]any{"error": "subagent: send_message 需 agent_id"}, nil
		}
		if strings.TrimSpace(a.Message) == "" {
			return map[string]any{"error": "subagent: send_message 需 message(注入内容)"}, nil
		}
		if err := t.fanout.SendMessage(strings.TrimSpace(a.AgentID), a.Message); err != nil {
			return map[string]any{"error": "subagent: 注入失败: " + err.Error()}, nil
		}
		return map[string]any{"sent": a.AgentID, "message": a.Message}, nil
	case "agents":
		if iso != "" {
			return map[string]any{"error": "subagent: isolate 仅适用于 delegate/spawn/fork"}, nil
		}
		return map[string]any{"agents": t.fanout.ListAgents()}, nil
	case "agent_status":
		if iso != "" {
			return map[string]any{"error": "subagent: isolate 仅适用于 delegate/spawn/fork"}, nil
		}
		if strings.TrimSpace(a.AgentID) == "" {
			return map[string]any{"error": "subagent: agent_status 需 agent_id"}, nil
		}
		h, ok := t.fanout.AgentStatus(strings.TrimSpace(a.AgentID))
		if !ok {
			return map[string]any{"error": fmt.Sprintf("subagent: 无此会话 %q", a.AgentID)}, nil
		}
		return h, nil
	case "agent_kill":
		if iso != "" {
			return map[string]any{"error": "subagent: isolate 仅适用于 delegate/spawn/fork"}, nil
		}
		if strings.TrimSpace(a.AgentID) == "" {
			return map[string]any{"error": "subagent: agent_kill 需 agent_id"}, nil
		}
		if err := t.fanout.KillAgent(strings.TrimSpace(a.AgentID)); err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		return map[string]any{"killed": a.AgentID}, nil
	default:
		return map[string]any{"error": fmt.Sprintf("subagent: 未知 action %q(delegate|spawn|fork|agents|agent_status|agent_kill|send_message)", a.Action)}, nil
	}
}

// runIsolated 隔离运行(worktree):宿主 fanout 未实现 IsolatedFanout → 显式报错,
// **不得静默退化为非隔离**(否则模型以为改在隔离区、实际改了主工作区)。
func (t *Tool) runIsolated(ctx context.Context, req sdk.WorktreeRun) (sdk.WorktreeRunResult, error) {
	iso, ok := t.fanout.(sdk.IsolatedFanout)
	if !ok {
		return sdk.WorktreeRunResult{}, fmt.Errorf("宿主 fanout 不支持 worktree 隔离(IsolatedFanout 未实现;需 host-fanout + host-worktrees)")
	}
	if req.Label == "" {
		req.Label = worktreeLabel(req.Input)
	}
	return iso.RunInWorktree(ctx, req)
}

// worktreeLabel worktree 目录名的任务标识(取任务首行的前几个词,便于 /worktree list 辨认)。
func worktreeLabel(task string) string {
	line := strings.TrimSpace(task)
	if i := strings.IndexAny(line, "\n。."); i > 0 {
		line = line[:i]
	}
	r := []rune(strings.TrimSpace(line))
	if len(r) > 12 {
		r = r[:12]
	}
	return string(r)
}

// isolatedResult 隔离运行回包:worktree 路径/分支必须回传(父级据此合并或回收)。
func isolatedResult(res sdk.WorktreeRunResult, text string) map[string]any {
	out := map[string]any{
		"worktree":    res.Worktree.Path,
		"branch":      res.Worktree.Branch,
		"base":        res.Worktree.Base,
		"worktree_id": res.Worktree.ID,
		"isolated":    true,
	}
	if res.Handle.ID != "" {
		out["agent_id"] = res.Handle.ID
		out["state"] = "running"
	}
	if text != "" {
		out["result"] = text
	}
	return out
}
