// 桥测试:编译迷你 MCP server → mcp-bridge 加载 → mcp_greet 工具可用并正确转发。
package mcpbridge

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildMiniServer 编译迷你 MCP server。
func buildMiniServer(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "mcpserver")
	cmd := exec.Command("go", "build", "-o", bin, "../../../tests/mcpserver")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("编译 mcpserver 失败: %v\n%s", err, out)
	}
	return bin
}

func TestHolderRespawnOnCrash(t *testing.T) {
	bin := buildMiniServer(t, t.TempDir())
	cli, err := spawn(bin, nil)
	if err != nil {
		t.Fatal(err)
	}
	rctx := context.Background()
	if err := cli.initialize(rctx); err != nil {
		t.Fatal(err)
	}
	h := &holder{cli: cli, command: bin, throttle: 150 * time.Millisecond, lg: slog.New(slog.DiscardHandler)}
	go h.supervise()

	// 杀进程模拟崩溃
	old := cli.cmd.Process
	_ = old.Kill()
	// 等重启(轮询 current 换新连接;超时防护)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if h.current() != cli {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("崩溃后未自动重启")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// 新连接可正常 tools/list
	nc := h.current()
	defs, err := nc.toolsList(rctx)
	if err != nil {
		t.Fatalf("重启后 tools/list 失败: %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "greet" {
		t.Fatalf("重启后工具定义异常: %+v", defs)
	}
	// 关闭:停看护、杀当前进程,无残留
	h.close()
	time.Sleep(100 * time.Millisecond)
	if h.closed == false {
		t.Fatal("holder 应已关闭")
	}
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
		t.Fatalf("MCP 工具应注册为 mcp_greet,实际: %v", registryToolNames(tools))
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

// registryToolNames 辅助(装配层测试另有 map 版 toolNames)。
func registryToolNames(tools sdk.ToolRegistry) []string {
	defs := tools.List()
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}
