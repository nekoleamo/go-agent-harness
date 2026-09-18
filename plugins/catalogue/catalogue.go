// Package catalogue 全内置插件声明(单一事实源)。bundle 装配与 plugin-manager 共用。
// 对齐设计 §4.2:Manifest 提供 provides/requires 拓扑信息;bundle 按 enabled 过滤装配。
package catalogue

import (
	"github.com/nekoleamo/go-agent-harness/plugins/adapter/llm-anthropic-compat"
	"github.com/nekoleamo/go-agent-harness/plugins/adapter/llm-mock"
	"github.com/nekoleamo/go-agent-harness/plugins/adapter/llm-openai-compat"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-agent-loop"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-backup"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-commands"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-confirm-fusion"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-cwd-sessions"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-docview"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-fanout"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-internal-commands"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-jobs"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-llm"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-notices"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-plugin-manager"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-schedule"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-session-log"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-session-summary"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-skills"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-usage-stats"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-worktrees"
	"github.com/nekoleamo/go-agent-harness/plugins/host/token-compress"
	"github.com/nekoleamo/go-agent-harness/plugins/mcp/acp-server"
	"github.com/nekoleamo/go-agent-harness/plugins/mcp/mcp-bridge"
	"github.com/nekoleamo/go-agent-harness/plugins/mcp/mcp-server"
	"github.com/nekoleamo/go-agent-harness/plugins/policy/policy-guard"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-ask"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-auto-plan"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-doc"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-files"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-memory"
	toolsessionsearch "github.com/nekoleamo/go-agent-harness/plugins/tool/tool-session-search"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-shell"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-subagent"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-todo"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-web"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-workflow"
	"github.com/nekoleamo/go-agent-harness/plugins/ui/ui-tui-app"
	"github.com/nekoleamo/go-agent-harness/plugins/ui/ui-web-app"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Def 插件定义:工厂 + 清单 + 所属 bundle + 管理域声明。
type Def struct {
	Factory  sdk.Factory
	Manifest *sdk.Manifest
	Bundle   string // base | tui | web
	// Manage 声明管理域(展示层单一事实源,勿在 web/tui 另行硬编码):
	// external(已外部化,界面勿启停)| scenario(场景专用,界面勿启)| 空(常规,按运行态派生)。
	Manage string
}

// All 全部内置插件。
var All = map[string]Def{
	"host-session-log": {Factory: func() sdk.Plugin { return &sessionlog.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-session-log", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.sessions"}}, Bundle: "base"},
	"host-llm": {Factory: func() sdk.Plugin { return &hostllm.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-llm", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.llm"}}, Bundle: "base"},
	"host-tools": {Factory: func() sdk.Plugin { return &hosttools.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-tools", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.tools"}}, Bundle: "base"},
	"host-system-prompt": {Factory: func() sdk.Plugin { return &hostsystemprompt.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-system-prompt", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.systemPrompt"}}, Bundle: "base"},
	"host-skills": {Factory: func() sdk.Plugin { return &hostskills.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-skills", Type: "host", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools", "ctx.systemPrompt"}}, Bundle: "base"},
	"policy-guard": {Factory: func() sdk.Plugin { return &policyguard.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "policy-guard", Type: "policy", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.sandbox", "ctx.approval"}}, Bundle: "base"},
	"host-agent-loop": {Factory: func() sdk.Plugin { return &hostagentloop.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-agent-loop", Type: "agent", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.agentLoop", "ctx.turnControl"},
		Requires: []string{"ctx.sessions", "ctx.llm", "ctx.tools", "ctx.systemPrompt"}}, Bundle: "base"},
	"llm-openai-compat": {Factory: func() sdk.Plugin { return &llmopenai.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "llm-openai-compat", Type: "llm", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.llm"}}, Bundle: "base"},
	"llm-mock": {Factory: func() sdk.Plugin { return &llmmock.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "llm-mock", Type: "llm", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.llm"}}, Bundle: "base", Manage: "scenario"},
	"llm-anthropic-compat": {Factory: func() sdk.Plugin { return &llmanthropic.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "llm-anthropic-compat", Type: "llm", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.llm"}}, Bundle: "base"},
	"tool-workflow": {Factory: func() sdk.Plugin { return &toolworkflow.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-workflow", Type: "tool", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools", "ctx.fanout"}}, Bundle: "base", Manage: "external"},
	"tool-shell": {Factory: func() sdk.Plugin { return &toolshell.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-shell", Type: "tool", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}, Bundle: "base", Manage: "external"},
	"tool-subagent": {Factory: func() sdk.Plugin { return &toolsubagent.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-subagent", Type: "tool", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools", "ctx.fanout"}}, Bundle: "base", Manage: "external"},
	"tool-files": {Factory: func() sdk.Plugin { return &toolfiles.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-files", Type: "tool", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}, Bundle: "base", Manage: "external"},
	"tool-web": {Factory: func() sdk.Plugin { return &toolweb.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-web", Type: "tool", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}, Bundle: "base", Manage: "external"},
	"tool-memory": {Factory: func() sdk.Plugin { return &toolmemory.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-memory", Type: "tool", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}, Bundle: "base", Manage: "external"},
	"tool-doc": {Factory: func() sdk.Plugin { return &tooldoc.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-doc", Type: "tool", APIVersion: ">=1.0,<2.0",
		// D 组文档预览(D5):read_document/doc_open/doc_list 三工具;
		// 经 ctx.doc(host-docview)统一 resolver 读取,不绕沙箱
		Requires: []string{"ctx.tools", "ctx.doc"}}, Bundle: "base"},
	"tool-auto-plan": {Factory: func() sdk.Plugin { return &toolautoplan.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-auto-plan", Type: "tool", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools", "ctx.systemPrompt"}}, Bundle: "base"},
	"tool-ask": {Factory: func() sdk.Plugin { return &toolask.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-ask", Type: "tool", APIVersion: ">=1.0,<2.0",
		// P3 语义交互:ask_user_question 结构化提问(单选/多选/自由文本);
		// 进程内装配(提问通道 ctx.question 运行期现取,由 host-confirm-fusion 提供)
		Requires: []string{"ctx.tools"}}, Bundle: "base"},
	"tool-todo": {Factory: func() sdk.Plugin { return &tooltodo.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-todo", Type: "tool", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}, Bundle: "base", Manage: "external"},
	"tool-session-search": {Factory: func() sdk.Plugin { return &toolsessionsearch.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-session-search", Type: "tool", APIVersion: ">=1.0,<2.0",
		// S-P2-5:跨会话检索(session_search)。只读会话账本,不建索引、不写盘。
		// 与 tool-todo 同模式:内嵌实现默认停用(enabled: false),由 extplugins/tool-basic 提供。
		Requires: []string{"ctx.tools"}}, Bundle: "base", Manage: "external"},
	"mcp-bridge": {Factory: func() sdk.Plugin { return &mcpbridge.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "mcp-bridge", Type: "host", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}, Bundle: "base", Manage: "external"},
	"mcp-server": {Factory: func() sdk.Plugin { return &mcpserver.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "mcp-server", Type: "host", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}, Bundle: "base", Manage: "scenario"},
	"acp-server": {Factory: func() sdk.Plugin { return &acpserver.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "acp-server", Type: "host", APIVersion: ">=1.0,<2.0",
		// S-P2-3:ACP agent 端(编辑器集成,如 Zed)经 stdio 暴露会话/回合/工具进度/权限请求。
		// ctx.confirmFusion 是运行期必需(审批/提问的送达通道)但不在此硬声明:它属
		// confirm-fusion bundle,硬声明会把插件绑死到那个 bundle 归属;Start 里显式校验
		// 并给出可操作错误(缺 fusion 时审批一律被拒,必须让人看见原因)。
		Requires: []string{"ctx.agentLoop", "ctx.cwdSessions"}}, Bundle: "base", Manage: "scenario"},
	"token-compress": {Factory: func() sdk.Plugin { return &tokencompress.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "token-compress", Type: "host", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.sessions"}}, Bundle: "base"},
	"host-usage-stats": {Factory: func() sdk.Plugin { return &hostusagestats.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-usage-stats", Type: "host", APIVersion: ">=1.0,<2.0",
		// 独立统计:只订阅 session/event 广播(session/usage 事件),零注入依赖;
		// data.context_window 配置模型上下文窗口(默认 65536,展示上下文使用率)。
		Provides: []string{"ctx.usageStats"}}, Bundle: "base"},
	"host-session-summary": {Factory: func() sdk.Plugin { return &hostsummary.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-session-summary", Type: "host", APIVersion: ">=1.0,<2.0",
		// F 组 F3 会话概述(LLM 总结):ctx.sessionSummary;ctx.llm 可选注入
		//(缺失 → Summary 显式 unavailable);概述只落 meta.json,不进模型上下文
		Provides: []string{"ctx.sessionSummary"},
		Requires: []string{"ctx.cwdSessions"}}, Bundle: "base"},
	"host-cwd-sessions": {Factory: func() sdk.Plugin { return &hostcwdsessions.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-cwd-sessions", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.cwdSessions"},
		Requires: []string{"ctx.sessions"}}, Bundle: "base"},
	"host-fanout": {Factory: func() sdk.Plugin { return &hostfanout.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-fanout", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.fanout"},
		Requires: []string{"ctx.llm", "ctx.tools", "ctx.systemPrompt"}}, Bundle: "base"},
	"host-commands": {Factory: func() sdk.Plugin { return &hostcommands.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-commands", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.commands"}}, Bundle: "base"},
	"host-internal-commands": {Factory: func() sdk.Plugin { return &hostintcmd.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-internal-commands", Type: "host", APIVersion: ">=1.0,<2.0",
		// B3 命令下沉:原生内部命令(thinking/model/…/reload)注册进 ctx.commands,任意 profile 通用
		Requires: []string{"ctx.commands"}}, Bundle: "base"},
	"host-backup": {Factory: func() sdk.Plugin { return &hostbackup.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-backup", Type: "host", APIVersion: ">=1.0,<2.0",
		// M18 整体备份/恢复:ctx.backup 服务 + /backup 命令;备份目录 $GAH_HOME/backups(排除自身)
		// Requires ctx.commands:命令注册依赖启动顺序(map 遍历随机) —— 硬声明让拓扑保证 host-commands 先行
		Provides: []string{"ctx.backup"},
		Requires: []string{"ctx.commands"}}, Bundle: "base"},
	"host-docview": {Factory: func() sdk.Plugin { return &hostdocview.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-docview", Type: "host", APIVersion: ">=1.0,<2.0",
		// D 组文档预览(D0/D1):ctx.doc(sdk.DocService)——一个文档模型 + 四端呈现器;
		// ctx.sandbox 可选注入(未装配则不限制读,单机 TUI/CLI 场景)
		// ctx.commands:/preview 命令注册依赖启动顺序(拓扑保证 host-commands 先行)
		Provides: []string{"ctx.doc"},
		Requires: []string{"ctx.commands"}}, Bundle: "base"},
	"host-bridge": {Factory: func() sdk.Plugin { return &hostbridge.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-bridge", Type: "host", APIVersion: ">=1.0,<2.0",
		// M6.8 回调通道:外部进程经 GAH_CB_ADDR 请求宿主 tools/jobs/fanout 服务
		// ctx.commands:外部命令 spec 注册(经 / 提示与选择器共用注册表)
		Requires: []string{"ctx.commands", "ctx.tools", "ctx.jobs", "ctx.fanout"},
		// NOND-M1 第 3 步:外部插件控制面(按名重启进程 → 重读其配置;web 用它做 MCP 热重载)
		Provides: []string{"ctx.extplugins"}}, Bundle: "base"},
	"host-jobs": {Factory: func() sdk.Plugin { return &hostjobs.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-jobs", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.jobs"},
		// ctx.commands:/jobs 命令注册(需 host-commands 先行);ctx.tools:任务执行依赖
		Requires: []string{"ctx.commands", "ctx.tools"}}, Bundle: "base"},
	"host-worktrees": {Factory: func() sdk.Plugin { return &hostworktrees.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-worktrees", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.worktrees"},
		// 无可选依赖声明:ctx.commands(注册 /worktree)与 ctx.sandbox(工作区根)均**惰性注入** ——
		// 硬 requires 会导致「不装 host-commands 就起不来」,而 worktree 服务的核心能力与 UI 无关。
	}, Bundle: "base"},
	"host-plugin-manager": {Factory: func() sdk.Plugin { return &hostplugmgr.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-plugin-manager", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.pluginManager"}}, Bundle: "base"},
	"host-schedule": {Factory: func() sdk.Plugin { return &hostschedule.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-schedule", Type: "host", APIVersion: ">=1.0,<2.0",
		// NOND-W4 定时任务:ctx.schedule(List/Add/Update/Remove/RunNow) + /schedule 命令。
		// ctx.agentLoop:到点**经既有回合入口**跑一轮(工具仍走 ctx.tools + policy 裁决,
		// 仍落会话记录)——定时任务不是第二条执行路径。
		// ctx.commands:命令注册依赖启动顺序(map 遍历随机),硬声明让拓扑保证 host-commands 先行。
		// ctx.turnControl 可选注入(有回合在跑时等空闲,超时记 skipped)。
		Provides: []string{"ctx.schedule"},
		Requires: []string{"ctx.agentLoop", "ctx.commands"}}, Bundle: "base"},
	"host-notices": {Factory: func() sdk.Plugin { return &hostnotices.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-notices", Type: "host", APIVersion: ">=1.0,<2.0",
		// NOND-N1 提示通道:ctx.notices(Publish/List)—— 「需要人回来的时刻」的主动提示,
		// 不落盘、不进会话记录;Web toast / TUI 状态行 / 桌面壳通知各自订阅 sdk.EventNotice。
		// 自动生产来源(ctx.jobs / ctx.schedule)**惰性注入**:不装这两个插件则对应来源缺席,
		// 提示通道本身仍可用(硬 requires 会让「没装定时任务就没有提示能力」)。
		Provides: []string{"ctx.notices"}}, Bundle: "base"},
	"ui-tui-app": {Factory: func() sdk.Plugin { return &uitui.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "ui-tui-app", Type: "ui", APIVersion: ">=1.0,<2.0",
		// ctx.commands:内部命令注册表(ui-tui-app 与宿主命令共表;判重跳过宿主已注册同名)
		Requires: []string{"ctx.agentLoop", "ctx.llm", "ctx.commands"}}, Bundle: "tui", Manage: "scenario"},
	"ui-web-app": {Factory: func() sdk.Plugin { return &uiweb.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "ui-web-app", Type: "ui", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.agentLoop", "ctx.sessions", "ctx.llm"}}, Bundle: "web"},
	"host-confirm-fusion": {Factory: func() sdk.Plugin { return &hostconfirmfusion.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-confirm-fusion", Type: "host", APIVersion: ">=1.0,<2.0",
		// 多端融合:统一 ctx.confirm(仲裁广播 web/tui 呈现者,首答生效);
		// 装配本插件时 web/tui 改为注册呈现者不 Provide,同进程并存不再冲突。
		// ctx.question:结构化提问服务(fusion 统一提供,tool-ask/auto-plan 运行期现取;
		// 单 UI profile 无 fusion 时由 ui-* 插件 Provide,故不在各工具 Requires 中硬声明)。
		Provides: []string{"ctx.confirm", "ctx.confirmFusion", "ctx.question"}}, Bundle: "confirm-fusion"},
}

// RegisterAll 把 bundle == name 的全部插件注册进 registry(不按 enabled 过滤;过滤在装配层)。
func RegisterAll(r interface {
	Register(f sdk.Factory, m *sdk.Manifest) error
}, name string) error {
	for id, d := range All {
		if d.Bundle != name {
			continue
		}
		mm := *d.Manifest
		if err := r.Register(d.Factory, &mm); err != nil {
			return err
		}
		_ = id
	}
	return nil
}
