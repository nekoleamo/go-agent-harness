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

	"github.com/nekoleamo/go-agent-harness/sdk"
)

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
//	                如 im_send 应入此表;支持列表或逗号/空白分隔字符串)
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
		// 确认通道每次现取(而非 Start 一次性注入):ctx.confirm 由 UI/IM 插件
		// Provide 且与本插件无拓扑顺序约束(可能后于本插件启动)——一次性注入会恒 nil,
		// 使 smart 档永远“无通道拒绝”。未装配 = nil → 安全拒绝(语义不变)。
		confirmOf := func() sdk.ConfirmService {
			var cf sdk.ConfirmService
			_ = c.Inject("ctx.confirm", &cf)
			return cf
		}
		if call.Name == "shell" {
			if pattern, hit := matchDangerous(call.Arguments); hit {
				if err := ap.check(ctx, confirmOf(), pattern); err != nil {
					return err
				}
			}
		}
		// 工具级审批(E-A):data.approval_tools 列出的工具每次调用都按同一三档语义
		// 裁决(与 shell 命令模式相互独立,可同时命中;默认空 = 行为零变化)。
		if ap.RequiresToolApproval(call.Name) {
			if err := ap.checkTool(ctx, confirmOf(), call.Name, call.Arguments); err != nil {
				return err
			}
		}
		return sp.CheckTool(call.Name)
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
