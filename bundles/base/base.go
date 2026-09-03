// Package base 是 base bundle 的插件装配层(对齐设计 §2.2:base = 共享第一层)。
// 维护"配置条目 id → 内置插件工厂/清单"映射;未实现条目跳过(配置可含未来条目)。
package base

import (
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/plugins/host-agent-loop"
	"github.com/nekoleamo/go-agent-harness/plugins/host-llm"
	"github.com/nekoleamo/go-agent-harness/plugins/host-session-log"
	"github.com/nekoleamo/go-agent-harness/plugins/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/plugins/host-tools"
	"github.com/nekoleamo/go-agent-harness/plugins/llm-mock"
	"github.com/nekoleamo/go-agent-harness/plugins/llm-openai-compat"
	"github.com/nekoleamo/go-agent-harness/plugins/tool-shell"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

type def struct {
	f sdk.Factory
	m *sdk.Manifest
}

// defs 内置插件全量清单(未启用条目经配置树 enabled 过滤)。
var defs = map[string]def{
	"host-session-log": {func() sdk.Plugin { return &sessionlog.Plugin{} }, &sdk.Manifest{
		ID: "host-session-log", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.sessions"}}},
	"host-llm": {func() sdk.Plugin { return &hostllm.Plugin{} }, &sdk.Manifest{
		ID: "host-llm", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.llm"}}},
	"host-tools": {func() sdk.Plugin { return &hosttools.Plugin{} }, &sdk.Manifest{
		ID: "host-tools", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.tools"}}},
	"host-system-prompt": {func() sdk.Plugin { return &hostsystemprompt.Plugin{} }, &sdk.Manifest{
		ID: "host-system-prompt", Type: "host", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.systemPrompt"}}},
	"host-agent-loop": {func() sdk.Plugin { return &hostagentloop.Plugin{} }, &sdk.Manifest{
		ID: "host-agent-loop", Type: "agent", APIVersion: ">=1.0,<2.0",
		Provides: []string{"ctx.agentLoop"},
		Requires: []string{"ctx.sessions", "ctx.llm", "ctx.tools", "ctx.systemPrompt"}}},
	"llm-openai-compat": {func() sdk.Plugin { return &llmopenai.Plugin{} }, &sdk.Manifest{
		ID: "llm-openai-compat", Type: "llm", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.llm"}}},
	"llm-mock": {func() sdk.Plugin { return &llmmock.Plugin{} }, &sdk.Manifest{
		ID: "llm-mock", Type: "llm", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.llm"}}},
	"tool-shell": {func() sdk.Plugin { return &toolshell.Plugin{} }, &sdk.Manifest{
		ID: "tool-shell", Type: "tool", APIVersion: ">=1.0,<2.0",
		Requires: []string{"ctx.tools"}}},
}

// RegisterAll 按配置树注册全部已启用且已实现的内置插件。
func RegisterAll(r *plugin.Registry, t *config.Tree) error {
	for _, id := range t.List() {
		if !t.Enabled(id) {
			continue
		}
		d, ok := defs[id]
		if !ok {
			continue // 未实现条目跳过(配置可含未来条目)
		}
		entry, _ := t.Get(id)
		m := *d.m
		m.Data = entry.Data
		if err := r.Register(d.f, &m); err != nil {
			return err
		}
	}
	return nil
}
