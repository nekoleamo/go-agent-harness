// Package policyguard 提供 policy-guard 统一策略插件。
// 由 policy-approval(审批档位)+ policy-sandbox(沙箱档位)融合而来:
//   对外契约不变——仍 Provide ctx.approval / ctx.sandbox 两个服务,
//   仍响应 tools/pre-execute 与 cwd/workspace-switched 事件,
//   /approval /sandbox 命令、TUI/Web 展示、prefs 持久化全部零改动;
//   对内统一——单一 pre-execute 裁决点(行为顺序可控),并新增
//   data.sync 档位联动(approval 为权威档位,驱动沙箱有效行为)。
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
//   approval: open|smart|strict(默认 smart,原 policy-approval mode)
//   sandbox:  read-only|workspace-write|full-access(默认 workspace-write,原 policy-sandbox mode)
//   sync:     档位联动开关(默认 true:open → 沙箱有效 full-access;strict → 有效 read-only)
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	approvalMode, sandboxMode, sync := sdk.ApprovalSmart, sdk.SandboxWorkspace, true
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
	}

	var confirm sdk.ConfirmService
	_ = c.Inject("ctx.confirm", &confirm) // 未装配:smart 档按无通道安全拒绝

	ap := &ApprovalPolicy{mode: approvalMode}
	sp := &SandboxPolicy{root: workspaceRoot(), mode: sandboxMode, sync: sync, approval: ap.Mode}
	if err := c.Provide("ctx.approval", ap); err != nil {
		return nil, err
	}
	if err := c.Provide("ctx.sandbox", sp); err != nil {
		return nil, err
	}

	// 单一 pre-execute 裁决点:先审批(危险命令按档),再沙箱(工具按有效档)——
	// 消除原两插件各自订阅的叠加不确定性,顺序固定、结果可预测。
	d := c.Subscribe("tools/pre-execute", func(ctx context.Context, ev *sdk.Event) error {
		call, ok := ev.Payload.(*sdk.ToolCallEvent)
		if !ok {
			return nil
		}
		if call.Name == "shell" {
			if pattern, hit := matchDangerous(call.Arguments); hit {
				if err := ap.check(ctx, confirm, pattern); err != nil {
					return err
				}
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
