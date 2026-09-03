// 桥测试:编译迷你 MCP server → mcp-bridge 加载 → mcp_greet 工具可用并正确转发。
package mcpbridge

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildMiniServer 编译迷你 MCP server。
func buildMiniServer(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "mcpserver")
	cmd := exec.Command("go", "build", "-o", bin, "../../tests/mcpserver")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("编译 mcpserver 失败: %v\n%s", err, out)
	}
	return bin
}

func TestMCPBridge(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}

	bin := buildMiniServer(t, t.TempDir())
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{
		"command": bin,
	}}); err != nil {
		t.Fatal(err)
	}

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	def, ok := tools.Get("mcp_greet")
	if !ok {
		t.Fatalf("MCP 工具应注册为 mcp_greet,实际: %v", toolNames(tools))
	}
	if !strings.Contains(def.Description, "问候") {
		t.Fatalf("定义应来自 MCP server: %+v", def)
	}
	res, err := tools.Execute(context.Background(), "mcp_greet", `{"name":"世界"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" || !strings.Contains(res.Content, "你好, 世界") {
		t.Fatalf("MCP 调用应转发并回传: %+v", res)
	}
}

// toolNames 辅助。
func toolNames(tools sdk.ToolRegistry) []string {
	defs := tools.List()
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}
