// Package policyguard 提供 policy-guard 统一策略插件。
// 由 policy-approval(审批档位)+ policy-sandbox(沙箱档位)融合而来:
//
//	对外契约不变——仍 Provide ctx.approval / ctx.sandbox 两个服务,
//	仍响应 tools/pre-execute 与 cwd/workspace-switched 事件,
//	/approval /sandbox 命令、TUI/Web 展示、prefs 持久化全部零改动;
//	对内统一——单一 pre-execute 裁决点(行为顺序可控),并新增
//	data.sync 档位联动(approval 为权威档位,驱动沙箱有效行为)。
package policyguard

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// toolDef 取工具自述定义(ctx.tools 每次现取,同 ctx.confirm:
// host-tools 与本插件无拓扑顺序约束,一次性注入会恒 nil)。
func toolDef(c sdk.Ctx, name string) (sdk.ToolDefinition, bool) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil || tools == nil {
		return sdk.ToolDefinition{}, false
	}
	return tools.Get(name)
}

// declaredPathParams 取工具自述的路径参数声明。
func declaredPathParams(c sdk.Ctx, name string) []sdk.PathParam {
	def, ok := toolDef(c, name)
	if !ok {
		return nil
	}
	return def.PathParams
}

// approvalTarget 代理工具（声明了 ApprovalTargetParam）的真实目标名：
// 如 MCP 检索模式的 `mcp_call{name:"mcp_srv_read"}` → 返回被代理的真实工具名。
// 未声明/取不到 → 空串（视为无代理层，直接按工具自身名匹配）。
func approvalTarget(c sdk.Ctx, name, args string) string {
	def, ok := toolDef(c, name)
	if !ok || def.ApprovalTargetParam == "" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) != nil {
		return ""
	}
	s, _ := m[def.ApprovalTargetParam].(string)
	return strings.TrimSpace(s)
}

// Plugin 实现 policy-guard。
type Plugin struct{}

func (p *Plugin) Name() string { return "policy-guard" }

// Start 装配审批 + 沙箱两个策略器,统一挂 pre-execute / workspace-switched 订阅。
// manifest data:
//
//	approval:       open|smart|strict(默认 smart,原 policy-approval mode)
//	sandbox:        read-only|workspace-write|full-access(默认 workspace-write,原 policy-sandbox mode)
//	sync:           档位联动开关(默认 true:open → 沙箱有效 full-access;strict → 有效 read-only)
//	approval_tools: 需逐次审批的工具名列表(默认空;E-A 工具级审批。远程/破坏性副作用工具
//	                如远程发送/部署类副作用工具应入此表;支持列表或逗号/空白分隔字符串)
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	approvalMode, sandboxMode, sync := sdk.ApprovalSmart, sdk.SandboxWorkspace, true
	var approvalTools map[string]bool
	if m != nil && m.Data != nil {
		if v, ok := m.Data["approval"].(string); ok && v != "" {
			approvalMode = sdk.ApprovalMode(v)
		}
		if v, ok := m.Data["sandbox"].(string); ok && v != "" {
			sandboxMode = sdk.SandboxMode(v)
		}
		if v, ok := m.Data["sync"].(bool); ok {
			sync = v
		}
		if v, ok := m.Data["approval_tools"]; ok {
			approvalTools = parseApprovalTools(v)
		}
	}

	var confirm sdk.ConfirmService
	_ = c.Inject("ctx.confirm", &confirm) // 未装配:smart 档按无通道安全拒绝

	ap := &ApprovalPolicy{mode: approvalMode, tools: approvalTools}
	sp := &SandboxPolicy{root: workspaceRoot(), mode: sandboxMode, sync: sync, approval: ap.Mode}
	if err := c.Provide("ctx.approval", ap); err != nil {
		return nil, err
	}
	if err := c.Provide("ctx.sandbox", sp); err != nil {
		return nil, err
	}

	// 单一 pre-execute 裁决点:先审批(危险命令按档 + 工具级名单),再沙箱(工具按有效档)——
	// 消除原两插件各自订阅的叠加不确定性,顺序固定、结果可预测。
	d := c.Subscribe("tools/pre-execute", func(ctx context.Context, ev *sdk.Event) error {
		call, ok := ev.Payload.(*sdk.ToolCallEvent)
		if !ok {
			return nil
		}
		// 确认通道每次现取(而非 Start 一次性注入):ctx.confirm 由 UI 插件
		// Provide 且与本插件无拓扑顺序约束(可能后于本插件启动)——一次性注入会恒 nil,
		// 使 smart 档永远“无通道拒绝”。未装配 = nil → 安全拒绝(语义不变)。
		confirmOf := func() sdk.ConfirmService {
			var cf sdk.ConfirmService
			_ = c.Inject("ctx.confirm", &cf)
			return cf
		}
		if call.Name == "shell" {
			// 解出真实命令文本再判定:JSON 转义(`\u0072m`)与解释器删除等绕过在此收敛
			cmd := shellCommand(call.Arguments)
			if pattern, hit := matchDangerous(cmd); hit {
				if err := ap.check(ctx, confirmOf(), pattern, cmd); err != nil {
					return err
				}
			}
			// 路径裁决(R10 ①,顺序在审批之后、与 file_* 一致):显式写目标必须落在档位允许范围
			// 内。审批通过不代表放开档位(workspace-write 下 `rm -rf /tmp/x` 即便人工同意仍被拒),
			// 需要放开请显式切 /sandbox full。
			if err := sp.CheckShellCommand(cmd); err != nil {
				return err
			}
		}
		// 工具级审批(E-A):data.approval_tools 列出的工具每次调用都按同一三档语义
		// 裁决(与 shell 命令模式相互独立,可同时命中;默认空 = 行为零变化)。
		// 代理工具(声明了 ApprovalTargetParam,如 search 模式的 mcp_call)按其**真实目标名**
		// 匹配:否则按单工具名写的规则会被一个间接名整体绕过(NOND-M1-3b)。
		target := approvalTarget(c, call.Name, call.Arguments)
		if ap.RequiresToolApproval(call.Name) || (target != "" && ap.RequiresToolApproval(target)) {
			subj := call.Name
			if target != "" {
				subj = call.Name + " → " + target
			}
			if err := ap.checkTool(ctx, confirmOf(), subj, call.Arguments); err != nil {
				return err
			}
		}
		if err := sp.CheckTool(call.Name); err != nil {
			return err
		}
		// 宿主侧路径裁决(P0):默认发行态 file_* 由外部插件进程提供(sb 未注入),
		// 仅靠工具侧沙箱会完全失效 —— 这里按工具名+参数统一裁决(插件零改动)。
		// 若工具自述了路径参数(sdk.ToolDefinition.PathParams),以声明为准。
		return sp.CheckToolCall(call.Name, call.Arguments, declaredPathParams(c, call.Name))
	})
	// 工作区切换:沙箱 root 同步(host-cwd-sessions 广播,与原 policy-sandbox 一致)
	d2 := c.Subscribe("cwd/workspace-switched", func(ctx context.Context, ev *sdk.Event) error {
		if dir, ok := ev.Payload.(string); ok {
			sp.SetRoot(dir)
		}
		return nil
	})
	return func() { d(); d2() }, nil
}
