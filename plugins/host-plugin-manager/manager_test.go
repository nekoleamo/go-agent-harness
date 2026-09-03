package hostplugmgr_test

import (
	"log/slog"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/plugins/catalogue"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildEnv 装配:host-tools + tool-shell + plugin-manager。
func buildEnv(t *testing.T) (sdk.Ctx, *plugin.Registry, sdk.PluginManager) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()

	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	cands := map[string]sdk.PluginInfo{}
	for id, d := range catalogue.All {
		cands[id] = sdk.PluginInfo{ID: id, Type: d.Manifest.Type, Bundle: d.Bundle}
	}
	if err := c.Provide("system.catalogue", cands); err != nil {
		t.Fatal(err)
	}

	tree := config.NewTree()
	tree.Apply([]config.Entry{{ID: "host-tools"}, {ID: "host-plugin-manager"}, {ID: "tool-shell"}})
	for id, d := range catalogue.All {
		if d.Bundle != "base" {
			continue
		}
		if _, ok := tree.Get(id); !ok {
			continue
		}
		mm := *d.Manifest
		if err := reg.Register(d.Factory, &mm); err != nil {
			t.Fatal(err)
		}
	}
	enabled := map[string]bool{"host-tools": true, "host-plugin-manager": true, "tool-shell": true}
	if err := reg.StartSubset(c, enabled); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.DisposeAll)

	var m sdk.PluginManager
	if err := c.Inject("ctx.pluginManager", &m); err != nil {
		t.Fatal(err)
	}
	return c, reg, m
}

func TestListStates(t *testing.T) {
	_, _, m := buildEnv(t)
	list := m.List()
	states := map[string]string{}
	for _, info := range list {
		states[info.ID] = info.State
	}
	if states["tool-shell"] != "loaded" {
		t.Fatalf("tool-shell 应 loaded,got %q", states["tool-shell"])
	}
	if states["llm-openai-compat"] != "configured" {
		t.Fatalf("未启用的 llm-openai-compat 应 configured,got %q", states["llm-openai-compat"])
	}
	if len(list) != len(catalogue.All) {
		t.Fatalf("三源清单应覆盖全部候选,got %d/%d", len(list), len(catalogue.All))
	}
}

func TestUnloadRemovesToolLoadRestores(t *testing.T) {
	c, _, m := buildEnv(t)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	if _, ok := tools.Get("shell"); !ok {
		t.Fatal("shell 工具应存在")
	}
	if err := m.Unload("tool-shell"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tools.Get("shell"); ok {
		t.Fatal("卸载后 shell 工具应消失")
	}
	if err := m.Unload("tool-shell"); err != nil {
		t.Fatalf("幂等卸载失败: %v", err)
	}
	if err := m.Load("tool-shell"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tools.Get("shell"); !ok {
		t.Fatal("重载后 shell 工具应恢复")
	}
}

func TestLoadsIdempotent(t *testing.T) {
	_, _, m := buildEnv(t)
	if err := m.Load("tool-shell"); err != nil {
		t.Fatal(err)
	}
	if err := m.Load("host-tools"); err != nil {
		t.Fatal(err)
	}
	if err := m.Unload("llm-mock"); err != nil {
		t.Fatal(err)
	}
	if err := m.Load("no-such-plugin"); err == nil {
		t.Fatal("未知插件应报错")
	}
}
