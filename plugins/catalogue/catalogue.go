// Package catalogue 全内置插件声明(单一事实源)。bundle 装配与 plugin-manager 共用。
// 对齐设计 §4.2:Manifest 提供 provides/requires 拓扑信息;bundle 按 enabled 过滤装配。
package catalogue

import (
	"github.com/nekoleamo/go-agent-harness/plugins/adapter/llm-anthropic-compat"
	"github.com/nekoleamo/go-agent-harness/plugins/adapter/llm-mock"
	"github.com/nekoleamo/go-agent-harness/plugins/adapter/llm-openai-compat"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-agent-loop"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-commands"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-backup"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-internal-commands"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-cwd-sessions"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-fanout"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-jobs"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-llm"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-plugin-manager"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-session-log"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-skills"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-usage-stats"
	"github.com/nekoleamo/go-agent-harness/plugins/host/token-compress"
	"github.com/nekoleamo/go-agent-harness/plugins/mcp/mcp-bridge"
	"github.com/nekoleamo/go-agent-harness/plugins/mcp/mcp-server"
	"github.com/nekoleamo/go-agent-harness/plugins/policy/policy-guard"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-auto-plan"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-files"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-memory"
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
		Provides: []string{"ctx.agentLoop"},
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
	"tool-auto-plan": {Factory: func() sdk.Plugin { return &toolautoplan.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-auto-plan", Type: "tool", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools", "ctx.systemPrompt"}}, Bundle: "base"},
	"tool-todo": {Factory: func() sdk.Plugin { return &tooltodo.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-todo", Type: "tool", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}, Bundle: "base", Manage: "external"},
	"mcp-bridge": {Factory: func() sdk.Plugin { return &mcpbridge.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "mcp-bridge", Type: "host", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}, Bundle: "base", Manage: "external"},
	"mcp-server": {Factory: func() sdk.Plugin { return &mcpserver.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "mcp-server", Type: "host", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}, Bundle: "base", Manage: "scenario"},
	"token-compress": {Factory: func() sdk.Plugin { return &tokencompress.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "token-compress", Type: "host", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.sessions"}}, Bundle: "base"},
	"host-usage-stats": {Factory: func() sdk.Plugin { return &hostusagestats.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-usage-stats", Type: "host", APIVersion: ">=1.0,<2.0",
		// 独立统计:只订阅 session/event 广播(session/usage 事件),零注入依赖;
		// data.context_window 配置模型上下文窗口(默认 65536,展示上下文使用率)。
		Provides: []string{"ctx.usageStats"}}, Bundle: "base"},
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
		Provides: []string{"ctx.backup"}}, Bundle: "base"},
	"host-bridge": {Factory: func() sdk.Plugin { return &hostbridge.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-bridge", Type: "host", APIVersion: ">=1.0,<2.0",
		// M6.8 回调通道:外部进程经 GAH_CB_ADDR 请求宿主 tools/jobs/fanout 服务
		Requires: []string{"ctx.tools", "ctx.jobs", "ctx.fanout"}}, Bundle: "base"},
	"host-jobs": {Factory: func() sdk.Plugin { return &hostjobs.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-jobs", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.jobs"},
		Requires: []string{"ctx.tools"}}, Bundle: "base"},
	"host-plugin-manager": {Factory: func() sdk.Plugin { return &hostplugmgr.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-plugin-manager", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.pluginManager"}}, Bundle: "base"},
	"ui-tui-app": {Factory: func() sdk.Plugin { return &uitui.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "ui-tui-app", Type: "ui", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.agentLoop", "ctx.llm"}}, Bundle: "tui", Manage: "scenario"},
	"ui-web-app": {Factory: func() sdk.Plugin { return &uiweb.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "ui-web-app", Type: "ui", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.agentLoop", "ctx.sessions", "ctx.llm"}}, Bundle: "web"},
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
