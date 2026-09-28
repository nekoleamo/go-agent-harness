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

	"github.com/nekoleamo/go-agent-harness/internal/prefs"
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

// pathAdjudication 解出路径裁决的**对象**:非代理工具 = 自身;代理工具(声明了 ProxyArgsParam,
// 如 search 模式 mcp_call) = 真实目标工具 + 其内层参数对象 —— 否则内层路径不经任何裁决
// (审批维度已按真实名匹配,路径维度此前没有,属同一类间接绕过;2026-09-27 审计 F2)。
// 返回 (裁决用工具名, 裁决用参数 JSON, 裁决用定义)。
func pathAdjudication(c sdk.Ctx, name, args string, def sdk.ToolDefinition) (string, string, sdk.ToolDefinition) {
	if def.ProxyArgsParam == "" {
		return name, args, def
	}
	var outer map[string]any
	if json.Unmarshal([]byte(args), &outer) != nil {
		return name, args, def
	}
	inner := innerArgsJSON(outer[def.ProxyArgsParam])
	if inner == "" {
		// 没有内层参数对象:仍按代理工具自身裁决(其 name 参数值不构成路径面)
		return name, args, def
	}
	target := approvalTarget(c, name, args)
	if target == "" {
		return name, inner, sdk.ToolDefinition{}
	}
	realDef, ok := toolDef(c, target)
	if !ok {
		// 真实定义取不到(名字错/未注册/已卸载):不猜声明,只留值级兜底(见 CheckToolCallAt);
		// 名字仍按真实名传 —— 值级兜底的读写意图来自工具名动词
		return target, inner, sdk.ToolDefinition{}
	}
	return target, inner, realDef
}

// innerArgsJSON 把代理工具的内层参数取成 JSON 对象串(*模型两种都爱用:对象或 JSON 字符串)。
func innerArgsJSON(raw any) string {
	switch v := raw.(type) {
	case map[string]any:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	case string:
		var m map[string]any
		if json.Unmarshal([]byte(v), &m) != nil {
			return ""
		}
		if b, err := json.Marshal(m); err == nil {
			return string(b)
		}
	}
	return ""
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
//	sync:           档位联动开关的**默认值**(默认 true:open → 沙箱有效 full-access;
//	                strict → 有效 read-only)。用户经 /sandbox sync on|off 或 Web 设置面板
//	                显式选择过时,以 prefs 里的用户选择为准(R10 ②-2:配置是默认、用户选择是覆盖)。
//	approval_tools: 需逐次审批的工具名列表(默认空;E-A 工具级审批。远程/破坏性副作用工具
//	                如远程发送/部署类副作用工具应入此表;支持列表或逗号/空白分隔字符串)
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	approvalMode, sandboxMode, sync := sdk.ApprovalSmart, sdk.SandboxWorkspace, true
	var approvalTools map[string]bool
	confirmTimeout := confirmTimeoutDefault
	if m != nil && m.Data != nil {
		if v, ok := m.Data["approval"].(string); ok && v != "" {
			approvalMode = sdk.ApprovalMode(v)
		}
		// 审批等待上限(秒):0/缺项 = 不限时(等到用户答复,见 approval.go 的默认说明)。
		if v, ok := m.Data["confirm_timeout_sec"]; ok {
			confirmTimeout = parseConfirmTimeout(v)
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

	// 用户显式选择覆盖配置默认(启动恢复;/sandbox sync 与设置面板写同一 prefs)。
	// 在这里读而不是在各端启动时读:headless/定时任务也走同一 Start,否则「关掉联动」
	// 这个安全相关选择会在无人值守场景静默失效。
	if v := prefs.Load().SandboxSync; v != nil {
		sync = *v
	}

	var confirm sdk.ConfirmService
	_ = c.Inject("ctx.confirm", &confirm) // 未装配:smart 档按无通道安全拒绝

	ap := &ApprovalPolicy{mode: approvalMode, tools: approvalTools, confirmTimeout: confirmTimeout}
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
		// S-P1-4:本次调用的工作根(隔离子代理 = 受管 worktree)。pre-execute 早于宿主执行入口
		// 注入 SandboxHint,所以这里从 ctx 直接读覆盖值;写范围/相对路径基准以它为准,
		// 否则子代理的写会被按主 workspace 裁决(要么误拒、要么落错地方)。
		callRoot := ""
		if dir, ok := sdk.WorkRootOf(ctx); ok {
			callRoot = dir
		}
		if call.Name == "shell" {
			// 解出真实命令文本再判定:JSON 转义(`\u0072m`)与解释器删除等绕过在此收敛
			cmd := shellCommand(call.Arguments)
			pattern, hit := matchDangerous(cmd)
			if !hit {
				// B2(2026-09-27):枚举漏网写法(`>> /etc/hosts`、`> ~/.zshrc`、`mv x /etc/y`)
				// 由“写目标落在受保护位置”派生补充(同一份写目标解析,不重复枚举)
				pattern, hit = derivedApprovalTarget(cmd)
			}
			if hit {
				if err := ap.check(ctx, confirmOf(), pattern, cmd); err != nil {
					return err
				}
			}
			// 路径裁决(R10 ①,顺序在审批之后、与 file_* 一致):显式写目标必须落在档位允许范围
			// 内。审批通过不代表放开档位(workspace-write 下 `rm -rf /tmp/x` 即便人工同意仍被拒),
			// 需要放开请显式切 /sandbox full。
			if err := sp.CheckShellCommandAt(callRoot, cmd); err != nil {
				return err
			}
		}
		// 工具级审批(E-A):data.approval_tools 列出的工具每次调用都按同一三档语义
		// 裁决(与 shell 命令模式相互独立,可同时命中;默认空 = 行为零变化)。
		// 代理工具(声明了 ApprovalTargetParam,如 search 模式的 mcp_call)按其**真实目标名**
		// 匹配:否则按单工具名写的规则会被一个间接名整体绕过(NOND-M1-3b)。
		target := approvalTarget(c, call.Name, call.Arguments)
		def, _ := toolDef(c, call.Name)
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
		// A7(2026-09-27 安全审计):网页抓取的**出口审批**(域名级 TOFU)。位置在工具级审批之后、
		// 路径裁决之前 —— 它决定的是"访问哪个域名",与文件路径无关;非抓取工具直接放行。
		if err := checkWebToolFetch(ctx, confirmOf(), call.Name, call.Arguments); err != nil {
			return err
		}
		// 宿主侧路径裁决(P0):默认发行态 file_* 由外部插件进程提供(sb 未注入),
		// 仅靠工具侧沙箱会完全失效 —— 这里按工具名+参数统一裁决(插件零改动)。
		// 依据 = 工具自述声明 → 内置名表 → 按定义推断 → 值级兜底(见 CheckToolCallAt);
		// 代理工具按**真实目标工具**裁决其内层参数(见 pathAdjudication)。
		pname, pargs, pdef := pathAdjudication(c, call.Name, call.Arguments, def)
		if err := sp.CheckToolCallAt(callRoot, pname, pargs, pdef); err != nil {
			return err
		}
		// 指令面写审批(S1/S2):角色/技能/全局 AGENTS.md 是“模型接下来要遵守的规则”,
		// 被不可信内容改写就是持久提权。位置在路径裁决**之后**:先保证档位允许写,
		// 再按审批档问“要不要人拍板”(open 放行 / smart 确认 / strict 拒结)。
		return checkInstructionFaceWrite(ctx, ap, confirmOf(), pname, pargs, pdef)
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
