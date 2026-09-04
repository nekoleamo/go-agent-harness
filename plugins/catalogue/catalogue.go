// Package catalogue 全内置插件声明(单一事实源)。bundle 装配与 plugin-manager 共用。
// 对齐设计 §4.2:Manifest 提供 provides/requires 拓扑信息;bundle 按 enabled 过滤装配。
package catalogue

import (
	"github.com/nekoleamo/go-agent-harness/plugins/host-agent-loop"
	"github.com/nekoleamo/go-agent-harness/plugins/host-bridge"
	"github.com/nekoleamo/go-agent-harness/plugins/host-cwd-sessions"
	"github.com/nekoleamo/go-agent-harness/plugins/host-llm"
	"github.com/nekoleamo/go-agent-harness/plugins/host-jobs"
	"github.com/nekoleamo/go-agent-harness/plugins/host-plugin-manager"
	"github.com/nekoleamo/go-agent-harness/plugins/host-session-log"
	"github.com/nekoleamo/go-agent-harness/plugins/host-skills"
	"github.com/nekoleamo/go-agent-harness/plugins/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/plugins/host-tools"
	"github.com/nekoleamo/go-agent-harness/plugins/llm-anthropic-compat"
	"github.com/nekoleamo/go-agent-harness/plugins/llm-mock"
	"github.com/nekoleamo/go-agent-harness/plugins/llm-openai-compat"
	"github.com/nekoleamo/go-agent-harness/plugins/mcp-bridge"
	"github.com/nekoleamo/go-agent-harness/plugins/policy-approval"
	"github.com/nekoleamo/go-agent-harness/plugins/policy-sandbox"
	"github.com/nekoleamo/go-agent-harness/plugins/tool-files"
	"github.com/nekoleamo/go-agent-harness/plugins/tool-shell"
	"github.com/nekoleamo/go-agent-harness/plugins/tool-web"
	"github.com/nekoleamo/go-agent-harness/plugins/tool-workflow"
	"github.com/nekoleamo/go-agent-harness/plugins/ui-tui-app"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Def 插件定义:工厂 + 清单 + 所属 bundle。
type Def struct {
	Factory  sdk.Factory
	Manifest *sdk.Manifest
	Bundle   string // base | tui
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
	"policy-approval": {Factory: func() sdk.Plugin { return &policyapproval.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "policy-approval", Type: "policy", APIVersion: ">=1.0,<2.0"}, Bundle: "base"},
	"policy-sandbox": {Factory: func() sdk.Plugin { return &policysandbox.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "policy-sandbox", Type: "policy", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.sandbox"}}, Bundle: "base"},
	"host-agent-loop": {Factory: func() sdk.Plugin { return &hostagentloop.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-agent-loop", Type: "agent", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.agentLoop"},
		Requires: []string{"ctx.sessions", "ctx.llm", "ctx.tools", "ctx.systemPrompt"}}, Bundle: "base"},
	"llm-openai-compat": {Factory: func() sdk.Plugin { return &llmopenai.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "llm-openai-compat", Type: "llm", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.llm"}}, Bundle: "base"},
	"llm-mock": {Factory: func() sdk.Plugin { return &llmmock.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "llm-mock", Type: "llm", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.llm"}}, Bundle: "base"},
	"llm-anthropic-compat": {Factory: func() sdk.Plugin { return &llmanthropic.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "llm-anthropic-compat", Type: "llm", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.llm"}}, Bundle: "base"},
	"tool-workflow": {Factory: func() sdk.Plugin { return &toolworkflow.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-workflow", Type: "tool", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}, Bundle: "base"},
	"tool-shell": {Factory: func() sdk.Plugin { return &toolshell.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-shell", Type: "tool", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}, Bundle: "base"},
	"tool-files": {Factory: func() sdk.Plugin { return &toolfiles.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-files", Type: "tool", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}, Bundle: "base"},
	"tool-web": {Factory: func() sdk.Plugin { return &toolweb.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-web", Type: "tool", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}, Bundle: "base"},
	"mcp-bridge": {Factory: func() sdk.Plugin { return &mcpbridge.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "mcp-bridge", Type: "host", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}, Bundle: "base"},
	"host-cwd-sessions": {Factory: func() sdk.Plugin { return &hostcwdsessions.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-cwd-sessions", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.cwdSessions"},
		Requires: []string{"ctx.sessions"}}, Bundle: "base"},
	"host-bridge": {Factory: func() sdk.Plugin { return &hostbridge.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-bridge", Type: "host", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}, Bundle: "base"},
	"host-jobs": {Factory: func() sdk.Plugin { return &hostjobs.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-jobs", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.jobs"},
		Requires: []string{"ctx.tools"}}, Bundle: "base"},
	"host-plugin-manager": {Factory: func() sdk.Plugin { return &hostplugmgr.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "host-plugin-manager", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.pluginManager"}}, Bundle: "base"},
	"ui-tui-app": {Factory: func() sdk.Plugin { return &uitui.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "ui-tui-app", Type: "ui", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.agentLoop", "ctx.llm"}}, Bundle: "tui"},
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
