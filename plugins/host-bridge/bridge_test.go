// 桥测试:编译外部插件→加载→执行→杀进程验证崩溃隔离(宿主存活、调用转结构化错误)。
package hostbridge

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/plugins/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildExternalPlugin 编译 tool-echo 到 dir。
func buildExternalPlugin(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "tool-echo"), "../../extplugins/tool-echo")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("编译外部插件失败: %v\n%s", err, out)
	}
}

// buildEnv:host-tools + host-bridge(dir)。
func buildEnv(t *testing.T, dir string) (sdk.Ctx, *plugin.Registry) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	defs := []struct {
		id   string
		f    sdk.Factory
		m    *sdk.Manifest
		data map[string]any
	}{
		{"host-tools", func() sdk.Plugin { return &hosttools.Plugin{} }, &sdk.Manifest{ID: "host-tools", Provides: []string{"ctx.tools"}}, nil},
		{"host-bridge", func() sdk.Plugin { return &Plugin{} }, &sdk.Manifest{ID: "host-bridge", Requires: []string{"ctx.tools"}}, map[string]any{"dir": dir}},
	}
	for _, d := range defs {
		mm := *d.m
		mm.Data = d.data
		if err := reg.Register(d.f, &mm); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.StartSubset(c, map[string]bool{"host-tools": true, "host-bridge": true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.DisposeAll)
	return c, reg
}

func toolNames(tools sdk.ToolRegistry) []string {
	defs := tools.List()
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}

func TestExternalToolLoadAndExecute(t *testing.T) {
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	c, _ := buildEnv(t, dir)

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	t.Logf("工具定义: %+v", tools.List())
	def, ok := tools.Get("echo")
	if !ok {
		t.Fatal("外部插件工具 echo 应注册")
	}
	if !strings.Contains(def.Description, "外部插件") {
		t.Fatalf("定义应来自外部插件: %+v", def)
	}
	res, err := tools.Execute(context.Background(), "echo", `{"text":"你好"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" || !strings.Contains(res.Content, "你好") {
		t.Fatalf("外部工具应回显: %+v", res)
	}
}

func TestExternalPluginCrashIsolation(t *testing.T) {
	dir := t.TempDir()
	buildExternalPlugin(t, dir)
	c, reg := buildEnv(t, dir)

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.Execute(context.Background(), "echo", `{"text":"ok"}`); err != nil {
		t.Fatal(err)
	}
	t.Logf("kill 前工具定义: %+v", tools.List())
	t.Log("kill 前工具列表: " + fmt.Sprint(toolNames(tools)))

	killed := killPluginProcess("tool-echo")
	if !killed {
		t.Skip("未找到外部插件进程,跳过崩溃隔离验证")
	}

	// 宿主应存活;调用返回结构化错误而非宿主崩溃
	deadline := time.Now().Add(10 * time.Second)
	var blocked string
	for time.Now().Before(deadline) {
		res, err := tools.Execute(context.Background(), "echo", `{"text":"again"}`)
		if err != nil {
			t.Fatalf("宿主侧不应 panic,got err %v", err)
		}
		if res.Error != "" {
			blocked = res.Error
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if blocked == "" {
		t.Fatal("进程被杀后调用应转结构化错误")
	}
	t.Logf("崩溃后调用结果: %s", blocked)
	if len(reg.Snapshot()) != 2 {
		t.Fatalf("宿主插件应保持存活: %v", reg.Snapshot())
	}
}

// killPluginProcess 精确名匹配击杀插件进程(macOS comm 名 = tool-echo)。
func killPluginProcess(name string) bool {
	out, err := exec.Command("pgrep", "-x", name).Output()
	if err != nil {
		return false
	}
	for _, pid := range strings.Fields(string(out)) {
		exec.Command("kill", "-9", pid).Run()
	}
	time.Sleep(300 * time.Millisecond)
	return true
}

var _ = os.Getpid
