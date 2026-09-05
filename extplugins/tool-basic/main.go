// Command tool-basic 外部化基础工具进程(P1 方案 B):shell/files/web/memory
// 合一批二进制(共享运行时,体积与进程数最优),经桥协议(多工具)暴露。
// 宿主 host-bridge 扫描 tool-* 二进制 → Definitions 枚举 → 逐个注册。
package main

import (
	"os"

	bridge "github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-files"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-memory"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-shell"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-todo"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-web"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func main() {
	// pty 开关经环境变量透传(bundle data 无法透传子进程,外部化按需自配)
	pty := os.Getenv("GAH_TOOL_SHELL_PTY") == "1"
	tools := map[string]sdk.Tool{
		"shell": toolshell.NewTool(pty),
	}
	for name, t := range toolfiles.NewTools() {
		tools[name] = t
	}
	tools["web_fetch"] = toolweb.NewTool()
	tools["web_search"] = toolweb.NewSearchTool(toolweb.NewExaProvider(nil)) // M6.14:默认 exa,EXA_API_KEY
	tools["memory"] = toolmemory.NewTool()                                  // M10:跨会话操作记忆
	tools["todo"] = tooltodo.NewTool()                                      // M8:任务清单(4 状态机/blockedBy)
	bridge.ServeTools(tools)
}
