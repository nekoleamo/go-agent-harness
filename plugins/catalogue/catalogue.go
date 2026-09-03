// Package catalogue 全内置插件声明(单一事实源)。bundle 装配与 plugin-manager 共用。
// 对齐设计 §4.2:Manifest 提供 provides/requires 拓扑信息;bundle 按 enabled 过滤装配。
package catalogue

import (
	"github.com/nekoleamo/go-agent-harness/plugins/host-agent-loop"
	"github.com/nekoleamo/go-agent-harness/plugins/host-llm"
	"github.com/nekoleamo/go-agent-harness/plugins/host-plugin-manager"
	"github.com/nekoleamo/go-agent-harness/plugins/host-session-log"
	"github.com/nekoleamo/go-agent-harness/plugins/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/plugins/host-tools"
	"github.com/nekoleamo/go-agent-harness/plugins/llm-mock"
	"github.com/nekoleamo/go-agent-harness/plugins/llm-openai-compat"
	"github.com/nekoleamo/go-agent-harness/plugins/policy-approval"
	"github.com/nekoleamo/go-agent-harness/plugins/policy-sandbox"
	"github.com/nekoleamo/go-agent-harness/plugins/tool-shell"
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
	"tool-shell": {Factory: func() sdk.Plugin { return &toolshell.Plugin{} }, Manifest: &sdk.Manifest{
		ID: "tool-shell", Type: "tool", APIVersion: ">=1.0,<2.0",
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
